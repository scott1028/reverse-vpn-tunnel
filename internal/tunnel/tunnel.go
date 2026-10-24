// Package tunnel wraps an embedded userspace WireGuard device.
package tunnel

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// Tunnel is one TUN interface with a WireGuard device and a single peer.
type Tunnel struct {
	dev      *device.Device
	bind     conn.Bind
	name     string
	probeSeq int
}

// Open creates the TUN interface. The device stays down until Up.
func Open(mtu int) (*Tunnel, error) {
	tdev, err := tun.CreateTUN(ifaceName, mtu)
	if err != nil {
		return nil, fmt.Errorf("create TUN %s: %w", ifaceName, err)
	}
	name, err := tdev.Name()
	if err != nil {
		tdev.Close()
		return nil, err
	}
	logger := &device.Logger{
		Verbosef: device.DiscardLogf,
		Errorf: func(format string, args ...any) {
			slog.Warn("wireguard: " + fmt.Sprintf(format, args...))
		},
	}
	bind := newBind()
	return &Tunnel{dev: device.NewDevice(tdev, bind, logger), bind: bind, name: name}, nil
}

// Name is the OS interface name (rvt0, or utunN assigned by macOS).
func (t *Tunnel) Name() string { return t.name }

// Configure applies c via UAPI and logs it with keys redacted.
func (t *Tunnel) Configure(c Config) error {
	uapi := c.UAPI()
	slog.Info("wireguard config", "uapi", Redact(uapi))
	return t.dev.IpcSet(uapi)
}

func (t *Tunnel) Up() error { return t.dev.Up() }

func (t *Tunnel) Stats() (Stats, error) {
	uapi, err := t.dev.IpcGet()
	if err != nil {
		return Stats{}, err
	}
	return ParseStats(uapi)
}

// BindUpdate reopens the UDP socket, e.g. after a network change or wake-up.
func (t *Tunnel) BindUpdate() error { return t.dev.BindUpdate() }

// SetBindInterface pins the UDP socket to a physical interface (macOS only).
// Takes effect on the next BindUpdate.
func (t *Tunnel) SetBindInterface(name string) error { return setBindInterface(t.bind, name) }

// Probe sends one ICMP echo to the peer's tunnel address dst. Any data packet
// makes the peer's wireguard-go answer within PeerReplyWithin, even when the
// peer host drops the echo. ICMP echo is accepted without logging by a
// default ufw setup, unlike UDP to a closed port.
func (t *Tunnel) Probe(dst netip.Addr) error {
	c, err := net.ListenPacket("ip4:icmp", "")
	if err != nil {
		return err
	}
	defer c.Close()
	t.probeSeq++
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: t.probeSeq & 0xffff, Data: []byte("reverse-vpn-tunnel probe")},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		return err
	}
	_, err = c.WriteTo(b, &net.IPAddr{IP: dst.AsSlice()})
	return err
}

// Close stops the device and removes the TUN interface.
func (t *Tunnel) Close() { t.dev.Close() }
