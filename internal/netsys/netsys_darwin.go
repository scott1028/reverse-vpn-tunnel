package netsys

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
)

const pfAnchor = "com.apple/reverse-vpn-tunnel"

var (
	fullTunnelDsts = []string{"0.0.0.0/1", "128.0.0.0/1"}
	v6RejectDsts   = []string{"::/1", "8000::/1"}
)

// SetupInterface assigns the point-to-point tunnel address and brings iface up.
// The utun name and address are recorded so a later start can recognize it.
func (s *System) SetupInterface(iface string, local netip.Prefix, peer netip.Addr) error {
	s.iface, s.local = iface, local.Addr()
	s.state.Utun, s.state.UtunAddr = iface, s.local.String()
	if err := s.state.save(); err != nil {
		return err
	}
	mask := net.IP(net.CIDRMask(local.Bits(), 32)).String()
	if _, err := s.r.Run("ifconfig", iface, "inet", s.local.String(), peer.String(), "netmask", mask, "up"); err != nil {
		return err
	}
	return s.AddRoute(local.Masked())
}

// AddRoute routes dst into the tunnel. It goes away with the interface.
func (s *System) AddRoute(dst netip.Prefix) error {
	_, err := s.r.Run("route", "-n", "add", "-net", dst.String(), "-interface", s.iface)
	return err
}

// ApplyFullTunnel overrides the default route with 0/1 + 128/1 into utun.
// The WireGuard socket is pinned to the physical interface (IP_BOUND_IF).
func (s *System) ApplyFullTunnel(blockIPv6 bool) error {
	for _, dst := range fullTunnelDsts {
		if err := s.addRoute([]string{"-net", dst, "-interface", s.iface}, []string{"-net", dst, "-interface", s.iface}); err != nil {
			return err
		}
	}
	s.SetIPv6Block(blockIPv6)
	return nil
}

// SetIPv6Block adds reject routes so IPv6 fails fast and apps fall back to IPv4.
// Failures are only logged; whatever was added is still removed on rollback.
func (s *System) SetIPv6Block(on bool) {
	if err := s.deleteRoutes(isIPv6Route); err != nil {
		slog.Warn("IPv6 block not removed", "err", err)
	}
	if !on {
		return
	}
	for _, dst := range v6RejectDsts {
		if err := s.addRoute([]string{"-inet6", "-net", dst, "::1", "-reject"}, v6RejectDel(dst)); err != nil {
			slog.Warn("IPv6 block not applied", "err", err)
			return
		}
	}
}

func v6RejectDel(dst string) []string { return []string{"-inet6", "-net", dst, "::1"} }

func isIPv6Route(del []string) bool { return slices.Contains(del, "-inet6") }

// addRoute runs `route -n add <add>` and remembers how to delete it.
func (s *System) addRoute(add, del []string) error {
	if _, err := s.r.Run("route", append([]string{"-n", "add"}, add...)...); err != nil {
		return err
	}
	s.routes = append(s.routes, del)
	return nil
}

// deleteRoutes deletes the remembered routes that match, in add order; a
// failed one stays remembered for a retry.
func (s *System) deleteRoutes(match func(del []string) bool) error {
	var errs []error
	var kept [][]string
	for _, del := range s.routes {
		if match(del) {
			err := s.deleteRoute(del)
			if err == nil {
				continue
			}
			errs = append(errs, err)
		}
		kept = append(kept, del)
	}
	s.routes = kept
	return errors.Join(errs...)
}

// deleteRoute runs `route -n delete <del>`; a route that is already gone counts as deleted.
func (s *System) deleteRoute(del []string) error {
	out, err := s.r.Run("route", append([]string{"-n", "delete"}, del...)...)
	if err != nil && strings.Contains(out+err.Error(), "not in table") {
		return nil
	}
	return err
}

// RollbackFullTunnel restores normal routing and reports anything left behind.
// Safe when nothing is applied.
func (s *System) RollbackFullTunnel() error {
	if s.iface == "" {
		return errors.Join(s.cleanupStaleRoutes(), s.restoreSysctls())
	}
	all := func([]string) bool { return true }
	return errors.Join(s.deleteRoutes(all), s.restoreSysctls())
}

// cleanupStaleRoutes removes the routes of a crashed run, but only from a utun
// that still holds our tunnel address: another VPN may own that name now.
func (s *System) cleanupStaleRoutes() error {
	utun, addr := s.state.Utun, s.state.UtunAddr
	if utun == "" {
		return nil
	}
	out, err := s.r.Query("ifconfig", utun)
	if err == nil && strings.Contains(out, "inet "+addr+" ") {
		var errs []error
		for _, del := range staleRouteDels(utun) {
			if err := s.deleteRoute(del); err != nil {
				errs = append(errs, err)
				// Any later rollback retries it, even after a new utun is set up.
				s.routes = append(s.routes, del)
			}
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
	} else {
		// IPv4 routes went away with the utun; the IPv6 reject routes are not
		// tied to it and cannot be told apart from another VPN's.
		slog.Warn("utun of the previous run is gone or not ours; if IPv6 stays unreachable, remove leftovers with: sudo route -n delete -inet6 -net ::/1 ::1; sudo route -n delete -inet6 -net 8000::/1 ::1", "utun", utun)
	}
	s.state.Utun, s.state.UtunAddr = "", ""
	return s.state.save()
}

// staleRouteDels are the delete args of every route a run may add on utun.
func staleRouteDels(utun string) [][]string {
	var dels [][]string
	for _, dst := range fullTunnelDsts {
		dels = append(dels, []string{"-net", dst, "-interface", utun})
	}
	for _, dst := range v6RejectDsts {
		dels = append(dels, v6RejectDel(dst))
	}
	return dels
}

// CleanupHints lists commands that undo what a previous run left, for the
// user to run by hand when the automatic cleanup failed.
func (s *System) CleanupHints(isServer bool) []string {
	var cmds []string
	if isServer {
		utun := s.state.Utun
		if utun == "" {
			utun = "<utunN>"
		}
		for _, del := range staleRouteDels(utun) {
			cmds = append(cmds, "sudo route -n delete "+strings.Join(del, " "))
		}
	} else {
		cmds = append(cmds, "sudo pfctl -a "+pfAnchor+" -F all")
		if s.state.PFToken != "" {
			cmds = append(cmds, "sudo pfctl -X "+s.state.PFToken)
		}
	}
	return append(cmds, s.stateHints()...)
}

// ApplyForwarding enables forwarding and loads NAT + LAN alias rules into our
// pf anchor, holding a pf enable reference until RemoveForwarding.
func (s *System) ApplyForwarding(f Forwarding) error {
	s.fwd = f
	s.isFwdOn = true
	if err := s.setSysctl("net.inet.ip.forwarding", "", "1"); err != nil {
		return err
	}
	out, err := s.r.Query("pfctl", "-s", "nat")
	if err != nil {
		return err
	}
	if !strings.Contains(out, `nat-anchor "com.apple/*"`) || !strings.Contains(out, `rdr-anchor "com.apple/*"`) {
		return errors.New(`pf main ruleset has no nat-anchor/rdr-anchor "com.apple/*"; restore the default /etc/pf.conf and run: sudo pfctl -f /etc/pf.conf`)
	}
	if err := s.loadAnchor(); err != nil {
		return err
	}
	if s.state.PFToken != "" {
		return nil
	}
	out, err = s.r.Run("pfctl", "-E")
	if err != nil {
		return err
	}
	if s.state.PFToken, err = ParsePFToken(out); err != nil {
		return err
	}
	return s.state.save()
}

func (s *System) loadAnchor() error {
	if s.fwd.Egress == "" {
		return errors.New("no egress interface for pf nat")
	}
	rules := fmt.Sprintf("nat on %s inet from %s to any -> (%s)\n", s.fwd.Egress, s.fwd.Tunnel, s.fwd.Egress)
	if s.fwd.Alias.IsValid() {
		rules += fmt.Sprintf("rdr on %s inet from any to %s -> %s bitmask\n", s.iface, s.fwd.Alias, s.fwd.LAN)
	}
	f, err := os.CreateTemp("", "reverse-vpn-tunnel-pf-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(rules); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	slog.Info("pf anchor rules", "anchor", pfAnchor, "rules", strings.TrimSpace(rules))
	_, err = s.r.Run("pfctl", "-a", pfAnchor, "-f", f.Name())
	return err
}

// RepairForwarding reloads the anchor when `pfctl -f` flushed it.
func (s *System) RepairForwarding() error {
	if !s.isFwdOn {
		return nil
	}
	out, err := s.r.Query("pfctl", "-a", pfAnchor, "-s", "nat")
	if err == nil && strings.Contains(out, "nat on ") {
		return nil
	}
	slog.Warn("pf anchor rules missing, reloading", "anchor", pfAnchor)
	return s.loadAnchor()
}

// SetEgress reloads the nat rule when the physical interface changes.
func (s *System) SetEgress(name string) error {
	if name == s.fwd.Egress {
		return nil
	}
	s.fwd.Egress = name
	if !s.isFwdOn {
		return nil
	}
	return s.loadAnchor()
}

// RemoveForwarding flushes our anchor, releases the pf reference and restores
// sysctls. Safe when nothing is applied. The token is kept until released.
func (s *System) RemoveForwarding() error {
	s.isFwdOn = false
	var errs []error
	if _, err := s.r.Run("pfctl", "-a", pfAnchor, "-F", "all"); err != nil {
		errs = append(errs, err)
	}
	if s.state.PFToken != "" {
		if _, err := s.r.Run("pfctl", "-X", s.state.PFToken); err != nil {
			errs = append(errs, err)
		} else {
			s.state.PFToken = ""
		}
	}
	// The client has no utun routes to undo; they go away with the interface.
	s.state.Utun, s.state.UtunAddr = "", ""
	errs = append(errs, s.restoreSysctls())
	return errors.Join(errs...)
}

// DefaultRoute returns the physical default route.
func (s *System) DefaultRoute() (Route, error) {
	out, err := s.r.Query("route", "-n", "get", "default")
	if err != nil {
		return Route{}, err
	}
	r, err := ParseRouteGet(out)
	if err == nil && r.Dev == s.iface {
		// Pinning the socket to utun would loop WireGuard into itself.
		return Route{}, fmt.Errorf("default route points into %s", s.iface)
	}
	return r, err
}

// routePrefixes returns nothing on macOS: only interface networks are checked.
func (s *System) routePrefixes() ([]netip.Prefix, error) { return nil, nil }
