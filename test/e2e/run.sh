#!/usr/bin/env bash
# Linux end-to-end test in Docker. Needs Docker access only, no sudo on the host.
set -euo pipefail

E2E_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$E2E_DIR/../.." && pwd)
OUT=$ROOT/.tmp/e2e
export RVT_BIN_DIR=$OUT/bin

API_KEY=e2e-api-key-0123456789abcdef
ECHO_URL=http://10.201.3.10/cgi-bin/ip
ENTRY_IP=10.201.1.10
NAT2_IP=10.201.2.10
SERVER_CFG=/work/reverse-vpn-tunnel.server.jsonc
# The minimum the config allows, so idle false-dead would show up here first.
DEAD_TIMEOUT=40

compose() { docker compose -p rvt-e2e -f "$E2E_DIR/docker-compose.yml" "$@"; }
dx() { compose exec -T "$@"; }

failures=0
check() {
  local desc=$1
  shift
  if "$@"; then echo "PASS: $desc"; else echo "FAIL: $desc"; failures=$((failures + 1)); fi
}

# wait_eq <seconds> <expected> <cmd...>: poll until cmd prints expected.
wait_eq() {
  local timeout=$1 want=$2 got=""
  shift 2
  for _ in $(seq "$timeout"); do
    got=$("$@" 2>/dev/null || true)
    [ "$got" = "$want" ] && return 0
    sleep 1
  done
  echo "  last output: '$got', want '$want'"
  return 1
}

# Bound fast fail-open checks by elapsed time, including each curl timeout.
wait_fast_eq() {
  local timeout=$1 want=$2 got="" started=$SECONDS
  shift 2
  while ((SECONDS - started <= timeout)); do
    got=$("$@" 2>/dev/null || true)
    if [ "$got" = "$want" ] && ((SECONDS - started <= timeout)); then
      echo "  restored after $((SECONDS - started))s (limit ${timeout}s)"
      return 0
    fi
    sleep 1
  done
  echo "  last output: '$got', want '$want' within ${timeout}s"
  return 1
}

# wait_ok <seconds> <cmd...>: poll until cmd succeeds.
wait_ok() {
  local timeout=$1
  shift
  for _ in $(seq "$timeout"); do
    "$@" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

not() { ! "$@"; }
echo_ip() { dx entry curl -s -m 2 "$ECHO_URL"; }
log_lines() { dx "$1" sh -c "wc -l <$2"; }
# log_since <service> <file> <line count>: log lines added after that count.
log_since() { dx "$1" sh -c "tail -n +$(($3 + 1)) $2"; }
# logged_since <service> <file> <line count> <regex>: a later line matches.
logged_since() { log_since "$1" "$2" "$3" | grep -qE "$4"; }

cleanup() {
  dx entry cat /work/server.log >"$OUT/server.log" 2>/dev/null || true
  dx exit cat /work/client.log >"$OUT/client.log" 2>/dev/null || true
  compose down -v -t 1 >/dev/null 2>&1 || true
  echo "logs: $OUT/server.log $OUT/client.log"
}

start_tunnel() {
  compose exec -d -T entry sh -c "cd /work && exec /opt/rvt/reverse-vpn-tunnel --server --debug >>/work/server.log 2>&1"
  compose exec -d -T exit sh -c "cd /work && exec /opt/rvt/reverse-vpn-tunnel --client --debug >>/work/client.log 2>&1"
}

echo "== setup"
mkdir -p "$RVT_BIN_DIR"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$RVT_BIN_DIR/reverse-vpn-tunnel" ./cmd/reverse-vpn-tunnel)
docker build -q -t rvt-e2e:latest "$E2E_DIR" >/dev/null
compose down -v -t 1 >/dev/null 2>&1 || true
trap cleanup EXIT
compose up -d
for svc in isp cloud entry nat2 exit web; do
  wait_eq 30 ok dx "$svc" sh -c 'test -f /tmp/ready && echo ok' || { echo "$svc not ready"; exit 1; }
done

dx entry sh -c "cat >$SERVER_CFG" <<EOF
{
  "api_key": "$API_KEY",
  "listen_port": 51820,
  "forward_all_traffic": true,
  "block_ipv6": true,
  // Short retry for the test; minimum dead timeout.
  "reconnect": { "dead_peer_timeout_sec": $DEAD_TIMEOUT, "retry_interval_sec": 5, "max_retries": 0 }
}
EOF
dx exit sh -c 'cat >/work/reverse-vpn-tunnel.client.jsonc' <<EOF
{
  "api_key": "$API_KEY",
  "server_address": "$ENTRY_IP:51820",
  "lan_alias": { "enabled": true, "alias_cidr": "10.78.0.0/16", "lan_cidr": "auto" },
  "reconnect": { "dead_peer_timeout_sec": $DEAD_TIMEOUT, "retry_interval_sec": 5, "max_retries": 0 }
}
EOF

snapshot_entry() { dx entry sh -c 'sysctl -a 2>/dev/null | grep -E "^net\.ipv4\.conf\..*\.(rp_filter|src_valid_mark) " | grep -v rvt0 | sort'; }
# All policy rules: the foreign ones at 5250-5252 must survive every rollback.
rules_entry() { dx entry sh -c 'ip -N -4 rule show; ip -N -6 rule show'; }
# ip_forward, policies and other programs' rules must survive start, repair and exit.
snapshot_exit() { dx exit sh -c 'sysctl -n net.ipv4.ip_forward; iptables -S; iptables -t nat -S'; }
entry_before=$(snapshot_entry)
entry_rules_before=$(rules_entry)
exit_before=$(snapshot_exit)

# stopped_clean <signal>: stop both sides with signal and check nothing is left.
stopped_clean() {
  local sig=$1
  dx entry pkill "-$sig" -f "reverse-vpn-tunnel --server" || true
  dx exit pkill "-$sig" -f "reverse-vpn-tunnel --client" || true
  # [r] keeps pgrep from matching this sh -c command line.
  check "$sig: server exited" wait_eq 10 gone dx entry sh -c 'pgrep -f "[r]everse-vpn-tunnel --server" >/dev/null || echo gone'
  check "$sig: client exited" wait_eq 10 gone dx exit sh -c 'pgrep -f "[r]everse-vpn-tunnel --client" >/dev/null || echo gone'
  check "$sig: entry ip rules as before start (foreign 5250-5252 kept, none of ours)" test "$(rules_entry)" = "$entry_rules_before"
  check "$sig: entry table 21078 empty" dx entry sh -c '[ -z "$(ip route show table 21078 2>/dev/null)" ] && [ -z "$(ip -6 route show table 21078 2>/dev/null)" ]'
  check "$sig: entry rvt0 removed" dx entry sh -c '! ip link show rvt0 2>/dev/null'
  check "$sig: entry sysctls restored" test "$(snapshot_entry)" = "$entry_before"
  check "$sig: entry state file removed" dx entry test ! -e /var/run/reverse-vpn-tunnel-server.json
  check "$sig: entry echo sees entry again" wait_eq 5 "$ENTRY_IP" echo_ip
  check "$sig: exit ip_forward, policy and sentinel rules unchanged, no RVT rules" test "$(snapshot_exit)" = "$exit_before"
  check "$sig: exit state file removed" dx exit test ! -e /var/run/reverse-vpn-tunnel-client.json
  check "$sig: no cleanup errors logged" not logged_since entry /work/server.log 0 'cleanup incomplete|rollback full tunnel failed'
  check "$sig: no client cleanup errors logged" not logged_since exit /work/client.log 0 'cleanup incomplete|repair forwarding'
}

echo "== 1. Entry traffic leaves via Exit"
check "before tunnel: echo sees entry ($ENTRY_IP)" wait_eq 5 "$ENTRY_IP" echo_ip
start_tunnel
check "tunnel up: echo sees nat2 ($NAT2_IP)" wait_eq 60 "$NAT2_IP" echo_ip
check "entry has our 3 IPv4 rules (proto 86) next to the foreign ones" \
  dx entry sh -c 'test "$(ip -N -4 rule show | grep -c " proto 86$")" = 3 && test "$(ip -N -4 rule show | grep -cE "^525[0-2]:")" = 7'
check "exit FORWARD policy is still DROP and our jump is first" \
  dx exit sh -c 'iptables -S FORWARD | sed -n 1,2p | tr "\n" ";" | grep -qx -- "-P FORWARD DROP;-A FORWARD -j RVT-FWD;"'

echo "== 2. Exit LAN via alias while both LANs are 192.168.77.0/24"
check "entry has its own 192.168.77.0/24" dx entry sh -c 'ip -4 route show 192.168.77.0/24 | grep -q lan-dup'
check "curl 10.78.0.20 reaches Exit web (sees client 192.168.77.10)" \
  wait_eq 10 192.168.77.10 dx entry curl -s -m 2 http://10.78.0.20/cgi-bin/ip

echo "== F8. idle tunnel for 3 x dead_peer_timeout_sec ($((3 * DEAD_TIMEOUT))s)"
srv_mark=$(log_lines entry /work/server.log)
cli_mark=$(log_lines exit /work/client.log)
sleep $((3 * DEAD_TIMEOUT + 4))
check "idle: server logged no 'peer dead'" not logged_since entry /work/server.log "$srv_mark" 'peer dead'
check "idle: client logged no 'server dead'" not logged_since exit /work/client.log "$cli_mark" 'server dead'
echo "  probes during idle: server $(log_since entry /work/server.log "$srv_mark" | grep -c 'probe sent' || true), client $(log_since exit /work/client.log "$cli_mark" | grep -c 'probe sent' || true)"
check "idle: still forwarding" wait_eq 5 "$NAT2_IP" echo_ip

echo "== F8. probe makes a silent client answer (raw ICMP on a real kernel)"
srv_mark=$(log_lines entry /work/server.log)
dx exit iptables -I OUTPUT -p udp --dport 51820 -j DROP
check "server probes the silent client" wait_ok 30 logged_since entry /work/server.log "$srv_mark" 'probe sent'
dx exit iptables -D OUTPUT -p udp --dport 51820 -j DROP
sleep 20
check "client answered before the dead timeout" not logged_since entry /work/server.log "$srv_mark" 'peer dead|probe peer'

check "after probe: echo sees nat2" wait_eq 5 "$NAT2_IP" echo_ip

echo "== 3. forward_all_traffic edited live"
flag_sed() { dx entry sed -i "s/\"forward_all_traffic\": $1/\"forward_all_traffic\": $2/" "$SERVER_CFG"; }
flag_rename() {
  dx entry sh -c "sed 's/\"forward_all_traffic\": $1/\"forward_all_traffic\": $2/' $SERVER_CFG >/work/.swp && mv /work/.swp $SERVER_CFG"
}
for method in sed rename; do
  "flag_$method" true false
  check "$method → false: echo sees entry" wait_eq 15 "$ENTRY_IP" echo_ip
  check "$method → false: rollback left foreign rules and removed ours" test "$(rules_entry)" = "$entry_rules_before"
  check "$method → false: ping 10.77.0.2 still works" dx entry sh -c 'ping -c1 -W2 10.77.0.2 >/dev/null'
  "flag_$method" false true
  check "$method → true: echo sees nat2" wait_eq 15 "$NAT2_IP" echo_ip
done

echo "== F16. firewall reload on Exit drops our FORWARD jump"
dx exit iptables -D FORWARD -j RVT-FWD
check "self-repair puts the jump back first" \
  wait_ok 45 dx exit sh -c 'iptables -S FORWARD | sed -n 2p | grep -qx -- "-A FORWARD -j RVT-FWD"'
check "after repair: echo sees nat2" wait_eq 15 "$NAT2_IP" echo_ip

echo "== 4. clean exit node stop: fast fail-open, then reconnect"
entry_filter_before=$(dx entry iptables-save -t filter)
entry_input_before=$(dx entry iptables -S INPUT)
dx entry iptables -F INPUT
dx entry iptables -A INPUT -i lo -j ACCEPT
dx entry iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
dx entry iptables -A INPUT -p udp --dport 51820 -j ACCEPT
dx entry iptables -P INPUT DROP
check "clean stop: entry INPUT is default-deny" \
  dx entry sh -c 'test "$(iptables -S INPUT | head -n 1)" = "-P INPUT DROP"'
srv_mark=$(log_lines entry /work/server.log)
dx exit pkill -TERM -f "reverse-vpn-tunnel --client"
check "clean stop: echo sees entry within 6s" wait_fast_eq 6 "$ENTRY_IP" echo_ip
check "clean stop: server received goodbye" \
  wait_ok 6 logged_since entry /work/server.log "$srv_mark" 'exit node said goodbye'
dx entry iptables-restore <<<"$entry_filter_before"
check "clean stop: original INPUT policy and rules restored" \
  test "$(dx entry iptables -S INPUT)" = "$entry_input_before"
check "clean stop: client exited" \
  wait_eq 10 gone dx exit sh -c 'pgrep -f "[r]everse-vpn-tunnel --client" >/dev/null || echo gone'
compose exec -d -T exit sh -c "cd /work && exec /opt/rvt/reverse-vpn-tunnel --client --debug >>/work/client.log 2>&1"
check "client restarted: echo sees nat2" wait_eq 60 "$NAT2_IP" echo_ip

echo "== 5. exit node paused with traffic: fast fail-open, then back"
compose pause exit
check "paused with traffic: echo sees entry within 38s" wait_fast_eq 38 "$ENTRY_IP" echo_ip
compose unpause exit
check "unpaused: echo sees nat2 again" wait_eq 90 "$NAT2_IP" echo_ip

echo "== 6. SIGTERM leaves nothing behind"
stopped_clean TERM

echo "== F6. SIGHUP (closed SSH session) cleans up the same way"
start_tunnel
check "restarted: echo sees nat2" wait_eq 60 "$NAT2_IP" echo_ip
stopped_clean HUP

echo
if [ "$failures" -ne 0 ]; then
  echo "E2E FAILED: $failures check(s)"
  exit 1
fi
echo "E2E PASSED"
