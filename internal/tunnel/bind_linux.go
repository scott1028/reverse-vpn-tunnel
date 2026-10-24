package tunnel

import "golang.zx2c4.com/wireguard/conn"

const ifaceName = "rvt0"

// newBind uses the standard bind; on Linux fwmark keeps WireGuard packets out of the tunnel.
func newBind() conn.Bind { return conn.NewDefaultBind() }

func setBindInterface(conn.Bind, string) error { return nil }
