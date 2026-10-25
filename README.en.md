# reverse-vpn-tunnel
[繁體中文](README.md) | English

Send traffic from your entry node (where traffic enters the tunnel) through an exit node (the machine that takes it to the internet), using an [encrypted WireGuard tunnel](docs/guide.en.md#glossary).

It's like a private hallway between your house and a friend's house. When you go online you walk out through your friend's front door, so websites see your friend's address.
Your friend digs the hallway from their side, because only your house has an address they can find (a public IP). If your friend stops answering, you go back to your own front door.

```text
entry node (--server) --send--> exit node (--client) --reach--> internet
entry node (--server) <--dial-- exit node (--client)
```

Both machines need root; the program changes routing and firewalls, and traffic is stuck until the peer is declared dead (treated as disconnected): about 2 seconds after a clean stop, 25 seconds with unanswered traffic, or the default 45 seconds while idle.

## What you need

- Two Linux or macOS machines, with root or sudo access.
- Go 1.26.8 or later and make to build from source; [required system tools](docs/guide.en.md#requirements) depend on the OS.
- A reachable UDP port (UDP carries the tunnel packets) on the entry node, `51820` by default; configure port forwarding on its router if needed.
- Docker only for the [e2e (end-to-end) test](docs/guide.en.md#e2e); you do not need it for normal use.

## Start both machines

Build on each machine, then start its role from the directory that holds its config.

```sh
make release
cd dist
sudo ./reverse-vpn-tunnel --server  # entry node; use --client on the exit node
```

On first run, the entry node creates `reverse-vpn-tunnel.server.jsonc` and keeps running; without a supplied key, it generates and prints an `api_key`.
The exit node creates `reverse-vpn-tunnel.client.jsonc` and exits; fill in the same key and the entry node's address, then run it again.
You can also prepare the configs below first; see [first startup](docs/guide.en.md#setup) for the full steps.
Closing SSH or the terminal sends SIGHUP and stops the tunnel; use [tmux or a service](docs/guide.en.md#cleanup) for a long run.

## Check where your traffic leaves

Once both nodes connect, run this on the entry node; you should see the public IPv4 address of the exit node's network.

```sh
curl -4 https://ifconfig.me
```

The entry node logs these messages; timestamps, levels, and extra fields depend on the run.

```text
server listening
peer connected
forwarding all traffic through the exit node
```

## Which machine runs which flag

Run `--server` on the machine that needs a different exit; it listens. Run `--client` on the machine that provides the exit; it dials.

| Machine | Flag | Network need | Config file | What it does |
|---|---|---|---|---|
| entry node | `--server` | Reachable inbound UDP port | `reverse-vpn-tunnel.server.jsonc` | Sends this machine's internet traffic through the exit node while connected |
| exit node | `--client` | Can dial the entry node; may sit behind NAT (network address translation) | `reverse-vpn-tunnel.client.jsonc` | Connects to the entry node and forwards its traffic to the internet |

See the [glossary](docs/guide.en.md#glossary) for other terms.

## Fill in the minimum config

Place each config in that node's working directory; omitted fields keep their defaults.

entry node: `reverse-vpn-tunnel.server.jsonc`

```json
{
  "api_key": "replace-with-the-same-random-secret",
  "listen_port": 51820
}
```

exit node: `reverse-vpn-tunnel.client.jsonc`

```json
{
  "api_key": "replace-with-the-same-random-secret",
  "server_address": "server.example.com:51820"
}
```

Three things to remember:

- Replace the example key with your own random secret, identical on both nodes; replace the address with your reachable entry node's address too.
- [LAN alias](docs/guide.en.md#lan-alias) is enabled by default and gives you another address range for the exit node's private network; set its network explicitly or disable it if `auto` detection fails.
- Saving `forward_all_traffic` or `block_ipv6` on the entry node applies them live; other fields and all exit node settings need a restart.

## Protect your key and traffic

Your `api_key` derives both private keys, so treat it as a secret that controls this pair's connection.

- Do not commit configs or share first-run output; generated files use `0600`, and for configs you create yourself, run `chmod 600 reverse-vpn-tunnel.*.jsonc`.
- `--api-key` can appear in process listings and stays in shell history; read it from the config for normal use.
- `block_ipv6` defaults to on, but a failed block only logs a warning; local routing returns only after the peer is declared dead, as covered in [IPv6 and limitations](docs/guide.en.md#ipv6).

## Where to go next

The [user guide](docs/guide.en.md) separates the details you may need next.

- [CLI and full config](docs/guide.en.md#flags): defaults, valid ranges, live edits, and restart rules.
- [Connections and system changes](docs/guide.en.md#reconnect): timeouts, LAN alias, IPv6, stopping, and crash recovery.
- [Build, test, and release](docs/guide.en.md#build): Makefile, Docker e2e, log locations, and the [tag release flow](docs/guide.en.md#release).

## What license can you use

This repository has no `LICENSE` file and does not yet declare license terms.
