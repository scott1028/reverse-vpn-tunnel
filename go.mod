module reverse-vpn-tunnel

go 1.26.8

require (
	github.com/tailscale/hujson v0.0.0-20260727124030-b80ff77dac4f
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.60.0
	golang.org/x/sys v0.49.0
	golang.zx2c4.com/wireguard v0.0.0-20261024220952-b3bc704ef0ee
)

require golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
