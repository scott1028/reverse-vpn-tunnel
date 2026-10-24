package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const apiKeyPlaceholder = "__API_KEY__"

const serverTemplate = `{
  // Shared secret. Copy the same value into the client config.
  "api_key": __API_KEY__,
  // UDP port. Router must port-forward it to this host.
  "listen_port": 51820,
  // true: while the tunnel is up, all traffic of this host goes out to the
  // internet through the exit node (the client).
  // false: normal local routing; tunnel stays connected. Applied live on save.
  "forward_all_traffic": true,
  // Block IPv6 while forwarding so it cannot leak via the local ISP.
  "block_ipv6": true,
  // Must match the client config.
  "tunnel_cidr": "10.77.0.0/30",
  "home2_lan_alias_cidr": "10.78.0.0/16",
  "mtu": 1280,
  "reconnect": {
    "dead_peer_timeout_sec": 45,
    "retry_interval_sec": 60,
    // 0 = retry forever
    "max_retries": 0
  }
}
`

const clientTemplate = `{
  // This host is the exit node: the server's traffic goes out to the
  // internet from this network.
  "api_key": "",
  // Server (entry node) public IP or DDNS name, with port.
  "server_address": "server.example.com:51820",
  "tunnel_cidr": "10.77.0.0/30",
  "lan_alias": {
    "enabled": true,
    "alias_cidr": "10.78.0.0/16",
    // "auto" or a CIDR such as "192.168.1.0/24"
    "lan_cidr": "auto"
  },
  "mtu": 1280,
  "reconnect": { "dead_peer_timeout_sec": 45, "retry_interval_sec": 60, "max_retries": 0 }
}
`

// CreateServer writes the server template with apiKey. It fails if the file exists.
func CreateServer(path, apiKey string) error {
	quoted, err := json.Marshal(apiKey)
	if err != nil {
		return err
	}
	return create(path, strings.Replace(serverTemplate, apiKeyPlaceholder, string(quoted), 1))
}

// CreateClient writes the client template. It fails if the file exists.
func CreateClient(path string) error {
	return create(path, clientTemplate)
}

// create writes a 0600 file and hands it to the sudo user so they can edit it.
func create(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return chownToSudoUser(path)
}

func chownToSudoUser(path string) error {
	uidStr, gidStr := os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID")
	if uidStr == "" || gidStr == "" {
		return nil
	}
	uid, err1 := strconv.Atoi(uidStr)
	gid, err2 := strconv.Atoi(gidStr)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("invalid SUDO_UID/SUDO_GID %q/%q", uidStr, gidStr)
	}
	return os.Chown(path, uid, gid)
}
