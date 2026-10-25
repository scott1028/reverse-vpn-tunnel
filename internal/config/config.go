// Package config loads, validates and creates the JSONC config files.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
)

const (
	ServerFile = "reverse-vpn-tunnel.server.jsonc"
	ClientFile = "reverse-vpn-tunnel.client.jsonc"

	MinAPIKeyLen = 20
	// ControlPort is the inner UDP destination port for shutdown notices.
	ControlPort = 51821
	// KeepaliveSec is the fixed persistent keepalive on both sides.
	KeepaliveSec = 15
	// MinDeadPeerTimeoutSec must exceed the longest rx gap of a healthy idle
	// peer. The worse case is a lost peer reply: tunnel.LostReplyRxGap
	// (35.334s = probe after 18s + wireguard-go new handshake 10s + 5s +
	// 0.334s jitter + tick 2s to see the answer), + 2s because the probe may
	// leave one tick after ProbeAfter, + 1s round trip = 38.334s, rounded up
	// to 40s (1.666s margin). A lost probe alone needs only
	// tunnel.WorstRxGap (32s, late tick included) + 2s + 1s.
	MinDeadPeerTimeoutSec = 40
)

type Reconnect struct {
	DeadPeerTimeoutSec int `json:"dead_peer_timeout_sec"`
	RetryIntervalSec   int `json:"retry_interval_sec"`
	MaxRetries         int `json:"max_retries"`
}

type Server struct {
	APIKey            string       `json:"api_key"`
	ListenPort        int          `json:"listen_port"`
	ForwardAllTraffic bool         `json:"forward_all_traffic"`
	BlockIPv6         bool         `json:"block_ipv6"`
	TunnelCIDR        netip.Prefix `json:"tunnel_cidr"`
	ExitLANAliasCIDR  netip.Prefix `json:"exit_lan_alias_cidr"`
	ExitLANAliasKey   string       `json:"-"`
	MTU               int          `json:"mtu"`
	Reconnect         Reconnect    `json:"reconnect"`
}

type LANAlias struct {
	Enabled   bool         `json:"enabled"`
	AliasCIDR netip.Prefix `json:"alias_cidr"`
	// "auto" or a CIDR.
	LANCIDR string `json:"lan_cidr"`
}

type Client struct {
	APIKey        string       `json:"api_key"`
	ServerAddress string       `json:"server_address"`
	TunnelCIDR    netip.Prefix `json:"tunnel_cidr"`
	LANAlias      LANAlias     `json:"lan_alias"`
	MTU           int          `json:"mtu"`
	Reconnect     Reconnect    `json:"reconnect"`
}

func defaultReconnect() Reconnect {
	return Reconnect{DeadPeerTimeoutSec: 45, RetryIntervalSec: 60, MaxRetries: 0}
}

func DefaultServer() Server {
	return Server{
		ListenPort:        51820,
		ForwardAllTraffic: true,
		BlockIPv6:         true,
		TunnelCIDR:        netip.MustParsePrefix("10.77.0.0/30"),
		ExitLANAliasCIDR:  netip.MustParsePrefix("10.78.0.0/16"),
		ExitLANAliasKey:   "exit_lan_alias_cidr",
		MTU:               1280,
		Reconnect:         defaultReconnect(),
	}
}

func DefaultClient() Client {
	return Client{
		ServerAddress: "server.example.com:51820",
		TunnelCIDR:    netip.MustParsePrefix("10.77.0.0/30"),
		LANAlias: LANAlias{
			Enabled:   true,
			AliasCIDR: netip.MustParsePrefix("10.78.0.0/16"),
			LANCIDR:   "auto",
		},
		MTU:       1280,
		Reconnect: defaultReconnect(),
	}
}

// LoadServer reads and validates a server config. Missing fields keep defaults.
func LoadServer(path string) (Server, error) {
	cfg, _, err := loadServer(path)
	return cfg, err
}

func loadServer(path string) (Server, fileSig, error) {
	cfg := DefaultServer()
	aliasName := "exit_lan_alias_cidr"
	wire := struct {
		*Server
		ExitLANAliasCIDR   json.RawMessage `json:"exit_lan_alias_cidr"`
		LegacyLANAliasCIDR json.RawMessage `json:"home2_lan_alias_cidr"`
	}{Server: &cfg}
	sig, err := load(path, &wire)
	if err == nil && len(wire.ExitLANAliasCIDR) > 0 && len(wire.LegacyLANAliasCIDR) > 0 {
		err = errors.New("cannot set both exit_lan_alias_cidr and home2_lan_alias_cidr")
	}
	if err == nil {
		alias := wire.ExitLANAliasCIDR
		if len(wire.LegacyLANAliasCIDR) > 0 {
			alias = wire.LegacyLANAliasCIDR
			aliasName = "home2_lan_alias_cidr"
			slog.Warn("home2_lan_alias_cidr is deprecated; rename it to exit_lan_alias_cidr")
		}
		if len(alias) > 0 && json.Unmarshal(alias, &cfg.ExitLANAliasCIDR) != nil {
			err = fmt.Errorf("%s must be an IPv4 network", aliasName)
		}
	}
	if err == nil {
		cfg.ExitLANAliasKey = aliasName
		err = cfg.validate(aliasName)
	}
	if err != nil {
		return Server{}, sig, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, sig, nil
}

// LoadClient reads and validates a client config. Missing fields keep defaults.
func LoadClient(path string) (Client, error) {
	cfg := DefaultClient()
	_, err := load(path, &cfg)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		return Client{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (c Server) Validate() error {
	return c.validate("exit_lan_alias_cidr")
}

func (c Server) validate(aliasName string) error {
	if err := validateAPIKey(c.APIKey); err != nil {
		return err
	}
	if c.ListenPort < 1 || c.ListenPort > 65535 {
		return errors.New("listen_port must be 1-65535")
	}
	if err := validateTunnel(c.TunnelCIDR); err != nil {
		return err
	}
	if err := validateAlias(aliasName, c.ExitLANAliasCIDR, c.TunnelCIDR); err != nil {
		return err
	}
	if err := validateMTU(c.MTU); err != nil {
		return err
	}
	return c.Reconnect.validate()
}

func (c Client) Validate() error {
	if err := validateAPIKey(c.APIKey); err != nil {
		return err
	}
	if err := validateServerAddress(c.ServerAddress); err != nil {
		return err
	}
	if err := validateTunnel(c.TunnelCIDR); err != nil {
		return err
	}
	if c.LANAlias.Enabled {
		if err := validateAlias("lan_alias.alias_cidr", c.LANAlias.AliasCIDR, c.TunnelCIDR); err != nil {
			return err
		}
		if c.LANAlias.LANCIDR != "auto" {
			lan, err := netip.ParsePrefix(c.LANAlias.LANCIDR)
			if err != nil || !lan.Addr().Is4() || lan != lan.Masked() {
				return errors.New(`lan_alias.lan_cidr must be "auto" or an IPv4 network such as 192.168.1.0/24`)
			}
		}
	}
	if err := validateMTU(c.MTU); err != nil {
		return err
	}
	return c.Reconnect.validate()
}

func validateAPIKey(key string) error {
	if key == "" {
		return errors.New("api_key is empty; copy it from the server config")
	}
	if len(key) < MinAPIKeyLen {
		return fmt.Errorf("api_key must be at least %d characters", MinAPIKeyLen)
	}
	return nil
}

func validateServerAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return errors.New("server_address must be host:port")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return errors.New("server_address port must be 1-65535")
	}
	return nil
}

func validateIPv4Net(name string, p netip.Prefix) error {
	if !p.IsValid() || !p.Addr().Is4() || p != p.Masked() {
		return fmt.Errorf("%s must be an IPv4 network such as 10.77.0.0/30", name)
	}
	return nil
}

func validateTunnel(p netip.Prefix) error {
	if err := validateIPv4Net("tunnel_cidr", p); err != nil {
		return err
	}
	// Needs .1 (server) and .2 (client).
	if p.Bits() > 30 {
		return errors.New("tunnel_cidr is too small; use /30 or larger")
	}
	return nil
}

func validateAlias(name string, alias, tunnel netip.Prefix) error {
	if err := validateIPv4Net(name, alias); err != nil {
		return err
	}
	if alias.Overlaps(tunnel) {
		return fmt.Errorf("%s overlaps tunnel_cidr", name)
	}
	return nil
}

func validateMTU(mtu int) error {
	if mtu < 576 || mtu > 1500 {
		return errors.New("mtu must be 576-1500")
	}
	return nil
}

func (r Reconnect) validate() error {
	if r.DeadPeerTimeoutSec < MinDeadPeerTimeoutSec {
		return fmt.Errorf("reconnect.dead_peer_timeout_sec must be at least %d", MinDeadPeerTimeoutSec)
	}
	if r.RetryIntervalSec < 1 {
		return errors.New("reconnect.retry_interval_sec must be at least 1")
	}
	if r.MaxRetries < 0 {
		return errors.New("reconnect.max_retries must be 0 (forever) or more")
	}
	return nil
}
