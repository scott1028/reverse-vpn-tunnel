# Get reverse-vpn-tunnel running
[繁體中文](guide.md) | English

<a id="glossary"></a>

## Learn the terms you will see

Use this table to read configs, commands, and logs without first learning the VPN networking tools.

| Term | Plain explanation |
|---|---|
| entry node | The machine running `--server`; traffic whose exit you want to change enters the tunnel here. |
| exit node | The machine running `--client`; it dials the entry node and forwards the entry node's traffic to the internet. |
| peer | The other end of the encrypted connection; each machine configures a single peer. |
| tunnel / WireGuard | The encrypted connection between the nodes; this program embeds userspace WireGuard, so you do not need a separate `wg` service. |
| LAN | A machine's private network, such as `192.168.1.0/24`. |
| LAN alias | Another address range for the exit node's LAN, so you can address remote hosts even when the two LANs overlap. |
| public / private IP | A public IP is externally routable; a private IP is an address for use inside private networks. |
| DNS / A record | DNS looks up addresses by name; an A record supplies an IPv4 address. |
| root / sudo | Root is the system administrator identity; sudo lets you run a command with that identity. |
| dead peer timeout | How long the program can go without receiving a packet before declaring the peer dead; configured as `dead_peer_timeout_sec`. |
| fail-open | The entry node returns to local routing when the peer dies, keeping internet access available; it does not block internet access on disconnect. |
| state file | A file recording original system values and recovery information, so the next start can handle changes left by a crash. |
| lock file | A separate lock for each role; you cannot run two instances of the same role on one machine. |
| CIDR | An address plus a bit count after a slash describing a network; `/24` has 256 addresses, and fewer prefix bits mean a larger network. |
| MTU | The allowed size of a network packet; `mtu` is measured in bytes. |
| UDP / port forwarding | UDP carries the outer tunnel packets; port forwarding sends traffic from a router's external port to a machine inside its network. |
| NAT / NETMAP / MASQUERADE | NAT rewrites addresses; NETMAP preserves host bits for a one-to-one mapping, and MASQUERADE uses the egress interface's address. |
| TUN / utun | A virtual interface that lets a program read and write IP packets; macOS names it `utunN`. |
| policy routing / fwmark | Policy routing chooses a routing table by rules; a fwmark is a Linux packet mark used to select rules. |
| route / default route / main table | A route chooses where packets go; the default is used without a more specific route, and the main table is Linux's primary routing table. |
| firewall / PF anchor | A firewall controls network traffic; macOS PF groups rules in an anchor, and this program uses its own named anchor. |
| jump / PF token | A jump sends processing to another firewall chain; a PF token is the reference identifier obtained when enabling PF and released during cleanup. |
| sysctl / rp_filter / src_valid_mark | sysctl reads and writes kernel settings; `rp_filter` checks a packet's source path, and `src_valid_mark` makes that check consider marks. |
| handshake / keepalive / probe | A handshake establishes an encrypted session; keepalive maintains the connection; a probe is an ICMP echo packet sent by the program. |
| socket | The operating system endpoint a program uses to send and receive network packets. |
| mtime / stderr | mtime is a file's last modification time; stderr is a process's standard error output, shown in the terminal by default. |
| e2e / API | e2e means end-to-end testing, here using real Linux networking; an API is an interface other programs can call. |

`home2` / `Home2` in the original names `home2_lan_alias_cidr`, `Home2 LAN alias`, and `forwarding all traffic through Home2` means the exit node.

<a id="requirements"></a>

## Prepare before starting

Check the system tools and network access first, so missing tools do not interrupt network setup halfway through.

| Item | What you need |
|---|---|
| OS | Linux or macOS; these are the only system networking implementations, and no minimum OS version is declared. |
| Permissions | Root or sudo on both nodes; an ordinary user gets `must run as root (sudo)`. |
| Build | Go 1.26.8 and make; building fetches the dependencies in `go.mod`. |
| Linux tools | `ip`, `iptables`, `sysctl`, and a usable TUN device; LAN alias also needs iptables NETMAP support. |
| macOS tools | `ifconfig`, `route`, `pfctl`, and `sysctl`; the exit node's main PF rules need the anchor hooks described below. |
| entry node network | A reachable inbound IPv4 UDP port, `51820` by default; allow it through the router and firewall. |
| exit node network | Access to the entry node's IPv4 address and port; the exit node can sit behind NAT without an externally reachable inbound port. |
| Test tools | `curl` to check the exit address; only e2e needs Docker and Docker Compose. |

Both nodes must use the same `api_key` and `tunnel_cidr`; their alias networks must also match when you use LAN alias.
You need a reachable entry node address; the program has no intermediary service to traverse NAT at both ends.

<a id="setup"></a>

## Connect the nodes for the first time

Start the entry node and get its key first, then let the exit node dial in with that key.

```text
Time -------------------------------------------------------------------->
you        | start entry | copy key and address | fill exit config | rerun exit
entry node | create config and listen          | accept <-----------------+
exit node  |                                   | create template  | dial -+
```

`--server` keeps running; `--client` exits successfully on the run that creates its template.
Start the commands below from the repository root; keep each machine's own config on that machine.

### Create the entry node config

Build and start on the entry node; the program looks for the server config in its current directory.

```sh
make release
cd dist
sudo ./reverse-vpn-tunnel --server
```

If `reverse-vpn-tunnel.server.jsonc` is missing, the program creates the full JSONC template and keeps starting; JSONC is JSON with comments.
Without `--api-key`, it generates 32 random bytes, encodes them as base64url without padding, and prints the key to stderr once.
Give the key to the exit node privately; you can also fill in the [README's minimal configs](../README.en.md) on both machines first.

### Fill in the exit node config and start again

Run once on the exit node to create the template; it only connects after you fill in the required fields.

```sh
make release
cd dist
sudo ./reverse-vpn-tunnel --client
```

The first run prints `Created reverse-vpn-tunnel.client.jsonc. Fill in api_key and server_address, then run again.` and exits.
Copy the entry node's `api_key`, replace `server_address` with its reachable IPv4 address or DNS name and port, then run `sudo ./reverse-vpn-tunnel --client` again.
The template enables LAN alias; adjust the [LAN settings](#lan-alias) first if `auto` detection does not fit your network.

### Understand what happens with an existing config

On later starts, you use the config in the current directory; the program does not overwrite existing files.

New files use permissions `0600`; when run through sudo, the program uses `SUDO_UID` and `SUDO_GID` to give the config back to the original user.
If you still pass `--api-key`, it must match the server file; a mismatch exits with a request to edit the file.
Omitted fields keep their defaults; unknown fields, invalid syntax, or values outside the valid ranges stop startup.

<a id="daily-use"></a>

## Check that it is working day to day

Start from the config directory, then check the logs and the entry node's actual exit address.

| Location | Original message you will see | What it means |
|---|---|---|
| entry node | `server listening` | Interface and listener setup finished; this alone does not mean the peer connected. |
| exit node | `client started` | Forwarding rules and the interface are ready; the connection attempt comes next. |
| entry node | `peer connected` | The peer meets the liveness conditions. |
| exit node | `connected to server` | The entry node is confirmed alive. |
| entry node | `forwarding all traffic through Home2` | Full traffic routing has been applied. |
| entry node | `local routing restored` | Full traffic routing was rolled back; base tunnel routes remain. |

Run `curl -4 https://ifconfig.me` on the entry node; with a healthy connection and `forward_all_traffic: true`, you should see the exit node network's public IPv4 address.
Add `--debug` to see read-only system commands and their failures; commands that change the system are already logged, logs go to stderr, and the program does not create its own log file.

<a id="reload"></a>

## Know which edits apply on save

You can change only two entry node fields live; stop and restart the program for other changes.

| Config change | How it applies |
|---|---|
| server `forward_all_traffic` | Applies on save; `false` restores local routing while keeping the tunnel and LAN alias route. |
| server `block_ipv6` | Applies on save; IPv6 blocking operates only while full traffic routing is active. |
| Other server fields, including `reconnect` | Need a restart; even when edited together, only the two fields above apply live. |
| All client fields | Need a restart; the exit node has no config watcher. |
| Invalid or unreadable new server config | Keeps the previous settings and logs `config not applied; keeping previous settings`. |

The watcher compares mtime and size on each 2-second loop and reads after two identical polls; it handles both in-place writes and save-by-rename.
Edits that preserve both mtime and size may go unnoticed; after a failed read, save again to retry, and remember that `SIGHUP` stops the program rather than reloading it.

<a id="reconnect"></a>

## Recover when the peer stops answering

You do not need to repair the default route after disconnecting; the entry node rolls back full traffic routing and uses the local exit.

```text
Time -------------------------------------------------------------------->
[alive] --stop receiving--> [dead: use local route] --fresh handshake--> [alive]
```

This is fail-open; existing internet connections may break during the switch, and traffic is not restricted to the tunnel afterward.

### Understand how liveness is checked

The program requires both recent packets and a successful handshake before calling a peer connected; retry packets alone do not count.

It samples every 2 seconds; receive counters must have changed, the last receive must be no older than `dead_peer_timeout_sec`, and the handshake must be less than 200 seconds old.
Both nodes set a fixed 15-second keepalive; an already live peer that is silent for 18 seconds gets a probe, with probes continuing on later loops.
After being declared dead, the peer needs a handshake newer than that death time to recover; the timeout defaults to 45 seconds and cannot be below 40 seconds.

### Understand what each node retries

You can set the retry interval and limit through `reconnect`, but the two roles do different retry work.

| Role | Disconnect and retry behavior |
|---|---|
| entry node | Restores local routing after a previously connected peer dies; refreshes its local UDP socket every `retry_interval_sec` seconds and cannot dial the exit node. It does not count these retries before the first connection. |
| exit node | Tries to connect immediately at startup; after failure or a dead peer, resolves DNS again, replaces the peer, and starts a new handshake at the configured interval. |
| Both | The default interval is `60` seconds; `max_retries: 0` keeps retrying, and a live connection resets the count. A positive limit exits with `reconnect.max_retries exhausted` and cleans up after the retries are used. |

The program follows the default route's egress interface; a loop gap greater than 6 seconds counts as a wake-up, refreshing the entry node's socket and redialing on the exit node.
The exit node checks forwarding every 30 seconds: Linux rebuilds when a dedicated jump is missing, and macOS reloads when its anchor lacks NAT rules; neither checks every rule in full.

<a id="lan-alias"></a>

## Reach a remote host when the LANs overlap

Use an alias address to identify an exit node LAN host without confusing it with the same local address at the entry node.

```text
entry node --send to 10.78.0.20--> exit node --map to 192.168.1.20--> LAN host
```

This example assumes an exit node LAN of `192.168.1.0/24` and alias `10.78.0.0/16`; the mapped range is `10.78.0.0/24`, preserving host bits.
You can run `curl http://10.78.0.20` on the entry node to reach that host; it still needs an HTTP service that accepts the connection.

### Handle a failed `auto` detection

Set `lan_alias.lan_cidr` explicitly if the automatically chosen interface address does not fit your LAN.

`auto` finds the IPv4 default route's interface and takes its first IPv4 network; it does not scan every LAN or skip an invalid address to try the next one.
The network must be private IPv4, aligned to its network address, with a prefix from `/16` through `/30`; public addresses, larger networks, `/31`, and `/32` are rejected.
With multiple interfaces or addresses, set the actual network, such as `192.168.1.0/24`, or set `lan_alias.enabled: false` to keep internet forwarding without the alias.

### Match the networks on both nodes

When changing networks, update both nodes so packets have a matching destination after they enter the tunnel.

| Condition | What you need to check |
|---|---|
| Matching aliases | server `home2_lan_alias_cidr` equals client `lan_alias.alias_cidr`; both default to `10.78.0.0/16`. |
| Alias capacity | The alias network cannot be smaller than the LAN; the actual mapped range starts at the alias base and uses the LAN prefix length. |
| Avoiding overlaps | Alias cannot overlap `tunnel_cidr`; startup also checks tunnel and alias against local interface networks, plus non-default main table routes on Linux. |

This maps a single LAN, with no DNS name mapping or list of multiple LANs; the base alias route remains available with full traffic forwarding turned off.

<a id="ipv6"></a>

## Decide what happens to IPv6

The tunnel carries IPv4, so you need to choose what happens to the entry node's IPv6 during full traffic forwarding.

| State | Behavior |
|---|---|
| Full traffic routing active, `block_ipv6: true` | Tries to block external IPv6; Linux uses an unreachable route and policy rules, while macOS uses two reject routes. |
| Full traffic routing active, `block_ipv6: false` | Does not block IPv6, which may reach the internet through local routes. |
| Full traffic routing disabled or peer declared dead | Removes the program's IPv6 block and restores local routing. |

Blocking defaults to on, but failures only log warnings such as `IPv6 block not applied` and do not stop the IPv4 tunnel; more specific local routes may still take priority.
The exit node has no `block_ipv6` field; the entry node's DNS settings are not automatically changed to the exit node's DNS.

<a id="cleanup"></a>

## Stop while keeping recovery possible

Send a normal stop signal so the program undoes network changes; it deletes the state file only after restoration succeeds.

```text
running --receive SIGINT / SIGTERM / SIGHUP--> restore routes and rules --succeed--> remove state file
```

`Ctrl-C` sends SIGINT; SIGHUP also runs full cleanup, while SIGPIPE is ignored so a closed log pipe does not interrupt cleanup.
Normal close removes the TUN interface; failed cleanup logs `cleanup incomplete; state file kept for the next start`.

### Understand the files under `/var/run`

You do not place state files beside the config; the program writes fixed paths in the system runtime directory for each role.

```text
/var/run/
|-- reverse-vpn-tunnel-server.json
|-- reverse-vpn-tunnel-client.json
|-- reverse-vpn-tunnel-server.lock
+-- reverse-vpn-tunnel-client.lock
```

State records original sysctl values, the macOS PF token, and utun information, saved through a `0600` temporary file and rename.
Locks are separate by role; failure to acquire the lock rejects a second instance, and process exit releases the lock even though the `.lock` file may remain.

### Understand a refused start after a crash

On your next start, the program first tries to undo the previous run's changes, keeping a new setup from hiding unfinished recovery.

Keep the state file after SIGKILL or a crash; an unreadable file reports `read state file`, and failed restoration reports `a previous run left changes that could not be undone, so not starting`.
Inspect and restore the system using the commands printed after that error, then start again; do not delete the state file first, since that loses the original system values.
If the old macOS utun is gone or no longer belongs to the program, it leaves IPv6 reject routes whose ownership it cannot verify and prints manual removal commands; automatic cleanup is not guaranteed to finish.

<a id="flags"></a>

## Use each CLI flag

Choose exactly one role per run; flags control startup, and config paths are fixed relative to the current working directory.

| Flag | Default | Purpose and limits |
|---|---|---|
| `--server` | `false` | Runs the entry node; reads `reverse-vpn-tunnel.server.jsonc`, creating it and continuing if it is missing. |
| `--client` | `false` | Runs the exit node; reads `reverse-vpn-tunnel.client.jsonc`, creating only a template and exiting if it is missing. |
| `--api-key KEY` | Empty string | Valid only with `--server`; at least 20 bytes, used to create a new config or compared against an existing file. |
| `--debug` | `false` | Adds read-only system commands, their failures, and debug messages to logs; it does not change network policy. |
| `--help` | Does not start | Shows usage and flags; this help entry is handled by the standard flag package. |

Both roles, neither role, or extra positional arguments produce `use exactly one of --server or --client`; there is no config-path flag.
`--api-key` appears in process listings and shell history; omit it once the config is created.

<a id="server-config"></a>

## Read the full entry node config

In `reverse-vpn-tunnel.server.jsonc`, set only what you want to change; omitted fields keep these defaults.

| Key | Default | Valid range | Plain meaning |
|---|---|---|---|
| `api_key` | Load default `""`; first creation generates a key or uses `--api-key` | At least 20 bytes; empty is invalid | The secret shared with the exit node to derive both nodes' keys. |
| `listen_port` | `51820` | Integer `1`–`65535` | The entry node's UDP port for tunnel connections. |
| `forward_all_traffic` | `true` | `true` / `false` | Sends this machine's internet traffic through the exit node while the peer is alive. |
| `block_ipv6` | `true` | `true` / `false` | Tries to block external IPv6 while full traffic routing is active. |
| `tunnel_cidr` | `"10.77.0.0/30"` | Aligned IPv4 network, prefix `/0`–`/30`, without alias or local network conflicts | The tunnel network; base plus one goes to the entry node, plus two to the exit node, defaulting to `10.77.0.1` and `10.77.0.2`. |
| `home2_lan_alias_cidr` | `"10.78.0.0/16"` | Aligned IPv4 network, `/0`–`/32`, without tunnel or local network conflicts | Routes these destination addresses to the exit node and must match its alias setting. |
| `mtu` | `1280` | Integer `576`–`1500` | The packet size set when creating the TUN interface. |
| `reconnect` | The three defaults in the next section | Object; see [retry settings](#reconnect-config) for fields | Controls when a peer is considered dead and how retries work. |

`tunnel_cidr` and `api_key` must match the exit node; a valid network can still fail startup if it overlaps local routes.
Only `forward_all_traffic` and `block_ipv6` update live; other changes need a restart.

<a id="client-config"></a>

## Read the full exit node config

Set the entry node address and LAN mapping in `reverse-vpn-tunnel.client.jsonc`; every change needs a restart.

| Key | Default | Valid range | Plain meaning |
|---|---|---|---|
| `api_key` | `""`, also blank in the template | At least 20 bytes; empty is invalid | Copy the entry node's secret so both nodes can establish the same connection. |
| `server_address` | `"server.example.com:51820"` | Nonempty `host:port`, integer port `1`–`65535`; dialing accepts only IPv4 or DNS A records | The entry node you dial, not the exit node's address. |
| `tunnel_cidr` | `"10.77.0.0/30"` | Aligned IPv4 network, `/0`–`/30`, without enabled alias or local network conflicts | Must match the entry node. |
| `lan_alias` | `enabled: true`, `alias_cidr: "10.78.0.0/16"`, `lan_cidr: "auto"` | Object | Controls whether the entry node can reach your LAN through mapped addresses. |
| `lan_alias.enabled` | `true` | `true` / `false` | `false` disables LAN mapping while keeping internet forwarding. |
| `lan_alias.alias_cidr` | `"10.78.0.0/16"` | When enabled, an aligned IPv4 network, `/0`–`/32`, without tunnel or local conflicts, at least as large as the LAN | The alternate addresses the entry node uses to reach your LAN hosts. |
| `lan_alias.lan_cidr` | `"auto"` | `"auto"` or an aligned private IPv4 network `/16`–`/30`, checked when enabled | The actual LAN to map; `auto` uses the first IPv4 network on the default route's interface. |
| `mtu` | `1280` | Integer `576`–`1500` | The packet size set when creating the TUN interface. |
| `reconnect` | The three defaults in the next section | Object; see [retry settings](#reconnect-config) for fields | Controls when a peer is considered dead and how retries work. |

Each redial resolves DNS again, takes only the first IPv4 result, and uses a 10-second lookup timeout; it does not continuously watch DNS.
Disabling alias does not skip JSON type or CIDR syntax parsing, but it does skip the alias enablement validations.

<a id="reconnect-config"></a>

## Set the shared retry fields

You can tune each node's `reconnect` independently; omitted subfields still keep their defaults.

| Key | Default | Valid range | Plain meaning |
|---|---|---|---|
| `reconnect.dead_peer_timeout_sec` | `45` | Integer at least `40`; no additional upper limit is set | The receive silence allowed before the peer is declared dead, in seconds. |
| `reconnect.retry_interval_sec` | `60` | Integer at least `1`; no additional upper limit is set | How long to wait before another retry, in seconds. |
| `reconnect.max_retries` | `0` | Integer at least `0`; no additional upper limit is set | `0` keeps retrying; a positive number limits retries during a disconnect, and a live connection resets the count. |

Do not lower the dead timeout below 40 seconds; validation rejects it, and it needs room for keepalive, probes, and a new handshake.

<a id="system-changes"></a>

## See which system settings change

Use this table to check what the program touches, especially if your host already runs another VPN or Docker.

| System and role | Tools | Actual changes |
|---|---|---|
| Linux, both nodes | `ip` | Configures the IPv4 address and routes for `rvt0`; the fixed interface name does not support multiple instances. |
| Linux entry node | `ip`, `sysctl` | Places the tunnel default route in table `21078`; policy priorities `5250`, `5251`, `5252`, protocol `86`, and fwmark `0x5256` keep outer UDP on local routes; `iif lo` selects only locally generated traffic, with more specific main table routes first. |
| Linux entry node | `sysctl` | Sets `net.ipv4.conf.all.src_valid_mark=1`; changes strict `rp_filter=1` to `2` for `all` and non-lo interfaces, recording originals for restoration. |
| Linux entry node | `ip -6` | Adds `unreachable default` in table `21078` and the matching policy rules when needed, removing them on full traffic rollback. |
| Linux exit node | `iptables`, `sysctl` | Sets `net.ipv4.ip_forward=1`; creates `RVT-FWD` (filter/FORWARD), `RVT-POST` (nat/POSTROUTING), and `RVT-PRE` (nat/PREROUTING) only with alias enabled; dedicated jumps go first, using ACCEPT, MASQUERADE, and NETMAP without changing the existing FORWARD policy. |
| macOS, both nodes | `ifconfig`, `route` | Configures the system-assigned `utunN`, point-to-point addresses, and base routes. |
| macOS entry node | `route` | Adds `0.0.0.0/1` and `128.0.0.0/1` through utun; outer IPv4 UDP uses `IP_BOUND_IF` to bind to the physical interface; when needed adds reject routes for `::/1` and `8000::/1` via `::1`. |
| macOS exit node | `pfctl`, `sysctl` | Sets `net.inet.ip.forwarding=1`; adds NAT and optional `rdr ... bitmask` in anchor `com.apple/reverse-vpn-tunnel`, obtains a token with `pfctl -E`, and releases it with `pfctl -X` during cleanup. |

The macOS exit node requires `nat-anchor "com.apple/*"` and `rdr-anchor "com.apple/*"` in the main rules; otherwise it exits with a request to restore `/etc/pf.conf`.
Linux cleanup deletes rules matching the program's selector, priority, and protocol; it refuses deletion for a different rule with the same priority and protocol, while table `21078` is flushed in full.
When Linux restores `net.ipv4.ip_forward=0`, it warns that you may need to restart Docker if Docker started while the exit node was running.

<a id="build"></a>

## Build and run the regular tests

Use the Makefile to build for your current host platform; release and debug binaries have separate names.

| Command | Output or purpose |
|---|---|
| `make` / `make help` | Lists targets without building. |
| `make release` | `dist/reverse-vpn-tunnel`; `CGO_ENABLED=0`, `-trimpath`, `-ldflags="-s -w"`, removing symbols and debug information. |
| `make debug` | `dist/reverse-vpn-tunnel-debug`; `CGO_ENABLED=0`, `-gcflags="all=-N -l"`, disabling optimization and inlining while keeping debug information. |
| `make test` | `go test ./...`; without a cache hit, the idle test takes about 80 seconds. |
| `make clean` | Removes only those two binaries, keeping the `dist/` directory and its config files. |
| `go vet ./...` | Checks for common Go mistakes without starting a tunnel. |

Both `dist/` and `.tmp/` are in `.gitignore`; this helps prevent accidental commits and does not provide access control for secrets.
There is no cross-platform Makefile target; before copying a binary to another machine, check that its OS and CPU architecture match.

<a id="e2e"></a>

## Test real Linux networking behavior

Use Docker e2e to check routing, forwarding, and cleanup inside containers, keeping tunnel setup changes off the host.

```text
build binary --build image--> start six containers --run checks--> save logs --remove containers-->
```

It needs a Linux host, Go, Bash, Docker daemon access, and Docker Compose, plus privileged containers, TUN, and macvlan network support.
The first build needs access to `ubuntu:24.04` and apt packages; the test includes a 124-second idle wait and other waits, usually taking several minutes, longer with first-time downloads.
Its exit trap writes logs to `.tmp/e2e/server.log` and `.tmp/e2e/client.log`; the binary is `.tmp/e2e/bin/reverse-vpn-tunnel`.

### Run the test

Run this from the repository root; the host command needs no sudo, but your user must be able to use Docker.

```sh
bash test/e2e/run.sh
```

The test uses Compose project `rvt-e2e` and removes its containers and volumes before and after the run; do not run another test with that project name at the same time.
It checks exit addresses, alias access with overlapping LANs, idle and probes, live updates through two save methods, missing-jump repair, fail-open after pausing, and SIGTERM / SIGHUP cleanup.
It does not test macOS; internal networks use `10.201.1.0/24`, `10.201.2.0/24`, `10.201.3.0/24`, and `192.168.77.0/24`, so avoid conflicts with existing Docker networks.

<a id="security"></a>

## Understand the key and operating boundaries

Sharing the key also shares the ability to derive both private keys, so pair only with a machine you trust.

- `api_key` goes through Argon2id (password derivation) and HKDF-SHA256 (key expansion) to produce both private keys and a preshared key; it is not an account, and there are no separate per-user permissions.
- System commands carry no keys, and WireGuard config logs redact private and preshared keys; the first generated `api_key` is still printed, so protect your terminal and logs and keep config permissions at `0600`.
- Fail-open restores local internet access; failed IPv6 blocking only logs a warning, and DNS does not change automatically. This is not an internet block on disconnect or complete privacy isolation.

<a id="limitations"></a>

## Check these limits when something goes wrong

Compare your setup with the program's actual supported scope before deciding whether to edit config or inspect host networking.

| Situation or limit | What you need to know |
|---|---|
| Only one peer pair | There are no multiple exit nodes, user lists, or control API; both nodes share one `api_key`. |
| The tunnel handles IPv4 only | The exit node resolves only A records; IPv6 is handled by blocking, not by forwarding it through the IPv4 tunnel. |
| Forwarding is for the entry node host | Linux uses `iif lo`, so it does not automatically change the exit for other LAN hosts or forwarded Docker traffic; macOS uses host routes without those Linux selection rules. |
| Local routes still have priority | Non-default Linux main table routes and macOS routes more specific than `/1` may stay local; full traffic routing does not guarantee overriding every route. |
| Names or numbers collide | Linux fixes `rvt0`, table `21078`, and protocol `86`; macOS fixes the PF anchor name. Role locks do not coordinate resources with other VPNs. |
| LAN detection fails | `auto` does not search multiple LANs; set private IPv4 `/16`–`/30` explicitly or disable alias. |
| Firewall repair is limited | Periodic checks look for jumps or NAT presence rather than auditing other firewall software or every rule. |
| Networking remains after a crash | Check the state file and printed cleanup commands; macOS IPv6 reject routes may need manual removal if the old utun disappears. |
| No background service installer | The program runs in the foreground with no built-in service manager configuration; SIGHUP stops and cleans up. |

Return to the [English README](../README.en.md) to repeat the minimal setup; the repository has no `LICENSE` and does not yet declare license terms.
