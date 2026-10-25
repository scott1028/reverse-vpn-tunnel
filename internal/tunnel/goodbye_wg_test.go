package tunnel

import (
	"bytes"
	"net/netip"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn/bindtest"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/tuntest"

	"reverse-vpn-tunnel/internal/keys"
)

func TestGoodbyeFiltersWithRealWireGuard(t *testing.T) {
	for _, guardExit := range []bool{false, true} {
		t.Run(map[bool]string{false: "entry Write", true: "exit Read"}[guardExit], func(t *testing.T) {
			k, err := keys.Derive("idle-test-api-key-0123456789")
			if err != nil {
				t.Fatal(err)
			}
			binds := bindtest.NewChannelBinds()
			stun, ctun := tuntest.NewChannelTUN(), tuntest.NewChannelTUN()
			serverIP, clientIP := netip.MustParseAddr("10.77.0.1"), netip.MustParseAddr("10.77.0.2")
			goodbyes := make(chan time.Time, 1)
			entry := &goodbyeFilter{Device: stun.TUN(), local: serverIP, peer: clientIP, goodbyes: goodbyes}
			exit := &Tunnel{name: "rvt-nonexistent-test-interface"}
			var clientTUN tun.Device = ctun.TUN()
			if guardExit {
				clientTUN = &goodbyeReadFilter{Device: clientTUN, local: serverIP, peer: clientIP, allowGoodbye: &exit.allowGoodbye}
			}
			logger := device.NewLogger(device.LogLevelSilent, "")
			srv := device.NewDevice(entry, binds[1], logger)
			cli := device.NewDevice(clientTUN, binds[0], logger)
			t.Cleanup(srv.Close)
			t.Cleanup(cli.Close)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(srv.IpcSet(Config{PrivateKey: k.ServerPrivate, PeerPublicKey: k.ClientPublic, PresharedKey: k.PresharedKey,
				AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}}.UAPI()))
			must(cli.IpcSet(Config{PrivateKey: k.ClientPrivate, PeerPublicKey: k.ServerPublic, PresharedKey: k.PresharedKey,
				Endpoint: netip.MustParseAddrPort("127.0.0.1:1"), AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.77.0.0/30")}}.UAPI()))
			must(srv.Up())
			must(cli.Up())
			ping := tuntest.Ping(serverIP, clientIP)
			secondPing := tuntest.Ping(serverIP, netip.MustParseAddr("192.0.2.2"))
			forged := getGoodbyePacket()
			forged[12] = 192
			checkPackets := func(sent, want [][]byte, wantSignal bool) {
				t.Helper()
				deadline := time.After(5 * time.Second)
				for _, packet := range sent {
					select {
					case ctun.Outbound <- packet:
					case <-deadline:
						t.Fatal("timed out injecting packets")
					}
				}
				for i, packet := range want {
					select {
					case got := <-stun.Inbound:
						if !bytes.Equal(got, packet) {
							t.Fatalf("packet %d: got %x, want %x", i, got, packet)
						}
					case <-deadline:
						t.Fatalf("timed out waiting for packet %d", i)
					}
				}
				select {
				case packet := <-stun.Inbound:
					t.Fatalf("extra packet reached entry OS: %x", packet)
				case <-time.After(100 * time.Millisecond):
				}
				if (len(goodbyes) == 1) != wantSignal {
					t.Fatalf("pending goodbye signals = %d, want signal %v", len(goodbyes), wantSignal)
				}
			}
			if !guardExit {
				checkPackets([][]byte{ping, getGoodbyePacket(), forged, getGoodbyePacket(), secondPing}, [][]byte{ping, forged, secondPing}, true)
				return
			}
			checkPackets([][]byte{ping, getGoodbyePacket(), secondPing}, [][]byte{ping, secondPing}, false)
			// Start the real send method; the absent interface makes its socket fail.
			if err := exit.SendGoodbye(clientIP, serverIP); err == nil {
				t.Fatal("send on nonexistent interface unexpectedly succeeded")
			}
			checkPackets([][]byte{secondPing, getGoodbyePacket(), ping}, [][]byte{secondPing, ping}, true)
		})
	}
}
