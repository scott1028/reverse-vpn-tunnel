package tunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"syscall"
	"time"

	"reverse-vpn-tunnel/internal/config"

	"golang.zx2c4.com/wireguard/tun"
)

const goodbyeToken = "rvt-bye-1"

// goodbyeFilter only sees packets decrypted and authenticated by WireGuard.
type goodbyeFilter struct {
	tun.Device
	local, peer netip.Addr
	goodbyes    chan<- time.Time
}

func (f *goodbyeFilter) Write(bufs [][]byte, offset int) (int, error) {
	total := len(bufs)
	forward := bufs[:0]
	for _, buf := range bufs {
		if offset < 0 || offset > len(buf) || !acceptGoodbye(buf[offset:], f.local, f.peer) {
			forward = append(forward, buf)
			continue
		}
		select {
		case f.goodbyes <- time.Now():
		default:
		}
	}
	if len(forward) > 0 {
		if _, err := f.Device.Write(forward, offset); err != nil {
			return total - len(forward), err
		}
	}
	// tun.Device counts packets on success; native Linux may return bytes.
	return total, nil
}

// goodbyeReadFilter blocks forwarded goodbyes until this exit node stops.
type goodbyeReadFilter struct {
	tun.Device
	local, peer  netip.Addr
	allowGoodbye *atomic.Bool
}

func (f *goodbyeReadFilter) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := f.Device.Read(bufs, sizes, offset)
	if !f.allowGoodbye.Load() {
		for i := 0; i < n; i++ {
			size := sizes[i]
			if offset >= 0 && offset <= len(bufs[i]) && size >= 0 && size <= len(bufs[i])-offset &&
				acceptGoodbye(bufs[i][offset:offset+size], f.local, f.peer) {
				sizes[i] = 0
			}
		}
	}
	return n, err
}

func listenControl(ctx context.Context, iface string, local netip.AddrPort) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var bindErr error
		if err := raw.Control(func(fd uintptr) { bindErr = bindControlSocket(int(fd), iface) }); err != nil {
			return err
		}
		return bindErr
	}}
	conn, err := lc.ListenPacket(ctx, "udp4", local.String())
	if err != nil {
		return nil, err
	}
	return conn.(*net.UDPConn), nil
}

func acceptGoodbye(packet []byte, local, peer netip.Addr) bool {
	return len(packet) == 28+len(goodbyeToken) && packet[0] == 0x45 && packet[9] == 17 &&
		binary.BigEndian.Uint16(packet[2:4]) == uint16(len(packet)) &&
		binary.BigEndian.Uint16(packet[6:8])&^uint16(0x4000) == 0 &&
		netip.AddrFrom4([4]byte(packet[12:16])) == peer &&
		netip.AddrFrom4([4]byte(packet[16:20])) == local &&
		binary.BigEndian.Uint16(packet[22:24]) == config.ControlPort &&
		binary.BigEndian.Uint16(packet[24:26]) == uint16(len(packet)-20) &&
		bytes.Equal(packet[28:], []byte(goodbyeToken))
}

// SendGoodbye leaves at most 400ms for all writes and waits after a signal.
func (t *Tunnel) SendGoodbye(local, peer netip.Addr) error {
	t.allowGoodbye.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	conn, err := listenControl(ctx, t.name, netip.AddrPortFrom(local, 0))
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	var errs []error
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := conn.WriteToUDPAddrPort([]byte(goodbyeToken), netip.AddrPortFrom(peer, config.ControlPort)); err != nil {
			errs = append(errs, err)
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return errors.Join(append(errs, ctx.Err())...)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return errors.Join(errs...)
}
