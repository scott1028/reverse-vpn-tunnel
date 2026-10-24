package netsys

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var mp = netip.MustParsePrefix

func TestTunnelAddrs(t *testing.T) {
	s, c := TunnelAddrs(mp("10.77.0.0/30"))
	if s.String() != "10.77.0.1" || c.String() != "10.77.0.2" {
		t.Fatalf("got %s %s", s, c)
	}
}

func TestAliasRange(t *testing.T) {
	got, err := AliasRange(mp("192.168.1.0/24"), mp("10.78.0.0/16"))
	if err != nil || got != mp("10.78.0.0/24") {
		t.Fatalf("got %s, %v", got, err)
	}
	if _, err := AliasRange(mp("10.0.0.0/16"), mp("10.78.0.0/24")); err == nil {
		t.Fatal("alias smaller than LAN must fail")
	}
}

func TestValidateLAN(t *testing.T) {
	tests := []struct {
		lan     string
		wantErr string
	}{
		{"192.168.1.0/24", ""},
		{"10.1.0.0/16", ""},
		{"172.16.5.0/24", ""},
		{"10.64.64.64/32", "point-to-point"},
		{"10.0.0.0/8", "larger than /16"},
		{"100.64.0.0/24", "not a private"},
		{"203.0.113.0/24", "not a private"},
		{"fd00::/64", "not IPv4"},
	}
	for _, tt := range tests {
		err := ValidateLAN(mp(tt.lan))
		if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
			t.Errorf("%s: got %v, want %q", tt.lan, err, tt.wantErr)
		}
	}
}

func TestCheckOverlap(t *testing.T) {
	ours := []NamedPrefix{{"tunnel_cidr", mp("10.77.0.0/30")}, {"alias", mp("10.78.0.0/16")}}
	if err := CheckOverlap(ours, []netip.Prefix{mp("0.0.0.0/0"), mp("192.168.1.0/24")}); err != nil {
		t.Fatalf("unexpected overlap: %v", err)
	}
	err := CheckOverlap(ours, []netip.Prefix{mp("10.0.0.0/8")})
	if err == nil || !strings.Contains(err.Error(), "tunnel_cidr") || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("want both overlaps reported, got %v", err)
	}
}

func TestParseIPRoutes(t *testing.T) {
	routes, err := ParseIPRoutes(fixture(t, "ip-route-main.txt"))
	if err != nil {
		t.Fatal(err)
	}
	gw := netip.MustParseAddr("192.168.1.1")
	want := []Route{
		{mp("0.0.0.0/0"), gw, "enp130s0"},
		{mp("0.0.0.0/0"), gw, "wlp131s0f0"},
		{mp("10.99.0.0/16"), netip.Addr{}, ""},
		{mp("10.200.0.0/16"), netip.Addr{}, ""},
		{mp("172.17.0.0/16"), netip.Addr{}, "docker0"},
		{mp("172.18.0.0/16"), netip.Addr{}, "br-393fecf036cb"},
		{mp("192.168.1.0/24"), netip.Addr{}, "enp130s0"},
		{mp("192.168.1.53/32"), gw, "enp130s0"},
	}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("got %+v\nwant %+v", routes, want)
	}
	def, err := DefaultRoute(routes)
	if err != nil || def.Dev != "enp130s0" {
		t.Fatalf("default = %+v, %v", def, err)
	}
	if _, err := DefaultRoute(routes[2:]); err == nil {
		t.Fatal("want error without default route")
	}
}

func TestParseRouteGet(t *testing.T) {
	r, err := ParseRouteGet(fixture(t, "route-get-default.txt"))
	if err != nil || r.Dev != "en0" || r.Via != netip.MustParseAddr("192.168.1.1") {
		t.Fatalf("got %+v, %v", r, err)
	}
	if _, err := ParseRouteGet("route: writing to routing socket: not in table\n"); err == nil {
		t.Fatal("want error without interface")
	}
}

func TestParsePFToken(t *testing.T) {
	tok, err := ParsePFToken(fixture(t, "pfctl-enable.txt"))
	if err != nil || tok != "6305844718406826687" {
		t.Fatalf("got %q, %v", tok, err)
	}
	if _, err := ParsePFToken("pfctl: pf already enabled\n"); err == nil {
		t.Fatal("want error without token")
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := LoadState(path)
	if err != nil || len(s.Sysctls) != 0 {
		t.Fatalf("fresh state: %+v %v", s, err)
	}
	s.Sysctls["net.ipv4.ip_forward"] = "0"
	s.PFToken = "123"
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	got, err := LoadState(path)
	if err != nil || got.Sysctls["net.ipv4.ip_forward"] != "0" || got.PFToken != "123" {
		t.Fatalf("reloaded %+v %v", got, err)
	}
	if err := got.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("state file not removed")
	}
	if err := got.Remove(); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}
