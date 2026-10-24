package server

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/netsys"
	"reverse-vpn-tunnel/internal/tunnel"
)

type fakeTunnel struct {
	stats       tunnel.Stats
	bindUpdates int
	probes      []netip.Addr
}

func (f *fakeTunnel) Stats() (tunnel.Stats, error)  { return f.stats, nil }
func (f *fakeTunnel) BindUpdate() error             { f.bindUpdates++; return nil }
func (f *fakeTunnel) SetBindInterface(string) error { return nil }
func (f *fakeTunnel) Probe(dst netip.Addr) error    { f.probes = append(f.probes, dst); return nil }

type fakeSystem struct {
	calls    []string
	applyErr error
	// rollbackFails is how many rollbacks fail before one succeeds.
	rollbackFails int
}

func (f *fakeSystem) ApplyFullTunnel(blockIPv6 bool) error {
	f.calls = append(f.calls, map[bool]string{true: "apply+v6block", false: "apply"}[blockIPv6])
	return f.applyErr
}
func (f *fakeSystem) SetIPv6Block(on bool) {
	f.calls = append(f.calls, map[bool]string{true: "v6block on", false: "v6block off"}[on])
}
func (f *fakeSystem) RollbackFullTunnel() error {
	f.calls = append(f.calls, "rollback")
	if f.rollbackFails > 0 {
		f.rollbackFails--
		return errors.New("ip: temporary failure")
	}
	return nil
}
func (f *fakeSystem) DefaultRoute() (netsys.Route, error) {
	return netsys.Route{Dev: "eth0"}, nil
}

// event is a tick at sec. Ticks every 2s since the previous event repeat the
// previous counters (unless isSleep); wantCalls covers all of them.
type event struct {
	sec       int
	rx        uint64
	handshake int // seconds; -1 = never
	edit      func(*config.Server)
	reloadErr bool
	isSleep   bool
	wantCalls []string
	wantErr   error
}

func runEvents(t *testing.T, cfg config.Server, sys *fakeSystem, events []event) *fakeTunnel {
	t.Helper()
	t0 := time.Unix(1_700_000_000, 0)
	tun := &fakeTunnel{}
	var pending *event
	reload := func() (config.Server, bool, error) {
		switch {
		case pending == nil:
			return config.Server{}, false, nil
		case pending.reloadErr:
			return config.Server{}, false, errors.New("invalid JSONC")
		case pending.edit != nil:
			next := cfg
			pending.edit(&next)
			cfg = next
			return next, true, nil
		}
		return config.Server{}, false, nil
	}
	s := New(cfg, tun, sys, reload)
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	prev := -TickSec
	for _, ev := range events {
		sys.calls = nil
		pending = nil
		for sec := prev + TickSec; sec < ev.sec && !ev.isSleep; sec += TickSec {
			if err := s.Step(at(sec)); err != nil {
				t.Fatalf("t=%d: %v", sec, err)
			}
		}
		prev = ev.sec
		pending = &ev
		tun.stats = tunnel.Stats{RxBytes: ev.rx}
		if ev.handshake >= 0 {
			tun.stats.LastHandshake = at(ev.handshake)
		}
		err := s.Step(at(ev.sec))
		if !errors.Is(err, ev.wantErr) {
			t.Fatalf("t=%d: err %v, want %v", ev.sec, err, ev.wantErr)
		}
		if !reflect.DeepEqual(sys.calls, ev.wantCalls) && (len(sys.calls) > 0 || len(ev.wantCalls) > 0) {
			t.Fatalf("t=%d: calls %v, want %v", ev.sec, sys.calls, ev.wantCalls)
		}
	}
	return tun
}

const TickSec = int(TickInterval / time.Second)

func testConfig() config.Server {
	cfg := config.DefaultServer()
	cfg.Reconnect = config.Reconnect{DeadPeerTimeoutSec: 20, RetryIntervalSec: 10, MaxRetries: 0}
	return cfg
}

func TestServerLoop(t *testing.T) {
	setForward := func(on bool) func(*config.Server) {
		return func(c *config.Server) { c.ForwardAllTraffic = on }
	}
	tests := []struct {
		name     string
		cfg      func(*config.Server)
		applyErr error
		events   []event
	}{
		{"alive applies full tunnel", nil, nil, []event{
			{sec: 0, handshake: -1},
			{sec: 2, rx: 148, handshake: -1}, // handshake init only
			{sec: 4, rx: 500, handshake: 3, wantCalls: []string{"apply+v6block"}},
			{sec: 6, rx: 600, handshake: 3},
		}},
		{"flag false rolls back, tunnel stays; true re-applies", nil, nil, []event{
			{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block"}},
			{sec: 2, rx: 600, handshake: 0, edit: setForward(false), wantCalls: []string{"rollback"}},
			{sec: 4, rx: 700, handshake: 0},
			{sec: 6, rx: 800, handshake: 0, edit: setForward(true), wantCalls: []string{"apply+v6block"}},
		}},
		{"flag false at start: never applied", func(c *config.Server) { c.ForwardAllTraffic = false }, nil, []event{
			{sec: 0, rx: 500, handshake: 0},
			{sec: 2, rx: 600, handshake: 0},
		}},
		{"block_ipv6 toggled live", nil, nil, []event{
			{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block"}},
			{sec: 2, rx: 600, handshake: 0, edit: func(c *config.Server) { c.BlockIPv6 = false }, wantCalls: []string{"v6block off"}},
		}},
		{"invalid config keeps previous settings", nil, nil, []event{
			{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block"}},
			{sec: 2, rx: 600, handshake: 0, reloadErr: true},
		}},
		{"dead fails open immediately, alive again re-applies", nil, nil, []event{
			{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block"}},
			{sec: 20, rx: 500, handshake: 0},
			{sec: 22, rx: 500, handshake: 0, wantCalls: []string{"rollback"}}, // 22s > dead_peer_timeout
			{sec: 24, rx: 600, handshake: 0},                                  // old handshake: hysteresis
			{sec: 26, rx: 700, handshake: 25, wantCalls: []string{"apply+v6block"}},
		}},
		{"apply failure rolls back and retries next tick", nil, errors.New("ip: not found"), []event{
			{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block", "rollback"}},
			{sec: 2, rx: 600, handshake: 0, wantCalls: []string{"apply+v6block", "rollback"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			runEvents(t, cfg, &fakeSystem{applyErr: tt.applyErr}, tt.events)
		})
	}
}

func TestServerRetriesExhausted(t *testing.T) {
	cfg := testConfig()
	cfg.Reconnect.MaxRetries = 2
	tun := runEvents(t, cfg, &fakeSystem{}, []event{
		{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block"}},
		{sec: 22, rx: 500, handshake: 0, wantCalls: []string{"rollback"}}, // dead at 22
		{sec: 32, rx: 500, handshake: 0},                                  // attempt 1
		{sec: 42, rx: 500, handshake: 0},                                  // attempt 2
		{sec: 50, rx: 500, handshake: 0},
		{sec: 52, rx: 500, handshake: 0, wantErr: ErrRetriesExhausted},
	})
	// First tick binds the egress; each attempt reopens the socket.
	if tun.bindUpdates != 3 {
		t.Fatalf("bind updates = %d, want 3", tun.bindUpdates)
	}
}

func TestServerWaitsForeverBeforeFirstPeer(t *testing.T) {
	cfg := testConfig()
	cfg.Reconnect.MaxRetries = 1
	runEvents(t, cfg, &fakeSystem{}, []event{
		{sec: 0, handshake: -1},
		{sec: 100, handshake: -1},
		{sec: 200, handshake: -1},
	})
}

func TestServerWakeUpRebinds(t *testing.T) {
	tun := runEvents(t, testConfig(), &fakeSystem{}, []event{
		{sec: 0, handshake: -1},
		{sec: 2, handshake: -1},
		{sec: 300, handshake: -1, isSleep: true},
		{sec: 302, handshake: -1},
	})
	if tun.bindUpdates != 2 {
		t.Fatalf("bind updates = %d, want 2 (start + wake)", tun.bindUpdates)
	}
}

func TestServerRetriesFailedRollback(t *testing.T) {
	setForward := func(on bool) func(*config.Server) {
		return func(c *config.Server) { c.ForwardAllTraffic = on }
	}
	t.Run("peer dead", func(t *testing.T) {
		runEvents(t, testConfig(), &fakeSystem{rollbackFails: 2}, []event{
			{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block"}},
			{sec: 22, rx: 500, handshake: 0, wantCalls: []string{"rollback"}}, // fails
			{sec: 24, rx: 500, handshake: 0, wantCalls: []string{"rollback"}}, // fails
			{sec: 26, rx: 500, handshake: 0, wantCalls: []string{"rollback"}}, // succeeds
			{sec: 28, rx: 500, handshake: 0},
		})
	})
	t.Run("leftovers are cleared before re-applying", func(t *testing.T) {
		runEvents(t, testConfig(), &fakeSystem{rollbackFails: 1}, []event{
			{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block"}},
			{sec: 2, rx: 600, handshake: 0, edit: setForward(false), wantCalls: []string{"rollback"}}, // fails
			{sec: 4, rx: 700, handshake: 0, edit: setForward(true), wantCalls: []string{"rollback", "apply+v6block"}},
		})
	})
	t.Run("close reports a failed rollback", func(t *testing.T) {
		s := New(testConfig(), &fakeTunnel{}, &fakeSystem{rollbackFails: 1}, func() (config.Server, bool, error) { return config.Server{}, false, nil })
		if err := s.Close(); err == nil {
			t.Fatal("Close reported success")
		}
	})
}

func TestServerProbesQuietPeer(t *testing.T) {
	cfg := testConfig()
	cfg.Reconnect.DeadPeerTimeoutSec = 35
	tun := runEvents(t, cfg, &fakeSystem{}, []event{
		{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block"}},
		{sec: 16, rx: 500, handshake: 0},
	})
	if len(tun.probes) != 0 {
		t.Fatalf("probed before %v: %v", tunnel.ProbeAfter, tun.probes)
	}
	tun = runEvents(t, cfg, &fakeSystem{}, []event{
		{sec: 0, rx: 500, handshake: 0, wantCalls: []string{"apply+v6block"}},
		{sec: 18, rx: 500, handshake: 0},
	})
	if len(tun.probes) != 1 || tun.probes[0] != netip.MustParseAddr("10.77.0.2") {
		t.Fatalf("probes %v, want one to the client tunnel IP", tun.probes)
	}
}

func TestServerSlowStepIsNotWake(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	clock := t0
	tun := &fakeTunnel{}
	sys := &slowRouteSystem{fakeSystem: &fakeSystem{}, advance: func() { clock = clock.Add(8 * time.Second) }}
	s := New(testConfig(), tun, sys, func() (config.Server, bool, error) { return config.Server{}, false, nil })
	now := func() time.Time { return clock }
	if err := s.tick(now); err != nil { // the route query takes 8s
		t.Fatal(err)
	}
	sys.advance = func() {}
	clock = clock.Add(2 * time.Second)
	if err := s.tick(now); err != nil {
		t.Fatal(err)
	}
	if tun.bindUpdates != 1 {
		t.Fatalf("bind updates = %d, want 1 (no wake)", tun.bindUpdates)
	}
}

// slowRouteSystem advances a fake clock while the route query runs.
type slowRouteSystem struct {
	*fakeSystem
	advance func()
}

func (s *slowRouteSystem) DefaultRoute() (netsys.Route, error) {
	s.advance()
	return s.fakeSystem.DefaultRoute()
}
