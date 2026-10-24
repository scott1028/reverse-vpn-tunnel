package netsys

import (
	"errors"
	"log/slog"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strings"
)

// Own numbers, away from wg-quick (51820) and netbird (105/110).
const (
	Mark  = 0x5256
	table = "21078" // 0x5256
)

// Forwarding is the client side NAT setup.
type Forwarding struct {
	Tunnel netip.Prefix
	// Alias is mapped 1:1 onto LAN. Zero Alias disables the mapping.
	Alias netip.Prefix
	LAN   netip.Prefix
	// Egress is the physical interface (used by macOS pf only).
	Egress string
}

// System applies role specific OS changes and undoes them.
type System struct {
	r     Runner
	state *State

	iface string
	local netip.Addr

	fwd     Forwarding
	isFwdOn bool
	// routes holds the delete args of routes we added (macOS), in add order.
	routes [][]string

	// listInterfaces returns interface names; replaced in tests.
	listInterfaces func() ([]string, error)
}

func New(r Runner, state *State) *System {
	return &System{r: r, state: state, listInterfaces: interfaceNames}
}

func interfaceNames() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ifaces))
	for _, ifc := range ifaces {
		names = append(names, ifc.Name)
	}
	return names, nil
}

// LocalNetworks returns interface networks and non-default routes for overlap checks.
func (s *System) LocalNetworks() ([]netip.Prefix, error) {
	local, err := LocalPrefixes()
	if err != nil {
		return nil, err
	}
	routes, err := s.routePrefixes()
	if err != nil {
		return nil, err
	}
	return append(local, routes...), nil
}

// setSysctl sets key to value and records the original for restore.
// With from set, it only changes a key whose current value is from.
func (s *System) setSysctl(key, from, value string) error {
	out, err := s.r.Query("sysctl", "-n", key)
	if err != nil {
		return err
	}
	cur := strings.TrimSpace(out)
	if cur == value || from != "" && cur != from {
		return nil
	}
	if _, saved := s.state.Sysctls[key]; !saved {
		s.state.Sysctls[key] = cur
		if err := s.state.save(); err != nil {
			return err
		}
	}
	_, err = s.r.Run("sysctl", "-w", key+"="+value)
	return err
}

// restoreSysctls writes back every recorded original value. A record is
// dropped only after its restore succeeded or its interface is gone.
func (s *System) restoreSysctls() error {
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(s.state.Sysctls)) {
		orig := s.state.Sysctls[key]
		if _, err := s.r.Run("sysctl", "-w", key+"="+orig); err != nil {
			if s.isInterfaceGone(key) {
				delete(s.state.Sysctls, key)
				continue
			}
			errs = append(errs, err)
			continue
		}
		if key == "net.ipv4.ip_forward" && orig == "0" {
			slog.Warn("restored net.ipv4.ip_forward=0; restart Docker if it was started while the client ran")
		}
		delete(s.state.Sysctls, key)
	}
	if err := s.state.save(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// isInterfaceGone reports whether key (net.ipv4.conf.<iface>.*) belongs to an
// interface that no longer exists. Unknown means no.
func (s *System) isInterfaceGone(key string) bool {
	rest, ok := strings.CutPrefix(key, "net.ipv4.conf.")
	if !ok {
		return false
	}
	name, _, ok := strings.Cut(rest, ".")
	if !ok || name == "all" || name == "default" {
		return false
	}
	names, err := s.listInterfaces()
	return err == nil && !slices.Contains(names, strings.ReplaceAll(name, "/", "."))
}

// stateHints restores the recorded sysctls, then drops the state file.
func (s *System) stateHints() []string {
	var cmds []string
	for _, key := range slices.Sorted(maps.Keys(s.state.Sysctls)) {
		cmds = append(cmds, "sudo sysctl -w "+key+"="+s.state.Sysctls[key])
	}
	if s.state.path != "" {
		cmds = append(cmds, "sudo rm -f "+s.state.path)
	}
	return cmds
}
