package netsys

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// Runner executes OS commands. Commands never carry key material.
type Runner interface {
	// Run executes a command that changes system state; logged at info.
	Run(name string, args ...string) (string, error)
	// Query executes a read-only command; logged at debug.
	Query(name string, args ...string) (string, error)
}

// ExecRunner runs commands with os/exec and logs each one so it can be
// reproduced by hand.
type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) (string, error) {
	return execLogged(slog.LevelInfo, name, args)
}

func (ExecRunner) Query(name string, args ...string) (string, error) {
	return execLogged(slog.LevelDebug, name, args)
}

func execLogged(level slog.Level, name string, args []string) (string, error) {
	cmdline := strings.Join(append([]string{name}, args...), " ")
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		slog.Log(context.Background(), level, "exec", "cmd", cmdline, "err", err, "output", msg)
		return string(out), fmt.Errorf("%s: %w: %s", cmdline, err, msg)
	}
	slog.Log(context.Background(), level, "exec", "cmd", cmdline)
	return string(out), nil
}
