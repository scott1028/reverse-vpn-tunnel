package netsys

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
)

// ruleProto tags our policy rules. It is unassigned in iproute2's rt_protos.
// The kernel treats unset attributes of a delete request as wildcards, but
// a protocol must match exactly, so deletes carrying it never hit rules of
// other programs at the same priority.
const ruleProto = "86"

// ipRule is one of our policy rules.
type ipRule struct {
	prio string
	// selector goes to ip rule add/del; shown is how `ip -N rule show` prints it.
	selector, shown string
}

// line is the rule as ParseRules returns it from `ip -N rule show`.
func (r ipRule) line() string { return r.prio + ": from all " + r.shown + " proto " + ruleProto }

func (r ipRule) args(family, action string) []string {
	return append([]string{family, "rule", action, "priority", r.prio, "protocol", ruleProto}, strings.Fields(r.selector)...)
}

// In add order; deleted in reverse so redirection stops first.
var ourRules = []ipRule{
	{"5250", "lookup main suppress_prefixlength 0", "lookup 254 suppress_prefixlength 0"},
	{"5251", "fwmark 0x5256 lookup main", "fwmark 0x5256 lookup 254"},
	// iif lo matches only locally generated traffic, not forwarded (Docker) traffic.
	{"5252", "iif lo lookup " + table, "iif lo lookup " + table},
}

type iptablesChain struct {
	table, parent, name string
}

var (
	chainFwd  = iptablesChain{"filter", "FORWARD", "RVT-FWD"}
	chainPost = iptablesChain{"nat", "POSTROUTING", "RVT-POST"}
	chainPre  = iptablesChain{"nat", "PREROUTING", "RVT-PRE"}
)

// SetupInterface assigns the tunnel address and brings iface up.
// peer is only needed for the macOS point-to-point setup.
func (s *System) SetupInterface(iface string, local netip.Prefix, _ netip.Addr) error {
	s.iface, s.local = iface, local.Addr()
	if _, err := s.r.Run("ip", "addr", "replace", local.String(), "dev", iface); err != nil {
		return err
	}
	_, err := s.r.Run("ip", "link", "set", "dev", iface, "up")
	return err
}

// AddRoute routes dst into the tunnel. It goes away with the interface.
func (s *System) AddRoute(dst netip.Prefix) error {
	_, err := s.r.Run("ip", "route", "replace", dst.String(), "dev", s.iface)
	return err
}

// ApplyFullTunnel sends all locally generated traffic into the tunnel.
// The WireGuard socket carries Mark, so its packets keep using the main table.
func (s *System) ApplyFullTunnel(blockIPv6 bool) error {
	if err := s.setSysctl("net.ipv4.conf.all.src_valid_mark", "", "1"); err != nil {
		return err
	}
	// Strict rp_filter drops the WireGuard handshake: its reverse route points into the tunnel.
	names, err := s.listInterfaces()
	if err != nil {
		return err
	}
	for _, name := range append([]string{"all"}, names...) {
		if name == "lo" {
			continue
		}
		key := "net.ipv4.conf." + strings.ReplaceAll(name, ".", "/") + ".rp_filter"
		if err := s.setSysctl(key, "1", "2"); err != nil {
			return err
		}
	}
	if _, err := s.r.Run("ip", "-4", "route", "replace", "default", "dev", s.iface, "src", s.local.String(), "table", table); err != nil {
		return err
	}
	if err := s.replaceRules("-4"); err != nil {
		return err
	}
	s.SetIPv6Block(blockIPv6)
	return nil
}

// SetIPv6Block makes IPv6 to non-local destinations fail fast while forwarding.
// Failures are only logged: hosts without IPv6 must keep working.
func (s *System) SetIPv6Block(on bool) {
	if err := s.removeIPv6Block(); err != nil {
		slog.Warn("IPv6 block not removed", "err", err)
	}
	if !on {
		return
	}
	_, err := s.r.Run("ip", "-6", "route", "replace", "unreachable", "default", "table", table)
	if err == nil {
		err = s.replaceRules("-6")
	}
	if err != nil {
		slog.Warn("IPv6 block not applied", "err", err)
	}
}

// removeIPv6Block deletes our IPv6 rules and table. Only "no IPv6 on this
// host" and "nothing there" are fine; a delete that fails is reported, no
// matter which run added the rule.
func (s *System) removeIPv6Block() error {
	return errors.Join(s.deleteRules("-6"), s.flushTable("-6"))
}

// isNoIPv6 reports an error of a host without IPv6, where nothing of ours can exist.
func isNoIPv6(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Address family not supported")
}

// RollbackFullTunnel restores normal routing and reports anything left behind.
// Safe when nothing is applied.
func (s *System) RollbackFullTunnel() error {
	return errors.Join(
		s.deleteRules("-4"),
		s.flushTable("-4"),
		s.removeIPv6Block(),
		s.restoreSysctls(),
	)
}

func (s *System) replaceRules(family string) error {
	if err := s.deleteRules(family); err != nil {
		return err
	}
	for _, r := range ourRules {
		if _, err := s.r.Run("ip", r.args(family, "add")...); err != nil {
			return err
		}
	}
	return nil
}

// deleteRules deletes our rules of one family. It refuses when a rule at our
// priorities carries our protocol but differs from ours: a delete could then
// still pick the wrong one.
func (s *System) deleteRules(family string) error {
	out, err := s.r.Query("ip", "-N", family, "rule", "show")
	if isNoIPv6(err) {
		return nil
	}
	if err != nil {
		return err
	}
	present := ParseRules(out)
	for _, line := range present {
		if strings.HasSuffix(line, " proto "+ruleProto) && isOurPriority(line) &&
			!slices.ContainsFunc(ourRules, func(r ipRule) bool { return r.line() == line }) {
			return fmt.Errorf("ip %s rule %q has our protocol %s but is not ours; not deleting", family, line, ruleProto)
		}
	}
	var errs []error
	for _, r := range slices.Backward(ourRules) {
		for _, line := range present {
			if line != r.line() {
				continue
			}
			if _, err := s.r.Run("ip", r.args(family, "del")...); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func isOurPriority(line string) bool {
	return slices.ContainsFunc(ourRules, func(r ipRule) bool { return strings.HasPrefix(line, r.prio+":") })
}

func (s *System) flushTable(family string) error {
	out, err := s.r.Query("ip", family, "route", "show", "table", table)
	switch {
	case isNoIPv6(err), err != nil && strings.Contains(err.Error(), "FIB table does not exist"):
		return nil // nothing of ours can be there
	case err != nil:
		return err
	case strings.TrimSpace(out) == "":
		return nil
	}
	_, err = s.r.Run("ip", family, "route", "flush", "table", table)
	return err
}

// CleanupHints lists commands that undo what a previous run left, for the
// user to run by hand when the automatic cleanup failed.
func (s *System) CleanupHints(isServer bool) []string {
	var cmds []string
	if isServer {
		for _, family := range []string{"-4", "-6"} {
			for _, r := range slices.Backward(ourRules) {
				cmds = append(cmds, "sudo ip "+strings.Join(r.args(family, "del"), " "))
			}
			cmds = append(cmds, "sudo ip "+family+" route flush table "+table)
		}
	} else {
		for _, c := range []iptablesChain{chainFwd, chainPost, chainPre} {
			cmds = append(cmds,
				"sudo iptables -t "+c.table+" -D "+c.parent+" -j "+c.name,
				"sudo iptables -t "+c.table+" -F "+c.name,
				"sudo iptables -t "+c.table+" -X "+c.name)
		}
	}
	return append(cmds, s.stateHints()...)
}

// ApplyForwarding enables forwarding, NAT and the LAN alias for tunnel traffic.
// Our jumps go first so a FORWARD DROP policy (Docker) does not block us.
func (s *System) ApplyForwarding(f Forwarding) error {
	s.fwd = f
	s.isFwdOn = true
	if err := s.setSysctl("net.ipv4.ip_forward", "", "1"); err != nil {
		return err
	}
	if err := s.removeChains(); err != nil {
		return err
	}
	rules := map[iptablesChain][][]string{
		chainFwd: {
			{"-i", s.iface, "-j", "ACCEPT"},
			{"-o", s.iface, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
		},
		// Also needed so hosts on this LAN reply to this host.
		chainPost: {{"-s", f.Tunnel.String(), "!", "-o", s.iface, "-j", "MASQUERADE"}},
		chainPre:  {{"-i", s.iface, "-d", f.Alias.String(), "-j", "NETMAP", "--to", f.LAN.String()}},
	}
	for _, c := range s.activeChains() {
		if _, err := s.r.Run("iptables", "-t", c.table, "-N", c.name); err != nil {
			return err
		}
		for _, rule := range rules[c] {
			if _, err := s.r.Run("iptables", append([]string{"-t", c.table, "-A", c.name}, rule...)...); err != nil {
				return err
			}
		}
		if _, err := s.r.Run("iptables", "-t", c.table, "-I", c.parent, "1", "-j", c.name); err != nil {
			return err
		}
	}
	return nil
}

func (s *System) activeChains() []iptablesChain {
	if s.fwd.Alias.IsValid() {
		return []iptablesChain{chainFwd, chainPost, chainPre}
	}
	return []iptablesChain{chainFwd, chainPost}
}

// jump is the parent chain rule as `iptables -S` prints it.
func (c iptablesChain) jump() string { return "-A " + c.parent + " -j " + c.name }

// listRules returns `iptables -t table -S`. A failed query is an error, never "absent".
func (s *System) listRules(table string) ([]string, error) {
	out, err := s.r.Query("iptables", "-t", table, "-S")
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(out), "\n"), nil
}

// RepairForwarding re-applies forwarding when a firewall reload removed a jump.
func (s *System) RepairForwarding() error {
	if !s.isFwdOn {
		return nil
	}
	rules := map[string][]string{}
	for _, c := range s.activeChains() {
		if _, ok := rules[c.table]; !ok {
			list, err := s.listRules(c.table)
			if err != nil {
				return err
			}
			rules[c.table] = list
		}
		if !slices.Contains(rules[c.table], c.jump()) {
			slog.Warn("forwarding rule missing, re-applying", "chain", c.name)
			return s.ApplyForwarding(s.fwd)
		}
	}
	return nil
}

// RemoveForwarding deletes our chains and restores sysctls. Safe when nothing
// is applied; reports anything it could not remove.
func (s *System) RemoveForwarding() error {
	s.isFwdOn = false
	return errors.Join(s.removeChains(), s.restoreSysctls())
}

func (s *System) removeChains() error {
	var errs []error
	for _, tbl := range []string{"filter", "nat"} {
		rules, err := s.listRules(tbl)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, c := range []iptablesChain{chainFwd, chainPost, chainPre} {
			if c.table != tbl {
				continue
			}
			for _, r := range rules {
				if r != c.jump() {
					continue
				}
				if _, err := s.r.Run("iptables", "-t", tbl, "-D", c.parent, "-j", c.name); err != nil {
					errs = append(errs, err)
				}
			}
			if !slices.Contains(rules, "-N "+c.name) {
				continue
			}
			if _, err := s.r.Run("iptables", "-t", tbl, "-F", c.name); err != nil {
				errs = append(errs, err)
				continue
			}
			if _, err := s.r.Run("iptables", "-t", tbl, "-X", c.name); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// SetEgress is a no-op on Linux: routing picks the egress per packet.
func (s *System) SetEgress(string) error { return nil }

// DefaultRoute returns the main table default route.
func (s *System) DefaultRoute() (Route, error) {
	routes, err := s.mainRoutes()
	if err != nil {
		return Route{}, err
	}
	return DefaultRoute(routes)
}

func (s *System) routePrefixes() ([]netip.Prefix, error) {
	routes, err := s.mainRoutes()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, r := range routes {
		if r.Dst.Bits() > 0 {
			out = append(out, r.Dst)
		}
	}
	return out, nil
}

func (s *System) mainRoutes() ([]Route, error) {
	out, err := s.r.Query("ip", "-4", "route", "show", "table", "main")
	if err != nil {
		return nil, err
	}
	return ParseIPRoutes(out)
}
