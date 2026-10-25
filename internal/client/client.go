// Package client runs the exit node loop: dial the server, keep forwarding
// rules in place, and redial when the peer goes dead.
package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"time"

	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/keys"
	"reverse-vpn-tunnel/internal/netsys"
	"reverse-vpn-tunnel/internal/tunnel"
)

const (
	TickInterval   = tunnel.TickInterval
	RepairInterval = 30 * time.Second
)

var ErrRetriesExhausted = errors.New("reconnect.max_retries exhausted")

type Tunnel interface {
	Configure(c tunnel.Config) error
	Stats() (tunnel.Stats, error)
	BindUpdate() error
	SetBindInterface(name string) error
	Probe(dst netip.Addr) error
}

type System interface {
	RepairForwarding() error
	SetEgress(name string) error
	DefaultRoute() (netsys.Route, error)
}

// Resolver returns the IPv4 address of host (A record only).
type Resolver func(host string) (netip.Addr, error)

type Client struct {
	cfg     config.Client
	keys    keys.Keys
	tun     Tunnel
	sys     System
	resolve Resolver
	live    *tunnel.Liveness
	peerIP  netip.Addr
	// epoch turns Step times into monotonic readings for rx age.
	epoch time.Time

	lastTick    time.Time
	lastRepair  time.Time
	lastAttempt time.Time
	egress      string
	isAlive     bool
	attempt     int
}

func New(cfg config.Client, k keys.Keys, tun Tunnel, sys System, resolve Resolver) *Client {
	timeout := time.Duration(cfg.Reconnect.DeadPeerTimeoutSec) * time.Second
	serverIP, _ := netsys.TunnelAddrs(cfg.TunnelCIDR)
	return &Client{
		cfg: cfg, keys: k, tun: tun, sys: sys, resolve: resolve,
		live: tunnel.NewLiveness(timeout, false), peerIP: serverIP, epoch: time.Now(),
	}
}

// Start dials the server for the first time. A failed dial is retried later.
// Time spent here (first DNS lookup) is not a sleep, so the wake check of
// the first Step measures from the end of Start.
func (c *Client) Start(clock func() time.Time) {
	now := clock()
	c.lastRepair, c.lastAttempt = now, now
	c.refreshBind(false)
	c.logDialErr(c.dial())
	c.lastTick = clock()
}

// Run ticks until ctx is done or retries are exhausted.
func (c *Client) Run(ctx context.Context) error {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := c.tick(time.Now); err != nil {
				return err
			}
		}
	}
}

// tick runs one Step. Time spent inside Step (e.g. slow DNS) is not a sleep,
// so the wake check measures from the end of the previous Step.
func (c *Client) tick(clock func() time.Time) error {
	err := c.Step(clock())
	c.lastTick = clock()
	return err
}

// Step evaluates liveness once and redials when needed. now should carry a
// monotonic reading (time.Now) for rx age; the wake check uses wall clock,
// because monotonic time stops during suspend.
func (c *Client) Step(now time.Time) error {
	if gap := now.Round(0).Sub(c.lastTick.Round(0)); gap > 3*TickInterval {
		// The server only trusts a handshake newer than its dead time, so
		// force one instead of waiting for the next rekey.
		slog.Info("wake-up detected; redialing", "gap", gap.Round(time.Second))
		c.refreshBind(true)
		c.logDialErr(c.dial())
	} else {
		c.refreshBind(false)
	}
	c.lastTick = now

	stats, err := c.tun.Stats()
	if err != nil {
		return err
	}
	mono := now.Sub(c.epoch)
	isAlive := c.live.Update(now, mono, stats)
	if c.live.ShouldProbe(mono) {
		if err := c.tun.Probe(c.peerIP); err != nil {
			slog.Warn("probe server", "err", err)
		} else {
			slog.Debug("probe sent", "to", c.peerIP)
		}
	}
	retry := time.Duration(c.cfg.Reconnect.RetryIntervalSec) * time.Second
	switch {
	case isAlive:
		if !c.isAlive {
			slog.Info("connected to server", "endpoint", stats.Endpoint)
		}
		c.attempt = 0
	case c.isAlive:
		slog.Warn("server dead; redialing later", "in", retry)
		c.lastAttempt = now
	case now.Sub(c.lastAttempt) >= retry:
		if maxRetries := c.cfg.Reconnect.MaxRetries; maxRetries > 0 && c.attempt >= maxRetries {
			return ErrRetriesExhausted
		}
		c.attempt++
		c.lastAttempt = now
		slog.Info("redialing server", "attempt", c.attempt)
		c.logDialErr(c.dial())
		c.refreshBind(true)
	}
	c.isAlive = isAlive

	if now.Sub(c.lastRepair) >= RepairInterval {
		c.lastRepair = now
		if err := c.sys.RepairForwarding(); err != nil {
			slog.Error("repair forwarding", "err", err)
		}
	}
	return nil
}

// dial resolves server_address and replaces the peer, which drops the old
// session and starts a fresh handshake.
func (c *Client) dial() error {
	host, portStr, err := net.SplitHostPort(c.cfg.ServerAddress)
	if err != nil {
		return err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return err
	}
	addr, err := c.resolve(host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	return c.tun.Configure(tunnel.Config{
		PrivateKey:    c.keys.ClientPrivate,
		PeerPublicKey: c.keys.ServerPublic,
		PresharedKey:  c.keys.PresharedKey,
		Endpoint:      netip.AddrPortFrom(addr, uint16(port)),
		AllowedIPs:    []netip.Prefix{c.cfg.TunnelCIDR},
	})
}

func (c *Client) logDialErr(err error) {
	if err != nil {
		slog.Error("dial", "err", err)
	}
}

// refreshBind follows the physical interface; on change it repins the socket
// and the pf rules (macOS), and reopens the socket on change or when forced.
func (c *Client) refreshBind(force bool) {
	isChanged := false
	if r, err := c.sys.DefaultRoute(); err == nil && r.Dev != c.egress {
		slog.Info("egress interface", "dev", r.Dev, "gateway", r.Via)
		c.egress, isChanged = r.Dev, true
		if err := c.tun.SetBindInterface(r.Dev); err != nil {
			slog.Error("bind interface", "err", err)
		}
		if err := c.sys.SetEgress(r.Dev); err != nil {
			slog.Error("set egress", "err", err)
		}
	}
	if isChanged || force {
		if err := c.tun.BindUpdate(); err != nil {
			slog.Error("bind update", "err", err)
		}
	}
}

// ResolveIPv4 is the production Resolver.
func ResolveIPv4(host string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		if !addr.Is4() {
			return netip.Addr{}, fmt.Errorf("%s is not IPv4", host)
		}
		return addr, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("no A record for %s", host)
	}
	return ips[0].Unmap(), nil
}
