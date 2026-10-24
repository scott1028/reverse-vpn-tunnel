// Package netsys configures OS networking (interfaces, routes, firewall).
package netsys

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// TunnelAddrs returns the server (.1) and client (.2) addresses of the tunnel network.
func TunnelAddrs(tunnel netip.Prefix) (server, client netip.Addr) {
	server = tunnel.Masked().Addr().Next()
	return server, server.Next()
}

// AliasRange returns the part of alias that maps 1:1 onto lan, keeping host bits:
// lan 192.168.1.0/24 + alias 10.78.0.0/16 → 10.78.0.0/24.
func AliasRange(lan, alias netip.Prefix) (netip.Prefix, error) {
	if alias.Bits() > lan.Bits() {
		return netip.Prefix{}, fmt.Errorf("alias %s is smaller than LAN %s", alias, lan)
	}
	return netip.PrefixFrom(alias.Addr(), lan.Bits()).Masked(), nil
}

// ValidateLAN rejects prefixes that are unlikely to be a home LAN.
func ValidateLAN(lan netip.Prefix) error {
	switch {
	case !lan.Addr().Is4():
		return fmt.Errorf("LAN %s is not IPv4", lan)
	case lan.Bits() > 30:
		return fmt.Errorf("LAN %s is a point-to-point or host prefix", lan)
	case lan.Bits() < 16:
		return fmt.Errorf("LAN %s is larger than /16", lan)
	case !lan.Addr().IsPrivate():
		return fmt.Errorf("LAN %s is not a private network", lan)
	}
	return nil
}

// DetectLAN returns the IPv4 network of iface, checked by ValidateLAN.
func DetectLAN(iface string) (netip.Prefix, error) {
	prefixes, err := interfacePrefixes(iface)
	if err != nil {
		return netip.Prefix{}, err
	}
	for _, p := range prefixes {
		if !p.Addr().Is4() {
			continue
		}
		lan := p.Masked()
		if err := ValidateLAN(lan); err != nil {
			return netip.Prefix{}, fmt.Errorf("auto-detect on %s: %w; set lan_alias.lan_cidr in the config", iface, err)
		}
		return lan, nil
	}
	return netip.Prefix{}, fmt.Errorf("no IPv4 address on %s; set lan_alias.lan_cidr in the config", iface)
}

// LocalPrefixes returns the IPv4 networks of all local interfaces except loopback.
func LocalPrefixes() ([]netip.Prefix, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		prefixes, err := interfacePrefixes(ifc.Name)
		if err != nil {
			return nil, err
		}
		for _, p := range prefixes {
			if p.Addr().Is4() {
				out = append(out, p.Masked())
			}
		}
	}
	return out, nil
}

func interfacePrefixes(name string) ([]netip.Prefix, error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok {
			continue
		}
		bits, _ := ipnet.Mask.Size()
		out = append(out, netip.PrefixFrom(addr.Unmap(), bits))
	}
	return out, nil
}

// NamedPrefix is a config network checked against local networks.
type NamedPrefix struct {
	Name   string
	Prefix netip.Prefix
}

// CheckOverlap fails if any of ours overlaps a local network. Default routes are ignored.
func CheckOverlap(ours []NamedPrefix, local []netip.Prefix) error {
	var errs []error
	for _, o := range ours {
		for _, l := range local {
			if l.Bits() > 0 && o.Prefix.Overlaps(l) {
				errs = append(errs, fmt.Errorf("%s %s overlaps local network %s; change it in the config (both sides must match)", o.Name, o.Prefix, l))
				break
			}
		}
	}
	return errors.Join(errs...)
}
