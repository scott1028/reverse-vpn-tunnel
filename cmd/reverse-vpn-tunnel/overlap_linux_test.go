//go:build linux

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/netsys"
)

type overlapRunner struct{ netsys.Runner }

func (r overlapRunner) Query(name string, args ...string) (string, error) {
	if name == "ip" && strings.Join(args, " ") == "-4 route show table main" {
		return "10.79.0.0/16 dev eth0\n", nil
	}
	return r.Runner.Query(name, args...)
}

func TestStartupOverlapNamesConfiguredAliasKey(t *testing.T) {
	for _, key := range []string{"exit_lan_alias_cidr", "home2_lan_alias_cidr"} {
		t.Run(key, func(t *testing.T) {
			_, runner := startupSandbox(t)
			newRunner = func() netsys.Runner { return overlapRunner{runner} }
			if err := os.WriteFile(config.ServerFile, []byte(`{"api_key":"`+testAPIKey+`","`+key+`":"10.79.0.0/16"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			err := run(context.Background(), true, false, "")
			if err == nil || !strings.Contains(err.Error(), key+" 10.79.0.0/16 overlaps local network") {
				t.Fatalf("want startup overlap error naming %s, got %v", key, err)
			}
		})
	}
}
