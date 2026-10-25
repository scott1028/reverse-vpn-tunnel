package tunnel

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/keys"
)

func filledKey(b byte) keys.Key {
	var k keys.Key
	for i := range k {
		k[i] = b
	}
	return k
}

func TestConfigUAPI(t *testing.T) {
	c := Config{
		PrivateKey:    filledKey(0x11),
		ListenPort:    51820,
		FwMark:        0x5256,
		PeerPublicKey: filledKey(0x22),
		PresharedKey:  filledKey(0x33),
		AllowedIPs:    []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	}
	want := "private_key=" + strings.Repeat("11", 32) + "\n" +
		"listen_port=51820\n" +
		"fwmark=21078\n" +
		"replace_peers=true\n" +
		"public_key=" + strings.Repeat("22", 32) + "\n" +
		"preshared_key=" + strings.Repeat("33", 32) + "\n" +
		"persistent_keepalive_interval=15\n" +
		"replace_allowed_ips=true\n" +
		"allowed_ip=0.0.0.0/0\n"
	if got := c.UAPI(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}

	// Client reconnect: peer only, with endpoint.
	c = Config{
		PeerPublicKey: filledKey(0x22),
		PresharedKey:  filledKey(0x33),
		Endpoint:      netip.MustParseAddrPort("203.0.113.7:51820"),
		AllowedIPs:    []netip.Prefix{netip.MustParsePrefix("10.77.0.0/30")},
	}
	got := c.UAPI()
	if strings.Contains(got, "private_key") || strings.Contains(got, "listen_port") || strings.Contains(got, "fwmark") {
		t.Fatalf("unexpected device fields:\n%s", got)
	}
	if !strings.Contains(got, "endpoint=203.0.113.7:51820\n") {
		t.Fatalf("endpoint missing:\n%s", got)
	}
}

func TestRedact(t *testing.T) {
	uapi := Config{PrivateKey: filledKey(0x11), PeerPublicKey: filledKey(0x22), PresharedKey: filledKey(0x33)}.UAPI()
	got := Redact(uapi)
	if strings.Contains(got, strings.Repeat("11", 32)) || strings.Contains(got, strings.Repeat("33", 32)) {
		t.Fatalf("secret leaked: %s", got)
	}
	if !strings.Contains(got, "private_key=(redacted)") || !strings.Contains(got, "public_key="+strings.Repeat("22", 32)) {
		t.Fatalf("unexpected redaction: %s", got)
	}
}

func TestParseStats(t *testing.T) {
	uapi := "private_key=" + strings.Repeat("11", 32) + "\n" +
		"listen_port=51820\n" +
		"public_key=" + strings.Repeat("22", 32) + "\n" +
		"preshared_key=" + strings.Repeat("33", 32) + "\n" +
		"protocol_version=1\n" +
		"endpoint=203.0.113.7:40000\n" +
		"last_handshake_time_sec=1700000000\n" +
		"last_handshake_time_nsec=500\n" +
		"tx_bytes=1234\n" +
		"rx_bytes=5678\n" +
		"persistent_keepalive_interval=15\n" +
		"allowed_ip=0.0.0.0/0\n"
	got, err := ParseStats(uapi)
	if err != nil {
		t.Fatal(err)
	}
	want := Stats{LastHandshake: time.Unix(1700000000, 500), RxBytes: 5678, TxBytes: 1234, Endpoint: "203.0.113.7:40000"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	got, err = ParseStats("public_key=00\nlast_handshake_time_sec=0\nlast_handshake_time_nsec=0\nrx_bytes=0\n")
	if err != nil || !got.LastHandshake.IsZero() {
		t.Fatalf("no handshake must give zero time: %+v %v", got, err)
	}
	for _, uapi := range []string{"rx_bytes=abc\n", "tx_bytes=invalid\n"} {
		if _, err := ParseStats(uapi); err == nil {
			t.Fatalf("want parse error for %q", uapi)
		}
	}
}

func TestLiveness(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	type sample struct {
		sec       int
		rx        uint64
		handshake int // seconds from t0; -1 = never
		want      bool
	}
	tests := []struct {
		name    string
		samples []sample
	}{
		{"waiting: no handshake", []sample{
			{0, 0, -1, false},
			{2, 148, -1, false}, // handshake initiation counts as rx
		}},
		{"alive then dead after timeout", []sample{
			{0, 0, -1, false},
			{2, 500, 1, true},
			{20, 500, 1, true},
			{47, 500, 1, true},
			{48, 500, 1, false},
		}},
		{"rx keeps flowing but handshake too old", []sample{
			{0, 100, 0, true},
			{199, 200, 0, true},
			{200, 300, 0, false},
		}},
		{"hysteresis: rx resumes with old handshake", []sample{
			{0, 100, 0, true},
			{50, 100, 0, false}, // dead at 50
			{52, 200, 0, false}, // rx again, but handshake not newer than death
			{54, 300, 30, false},
			{56, 400, 55, true}, // fresh handshake
		}},
		{"counter reset by peer replace counts as rx", []sample{
			{0, 900, 0, true},
			{50, 900, 0, false},
			{52, 92, 51, true},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewLiveness(45*time.Second, false)
			for _, s := range tt.samples {
				st := Stats{RxBytes: s.rx}
				if s.handshake >= 0 {
					st.LastHandshake = at(s.handshake)
				}
				if got := l.Update(at(s.sec), time.Duration(s.sec)*time.Second, st); got != s.want {
					t.Fatalf("t=%ds rx=%d hs=%d: alive=%v, want %v", s.sec, s.rx, s.handshake, got, s.want)
				}
			}
		})
	}
}

func TestLivenessIgnoresWallClockJumps(t *testing.T) {
	wall := time.Unix(1_700_000_000, 0)
	l := NewLiveness(45*time.Second, false)
	if !l.Update(wall, 0, Stats{RxBytes: 100, LastHandshake: wall}) {
		t.Fatal("want alive")
	}
	// NTP moves the wall clock back an hour while 46s really pass.
	if l.Update(wall.Add(-time.Hour), 46*time.Second, Stats{RxBytes: 100, LastHandshake: wall}) {
		t.Fatal("rx age must follow the monotonic clock")
	}
}

func TestShouldProbe(t *testing.T) {
	wall := time.Unix(1_700_000_000, 0)
	l := NewLiveness(45*time.Second, false)
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	update := func(n int, rx uint64) { l.Update(wall.Add(sec(n)), sec(n), Stats{RxBytes: rx, LastHandshake: wall}) }

	update(0, 100)
	update(16, 100)
	if l.ShouldProbe(sec(16)) {
		t.Fatal("probe before ProbeAfter")
	}
	update(18, 100)
	if !l.ShouldProbe(sec(18)) {
		t.Fatal("no probe after ProbeAfter")
	}
	update(20, 200) // the peer answered
	if l.ShouldProbe(sec(20)) {
		t.Fatal("probe right after rx")
	}
	update(66, 200) // dead now
	if l.ShouldProbe(sec(66)) {
		t.Fatal("probe while dead")
	}
}

func TestMinDeadPeerTimeoutCoversWorstRxGap(t *testing.T) {
	if WorstRxGap != 32*time.Second || LostReplyRxGap != 35334*time.Millisecond {
		t.Fatalf("WorstRxGap = %v, LostReplyRxGap = %v; update the formula comment on config.MinDeadPeerTimeoutSec", WorstRxGap, LostReplyRxGap)
	}
	got := time.Duration(config.MinDeadPeerTimeoutSec) * time.Second
	// A lost probe is re-sent a tick later; keep 1s for the round trip.
	if need := WorstRxGap + TickInterval + time.Second; got < need {
		t.Fatalf("MinDeadPeerTimeoutSec = %v, need at least %v for a lost probe", got, need)
	}
	// A lost reply waits for the new handshake; add the tick the probe may
	// leave late and 1s round trip.
	if need := LostReplyRxGap + time.Second + TickInterval; got < need {
		t.Fatalf("MinDeadPeerTimeoutSec = %v, need at least %v for a lost reply", got, need)
	}
}

func TestFastDeadAfterMatchesDocs(t *testing.T) {
	// The documented 25-second limit is the value agreed with the user.
	if FastDeadAfter != 25*time.Second {
		t.Fatalf("FastDeadAfter = %v, want 25s", FastDeadAfter)
	}
}

func TestKeepaliveTxAllowanceMatchesDocs(t *testing.T) {
	if KeepaliveTxAllowance != 64 {
		t.Fatalf("KeepaliveTxAllowance = %d, want 64", KeepaliveTxAllowance)
	}
}

func TestGoodbyeRequiresNewHandshake(t *testing.T) {
	wall := time.Unix(1_700_000_000, 0)
	live := NewLiveness(45*time.Second, false)
	stats := Stats{RxBytes: 100, LastHandshake: wall}
	if !live.Update(wall, 0, stats) {
		t.Fatal("initial peer is not alive")
	}
	live.MarkDead(wall.Add(time.Second))
	if live.ShouldProbe(20 * time.Second) {
		t.Fatal("goodbye must mark dead immediately")
	}
	stats.RxBytes++
	if live.Update(wall.Add(2*time.Second), 2*time.Second, stats) {
		t.Fatal("old handshake revived peer after goodbye")
	}
	stats.RxBytes++
	stats.LastHandshake = wall.Add(3 * time.Second)
	if !live.Update(wall.Add(4*time.Second), 4*time.Second, stats) {
		t.Fatal("fresh handshake did not revive peer")
	}
}

func TestKeepalivesAndIdleProbesKeepConfiguredTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{45 * time.Second, 120 * time.Second} {
		for _, probe := range []bool{false, true} {
			wall := time.Unix(1_700_000_000, 0)
			live := NewLiveness(timeout, true)
			stats := Stats{RxBytes: 100, LastHandshake: wall}
			for elapsed := time.Duration(0); elapsed <= timeout+TickInterval; elapsed += TickInterval {
				stats.TxBytes += KeepaliveTxAllowance
				got := live.Update(wall.Add(elapsed), elapsed, stats)
				if want := elapsed <= timeout; got != want {
					t.Fatalf("timeout=%s probe=%v elapsed=%s: alive=%v want %v", timeout, probe, elapsed, got, want)
				}
				if probe && live.ShouldProbe(elapsed) {
					live.RecordProbe()
					stats.TxBytes += probeTransportSize
				}
			}
		}
	}
}

func TestUnansweredDataDiesAtFastDeadline(t *testing.T) {
	wall := time.Unix(1_700_000_000, 0)
	live := NewLiveness(45*time.Second, true)
	stats := Stats{RxBytes: 100, LastHandshake: wall}
	live.Update(wall, 0, stats)
	stats.TxBytes = KeepaliveTxAllowance + 1
	live.Update(wall.Add(TickInterval), TickInterval, stats)
	probeAt := TickInterval + PeerReplyWithin + TickInterval
	if live.ShouldProbe(probeAt-time.Nanosecond) || !live.ShouldProbe(probeAt) {
		t.Fatal("unanswered traffic probe does not start after the reply window")
	}
	deadline := TickInterval + FastDeadAfter
	if !live.Update(wall.Add(deadline-time.Nanosecond), deadline-time.Nanosecond, stats) {
		t.Fatal("peer died before fast deadline")
	}
	if live.Update(wall.Add(deadline), deadline, stats) {
		t.Fatal("peer alive at fast deadline")
	}
	stats.RxBytes++
	if live.Update(wall.Add(deadline+TickInterval), deadline+TickInterval, stats) {
		t.Fatal("fast death accepted an old handshake")
	}
}

func TestReplyResetsUnansweredTraffic(t *testing.T) {
	for _, replyAt := range []time.Duration{5 * time.Second, TickInterval + FastDeadAfter - time.Nanosecond} {
		wall := time.Unix(1_700_000_000, 0)
		live := NewLiveness(45*time.Second, true)
		stats := Stats{RxBytes: 100, LastHandshake: wall}
		live.Update(wall, 0, stats)
		stats.TxBytes += 1000
		live.Update(wall.Add(TickInterval), TickInterval, stats)
		stats.RxBytes++
		if !live.Update(wall.Add(replyAt), replyAt, stats) {
			t.Fatal("reply did not keep peer alive")
		}
		if !live.Update(wall.Add(30*time.Second), 30*time.Second, stats) {
			t.Fatalf("reply at %v did not clear first unanswered data tick", replyAt)
		}
	}
}

func TestExitNodeDoesNotUseFastDeadline(t *testing.T) {
	wall := time.Unix(1_700_000_000, 0)
	live := NewLiveness(45*time.Second, false)
	stats := Stats{RxBytes: 100, LastHandshake: wall}
	live.Update(wall, 0, stats)
	for elapsed := TickInterval; elapsed <= 44*time.Second; elapsed += TickInterval {
		stats.TxBytes += 1000
		if !live.Update(wall.Add(elapsed), elapsed, stats) {
			t.Fatal("exit node used the fast deadline")
		}
	}
}
