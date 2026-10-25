package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"reverse-vpn-tunnel/internal/config"
)

func TestAliasKeyRenameDoesNotRequireRestart(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cfg := config.DefaultServer()
	cfg.ExitLANAliasKey = "home2_lan_alias_cidr"
	srv := &Server{cfg: cfg, reload: func() (config.Server, bool, error) {
		return config.DefaultServer(), true, nil
	}}
	srv.reloadConfig()
	if strings.Contains(logs.String(), "need a restart") || !strings.Contains(logs.String(), "config reloaded") {
		t.Fatalf("key-only rename should reload without a restart warning: %s", logs.String())
	}
}
