package tunnel

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/conn"
)

// macOS assigns utunN itself.
const ifaceName = "utun"

// boundBind is an IPv4-only conn.Bind whose socket is pinned to the physical
// interface with IP_BOUND_IF, so WireGuard packets never loop into the
// tunnel while 0/1 + 128/1 point at utun.
type boundBind struct {
	mu      sync.Mutex
	ifindex int
	udp     *net.UDPConn
}

func newBind() conn.Bind { return &boundBind{} }

func setBindInterface(b conn.Bind, name string) error {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return err
	}
	bb := b.(*boundBind)
	bb.mu.Lock()
	bb.ifindex = ifc.Index
	bb.mu.Unlock()
	return nil
}

func (b *boundBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.udp != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	ifindex := b.ifindex
	lc := net.ListenConfig{Control: func(_, _ string, rc syscall.RawConn) error {
		if ifindex == 0 {
			return nil
		}
		var sockErr error
		err := rc.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifindex)
		})
		if err != nil {
			return err
		}
		return sockErr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":"+strconv.Itoa(int(port)))
	if err != nil {
		return nil, 0, err
	}
	udp := pc.(*net.UDPConn)
	b.udp = udp
	recv := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, addr, err := udp.ReadFromUDPAddrPort(packets[0])
		if err != nil {
			return 0, err
		}
		sizes[0] = n
		eps[0] = &conn.StdNetEndpoint{AddrPort: netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())}
		return 1, nil
	}
	return []conn.ReceiveFunc{recv}, uint16(udp.LocalAddr().(*net.UDPAddr).Port), nil
}

func (b *boundBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.udp == nil {
		return nil
	}
	err := b.udp.Close()
	b.udp = nil
	return err
}

func (b *boundBind) SetMark(uint32) error { return nil }

func (b *boundBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	b.mu.Lock()
	udp := b.udp
	b.mu.Unlock()
	if udp == nil {
		return net.ErrClosed
	}
	se, ok := ep.(*conn.StdNetEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	for _, buf := range bufs {
		if _, err := udp.WriteToUDPAddrPort(buf, se.AddrPort); err != nil {
			return err
		}
	}
	return nil
}

func (b *boundBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &conn.StdNetEndpoint{AddrPort: ap}, nil
}

func (b *boundBind) BatchSize() int { return 1 }
