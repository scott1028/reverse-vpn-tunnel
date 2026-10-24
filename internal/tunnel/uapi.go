package tunnel

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/keys"
)

// Config is the device and single peer setup sent via UAPI.
type Config struct {
	// Zero PrivateKey / ListenPort / FwMark leave the current value.
	PrivateKey keys.Key
	ListenPort int
	FwMark     uint32

	PeerPublicKey keys.Key
	PresharedKey  keys.Key
	// Zero Endpoint lets the device learn it from the first handshake (server).
	Endpoint   netip.AddrPort
	AllowedIPs []netip.Prefix
}

// UAPI renders c. The peer is always replaced, which also drops its session.
func (c Config) UAPI() string {
	var b strings.Builder
	line := func(key, value string) { fmt.Fprintf(&b, "%s=%s\n", key, value) }
	if c.PrivateKey != (keys.Key{}) {
		line("private_key", hex.EncodeToString(c.PrivateKey[:]))
	}
	if c.ListenPort != 0 {
		line("listen_port", strconv.Itoa(c.ListenPort))
	}
	if c.FwMark != 0 {
		line("fwmark", strconv.FormatUint(uint64(c.FwMark), 10))
	}
	line("replace_peers", "true")
	line("public_key", hex.EncodeToString(c.PeerPublicKey[:]))
	line("preshared_key", hex.EncodeToString(c.PresharedKey[:]))
	if c.Endpoint.IsValid() {
		line("endpoint", c.Endpoint.String())
	}
	line("persistent_keepalive_interval", strconv.Itoa(config.KeepaliveSec))
	line("replace_allowed_ips", "true")
	for _, p := range c.AllowedIPs {
		line("allowed_ip", p.String())
	}
	return b.String()
}

// Redact hides secret values in UAPI text for logging.
func Redact(uapi string) string {
	lines := strings.Split(strings.TrimRight(uapi, "\n"), "\n")
	for i, l := range lines {
		key, _, _ := strings.Cut(l, "=")
		if key == "private_key" || key == "preshared_key" {
			lines[i] = key + "=(redacted)"
		}
	}
	return strings.Join(lines, " ")
}

// Stats are the peer counters from UAPI get.
type Stats struct {
	LastHandshake time.Time
	RxBytes       uint64
	Endpoint      string
}

// ParseStats reads the (single) peer counters from UAPI get output.
func ParseStats(uapi string) (Stats, error) {
	var s Stats
	var sec, nsec int64
	for _, l := range strings.Split(uapi, "\n") {
		key, value, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		var err error
		switch key {
		case "last_handshake_time_sec":
			sec, err = strconv.ParseInt(value, 10, 64)
		case "last_handshake_time_nsec":
			nsec, err = strconv.ParseInt(value, 10, 64)
		case "rx_bytes":
			s.RxBytes, err = strconv.ParseUint(value, 10, 64)
		case "endpoint":
			s.Endpoint = value
		}
		if err != nil {
			return Stats{}, fmt.Errorf("parse %s: %w", key, err)
		}
	}
	if sec != 0 || nsec != 0 {
		s.LastHandshake = time.Unix(sec, nsec)
	}
	return s, nil
}
