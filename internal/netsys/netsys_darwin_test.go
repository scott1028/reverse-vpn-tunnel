package netsys

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestDarwinPartialIPv6BlockIsRolledBack(t *testing.T) {
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		if cmd == "route -n add -inet6 -net 8000::/1 ::1 -reject" {
			return "", errors.New("temporary failure")
		}
		return "", nil
	}}
	s := newTestSystem(t, r)
	if err := s.SetupInterface("utun9", netip.MustParsePrefix("10.77.0.1/30"), netip.MustParseAddr("10.77.0.2")); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyFullTunnel(true); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil
	if err := s.RollbackFullTunnel(); err != nil {
		t.Fatal(err)
	}
	assertCmds(t, r.cmds, []string{
		"route -n delete -net 0.0.0.0/1 -interface utun9",
		"route -n delete -net 128.0.0.0/1 -interface utun9",
		"route -n delete -inet6 -net ::/1 ::1",
	})
}

func TestDarwinFailedRouteDeleteIsRetried(t *testing.T) {
	isFailing := true
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		if isFailing && cmd == "route -n delete -net 128.0.0.0/1 -interface utun9" {
			return "", errors.New("temporary failure")
		}
		if cmd == "route -n delete -net 0.0.0.0/1 -interface utun9" && !isFailing {
			return "route: writing to routing socket: not in table\n", errors.New("exit status 1")
		}
		return "", nil
	}}
	s := newTestSystem(t, r)
	s.SetupInterface("utun9", netip.MustParsePrefix("10.77.0.1/30"), netip.MustParseAddr("10.77.0.2"))
	s.ApplyFullTunnel(false)
	if err := s.RollbackFullTunnel(); err == nil {
		t.Fatal("rollback reported success")
	}
	isFailing = false
	r.cmds = nil
	if err := s.RollbackFullTunnel(); err != nil {
		t.Fatal(err)
	}
	assertCmds(t, r.cmds, []string{"route -n delete -net 128.0.0.0/1 -interface utun9"})
}

func TestDarwinStartupCleanupChecksUtunOwner(t *testing.T) {
	tests := []struct {
		name      string
		ifconfig  string
		ifErr     error
		wantRoute bool
	}{
		{"utun still ours", "utun9: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1280\n\tinet 10.77.0.1 --> 10.77.0.2 netmask 0xfffffffc\n", nil, true},
		{"utun now another VPN", "utun9: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1280\n\tinet 10.8.0.2 --> 10.8.0.1 netmask 0xffffffff\n", nil, false},
		{"utun gone", "ifconfig: interface utun9 does not exist\n", errors.New("exit status 1"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{respond: func(cmd string) (string, error) {
				if cmd == "ifconfig utun9" {
					return tt.ifconfig, tt.ifErr
				}
				return "", nil
			}}
			s := newTestSystem(t, r)
			s.state.Utun, s.state.UtunAddr = "utun9", "10.77.0.1"
			if err := s.RollbackFullTunnel(); err != nil {
				t.Fatal(err)
			}
			hasRouteCmd := slices.ContainsFunc(r.cmds, func(c string) bool { return strings.HasPrefix(c, "route ") })
			if hasRouteCmd != tt.wantRoute {
				t.Fatalf("route commands %v, want any=%v", r.cmds, tt.wantRoute)
			}
			if tt.wantRoute && len(r.cmds) != 4 {
				t.Fatalf("want 4 route deletes, got %v", r.cmds)
			}
			if s.state.Utun != "" || s.state.UtunAddr != "" {
				t.Fatalf("utun record not dropped: %+v", s.state)
			}
		})
	}
}

func TestDarwinFailedPFReleaseKeepsToken(t *testing.T) {
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "pfctl -X ") {
			return "", errors.New("temporary failure")
		}
		return "", nil
	}}
	s := newTestSystem(t, r)
	s.state.PFToken = "123"
	if err := s.RemoveForwarding(); err == nil {
		t.Fatal("want pf release error")
	}
	if s.state.PFToken != "123" {
		t.Fatalf("token dropped: %q", s.state.PFToken)
	}
}

func TestDarwinStaleRouteFailureIsRetriedLater(t *testing.T) {
	isFailing := true
	r := &fakeRunner{respond: func(cmd string) (string, error) {
		switch {
		case cmd == "ifconfig utun9":
			return "utun9: flags=8051<UP>\n\tinet 10.77.0.1 --> 10.77.0.2 netmask 0xfffffffc\n", nil
		case isFailing && cmd == "route -n delete -inet6 -net ::/1 ::1":
			return "", errors.New("temporary failure")
		}
		return "", nil
	}}
	s := newTestSystem(t, r)
	s.state.Utun, s.state.UtunAddr = "utun9", "10.77.0.1"
	if err := s.RollbackFullTunnel(); err == nil {
		t.Fatal("startup cleanup reported success")
	}
	if s.state.Utun != "utun9" {
		t.Fatal("utun record dropped although a route is left")
	}
	isFailing = false
	s.SetupInterface("utun10", netip.MustParsePrefix("10.77.0.1/30"), netip.MustParseAddr("10.77.0.2"))
	r.cmds = nil
	if err := s.RollbackFullTunnel(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.cmds, "route -n delete -inet6 -net ::/1 ::1") {
		t.Fatalf("stale route not retried: %v", r.cmds)
	}
}

func TestDarwinCleanupHints(t *testing.T) {
	s := newTestSystem(t, &fakeRunner{})
	s.state.Utun, s.state.PFToken = "utun9", "123"
	server := s.CleanupHints(true)
	if !slices.Contains(server, "sudo route -n delete -net 0.0.0.0/1 -interface utun9") || !slices.Contains(server, "sudo route -n delete -inet6 -net 8000::/1 ::1") {
		t.Errorf("server hints: %v", server)
	}
	client := s.CleanupHints(false)
	if !slices.Contains(client, "sudo pfctl -X 123") || !slices.Contains(client, "sudo pfctl -a com.apple/reverse-vpn-tunnel -F all") {
		t.Errorf("client hints: %v", client)
	}
}
