// Package server runs the entry node reconcile loop: full tunnel through the
// exit node only while the peer is alive and forward_all_traffic is true.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"time"

	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/netsys"
	"reverse-vpn-tunnel/internal/tunnel"
)

const TickInterval = tunnel.TickInterval

var ErrRetriesExhausted = errors.New("reconnect.max_retries exhausted")

type Tunnel interface {
	Stats() (tunnel.Stats, error)
	BindUpdate() error
	SetBindInterface(name string) error
	Probe(dst netip.Addr) error
}

type System interface {
	ApplyFullTunnel(blockIPv6 bool) error
	SetIPv6Block(on bool)
	RollbackFullTunnel() error
	DefaultRoute() (netsys.Route, error)
}

// Reloader returns a new config when the file changed. Errors keep the old config.
type Reloader func() (cfg config.Server, isChanged bool, err error)

type Server struct {
	cfg    config.Server
	tun    Tunnel
	sys    System
	reload Reloader
	live   *tunnel.Liveness
	peerIP netip.Addr
	// epoch turns Step times into monotonic readings for rx age.
	epoch time.Time

	lastTick     time.Time
	egress       string
	isAlive      bool
	wasConnected bool
	lastAttempt  time.Time
	attempt      int
	// isApplied: full tunnel fully in place. isDirty: something of it may be
	// in place and a rollback is still owed (set until a rollback succeeds).
	isApplied   bool
	isDirty     bool
	isV6Blocked bool
}

func New(cfg config.Server, tun Tunnel, sys System, reload Reloader) *Server {
	timeout := time.Duration(cfg.Reconnect.DeadPeerTimeoutSec) * time.Second
	_, clientIP := netsys.TunnelAddrs(cfg.TunnelCIDR)
	return &Server{
		cfg: cfg, tun: tun, sys: sys, reload: reload,
		live: tunnel.NewLiveness(timeout), peerIP: clientIP, epoch: time.Now(),
	}
}

// Run ticks until ctx is done or retries are exhausted.
func (s *Server) Run(ctx context.Context) error {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := s.tick(time.Now); err != nil {
				return err
			}
		}
	}
}

// tick runs one Step. Time spent inside Step (slow commands) is not a sleep,
// so the wake check measures from the end of the previous Step.
func (s *Server) tick(clock func() time.Time) error {
	err := s.Step(clock())
	s.lastTick = clock()
	return err
}

// Step evaluates state once and reconciles routing to it. now should carry a
// monotonic reading (time.Now) for rx age; the wake check uses wall clock,
// because monotonic time stops during suspend.
func (s *Server) Step(now time.Time) error {
	gap := now.Round(0).Sub(s.lastTick.Round(0))
	isWake := !s.lastTick.IsZero() && gap > 3*TickInterval
	if isWake {
		slog.Info("wake-up detected", "gap", gap.Round(time.Second))
	}
	s.lastTick = now
	s.refreshBind(isWake)
	s.reloadConfig()

	stats, err := s.tun.Stats()
	if err != nil {
		return err
	}
	mono := now.Sub(s.epoch)
	isAlive := s.live.Update(now, mono, stats)
	if s.live.ShouldProbe(mono) {
		if err := s.tun.Probe(s.peerIP); err != nil {
			slog.Warn("probe peer", "err", err)
		} else {
			slog.Debug("probe sent", "to", s.peerIP)
		}
	}
	retry := time.Duration(s.cfg.Reconnect.RetryIntervalSec) * time.Second
	switch {
	case isAlive:
		if !s.isAlive {
			slog.Info("peer connected", "endpoint", stats.Endpoint)
		}
		s.wasConnected = true
		s.attempt = 0
	case s.isAlive:
		slog.Warn("peer dead; restoring local routing")
		s.lastAttempt = now
	// The server cannot dial; attempts only refresh the local socket.
	case s.wasConnected && now.Sub(s.lastAttempt) >= retry:
		if maxRetries := s.cfg.Reconnect.MaxRetries; maxRetries > 0 && s.attempt >= maxRetries {
			return ErrRetriesExhausted
		}
		s.attempt++
		s.lastAttempt = now
		slog.Info("waiting for peer", "attempt", s.attempt)
		s.refreshBind(true)
	}
	s.isAlive = isAlive
	s.reconcile()
	return nil
}

func (s *Server) reconcile() {
	isDesired := s.isAlive && s.cfg.ForwardAllTraffic
	switch {
	case isDesired && !s.isApplied:
		// Clear leftovers of a failed apply or rollback first.
		if s.isDirty && s.rollback() != nil {
			return
		}
		s.isDirty = true
		if err := s.sys.ApplyFullTunnel(s.cfg.BlockIPv6); err != nil {
			slog.Error("apply full tunnel failed; rolling back", "err", err)
			s.rollback()
			return
		}
		s.isApplied, s.isV6Blocked = true, s.cfg.BlockIPv6
		slog.Info("forwarding all traffic through Home2")
	case !isDesired && s.isDirty:
		// A failed rollback stays dirty and is retried on the next tick.
		if s.rollback() == nil {
			slog.Info("local routing restored")
		}
	case s.isApplied && s.isV6Blocked != s.cfg.BlockIPv6:
		s.sys.SetIPv6Block(s.cfg.BlockIPv6)
		s.isV6Blocked = s.cfg.BlockIPv6
		slog.Info("IPv6 block updated", "block_ipv6", s.cfg.BlockIPv6)
	}
}

func (s *Server) rollback() error {
	s.isApplied = false
	if err := s.sys.RollbackFullTunnel(); err != nil {
		slog.Error("rollback full tunnel failed; retrying next tick", "err", err)
		return err
	}
	s.isDirty = false
	return nil
}

// Close restores local routing; an error means something may be left behind.
func (s *Server) Close() error {
	s.isAlive = false
	return s.rollback()
}

func (s *Server) reloadConfig() {
	cfg, isChanged, err := s.reload()
	if err != nil {
		slog.Error("config not applied; keeping previous settings", "err", err)
		return
	}
	if !isChanged {
		return
	}
	hot := s.cfg
	hot.ForwardAllTraffic, hot.BlockIPv6 = cfg.ForwardAllTraffic, cfg.BlockIPv6
	if hot != cfg {
		slog.Warn("config changes other than forward_all_traffic / block_ipv6 need a restart")
	}
	slog.Info("config reloaded", "forward_all_traffic", cfg.ForwardAllTraffic, "block_ipv6", cfg.BlockIPv6)
	s.cfg.ForwardAllTraffic, s.cfg.BlockIPv6 = cfg.ForwardAllTraffic, cfg.BlockIPv6
}

// refreshBind follows the physical interface and reopens the socket on change or when forced.
func (s *Server) refreshBind(force bool) {
	isChanged := false
	if r, err := s.sys.DefaultRoute(); err == nil && r.Dev != s.egress {
		slog.Info("egress interface", "dev", r.Dev, "gateway", r.Via)
		s.egress, isChanged = r.Dev, true
		if err := s.tun.SetBindInterface(r.Dev); err != nil {
			slog.Error("bind interface", "err", err)
		}
	}
	if isChanged || force {
		if err := s.tun.BindUpdate(); err != nil {
			slog.Error("bind update", "err", err)
		}
	}
}
