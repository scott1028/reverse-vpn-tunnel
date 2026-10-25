package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testKey = "0123456789abcdefghij"

func TestServerTemplateMatchesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), ServerFile)
	if err := CreateServer(path, testKey); err != nil {
		t.Fatal(err)
	}
	got, err := LoadServer(path)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultServer()
	want.APIKey = testKey
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestServerTemplateQuotesAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ServerFile)
	key := `with "quotes" and \ backslash`
	if err := CreateServer(path, key); err != nil {
		t.Fatal(err)
	}
	got, err := LoadServer(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != key {
		t.Fatalf("api_key = %q, want %q", got.APIKey, key)
	}
}

func TestClientTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), ClientFile)
	if err := CreateClient(path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClient(path); err == nil || !strings.Contains(err.Error(), "api_key is empty") {
		t.Fatalf("want empty api_key error, got %v", err)
	}
	raw, _ := os.ReadFile(path)
	filled := strings.Replace(string(raw), `"api_key": ""`, `"api_key": "`+testKey+`"`, 1)
	if err := os.WriteFile(path, []byte(filled), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultClient()
	want.APIKey = testKey
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCreateFilePermissions(t *testing.T) {
	t.Setenv("SUDO_UID", strconv.Itoa(os.Getuid()))
	t.Setenv("SUDO_GID", strconv.Itoa(os.Getgid()))
	path := filepath.Join(t.TempDir(), ServerFile)
	if err := CreateServer(path, testKey); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	if err := CreateServer(path, testKey); err == nil {
		t.Fatal("create must not overwrite an existing file")
	}
}

func TestLoadServer(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"missing fields keep defaults", `{"api_key": "` + testKey + `",}`, ""},
		{"short api_key", `{"api_key": "short"}`, "at least 20"},
		{"unknown field", `{"api_key": "` + testKey + `", "forward_all": false}`, "unknown field"},
		{"bad jsonc", `{"api_key": `, "invalid JSONC"},
		{"bad port", `{"api_key": "` + testKey + `", "listen_port": 0}`, "listen_port"},
		{"tunnel too small", `{"api_key": "` + testKey + `", "tunnel_cidr": "10.77.0.0/31"}`, "too small"},
		{"tunnel not masked", `{"api_key": "` + testKey + `", "tunnel_cidr": "10.77.0.1/30"}`, "IPv4 network"},
		{"alias overlaps tunnel", `{"api_key": "` + testKey + `", "exit_lan_alias_cidr": "10.77.0.0/16"}`, "overlaps"},
		{"bad mtu", `{"api_key": "` + testKey + `", "mtu": 100}`, "mtu"},
		{"short timeout", `{"api_key": "` + testKey + `", "reconnect": {"dead_peer_timeout_sec": 5}}`, "dead_peer_timeout_sec"},
		{"negative retries", `{"api_key": "` + testKey + `", "reconnect": {"max_retries": -1}}`, "max_retries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ServerFile)
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadServer(path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !cfg.ForwardAllTraffic || cfg.ListenPort != 51820 {
					t.Fatalf("defaults not kept: %+v", cfg)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadClient(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"lan cidr", `{"api_key": "` + testKey + `", "server_address": "1.2.3.4:51820", "lan_alias": {"lan_cidr": "192.168.1.0/24"}}`, ""},
		{"no port", `{"api_key": "` + testKey + `", "server_address": "entry.example.com"}`, "host:port"},
		{"bad port", `{"api_key": "` + testKey + `", "server_address": "entry.example.com:0"}`, "port must be"},
		{"bad lan cidr", `{"api_key": "` + testKey + `", "lan_alias": {"lan_cidr": "lan"}}`, "lan_cidr"},
		{"alias disabled skips alias checks", `{"api_key": "` + testKey + `", "lan_alias": {"enabled": false, "lan_cidr": "lan"}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ClientFile)
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadClient(path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadErrorsDoNotLeakContent(t *testing.T) {
	const secret = "SECRETsecret0123456789"
	tests := []struct {
		name, body, want string
	}{
		{"unquoted key", `{"api_key": ` + secret + `}`, "line 1, column 13: invalid literal"},
		{"bad escape", "{\n  \"api_key\": \"" + secret + `\q"}`, "line 2, column 14: invalid literal"},
		{"bad character", `{"api_key": "` + secret + `" x}`, "invalid character"},
		{"unterminated string", `{"api_key": "` + secret, "unexpected end of file"},
		{"wrong type", `{"listen_port": "` + secret + `"}`, "listen_port has the wrong type"},
		{"unknown field", "{\n  \"api_key\": \"" + secret + "\",\n  \"" + secret + "\": 1\n}", "line 3, column 3: unknown field"},
		{"bad CIDR", "{\"api_key\": \"x\",\n \"tunnel_cidr\": \"" + secret + "\"}", "line 2, column 2: invalid CIDR"},
		{"bad nested CIDR", `{"lan_alias": {"alias_cidr": "` + secret + `"}}`, "invalid CIDR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ServerFile)
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			load := func() error { _, err := LoadServer(path); return err }
			if strings.Contains(tt.name, "nested") {
				load = func() error { _, err := LoadClient(path); return err }
			}
			err := load()
			if err == nil {
				t.Fatal("want error")
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("content leaked: %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestExitAliasConfig(t *testing.T) {
	for _, test := range []struct {
		name, fields, wantAlias, wantError string
		wantWarning                        bool
	}{
		{"default", "", "10.78.0.0/16", "", false},
		{"new key", `,"exit_lan_alias_cidr":"10.79.0.0/16"`, "10.79.0.0/16", "", false},
		{"legacy key", `,"home2_lan_alias_cidr":"10.79.0.0/16"`, "10.79.0.0/16", "", true},
		{"both keys", `,"exit_lan_alias_cidr":"10.78.0.0/16","home2_lan_alias_cidr":"10.79.0.0/16"`, "", "both", false},
		{"both null", `,"exit_lan_alias_cidr":null,"home2_lan_alias_cidr":null`, "", "both", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			path := filepath.Join(t.TempDir(), ServerFile)
			if err := os.WriteFile(path, []byte(`{"api_key":"`+testKey+`"`+test.fields+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadServer(path)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || !strings.Contains(err.Error(), "exit_lan_alias_cidr") || !strings.Contains(err.Error(), "home2_lan_alias_cidr") {
					t.Fatalf("want both-key error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(cfg)
			if err != nil || !bytes.Contains(encoded, []byte(`"exit_lan_alias_cidr":"`+test.wantAlias+`"`)) {
				t.Fatalf("alias not loaded under new key: %s, %v", encoded, err)
			}
			warning := "home2_lan_alias_cidr is deprecated; rename it to exit_lan_alias_cidr"
			aliasKey := "exit_lan_alias_cidr"
			if test.wantWarning {
				aliasKey = "home2_lan_alias_cidr"
			}
			if cfg.ExitLANAliasKey != aliasKey || bytes.Contains(encoded, []byte("ExitLANAliasKey")) {
				t.Fatalf("configured key metadata = %q; encoded config: %s", cfg.ExitLANAliasKey, encoded)
			}
			wantCount := 0
			if test.wantWarning {
				wantCount = 1
			}
			if count := strings.Count(logs.String(), warning); count != wantCount {
				t.Fatalf("warning count %d, want %d: %s", count, wantCount, logs.String())
			}
		})
	}
}

func TestAliasErrorsNameConfiguredKey(t *testing.T) {
	for _, key := range []string{"exit_lan_alias_cidr", "home2_lan_alias_cidr"} {
		for _, alias := range []string{"not-a-cidr", "2001:db8::/64", "10.77.0.0/16"} {
			t.Run(key+"/"+alias, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), ServerFile)
				if err := os.WriteFile(path, []byte(`{"api_key":"`+testKey+`","`+key+`":"`+alias+`"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				_, err := LoadServer(path)
				if err == nil {
					t.Fatal("invalid alias was accepted")
				}
				message := strings.TrimPrefix(err.Error(), path+": ")
				otherKey := "home2_lan_alias_cidr"
				if key == otherKey {
					otherKey = "exit_lan_alias_cidr"
				}
				if !strings.Contains(message, key) || strings.Contains(message, otherKey) {
					t.Fatalf("want only configured key %s in error, got %v", key, err)
				}
			})
		}
	}
}

func TestServerAllowsControlPort(t *testing.T) {
	cfg := DefaultServer()
	cfg.APIKey = testKey
	cfg.ListenPort = ControlPort
	if err := cfg.Validate(); err != nil {
		t.Fatalf("inner destination port must not reserve a kernel listener: %v", err)
	}
}
