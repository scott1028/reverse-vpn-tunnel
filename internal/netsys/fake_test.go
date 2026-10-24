package netsys

import (
	"reflect"
	"strings"
	"testing"
)

// fakeRunner records Run commands and answers both Run and Query via respond.
type fakeRunner struct {
	cmds    []string
	respond func(cmd string) (string, error)
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.cmds = append(f.cmds, cmd)
	return f.reply(cmd)
}

func (f *fakeRunner) Query(name string, args ...string) (string, error) {
	return f.reply(strings.Join(append([]string{name}, args...), " "))
}

func (f *fakeRunner) reply(cmd string) (string, error) {
	if f.respond == nil {
		return "", nil
	}
	return f.respond(cmd)
}

func newTestSystem(t *testing.T, r Runner) *System {
	t.Helper()
	state, err := LoadState("")
	if err != nil {
		t.Fatal(err)
	}
	s := New(r, state)
	s.listInterfaces = func() ([]string, error) { return []string{"lo", "eth0", "eth0.5", "rvt0"}, nil }
	return s
}

func assertCmds(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
