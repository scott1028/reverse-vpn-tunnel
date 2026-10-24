package netsys

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// Route is one IPv4 route. Dst is 0.0.0.0/0 for the default route.
type Route struct {
	Dst netip.Prefix
	Via netip.Addr
	Dev string
}

var ipRouteTypes = map[string]bool{
	"unicast": true, "local": true, "broadcast": true, "multicast": true, "throw": true,
	"unreachable": true, "prohibit": true, "blackhole": true, "nat": true, "anycast": true,
}

// ParseIPRoutes parses Linux `ip -4 route show` output.
func ParseIPRoutes(out string) ([]Route, error) {
	var routes []Route
	for _, line := range strings.Split(out, "\n") {
		// Indented lines are multipath nexthops.
		if strings.TrimSpace(line) == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		fields := strings.Fields(line)
		if ipRouteTypes[fields[0]] {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		var r Route
		var err error
		switch dst := fields[0]; {
		case dst == "default":
			r.Dst = netip.PrefixFrom(netip.IPv4Unspecified(), 0)
		case strings.Contains(dst, "/"):
			r.Dst, err = netip.ParsePrefix(dst)
		default:
			var a netip.Addr
			a, err = netip.ParseAddr(dst)
			r.Dst = netip.PrefixFrom(a, a.BitLen())
		}
		if err != nil {
			return nil, fmt.Errorf("parse route %q: %w", line, err)
		}
		for i := 1; i+1 < len(fields); i++ {
			switch fields[i] {
			case "via":
				r.Via, _ = netip.ParseAddr(fields[i+1])
			case "dev":
				r.Dev = fields[i+1]
			}
		}
		routes = append(routes, r)
	}
	return routes, nil
}

// DefaultRoute returns the first default route; ip lists the lowest metric first.
func DefaultRoute(routes []Route) (Route, error) {
	for _, r := range routes {
		if r.Dst.Bits() == 0 {
			return r, nil
		}
	}
	return Route{}, errors.New("no IPv4 default route")
}

// ParseRouteGet parses macOS `route -n get default` output.
func ParseRouteGet(out string) (Route, error) {
	r := Route{Dst: netip.PrefixFrom(netip.IPv4Unspecified(), 0)}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "gateway":
			r.Via, _ = netip.ParseAddr(value)
		case "interface":
			r.Dev = value
		}
	}
	if r.Dev == "" {
		return Route{}, errors.New("no interface in route get output")
	}
	return r, nil
}

var pfTokenRe = regexp.MustCompile(`(?m)^Token\s*:\s*(\d+)\s*$`)

// ParsePFToken returns the reference token printed by macOS `pfctl -E`.
func ParsePFToken(out string) (string, error) {
	m := pfTokenRe.FindStringSubmatch(out)
	if m == nil {
		return "", errors.New("no token in pfctl -E output")
	}
	return m[1], nil
}

// ParseRules returns `ip rule show` lines with whitespace collapsed,
// e.g. "5250: from all lookup main suppress_prefixlength 0".
func ParseRules(out string) []string {
	var rules []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			rules = append(rules, strings.Join(f, " "))
		}
	}
	return rules
}
