package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ServerFile)
	write := func(body string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now().Add(-time.Hour)
	write(`{"forward_all_traffic": true}`, t0)
	w := NewWatcher(path)
	w.LoadServer() // invalid (no api_key) but still marks this read as seen

	if w.Changed() || w.Changed() {
		t.Fatal("unchanged file reported as changed")
	}

	// In-place save: needs two equal polls.
	write(`{"forward_all_traffic": false}`, t0.Add(time.Second))
	if w.Changed() {
		t.Fatal("first poll after a change must wait")
	}
	if !w.Changed() {
		t.Fatal("stable change not reported")
	}
	if w.Changed() {
		t.Fatal("change reported twice")
	}

	// Truncate then write in two steps (nano): the half state is never applied.
	write(``, t0.Add(2*time.Second))
	if w.Changed() {
		t.Fatal("first poll must wait")
	}
	write(`{"forward_all_traffic": true}`, t0.Add(3*time.Second))
	if w.Changed() {
		t.Fatal("still changing, must wait")
	}
	if !w.Changed() {
		t.Fatal("stable change not reported")
	}

	// Rename-on-save (vim): new file replaces the old one.
	tmp := filepath.Join(dir, "swap")
	if err := os.WriteFile(tmp, []byte(`{"forward_all_traffic": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, t0.Add(4*time.Second), t0.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	if w.Changed() || !w.Changed() {
		t.Fatal("rename save not reported after two polls")
	}

	// Revert to the applied content before it settles: nothing to apply.
	write(`{"forward_all_traffic": true}`, t0.Add(5*time.Second))
	w.Changed()
	write(`{"forward_all_traffic": false}`, t0.Add(4*time.Second))
	if w.Changed() || w.Changed() {
		t.Fatal("file back to applied state must not report a change")
	}
}

func TestWatcherSeesEditDuringStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), ServerFile)
	if err := CreateServer(path, testKey); err != nil {
		t.Fatal(err)
	}
	w := NewWatcher(path)
	cfg, err := w.LoadServer()
	if err != nil || !cfg.ForwardAllTraffic {
		t.Fatalf("load: %+v %v", cfg, err)
	}
	// Edited while the server is still starting up.
	edited := `{"api_key": "` + testKey + `", "forward_all_traffic": false}`
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if w.Changed() || !w.Changed() {
		t.Fatal("edit after the initial load not reported")
	}
	if cfg, err := w.LoadServer(); err != nil || cfg.ForwardAllTraffic {
		t.Fatalf("reload: %+v %v", cfg, err)
	}
}
