package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/netsys"
)

const testAPIKey = "0123456789abcdefghij"

func TestMain(m *testing.M) {
	if marker := os.Getenv("RVT_SIGPIPE_HELPER"); marker != "" {
		sigpipeHelper(marker)
		return
	}
	os.Exit(m.Run())
}

// sigpipeHelper runs as a child process whose stderr is a pipe nobody reads:
// it waits for an exit signal, logs like a cleanup does, then writes marker.
func sigpipeHelper(marker string) {
	ctx, stop := handleSignals()
	defer stop()
	if err := os.WriteFile(marker+".ready", nil, 0o600); err != nil {
		os.Exit(2)
	}
	<-ctx.Done()
	for range 20 {
		slog.Info("exec", "cmd", "ip -4 rule del ...")
	}
	if err := os.WriteFile(marker, []byte("done"), 0o600); err != nil {
		os.Exit(2)
	}
}

func TestDeadLogPipeDoesNotStopCleanup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "done")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close() // like tee that died with the SSH session
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "RVT_SIGPIPE_HELPER="+marker)
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	for i := 0; ; i++ {
		if _, err := os.Stat(marker + ".ready"); err == nil {
			break
		}
		if i == 100 {
			cmd.Process.Kill()
			t.Fatal("helper did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cleanup did not finish (process state %v)", cmd.ProcessState)
	}
}

func TestExitSignalsIncludeSIGHUP(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(exitSignals, sig) {
			t.Errorf("%v does not trigger cleanup", sig)
		}
	}
}

func TestHelpListsOnlyLongFlags(t *testing.T) {
	fs := flag.NewFlagSet("reverse-vpn-tunnel", flag.ContinueOnError)
	registerFlags(fs)
	var out bytes.Buffer
	fs.SetOutput(&out)
	fs.Usage()
	help := out.String()

	for _, want := range []string{"--server", "--client", "--api-key string", "--debug"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not list %q:\n%s", want, help)
		}
	}
	if got := regexp.MustCompile(`(^|[^-\w])-(server|client|api-key|debug)\b`).FindString(help); got != "" {
		t.Errorf("help shows a single-dash flag %q:\n%s", got, help)
	}
}

// run prints usage on bad args; it must use our usage, not the std flag.Usage.
func TestRunUsageListsOnlyLongFlags(t *testing.T) {
	orig := flag.CommandLine
	t.Cleanup(func() { flag.CommandLine = orig })
	flag.CommandLine = flag.NewFlagSet("reverse-vpn-tunnel", flag.ContinueOnError)
	registerFlags(flag.CommandLine)
	var out bytes.Buffer
	flag.CommandLine.SetOutput(&out)

	if err := run(context.Background(), false, false, ""); err == nil {
		t.Fatal("run accepted neither --server nor --client")
	}
	help := out.String()
	if !strings.Contains(help, "--server") {
		t.Errorf("usage does not list --server:\n%s", help)
	}
	if got := regexp.MustCompile(`(^|[^-\w])-(server|client|api-key|debug)\b`).FindString(help); got != "" {
		t.Errorf("usage shows a single-dash flag %q:\n%s", got, help)
	}
}

func TestCloseStateKeepsFileUntilRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"sysctls": {"net.ipv4.ip_forward": "0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := netsys.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	closeState(state, errors.New("sysctl: permission denied"))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file removed after a failed restore: %v", err)
	}
	closeState(state, nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file kept after a full restore: %v", err)
	}
}

// recordingRunner records every command; respond may fail some.
type recordingRunner struct {
	cmds    []string
	respond func(cmd string) error
}

func (r *recordingRunner) Run(name string, args ...string) (string, error) {
	return r.Query(name, args...)
}

func (r *recordingRunner) Query(name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	r.cmds = append(r.cmds, cmd)
	if r.respond != nil {
		return "", r.respond(cmd)
	}
	return "", nil
}

// startupSandbox points the startup seams at a temp dir, which also holds
// both config files and a state file with a stale sysctl record.
func startupSandbox(t *testing.T) (dir string, runner *recordingRunner) {
	dir = t.TempDir()
	t.Chdir(dir)
	runner = &recordingRunner{}
	oldEuid, oldLock, oldState, oldRunner, oldWatcher := geteuid, lockPath, statePath, newRunner, newWatcher
	t.Cleanup(func() {
		geteuid, lockPath, statePath, newRunner, newWatcher = oldEuid, oldLock, oldState, oldRunner, oldWatcher
	})
	geteuid = func() int { return 0 }
	lockPath = func(role string) string { return filepath.Join(dir, role+".lock") }
	statePath = func(role string) string { return filepath.Join(dir, role+".json") }
	newRunner = func() netsys.Runner { return runner }

	if err := config.CreateServer(config.ServerFile, testAPIKey); err != nil {
		t.Fatal(err)
	}
	client := `{"api_key": "` + testAPIKey + `", "server_address": "192.0.2.1:51820", "lan_alias": {"lan_cidr": "192.168.1.0/24"}}`
	if err := os.WriteFile(config.ClientFile, []byte(client), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"server", "client"} {
		if err := os.WriteFile(statePath(role), []byte(`{"sysctls": {"net.ipv4.ip_forward": "0"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, runner
}

func TestLockComesBeforeAnyCleanup(t *testing.T) {
	for _, role := range []string{"server", "client"} {
		t.Run(role, func(t *testing.T) {
			_, runner := startupSandbox(t)
			held, err := netsys.Lock(lockPath(role))
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			before, _ := os.ReadFile(statePath(role))

			err = run(context.Background(), role == "server", role == "client", "")
			if err == nil || !strings.Contains(err.Error(), "another instance") {
				t.Fatalf("want lock error, got %v", err)
			}
			if len(runner.cmds) != 0 {
				t.Fatalf("touched the system without the lock: %v", runner.cmds)
			}
			if after, _ := os.ReadFile(statePath(role)); string(after) != string(before) {
				t.Fatalf("state file changed without the lock: %s", after)
			}
		})
	}
}

func TestStartupStopsOnLeftovers(t *testing.T) {
	for _, role := range []string{"server", "client"} {
		t.Run(role, func(t *testing.T) {
			_, runner := startupSandbox(t)
			runner.respond = func(cmd string) error {
				if strings.HasPrefix(cmd, "sysctl -w ") {
					return errors.New("sysctl: permission denied")
				}
				return nil
			}
			err := run(context.Background(), role == "server", role == "client", "")
			if err == nil {
				t.Fatal("started on top of leftovers")
			}
			for _, want := range []string{"not starting", "undo them by hand", "sudo sysctl -w net.ipv4.ip_forward=0", "sudo rm -f " + statePath(role)} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error lacks %q:\n%v", want, err)
				}
			}
			if _, err := os.Stat(statePath(role)); err != nil {
				t.Fatalf("state file removed: %v", err)
			}
			for _, c := range runner.cmds {
				if strings.HasPrefix(c, "ip addr") || strings.HasPrefix(c, "ip link") || strings.Contains(c, " -N RVT") {
					t.Fatalf("set up on top of leftovers: %q", c)
				}
			}
		})
	}
}

func TestFirstConfigLoadMarksWatcher(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root run() would create a real TUN interface")
	}
	_, _ = startupSandbox(t)
	var watcher *config.Watcher
	newWatcher = func(path string) *config.Watcher {
		watcher = config.NewWatcher(path)
		return watcher
	}
	// Stops at TUN creation (no root), after the config was loaded.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, true, false, ""); err == nil || !strings.Contains(err.Error(), "TUN") {
		t.Fatalf("want TUN error, got %v", err)
	}
	if watcher == nil {
		t.Fatal("no watcher created")
	}
	if watcher.Changed() || watcher.Changed() {
		t.Fatal("first config load did not go through the watcher")
	}
	edited := `{"api_key": "` + testAPIKey + `", "forward_all_traffic": false}`
	if err := os.WriteFile(config.ServerFile, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if watcher.Changed() || !watcher.Changed() {
		t.Fatal("edit after the first load not reported")
	}
}
