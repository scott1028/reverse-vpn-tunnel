package netsys

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.lock")
	first, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path); err == nil || !strings.Contains(err.Error(), "another instance") {
		t.Fatalf("second lock: %v", err)
	}
	first.Close()
	again, err := Lock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again.Close()
}
