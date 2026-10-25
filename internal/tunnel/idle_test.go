package tunnel

import (
	"encoding/hex"
	"net/netip"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/conn/bindtest"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/tuntest"

	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/keys"
)

// delayBind delivers every packet after a fixed one-way delay.
type delayBind struct {
	conn.Bind
	delay time.Duration
}

func (b delayBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	cp := make([][]byte, len(bufs))
	for i, buf := range bufs {
		cp[i] = append([]byte(nil), buf...)
	}
	time.AfterFunc(b.delay, func() { b.Bind.Send(cp, ep) })
	return nil
}

// idleSide samples one device the way the server / client loops do.
type idleSide struct {
	name       string
	dev        *device.Device
	tun        *tuntest.ChannelTUN
	self, peer netip.Addr
	live       *Liveness

	lastRx     uint64
	rxAt       time.Duration
	maxGap     time.Duration
	probes     int
	wasAlive   bool
	deadAtMono time.Duration
}

func (s *idleSide) sample(t *testing.T, now time.Time, mono time.Duration) {
	uapi, err := s.dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	st, err := ParseStats(uapi)
	if err != nil {
		t.Fatal(err)
	}
	isAlive := s.live.Update(now, mono, st)
	if st.RxBytes != s.lastRx {
		s.lastRx, s.rxAt = st.RxBytes, mono
	}
	if s.wasAlive {
		s.maxGap = max(s.maxGap, mono-s.rxAt)
		if !isAlive && s.deadAtMono == 0 {
			s.deadAtMono = mono
		}
	}
	s.wasAlive = s.wasAlive || isAlive
	if s.live.ShouldProbe(mono) {
		s.tun.Outbound <- tuntest.Ping(s.peer, s.self)
		s.live.RecordProbe()
		s.probes++
	}
}

// TestIdlePeerStaysAlive proves an idle tunnel is never declared dead at the
// minimum timeout: two wireguard-go devices with production UAPI config, one
// packet, then idle with a one-way delay. The peer host never answers the
// probe echo (like a firewall dropping it), so only wireguard-go replies.
func TestIdlePeerStaysAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("runs 80s of real time; run without -short")
	}
	const duration = 80 * time.Second
	k, err := keys.Derive("idle-test-api-key-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name             string
		isClientSilent   bool
		wantServerProbes bool
	}{
		// Worst case: the client never sends a persistent keepalive, as when
		// the server's timer always wins the race, so the server relies on probes.
		{"client keepalive never fires", true, true},
		{"both keepalive 15s", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			binds := bindtest.NewChannelBinds()
			stun, ctun := tuntest.NewChannelTUN(), tuntest.NewChannelTUN()
			logger := device.NewLogger(device.LogLevelSilent, "")
			srv := device.NewDevice(stun.TUN(), delayBind{binds[1], 20 * time.Millisecond}, logger)
			cli := device.NewDevice(ctun.TUN(), delayBind{binds[0], 20 * time.Millisecond}, logger)
			t.Cleanup(srv.Close)
			t.Cleanup(cli.Close)
			for _, ch := range []chan []byte{stun.Inbound, ctun.Inbound} {
				go func() {
					for range ch {
					}
				}()
			}

			tunnelCIDR := netip.MustParsePrefix("10.77.0.0/30")
			serverIP, clientIP := netip.MustParseAddr("10.77.0.1"), netip.MustParseAddr("10.77.0.2")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(srv.IpcSet(Config{
				PrivateKey: k.ServerPrivate, PeerPublicKey: k.ClientPublic, PresharedKey: k.PresharedKey,
				AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
			}.UAPI()))
			must(cli.IpcSet(Config{
				PrivateKey: k.ClientPrivate, PeerPublicKey: k.ServerPublic, PresharedKey: k.PresharedKey,
				Endpoint: netip.MustParseAddrPort("127.0.0.1:1"), AllowedIPs: []netip.Prefix{tunnelCIDR},
			}.UAPI()))
			if tt.isClientSilent {
				must(cli.IpcSet("public_key=" + hex.EncodeToString(k.ServerPublic[:]) + "\npersistent_keepalive_interval=0\n"))
			}
			must(srv.Up())
			must(cli.Up())
			ctun.Outbound <- tuntest.Ping(serverIP, clientIP)

			timeout := time.Duration(config.MinDeadPeerTimeoutSec) * time.Second
			sides := []*idleSide{
				{name: "server", dev: srv, tun: stun, self: serverIP, peer: clientIP, live: NewLiveness(timeout, true)},
				{name: "client", dev: cli, tun: ctun, self: clientIP, peer: serverIP, live: NewLiveness(timeout, false)},
			}
			start := time.Now()
			ticker := time.NewTicker(TickInterval)
			defer ticker.Stop()
			for now := range ticker.C {
				mono := now.Sub(start)
				for _, s := range sides {
					s.sample(t, now, mono)
				}
				if mono >= duration {
					break
				}
			}

			for _, s := range sides {
				t.Logf("%s: max rx gap %v, probes %d", s.name, s.maxGap, s.probes)
				if !s.wasAlive {
					t.Fatalf("%s never saw the peer alive", s.name)
				}
				if s.deadAtMono != 0 {
					t.Errorf("%s declared the idle peer dead at %v", s.name, s.deadAtMono)
				}
				if s.maxGap >= timeout {
					t.Errorf("%s max rx gap %v reaches the minimum timeout %v", s.name, s.maxGap, timeout)
				}
			}
			if srvSide := sides[0]; tt.wantServerProbes && (srvSide.probes < 2 || srvSide.maxGap < ProbeAfter) {
				t.Errorf("worst case not exercised: server probes %d, max gap %v", srvSide.probes, srvSide.maxGap)
			}
		})
	}
}
