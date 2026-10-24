package netsys

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestQueryLogsOnlyAtDebug(t *testing.T) {
	var buf bytes.Buffer
	level := new(slog.LevelVar)
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))

	level.Set(slog.LevelInfo)
	ExecRunner{}.Query("sh", "-c", "echo nope >&2; exit 3")
	if buf.Len() != 0 {
		t.Fatalf("query logged at info: %s", buf.String())
	}

	level.Set(slog.LevelDebug)
	if _, err := (ExecRunner{}).Query("sh", "-c", "echo nope >&2; exit 3"); err == nil {
		t.Fatal("want error")
	}
	got := buf.String()
	for _, want := range []string{"level=DEBUG", `cmd="sh -c echo nope >&2; exit 3"`, "err=", "output=nope"} {
		if !strings.Contains(got, want) {
			t.Fatalf("debug log %q missing %q", got, want)
		}
	}
}
