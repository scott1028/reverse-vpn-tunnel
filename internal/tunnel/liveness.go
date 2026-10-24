package tunnel

import (
	"time"

	"golang.zx2c4.com/wireguard/device"
)

const (
	// MaxHandshakeAge is above the ~2 min rekey interval and below session expiry.
	MaxHandshakeAge = 200 * time.Second

	// TickInterval is how often the server and client loops sample the peer.
	TickInterval = 2 * time.Second

	// ProbeAfter is how long rx may stay quiet before we ask the peer for a
	// packet. Just above the 15s keepalive, so a healthy tunnel is rarely probed.
	ProbeAfter = 18 * time.Second

	// PeerReplyWithin is wireguard-go's passive keepalive: a peer that got a
	// data packet and sent nothing back sends a keepalive this soon.
	PeerReplyWithin = device.KeepaliveTimeout

	// WorstRxGap is the longest rx silence the loop sees from a healthy peer
	// when only probes make it talk (both keepalive timers lost the race):
	// the probe leaves up to one tick after ProbeAfter, the peer answers
	// within PeerReplyWithin, the answer is seen up to one tick later.
	// Round trip time and lost probes (each re-sent one tick later) add to it.
	WorstRxGap = ProbeAfter + TickInterval + PeerReplyWithin + TickInterval

	// RekeyJitter is the most wireguard-go adds to its new-handshake timer.
	RekeyJitter = device.RekeyTimeoutJitterMaxMs * time.Millisecond

	// LostReplyRxGap covers a lost peer reply when the peer host drops the
	// echo: then nothing answers until the first probe's data packet fires
	// wireguard-go's new-handshake timer (PeerReplyWithin + RekeyTimeout +
	// jitter), whose handshake makes the peer talk again; seen up to one tick
	// later. Not included, unlike WorstRxGap: the tick the probe may leave
	// after ProbeAfter, and round trip time.
	LostReplyRxGap = ProbeAfter + PeerReplyWithin + device.RekeyTimeout + RekeyJitter + TickInterval
)

// Liveness decides from peer counters whether the peer is reachable.
//
// rx alone is not enough: a client stuck retrying sends a handshake
// initiation every 5s, which also counts as rx. The wireguard-go responder
// only updates the handshake time after the peer's first data packet, so rx
// and handshake age together are reliable.
//
// Both sides keep persistent keepalive, but a received keepalive resets the
// local timer, so one side can stay silent for rounds. ShouldProbe covers it.
type Liveness struct {
	timeout time.Duration
	lastRx  uint64
	rxAt    time.Duration
	hasRx   bool
	deadAt  time.Time
	isAlive bool
}

func NewLiveness(timeout time.Duration) *Liveness {
	return &Liveness{timeout: timeout}
}

// Update feeds one sample and reports liveness. now is wall clock, compared
// with WireGuard handshake times; mono is a monotonic reading for rx age, so
// a clock change cannot stretch it. After a death only a handshake newer
// than the death counts (hysteresis).
func (l *Liveness) Update(now time.Time, mono time.Duration, s Stats) bool {
	// != instead of >: replacing the peer resets the counter.
	if s.RxBytes != l.lastRx {
		l.lastRx = s.RxBytes
		l.rxAt, l.hasRx = mono, true
	}
	isAlive := l.hasRx && mono-l.rxAt <= l.timeout &&
		!s.LastHandshake.IsZero() && now.Sub(s.LastHandshake) < MaxHandshakeAge &&
		s.LastHandshake.After(l.deadAt)
	if l.isAlive && !isAlive {
		l.deadAt = now
	}
	l.isAlive = isAlive
	return isAlive
}

// ShouldProbe reports whether a live peer has been quiet for ProbeAfter, so a
// probe should make it send something before the dead timeout.
func (l *Liveness) ShouldProbe(mono time.Duration) bool {
	return l.isAlive && mono-l.rxAt >= ProbeAfter
}
