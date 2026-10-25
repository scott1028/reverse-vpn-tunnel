// Command reverse-vpn-tunnel sends the entry node's traffic (--server) out to
// the internet through the exit node (--client).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"reverse-vpn-tunnel/internal/client"
	"reverse-vpn-tunnel/internal/config"
	"reverse-vpn-tunnel/internal/keys"
	"reverse-vpn-tunnel/internal/netsys"
	"reverse-vpn-tunnel/internal/server"
	"reverse-vpn-tunnel/internal/tunnel"
)

// exitSignals stop the loop and run the full cleanup. SIGHUP is included so a
// closed SSH session does not leave routes behind.
var exitSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// Seams for the startup tests.
var (
	geteuid    = os.Geteuid
	lockPath   = netsys.LockPath
	statePath  = netsys.StatePath
	newRunner  = func() netsys.Runner { return netsys.ExecRunner{} }
	newWatcher = config.NewWatcher
)

// handleSignals cancels the returned context on exitSignals. It also ignores
// SIGPIPE: a log pipe that died with the SSH session (| tee) must not kill
// the process halfway through cleanup.
func handleSignals() (context.Context, context.CancelFunc) {
	signal.Ignore(syscall.SIGPIPE)
	return signal.NotifyContext(context.Background(), exitSignals...)
}

// registerFlags registers the CLI flags on fs and points fs.Usage at
// printUsage. main uses flag.CommandLine; the help test uses its own FlagSet.
func registerFlags(fs *flag.FlagSet) (isServer, isClient, isDebug *bool, apiKey *string) {
	isServer = fs.Bool("server", false, "run on the entry node: listens for the client and, while forward_all_traffic\n"+
		"is true, sends this machine's traffic out through the exit node (the client).\n"+
		"Config: reverse-vpn-tunnel.server.jsonc in the current directory.")
	isClient = fs.Bool("client", false, "run on the exit node: connects to the server, and the server's traffic\n"+
		"goes out to the internet from this network.\n"+
		"Config: reverse-vpn-tunnel.client.jsonc in the current directory.")
	apiKey = fs.String("api-key", "", "with --server, first start only: api_key for the new config file (at least 20 characters).\n"+
		"Visible to other users in ps and kept in shell history; omit it once the config exists.")
	isDebug = fs.Bool("debug", false, "also log read-only commands and their failures")
	fs.Usage = func() { printUsage(fs) }
	return isServer, isClient, isDebug, apiKey
}

// printUsage prints a synopsis with the program base name and every flag with
// a -- prefix; the std default would print "-server" style entries.
func printUsage(fs *flag.FlagSet) {
	out := fs.Output()
	base := filepath.Base(os.Args[0])
	fmt.Fprintf(out, "Usage: %s --server [--api-key KEY] [--debug]\n       %s --client [--debug]\n\nFlags:\n", base, base)
	fs.VisitAll(func(f *flag.Flag) {
		name, usage := flag.UnquoteUsage(f)
		if name != "" {
			name = " " + name
		}
		fmt.Fprintf(out, "  --%s%s\n    \t%s\n", f.Name, name, strings.ReplaceAll(usage, "\n", "\n    \t"))
	})
}

func main() {
	ctx, stop := handleSignals()
	defer stop()
	isServer, isClient, isDebug, apiKey := registerFlags(flag.CommandLine)
	flag.Parse()

	if *isDebug {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	if err := run(ctx, *isServer, *isClient, *apiKey); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, isServer, isClient bool, apiKey string) error {
	if isServer == isClient || flag.NArg() > 0 {
		flag.CommandLine.Usage()
		return errors.New("use exactly one of --server or --client")
	}
	if apiKey != "" && !isServer {
		return errors.New("--api-key is only valid with --server")
	}
	if apiKey != "" && len(apiKey) < config.MinAPIKeyLen {
		return fmt.Errorf("--api-key must be at least %d characters", config.MinAPIKeyLen)
	}
	if geteuid() != 0 {
		return errors.New("must run as root (sudo)")
	}

	role := "client"
	if isServer {
		role = "server"
	}
	// Before any cleanup: a second instance must not touch the first one's setup.
	lock, err := netsys.Lock(lockPath(role))
	if err != nil {
		return err
	}
	defer lock.Close()

	if isServer {
		// Created before the first load, which marks what it read.
		watcher := newWatcher(config.ServerFile)
		cfg, err := prepareServerConfig(watcher, apiKey)
		if err != nil {
			return err
		}
		return runServer(ctx, cfg, watcher)
	}
	cfg, isCreated, err := prepareClientConfig(config.ClientFile)
	if err != nil || isCreated {
		return err
	}
	return runClient(ctx, cfg)
}

// openSystem loads the state file left by a previous run.
func openSystem(role string) (*netsys.System, *netsys.State, error) {
	state, err := netsys.LoadState(statePath(role))
	if err != nil {
		return nil, nil, fmt.Errorf("read state file: %w", err)
	}
	return netsys.New(newRunner(), state), state, nil
}

// undoPreviousRun undoes what a crashed run left behind. If anything stays,
// it refuses to start: a new interface on top of leftovers would bury them.
func undoPreviousRun(sys *netsys.System, role string, undo func() error) error {
	err := undo()
	if err == nil {
		return nil
	}
	return fmt.Errorf("a previous run left changes that could not be undone, so not starting (state file %s kept): %w\n"+
		"undo them by hand, then start again:\n  %s", statePath(role), err, strings.Join(sys.CleanupHints(role == "server"), "\n  "))
}

// closeState removes the state file only when every restore succeeded, so a
// later start can still restore the recorded original values.
func closeState(state *netsys.State, restoreErr error) {
	if restoreErr != nil {
		slog.Error("cleanup incomplete; state file kept for the next start", "err", restoreErr)
		return
	}
	if err := state.Remove(); err != nil {
		slog.Error("remove state file", "err", err)
	}
}

func checkOverlap(sys *netsys.System, ours []netsys.NamedPrefix) error {
	local, err := sys.LocalNetworks()
	if err != nil {
		return err
	}
	return netsys.CheckOverlap(ours, local)
}

func runServer(ctx context.Context, cfg config.Server, watcher *config.Watcher) error {
	k, err := keys.Derive(cfg.APIKey)
	if err != nil {
		return err
	}
	sys, state, err := openSystem("server")
	if err != nil {
		return err
	}
	if err := undoPreviousRun(sys, "server", sys.RollbackFullTunnel); err != nil {
		return err // the state file stays as it is
	}
	var restoreErr error
	defer func() { closeState(state, restoreErr) }()
	if err := checkOverlap(sys, []netsys.NamedPrefix{
		{Name: "tunnel_cidr", Prefix: cfg.TunnelCIDR},
		{Name: cfg.ExitLANAliasKey, Prefix: cfg.ExitLANAliasCIDR},
	}); err != nil {
		return err
	}

	serverIP, clientIP := netsys.TunnelAddrs(cfg.TunnelCIDR)
	tun, err := tunnel.Open(cfg.MTU, serverIP, clientIP, true)
	if err != nil {
		return err
	}
	defer tun.Close()
	if err := sys.SetupInterface(tun.Name(), netip.PrefixFrom(serverIP, cfg.TunnelCIDR.Bits()), clientIP); err != nil {
		return err
	}
	// Base routes: this host reaches 10.77.0.2 and the exit node LAN alias even with forwarding off.
	if err := sys.AddRoute(cfg.ExitLANAliasCIDR); err != nil {
		return err
	}
	if err := tun.Configure(tunnel.Config{
		PrivateKey:    k.ServerPrivate,
		ListenPort:    cfg.ListenPort,
		FwMark:        netsys.Mark,
		PeerPublicKey: k.ClientPublic,
		PresharedKey:  k.PresharedKey,
		// Replies from the internet come back with any source address.
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	}); err != nil {
		return err
	}
	if err := tun.Up(); err != nil {
		return err
	}
	slog.Info("server listening", "iface", tun.Name(), "port", cfg.ListenPort, "tunnel", cfg.TunnelCIDR)

	reload := func() (config.Server, bool, error) {
		if !watcher.Changed() {
			return config.Server{}, false, nil
		}
		cfg, err := watcher.LoadServer()
		return cfg, err == nil, err
	}
	srv := server.New(cfg, tun, sys, reload, tun.Goodbyes)
	runErr := srv.Run(ctx)
	restoreErr = srv.Close()
	slog.Info("server stopped")
	return runErr
}

func runClient(ctx context.Context, cfg config.Client) error {
	k, err := keys.Derive(cfg.APIKey)
	if err != nil {
		return err
	}
	sys, state, err := openSystem("client")
	if err != nil {
		return err
	}
	if err := undoPreviousRun(sys, "client", sys.RemoveForwarding); err != nil {
		return err // the state file stays as it is
	}
	var restoreErr error
	defer func() { closeState(state, restoreErr) }()
	fwd := netsys.Forwarding{Tunnel: cfg.TunnelCIDR}
	ours := []netsys.NamedPrefix{{Name: "tunnel_cidr", Prefix: cfg.TunnelCIDR}}
	if cfg.LANAlias.Enabled {
		if fwd.LAN, err = clientLAN(sys, cfg.LANAlias.LANCIDR); err != nil {
			return err
		}
		aliasRange, err := netsys.AliasRange(fwd.LAN, cfg.LANAlias.AliasCIDR)
		if err != nil {
			return err
		}
		fwd.Alias = cfg.LANAlias.AliasCIDR
		ours = append(ours, netsys.NamedPrefix{Name: "lan_alias.alias_cidr", Prefix: fwd.Alias})
		slog.Info("exit node LAN alias", "lan", fwd.LAN, "alias", aliasRange)
	}
	if err := checkOverlap(sys, ours); err != nil {
		return err
	}

	serverIP, clientIP := netsys.TunnelAddrs(cfg.TunnelCIDR)
	tun, err := tunnel.Open(cfg.MTU, serverIP, clientIP, false)
	if err != nil {
		return err
	}
	defer tun.Close()
	if err := sys.SetupInterface(tun.Name(), netip.PrefixFrom(clientIP, cfg.TunnelCIDR.Bits()), serverIP); err != nil {
		return err
	}
	if r, err := sys.DefaultRoute(); err == nil {
		fwd.Egress = r.Dev
	}
	if err := sys.ApplyForwarding(fwd); err != nil {
		restoreErr = sys.RemoveForwarding()
		return err
	}
	if err := tun.Up(); err != nil {
		restoreErr = sys.RemoveForwarding()
		return err
	}
	slog.Info("client started", "iface", tun.Name(), "server", cfg.ServerAddress)

	cli := client.New(cfg, k, tun, sys, client.ResolveIPv4)
	cli.Start(time.Now)
	runErr := cli.Run(ctx)
	if ctx.Err() != nil {
		if err := tun.SendGoodbye(clientIP, serverIP); err != nil {
			slog.Warn("send exit node goodbye", "err", err)
		}
	}
	restoreErr = sys.RemoveForwarding()
	slog.Info("client stopped")
	return runErr
}

// clientLAN returns lan_cidr, auto-detected from the default route interface.
func clientLAN(sys *netsys.System, lanCIDR string) (netip.Prefix, error) {
	if lanCIDR != "auto" {
		lan := netip.MustParsePrefix(lanCIDR) // checked by config validation
		return lan, netsys.ValidateLAN(lan)
	}
	r, err := sys.DefaultRoute()
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("detect LAN: %w; set lan_alias.lan_cidr in the config", err)
	}
	return netsys.DetectLAN(r.Dev)
}

// prepareServerConfig creates the config on first run, then loads it through
// the watcher so later edits are measured against this very read.
func prepareServerConfig(watcher *config.Watcher, apiKey string) (config.Server, error) {
	path := config.ServerFile
	_, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		isGenerated := apiKey == ""
		if isGenerated {
			if apiKey, err = keys.GenerateAPIKey(); err != nil {
				return config.Server{}, err
			}
		}
		if err := config.CreateServer(path, apiKey); err != nil {
			return config.Server{}, err
		}
		slog.Info("created server config", "path", path)
		if isGenerated {
			// Printed once on purpose so the user can copy it to the client.
			fmt.Fprintf(os.Stderr, "\nGenerated api_key (copy into the client config):\n\n    %s\n\n", apiKey)
		}
	case err != nil:
		return config.Server{}, err
	}

	cfg, err := watcher.LoadServer()
	if err != nil {
		return config.Server{}, err
	}
	if apiKey != "" && cfg.APIKey != apiKey {
		return config.Server{}, fmt.Errorf("--api-key differs from api_key in existing %s; edit the file instead", path)
	}
	return cfg, nil
}

// prepareClientConfig writes the template on first run and reports created=true.
func prepareClientConfig(path string) (cfg config.Client, created bool, err error) {
	_, err = os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := config.CreateClient(path); err != nil {
			return config.Client{}, false, err
		}
		fmt.Fprintf(os.Stderr, "Created %s. Fill in api_key and server_address, then run again.\n", path)
		return config.Client{}, true, nil
	}
	if err != nil {
		return config.Client{}, false, err
	}
	cfg, err = config.LoadClient(path)
	return cfg, false, err
}
