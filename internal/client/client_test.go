package client

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/keys"
	"reverse-vpn-tunnel/internal/netsys"
	"reverse-vpn-tunnel/internal/tunnel"
)

type fakeTunnel struct {
	stats       tunnel.Stats
	configs     []tunnel.Config
	bindUpdates int
	boundIface  string
	probes      []netip.Addr
}

func (f *fakeTunnel) Configure(c tunnel.Config) error {
	f.configs = append(f.configs, c)
	f.stats.RxBytes = 0 // replacing the peer resets counters
	return nil
}
func (f *fakeTunnel) Stats() (tunnel.Stats, error)       { return f.stats, nil }
func (f *fakeTunnel) BindUpdate() error                  { f.bindUpdates++; return nil }
func (f *fakeTunnel) SetBindInterface(name string) error { f.boundIface = name; return nil }
func (f *fakeTunnel) Probe(dst netip.Addr) error         { f.probes = append(f.probes, dst); return nil }

type fakeSystem struct {
	repairs int
	egress  []string
	dev     string
}

func (f *fakeSystem) RepairForwarding() error { f.repairs++; return nil }
func (f *fakeSystem) SetEgress(name string) error {
	f.egress = append(f.egress, name)
	return nil
}
func (f *fakeSystem) DefaultRoute() (netsys.Route, error) { return netsys.Route{Dev: f.dev}, nil }

type harness struct {
	t0  time.Time
	sec int
	c   *Client
	tun *fakeTunnel
	sys *fakeSystem
	ips []netip.Addr
}

func newHarness(t *testing.T, maxRetries int) *harness {
	t.Helper()
	cfg := config.DefaultClient()
	cfg.ServerAddress = "home1.example.com:51820"
	cfg.Reconnect = config.Reconnect{DeadPeerTimeoutSec: 20, RetryIntervalSec: 10, MaxRetries: maxRetries}
	h := &harness{t0: time.Unix(1_700_000_000, 0), tun: &fakeTunnel{}, sys: &fakeSystem{dev: "eth0"}}
	h.ips = []netip.Addr{netip.MustParseAddr("203.0.113.7")}
	resolve := func(host string) (netip.Addr, error) {
		if host != "home1.example.com" {
			t.Fatalf("resolved %q", host)
		}
		ip := h.ips[0]
		if len(h.ips) > 1 {
			h.ips = h.ips[1:]
		}
		return ip, nil
	}
	h.c = New(cfg, keys.Keys{ClientPrivate: keys.Key{1}, ServerPublic: keys.Key{2}, PresharedKey: keys.Key{3}}, h.tun, h.sys, resolve)
	h.c.Start(func() time.Time { return h.t0 })
	return h
}

// tick advances 2s with the given counters.
func (h *harness) tick(t *testing.T, rx uint64, handshakeSec int) error {
	t.Helper()
	h.sec += 2
	h.tun.stats.RxBytes = rx
	h.tun.stats.LastHandshake = h.t0.Add(time.Duration(handshakeSec) * time.Second)
	return h.c.Step(h.t0.Add(time.Duration(h.sec) * time.Second))
}

func TestClientStartDials(t *testing.T) {
	h := newHarness(t, 0)
	if len(h.tun.configs) != 1 {
		t.Fatalf("configs = %d, want 1", len(h.tun.configs))
	}
	c := h.tun.configs[0]
	if c.Endpoint != netip.MustParseAddrPort("203.0.113.7:51820") || c.PrivateKey != (keys.Key{1}) ||
		c.PeerPublicKey != (keys.Key{2}) || c.PresharedKey != (keys.Key{3}) ||
		len(c.AllowedIPs) != 1 || c.AllowedIPs[0] != netip.MustParsePrefix("10.77.0.0/30") {
		t.Fatalf("unexpected config %+v", c)
	}
	if h.tun.boundIface != "eth0" || h.tun.bindUpdates != 1 || len(h.sys.egress) != 1 {
		t.Fatalf("egress not set up: %+v %+v", h.tun, h.sys)
	}
}

func TestClientRedialsAfterDeath(t *testing.T) {
	h := newHarness(t, 0)
	h.ips = []netip.Addr{netip.MustParseAddr("198.51.100.9")} // DDNS moved
	if err := h.tick(t, 500, 1); err != nil || !h.c.isAlive {
		t.Fatalf("want alive at 2s: %v", err)
	}
	// No rx: dead once more than 20s have passed since the last change at 2s.
	for h.sec < 24 {
		h.tick(t, 500, 1)
	}
	if h.c.isAlive {
		t.Fatal("still alive after dead_peer_timeout")
	}
	deadAt := h.sec
	for h.sec < deadAt+8 {
		h.tick(t, 500, 1)
	}
	if len(h.tun.configs) != 1 {
		t.Fatal("redialed before retry_interval")
	}
	h.tick(t, 500, 1)
	if len(h.tun.configs) != 2 || h.c.attempt != 1 {
		t.Fatalf("configs = %d attempt = %d, want redial", len(h.tun.configs), h.c.attempt)
	}
	if got := h.tun.configs[1].Endpoint; got != netip.MustParseAddrPort("198.51.100.9:51820") {
		t.Fatalf("endpoint %s not re-resolved", got)
	}
	// Fresh handshake after the redial: alive again, attempts reset.
	h.tick(t, 92, h.sec)
	if !h.c.isAlive || h.c.attempt != 0 {
		t.Fatalf("alive=%v attempt=%d", h.c.isAlive, h.c.attempt)
	}
}

func TestClientRetriesExhausted(t *testing.T) {
	h := newHarness(t, 2)
	var err error
	for h.sec < 100 && err == nil {
		err = h.tick(t, 0, -1_000_000)
	}
	if !errors.Is(err, ErrRetriesExhausted) {
		t.Fatalf("err = %v", err)
	}
	// Start dial + 2 redials, exit when the third would start (at 30s).
	if len(h.tun.configs) != 3 || h.sec != 30 {
		t.Fatalf("configs = %d at %ds", len(h.tun.configs), h.sec)
	}
}

func TestClientWakeUpRedials(t *testing.T) {
	h := newHarness(t, 0)
	h.tick(t, 500, 1)
	h.sec += 60 // asleep
	h.tick(t, 600, 1)
	if len(h.tun.configs) != 2 || h.tun.bindUpdates != 2 {
		t.Fatalf("configs = %d bindUpdates = %d, want redial + rebind on wake", len(h.tun.configs), h.tun.bindUpdates)
	}
}

func TestClientRepairsEvery30s(t *testing.T) {
	h := newHarness(t, 0)
	for h.sec < 60 {
		h.tick(t, uint64(h.sec), h.sec)
	}
	if h.sys.repairs != 2 {
		t.Fatalf("repairs = %d, want 2", h.sys.repairs)
	}
}

func TestClientEgressChange(t *testing.T) {
	h := newHarness(t, 0)
	h.sys.dev = "wlan0"
	h.tick(t, 500, 1)
	if h.tun.boundIface != "wlan0" || h.tun.bindUpdates != 2 || h.sys.egress[len(h.sys.egress)-1] != "wlan0" {
		t.Fatalf("egress change not followed: %+v %+v", h.tun, h.sys)
	}
}

func TestResolveIPv4(t *testing.T) {
	if ip, err := ResolveIPv4("192.0.2.1"); err != nil || ip != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("got %s, %v", ip, err)
	}
	if _, err := ResolveIPv4("2001:db8::1"); err == nil {
		t.Fatal("IPv6 literal must fail")
	}
}

func TestClientProbesQuietServer(t *testing.T) {
	h := newHarness(t, 0)
	h.c.live = tunnel.NewLiveness(35 * time.Second)
	h.tick(t, 500, 1)
	for h.sec < 20 {
		h.tick(t, 500, 1)
	}
	if len(h.tun.probes) != 1 || h.tun.probes[0] != netip.MustParseAddr("10.77.0.1") {
		t.Fatalf("probes %v, want one to the server tunnel IP", h.tun.probes)
	}
}

func TestClientSlowDNSIsNotWake(t *testing.T) {
	h := newHarness(t, 0)
	h.c.cfg.Reconnect.RetryIntervalSec = 30
	clock := h.t0.Add(60 * time.Second)
	h.c.lastTick, h.c.lastAttempt = clock.Add(-2*time.Second), clock.Add(-30*time.Second)
	isSlow := true
	h.c.resolve = func(string) (netip.Addr, error) {
		if isSlow {
			clock = clock.Add(8 * time.Second)
		}
		return netip.MustParseAddr("203.0.113.7"), nil
	}
	now := func() time.Time { return clock }
	// Never connected: this tick redials and DNS takes 8s.
	before := len(h.tun.configs)
	if err := h.c.tick(now); err != nil {
		t.Fatal(err)
	}
	if len(h.tun.configs) != before+1 {
		t.Fatal("retry did not redial")
	}
	isSlow = false
	before = len(h.tun.configs)
	clock = clock.Add(2 * time.Second)
	if err := h.c.tick(now); err != nil {
		t.Fatal(err)
	}
	if len(h.tun.configs) != before {
		t.Fatal("slow DNS was taken for a wake-up and redialed again")
	}
}

func TestClientSlowStartIsNotWake(t *testing.T) {
	cfg := config.DefaultClient()
	t0 := time.Unix(1_700_000_000, 0)
	clock := t0
	tun := &fakeTunnel{}
	resolve := func(string) (netip.Addr, error) {
		clock = clock.Add(8 * time.Second) // slow first DNS lookup
		return netip.MustParseAddr("203.0.113.7"), nil
	}
	c := New(cfg, keys.Keys{}, tun, &fakeSystem{dev: "eth0"}, resolve)
	c.Start(func() time.Time { return clock })
	tun.stats = tunnel.Stats{RxBytes: 500, LastHandshake: clock}
	clock = clock.Add(2 * time.Second)
	if err := c.Step(clock); err != nil {
		t.Fatal(err)
	}
	if len(tun.configs) != 1 {
		t.Fatalf("configs = %d: the slow start was taken for a wake-up", len(tun.configs))
	}
}
