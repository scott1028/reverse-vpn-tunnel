package netsys

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// StatePath is where a role records original system values for crash recovery.
func StatePath(role string) string {
	return "/var/run/reverse-vpn-tunnel-" + role + ".json"
}

// State holds values needed to undo our changes, persisted on every change.
type State struct {
	path string

	// Sysctls maps a key to its value before we changed it.
	Sysctls map[string]string `json:"sysctls,omitempty"`
	PFToken string            `json:"pf_token,omitempty"`
	// Utun and UtunAddr identify the macOS interface of this run.
	Utun     string `json:"utun,omitempty"`
	UtunAddr string `json:"utun_addr,omitempty"`
}

// LoadState reads the state left by a previous run, or returns an empty one.
// An empty path keeps the state in memory only.
func LoadState(path string) (*State, error) {
	s := &State{path: path, Sysctls: map[string]string{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Sysctls == nil {
		s.Sysctls = map[string]string{}
	}
	return s, nil
}

func (s *State) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Remove deletes the state file after everything has been restored.
func (s *State) Remove() error {
	if s.path == "" {
		return nil
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
