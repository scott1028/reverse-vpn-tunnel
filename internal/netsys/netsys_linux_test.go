package netsys

import (
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestServerFullTunnel(t *testing.T) {
	sysctls := map[string]string{
		"net.ipv4.conf.all.src_valid_mark": "0",
		"net.ipv4.conf.all.rp_filter":      "1",
		"net.ipv4.conf.eth0.rp_filter":     "1",
		"net.ipv4.conf.eth0/5.rp_filter":   "2",
		"net.ipv4.conf.rvt0.rp_filter":     "0",
	}
	rules := ""
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "sysctl -n "):
			return sysctls[strings.TrimPrefix(cmd, "sysctl -n ")] + "\n", nil
		case cmd == "ip -N -4 rule show":
			return rules, nil
		case strings.HasPrefix(cmd, "ip -N -6 rule show"), strings.HasPrefix(cmd, "ip -6 "):
			return "", errors.New("RTNETLINK answers: Address family not supported by protocol")
		case cmd == "ip -4 route show table 21078":
			return "default dev rvt0 scope link src 10.77.0.1\n", nil
		}
		return "", nil
	}}
	s := newTestSystem(t, r)

	if err := s.SetupInterface("rvt0", netip.MustParsePrefix("10.77.0.1/30"), netip.MustParseAddr("10.77.0.2")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoute(netip.MustParsePrefix("10.78.0.0/16")); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyFullTunnel(true); err != nil {
		t.Fatalf("IPv6 failure must not fail apply: %v", err)
	}
	assertCmds(t, r.cmds, []string{
		"ip addr replace 10.77.0.1/30 dev rvt0",
		"ip link set dev rvt0 up",
		"ip route replace 10.78.0.0/16 dev rvt0",
		"sysctl -w net.ipv4.conf.all.src_valid_mark=1",
		"sysctl -w net.ipv4.conf.all.rp_filter=2",
		"sysctl -w net.ipv4.conf.eth0.rp_filter=2",
		"ip -4 route replace default dev rvt0 src 10.77.0.1 table 21078",
		"ip -4 rule add priority 5250 protocol 86 lookup main suppress_prefixlength 0",
		"ip -4 rule add priority 5251 protocol 86 fwmark 0x5256 lookup main",
		"ip -4 rule add priority 5252 protocol 86 iif lo lookup 21078",
		"ip -6 route replace unreachable default table 21078",
	})

	// Rollback deletes only our rules, by protocol and full selector, highest
	// priority first. The host has no IPv6, so there is nothing to remove there.
	r.cmds = nil
	rules = fixture(t, "ip-rule-show.txt")
	if err := s.RollbackFullTunnel(); err != nil {
		t.Fatal(err)
	}
	assertCmds(t, r.cmds, []string{
		"ip -4 rule del priority 5252 protocol 86 iif lo lookup 21078",
		"ip -4 rule del priority 5252 protocol 86 iif lo lookup 21078",
		"ip -4 rule del priority 5251 protocol 86 fwmark 0x5256 lookup main",
		"ip -4 rule del priority 5250 protocol 86 lookup main suppress_prefixlength 0",
		"ip -4 route flush table 21078",
		"sysctl -w net.ipv4.conf.all.rp_filter=1",
		"sysctl -w net.ipv4.conf.all.src_valid_mark=0",
		"sysctl -w net.ipv4.conf.eth0.rp_filter=1",
	})
	if len(s.state.Sysctls) != 0 {
		t.Fatalf("state not cleared: %v", s.state.Sysctls)
	}
}

func TestRollbackReportsFailures(t *testing.T) {
	// A crashed run left rules and a table in both families; this process has
	// added nothing yet.
	for _, fail := range []string{
		"ip -N -4 rule show",
		"ip -4 rule del priority 5252 protocol 86 iif lo lookup 21078",
		"ip -4 route show table 21078",
		"ip -4 route flush table 21078",
		"ip -N -6 rule show",
		"ip -6 rule del priority 5252 protocol 86 iif lo lookup 21078",
		"ip -6 route show table 21078",
		"ip -6 route flush table 21078",
		"sysctl -w net.ipv4.conf.all.rp_filter=1",
	} {
		t.Run(fail, func(t *testing.T) {
			r := &fakeRunner{respond: func(cmd string) (string, error) {
				switch {
				case cmd == fail:
					return "", errors.New("temporary failure")
				case strings.HasSuffix(cmd, "rule show"):
					return "5252:\tfrom all iif lo lookup 21078 proto 86\n", nil
				case strings.Contains(cmd, "route show table"):
					return "default dev rvt0\n", nil
				}
				return "", nil
			}}
			s := newTestSystem(t, r)
			s.state.Sysctls["net.ipv4.conf.all.rp_filter"] = "1"
			if err := s.RollbackFullTunnel(); err == nil {
				t.Fatal("rollback reported success")
			}
		})
	}
}

func TestRollbackOnHostWithoutIPv6(t *testing.T) {
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		if strings.Contains(cmd, "-6 ") {
			return "", errors.New("RTNETLINK answers: Address family not supported by protocol")
		}
		return "", nil
	}}
	if err := newTestSystem(t, r).RollbackFullTunnel(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRulesRefusesAmbiguousRule(t *testing.T) {
	// Carries our protocol at our priority but has an extra fwmark: a
	// protocol-qualified delete of our 5250 rule could still remove it.
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		if cmd == "ip -N -4 rule show" {
			return "5250:\tfrom all fwmark 0x1 lookup 254 suppress_prefixlength 0 proto 86\n" +
				"5250:\tfrom all lookup 254 suppress_prefixlength 0 proto 86\n", nil
		}
		return "", nil
	}}
	s := newTestSystem(t, r)
	if err := s.deleteRules("-4"); err == nil || !strings.Contains(err.Error(), "not ours") {
		t.Fatalf("want refusal, got %v", err)
	}
	if len(r.cmds) != 0 {
		t.Fatalf("deleted despite ambiguity: %v", r.cmds)
	}
}

func TestIPv6BlockPartialApplyIsRolledBack(t *testing.T) {
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		switch {
		case cmd == "ip -6 rule add priority 5251 protocol 86 fwmark 0x5256 lookup main":
			return "", errors.New("temporary failure")
		case cmd == "ip -N -6 rule show":
			return "5250:\tfrom all lookup 254 suppress_prefixlength 0 proto 86\n", nil
		case cmd == "ip -6 route show table 21078":
			return "unreachable default dev lo\n", nil
		}
		return "", nil
	}}
	s := newTestSystem(t, r)
	s.SetIPv6Block(true)
	r.cmds = nil
	if err := s.RollbackFullTunnel(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ip -6 rule del priority 5250 protocol 86 lookup main suppress_prefixlength 0",
		"ip -6 route flush table 21078",
	} {
		if !slices.Contains(r.cmds, want) {
			t.Fatalf("missing %q in %v", want, r.cmds)
		}
	}
}

// fakeIptables keeps `iptables -S` lines per table and applies the commands
// ApplyForwarding / removeChains use.
type fakeIptables struct {
	tables    map[string][]string
	failCmd   string
	ipForward string
}

func newFakeIptables() *fakeIptables {
	return &fakeIptables{
		ipForward: "1",
		tables: map[string][]string{
			"filter": {"-P INPUT ACCEPT", "-P FORWARD DROP", "-P OUTPUT ACCEPT", "-A FORWARD -s 198.51.100.0/24 -j ACCEPT"},
			"nat":    {"-P PREROUTING ACCEPT", "-P INPUT ACCEPT", "-P OUTPUT ACCEPT", "-P POSTROUTING ACCEPT", "-A POSTROUTING -d 198.51.100.1/32 -j RETURN"},
		},
	}
}

func (f *fakeIptables) respond(cmd string) (string, error) {
	if cmd == f.failCmd {
		return "", errors.New("temporary failure")
	}
	if cmd == "sysctl -n net.ipv4.ip_forward" {
		return f.ipForward + "\n", nil
	}
	if v, ok := strings.CutPrefix(cmd, "sysctl -w net.ipv4.ip_forward="); ok {
		f.ipForward = v
		return "", nil
	}
	a := strings.Fields(cmd)
	if a[0] != "iptables" {
		return "", nil
	}
	tbl, op, rest := a[2], a[3], a[4:]
	lines := f.tables[tbl]
	switch op {
	case "-S":
		return strings.Join(lines, "\n") + "\n", nil
	case "-N":
		lines = append(lines, "-N "+rest[0])
	case "-A":
		lines = append(lines, "-A "+strings.Join(rest, " "))
	case "-I": // -I PARENT 1 -j CHAIN
		lines = append([]string{"-A " + rest[0] + " -j " + rest[3]}, lines...)
	case "-D":
		i := slices.Index(lines, "-A "+strings.Join(rest, " "))
		if i < 0 {
			return "", errors.New("bad rule")
		}
		lines = slices.Delete(lines, i, i+1)
	case "-F":
		lines = slices.DeleteFunc(lines, func(l string) bool { return strings.HasPrefix(l, "-A "+rest[0]+" ") })
	case "-X":
		lines = slices.DeleteFunc(lines, func(l string) bool { return l == "-N "+rest[0] })
	}
	f.tables[tbl] = lines
	return "", nil
}

func (f *fakeIptables) snapshot() map[string][]string {
	out := map[string][]string{}
	for k, v := range f.tables {
		out[k] = slices.Clone(v)
	}
	return out
}

var testFwd = Forwarding{
	Tunnel: netip.MustParsePrefix("10.77.0.0/30"),
	Alias:  netip.MustParsePrefix("10.78.0.0/16"),
	LAN:    netip.MustParsePrefix("192.168.1.0/24"),
}

func TestClientForwarding(t *testing.T) {
	ipt := newFakeIptables()
	ipt.ipForward = "0"
	before := ipt.snapshot()
	r := &fakeRunner{respond: ipt.respond}
	s := newTestSystem(t, r)
	s.iface = "rvt0"
	if err := s.ApplyForwarding(testFwd); err != nil {
		t.Fatal(err)
	}
	applied := []string{
		"iptables -t filter -N RVT-FWD",
		"iptables -t filter -A RVT-FWD -i rvt0 -j ACCEPT",
		"iptables -t filter -A RVT-FWD -o rvt0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
		"iptables -t filter -I FORWARD 1 -j RVT-FWD",
		"iptables -t nat -N RVT-POST",
		"iptables -t nat -A RVT-POST -s 10.77.0.0/30 ! -o rvt0 -j MASQUERADE",
		"iptables -t nat -I POSTROUTING 1 -j RVT-POST",
		"iptables -t nat -N RVT-PRE",
		"iptables -t nat -A RVT-PRE -i rvt0 -d 10.78.0.0/16 -j NETMAP --to 192.168.1.0/24",
		"iptables -t nat -I PREROUTING 1 -j RVT-PRE",
	}
	assertCmds(t, r.cmds, append([]string{"sysctl -w net.ipv4.ip_forward=1"}, applied...))

	// Intact rules: nothing to repair.
	r.cmds = nil
	if err := s.RepairForwarding(); err != nil || len(r.cmds) != 0 {
		t.Fatalf("unexpected repair: %v %v", r.cmds, err)
	}

	// A firewall reload dropped the POSTROUTING jump.
	ipt.respond("iptables -t nat -D POSTROUTING -j RVT-POST")
	if err := s.RepairForwarding(); err != nil {
		t.Fatal(err)
	}
	assertCmds(t, r.cmds, append([]string{
		"iptables -t filter -D FORWARD -j RVT-FWD",
		"iptables -t filter -F RVT-FWD",
		"iptables -t filter -X RVT-FWD",
		"iptables -t nat -F RVT-POST",
		"iptables -t nat -X RVT-POST",
		"iptables -t nat -D PREROUTING -j RVT-PRE",
		"iptables -t nat -F RVT-PRE",
		"iptables -t nat -X RVT-PRE",
	}, applied...))

	r.cmds = nil
	if err := s.RemoveForwarding(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ipt.tables, before) || ipt.ipForward != "0" {
		t.Fatalf("not restored:\n got %v ip_forward=%s\nwant %v ip_forward=0", ipt.tables, ipt.ipForward, before)
	}
	n := len(r.cmds)
	if err := s.RepairForwarding(); err != nil || len(r.cmds) != n {
		t.Fatalf("repair after remove must do nothing: %v %v", r.cmds[n:], err)
	}
}

func TestClientForwardingKeepsHostState(t *testing.T) {
	// Docker-like host: ip_forward=1, FORWARD policy DROP, other rules present.
	ipt := newFakeIptables()
	before := ipt.snapshot()
	r := &fakeRunner{respond: ipt.respond}
	s := newTestSystem(t, r)
	s.iface = "rvt0"
	if err := s.ApplyForwarding(Forwarding{Tunnel: testFwd.Tunnel}); err != nil {
		t.Fatal(err)
	}
	if ipt.tables["filter"][0] != "-A FORWARD -j RVT-FWD" {
		t.Fatalf("our jump is not first: %v", ipt.tables["filter"])
	}
	for _, c := range r.cmds {
		if strings.Contains(c, "RVT-PRE") || strings.Contains(c, "sysctl -w") {
			t.Fatalf("unexpected command %q", c)
		}
	}
	if err := s.RemoveForwarding(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ipt.tables, before) || ipt.ipForward != "1" || len(s.state.Sysctls) != 0 {
		t.Fatalf("host state changed: %v ip_forward=%s state=%v", ipt.tables, ipt.ipForward, s.state.Sysctls)
	}
}

func TestRemoveForwardingReportsFailures(t *testing.T) {
	for _, fail := range []string{
		"iptables -t filter -S",
		"iptables -t nat -S",
		"iptables -t filter -D FORWARD -j RVT-FWD",
		"iptables -t nat -F RVT-POST",
		"iptables -t nat -X RVT-PRE",
	} {
		t.Run(fail, func(t *testing.T) {
			ipt := newFakeIptables()
			r := &fakeRunner{respond: ipt.respond}
			s := newTestSystem(t, r)
			s.iface = "rvt0"
			if err := s.ApplyForwarding(testFwd); err != nil {
				t.Fatal(err)
			}
			ipt.failCmd = fail
			if err := s.RemoveForwarding(); err == nil {
				t.Fatal("RemoveForwarding reported success")
			}
		})
	}
}

func TestRepairDoesNotGuessOnQueryFailure(t *testing.T) {
	ipt := newFakeIptables()
	r := &fakeRunner{respond: ipt.respond}
	s := newTestSystem(t, r)
	s.iface = "rvt0"
	if err := s.ApplyForwarding(testFwd); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil
	ipt.failCmd = "iptables -t filter -S"
	if err := s.RepairForwarding(); err == nil || len(r.cmds) != 0 {
		t.Fatalf("want error and no changes, got %v %v", err, r.cmds)
	}
}

func TestParseRules(t *testing.T) {
	got := ParseRules(fixture(t, "ip-rule-show.txt"))
	if len(got) != 13 || got[4] != "5250: from all lookup 254 suppress_prefixlength 0 proto 86" || got[8] != "5252: from all iif lo lookup 52" {
		t.Fatalf("got %q", got)
	}
}

func TestRestoreKeepsRecordUnlessInterfaceGone(t *testing.T) {
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "sysctl -w") && (strings.Contains(cmd, "veth1") || strings.Contains(cmd, "eth0.")) {
			return "", errors.New("temporary failure")
		}
		return "", nil
	}}
	s := newTestSystem(t, r)
	s.state.Sysctls["net.ipv4.conf.veth1.rp_filter"] = "1" // interface gone: dropped
	s.state.Sysctls["net.ipv4.conf.eth0.rp_filter"] = "1"  // restore fails: kept
	s.state.Sysctls["net.ipv4.conf.rvt0.rp_filter"] = "1"  // restored: dropped
	if err := s.restoreSysctls(); err == nil || !strings.Contains(err.Error(), "temporary failure") {
		t.Fatalf("want eth0 error, got %v", err)
	}
	want := map[string]string{"net.ipv4.conf.eth0.rp_filter": "1"}
	if !reflect.DeepEqual(s.state.Sysctls, want) {
		t.Fatalf("state %v, want %v", s.state.Sysctls, want)
	}
}

func TestRollbackOnFreshHost(t *testing.T) {
	// Table 21078 was never created: listing it fails, but nothing is left behind.
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		if strings.Contains(cmd, "route show table 21078") {
			return "", errors.New("exit status 2: Error: ipv4: FIB table does not exist.\nDump terminated")
		}
		return "", nil
	}}
	s := newTestSystem(t, r)
	if err := s.RollbackFullTunnel(); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 0 {
		t.Fatalf("unexpected commands %v", r.cmds)
	}
}

func TestCleanupHints(t *testing.T) {
	s := newTestSystem(t, &fakeRunner{})
	s.state.Sysctls["net.ipv4.conf.all.rp_filter"] = "1"
	server := s.CleanupHints(true)
	for _, want := range []string{
		"sudo ip -4 rule del priority 5252 protocol 86 iif lo lookup 21078",
		"sudo ip -6 rule del priority 5250 protocol 86 lookup main suppress_prefixlength 0",
		"sudo ip -4 route flush table 21078",
		"sudo sysctl -w net.ipv4.conf.all.rp_filter=1",
	} {
		if !slices.Contains(server, want) {
			t.Errorf("server hints lack %q: %v", want, server)
		}
	}
	client := s.CleanupHints(false)
	if !slices.Contains(client, "sudo iptables -t nat -D POSTROUTING -j RVT-POST") || slices.ContainsFunc(client, func(c string) bool { return strings.Contains(c, "ip -4 rule") }) {
		t.Errorf("unexpected client hints: %v", client)
	}
}
