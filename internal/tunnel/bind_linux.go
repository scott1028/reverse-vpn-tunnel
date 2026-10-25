package tunnel

import (
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/conn"
)

const ifaceName = "rvt0"

// newBind uses the standard bind; on Linux fwmark keeps WireGuard packets out of the tunnel.
func newBind() conn.Bind { return conn.NewDefaultBind() }

func setBindInterface(conn.Bind, string) error { return nil }

func bindControlSocket(fd int, iface string) error {
	return unix.SetsockoptString(fd, unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
}
