#!/usr/bin/env bash
# Linux end-to-end test in Docker. Needs Docker access only, no sudo on the host.
set -euo pipefail

E2E_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$E2E_DIR/../.." && pwd)
OUT=$ROOT/.tmp/e2e
export RVT_BIN_DIR=$OUT/bin

API_KEY=e2e-api-key-0123456789abcdef
ECHO_URL=http://10.201.3.10/cgi-bin/ip
HOME1_IP=10.201.1.10
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
echo_ip() { dx home1 curl -s -m 2 "$ECHO_URL"; }
log_lines() { dx "$1" sh -c "wc -l <$2"; }
# log_since <service> <file> <line count>: log lines added after that count.
log_since() { dx "$1" sh -c "tail -n +$(($3 + 1)) $2"; }
# logged_since <service> <file> <line count> <regex>: a later line matches.
logged_since() { log_since "$1" "$2" "$3" | grep -qE "$4"; }

cleanup() {
  dx home1 cat /work/server.log >"$OUT/server.log" 2>/dev/null || true
  dx home2 cat /work/client.log >"$OUT/client.log" 2>/dev/null || true
  compose down -v -t 1 >/dev/null 2>&1 || true
  echo "logs: $OUT/server.log $OUT/client.log"
}

start_tunnel() {
  compose exec -d -T home1 sh -c "cd /work && exec /opt/rvt/reverse-vpn-tunnel --server --debug >>/work/server.log 2>&1"
  compose exec -d -T home2 sh -c "cd /work && exec /opt/rvt/reverse-vpn-tunnel --client --debug >>/work/client.log 2>&1"
}

echo "== setup"
mkdir -p "$RVT_BIN_DIR"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$RVT_BIN_DIR/reverse-vpn-tunnel" ./cmd/reverse-vpn-tunnel)
docker build -q -t rvt-e2e:latest "$E2E_DIR" >/dev/null
compose down -v -t 1 >/dev/null 2>&1 || true
trap cleanup EXIT
compose up -d
for svc in isp cloud home1 nat2 home2 web; do
  wait_eq 30 ok dx "$svc" sh -c 'test -f /tmp/ready && echo ok' || { echo "$svc not ready"; exit 1; }
done

dx home1 sh -c "cat >$SERVER_CFG" <<EOF
{
  "api_key": "$API_KEY",
  "listen_port": 51820,
  "forward_all_traffic": true,
  "block_ipv6": true,
  // Short retry for the test; minimum dead timeout.
  "reconnect": { "dead_peer_timeout_sec": $DEAD_TIMEOUT, "retry_interval_sec": 5, "max_retries": 0 }
}
EOF
dx home2 sh -c 'cat >/work/reverse-vpn-tunnel.client.jsonc' <<EOF
{
  "api_key": "$API_KEY",
  "server_address": "$HOME1_IP:51820",
  "lan_alias": { "enabled": true, "alias_cidr": "10.78.0.0/16", "lan_cidr": "auto" },
  "reconnect": { "dead_peer_timeout_sec": $DEAD_TIMEOUT, "retry_interval_sec": 5, "max_retries": 0 }
}
EOF

snapshot_home1() { dx home1 sh -c 'sysctl -a 2>/dev/null | grep -E "^net\.ipv4\.conf\..*\.(rp_filter|src_valid_mark) " | grep -v rvt0 | sort'; }
# All policy rules: the foreign ones at 5250-5252 must survive every rollback.
rules_home1() { dx home1 sh -c 'ip -N -4 rule show; ip -N -6 rule show'; }
# ip_forward, policies and other programs' rules must survive start, repair and exit.
snapshot_home2() { dx home2 sh -c 'sysctl -n net.ipv4.ip_forward; iptables -S; iptables -t nat -S'; }
home1_before=$(snapshot_home1)
home1_rules_before=$(rules_home1)
home2_before=$(snapshot_home2)

# stopped_clean <signal>: stop both sides with signal and check nothing is left.
stopped_clean() {
  local sig=$1
  dx home1 pkill "-$sig" -f "reverse-vpn-tunnel --server" || true
  dx home2 pkill "-$sig" -f "reverse-vpn-tunnel --client" || true
  # [r] keeps pgrep from matching this sh -c command line.
  check "$sig: server exited" wait_eq 10 gone dx home1 sh -c 'pgrep -f "[r]everse-vpn-tunnel --server" >/dev/null || echo gone'
  check "$sig: client exited" wait_eq 10 gone dx home2 sh -c 'pgrep -f "[r]everse-vpn-tunnel --client" >/dev/null || echo gone'
  check "$sig: home1 ip rules as before start (foreign 5250-5252 kept, none of ours)" test "$(rules_home1)" = "$home1_rules_before"
  check "$sig: home1 table 21078 empty" dx home1 sh -c '[ -z "$(ip route show table 21078 2>/dev/null)" ] && [ -z "$(ip -6 route show table 21078 2>/dev/null)" ]'
  check "$sig: home1 rvt0 removed" dx home1 sh -c '! ip link show rvt0 2>/dev/null'
  check "$sig: home1 sysctls restored" test "$(snapshot_home1)" = "$home1_before"
  check "$sig: home1 state file removed" dx home1 test ! -e /var/run/reverse-vpn-tunnel-server.json
  check "$sig: home1 echo sees home1 again" wait_eq 5 "$HOME1_IP" echo_ip
  check "$sig: home2 ip_forward, policy and sentinel rules unchanged, no RVT rules" test "$(snapshot_home2)" = "$home2_before"
  check "$sig: home2 state file removed" dx home2 test ! -e /var/run/reverse-vpn-tunnel-client.json
  check "$sig: no cleanup errors logged" not logged_since home1 /work/server.log 0 'cleanup incomplete|rollback full tunnel failed'
  check "$sig: no client cleanup errors logged" not logged_since home2 /work/client.log 0 'cleanup incomplete|repair forwarding'
}

echo "== 1. Home1 traffic leaves via Home2"
check "before tunnel: echo sees home1 ($HOME1_IP)" wait_eq 5 "$HOME1_IP" echo_ip
start_tunnel
check "tunnel up: echo sees nat2 ($NAT2_IP)" wait_eq 60 "$NAT2_IP" echo_ip
check "home1 has our 3 IPv4 rules (proto 86) next to the foreign ones" \
  dx home1 sh -c 'test "$(ip -N -4 rule show | grep -c " proto 86$")" = 3 && test "$(ip -N -4 rule show | grep -cE "^525[0-2]:")" = 7'
check "home2 FORWARD policy is still DROP and our jump is first" \
  dx home2 sh -c 'iptables -S FORWARD | sed -n 1,2p | tr "\n" ";" | grep -qx -- "-P FORWARD DROP;-A FORWARD -j RVT-FWD;"'

echo "== 2. Home2 LAN via alias while both LANs are 192.168.77.0/24"
check "home1 has its own 192.168.77.0/24" dx home1 sh -c 'ip -4 route show 192.168.77.0/24 | grep -q lan-dup'
check "curl 10.78.0.20 reaches Home2 web (sees client 192.168.77.10)" \
  wait_eq 10 192.168.77.10 dx home1 curl -s -m 2 http://10.78.0.20/cgi-bin/ip

echo "== F8. idle tunnel for 3 x dead_peer_timeout_sec ($((3 * DEAD_TIMEOUT))s)"
srv_mark=$(log_lines home1 /work/server.log)
cli_mark=$(log_lines home2 /work/client.log)
sleep $((3 * DEAD_TIMEOUT + 4))
check "idle: server logged no 'peer dead'" not logged_since home1 /work/server.log "$srv_mark" 'peer dead'
check "idle: client logged no 'server dead'" not logged_since home2 /work/client.log "$cli_mark" 'server dead'
echo "  probes during idle: server $(log_since home1 /work/server.log "$srv_mark" | grep -c 'probe sent' || true), client $(log_since home2 /work/client.log "$cli_mark" | grep -c 'probe sent' || true)"
check "idle: still forwarding" wait_eq 5 "$NAT2_IP" echo_ip

echo "== F8. probe makes a silent client answer (raw ICMP on a real kernel)"
srv_mark=$(log_lines home1 /work/server.log)
dx home2 iptables -I OUTPUT -p udp --dport 51820 -j DROP
check "server probes the silent client" wait_ok 30 logged_since home1 /work/server.log "$srv_mark" 'probe sent'
dx home2 iptables -D OUTPUT -p udp --dport 51820 -j DROP
sleep 20
check "client answered before the dead timeout" not logged_since home1 /work/server.log "$srv_mark" 'peer dead|probe peer'

check "after probe: echo sees nat2" wait_eq 5 "$NAT2_IP" echo_ip

echo "== 3. forward_all_traffic edited live"
flag_sed() { dx home1 sed -i "s/\"forward_all_traffic\": $1/\"forward_all_traffic\": $2/" "$SERVER_CFG"; }
flag_rename() {
  dx home1 sh -c "sed 's/\"forward_all_traffic\": $1/\"forward_all_traffic\": $2/' $SERVER_CFG >/work/.swp && mv /work/.swp $SERVER_CFG"
}
for method in sed rename; do
  "flag_$method" true false
  check "$method → false: echo sees home1" wait_eq 15 "$HOME1_IP" echo_ip
  check "$method → false: rollback left foreign rules and removed ours" test "$(rules_home1)" = "$home1_rules_before"
  check "$method → false: ping 10.77.0.2 still works" dx home1 sh -c 'ping -c1 -W2 10.77.0.2 >/dev/null'
  "flag_$method" false true
  check "$method → true: echo sees nat2" wait_eq 15 "$NAT2_IP" echo_ip
done

echo "== F16. firewall reload on Home2 drops our FORWARD jump"
dx home2 iptables -D FORWARD -j RVT-FWD
check "self-repair puts the jump back first" \
  wait_ok 45 dx home2 sh -c 'iptables -S FORWARD | sed -n 2p | grep -qx -- "-A FORWARD -j RVT-FWD"'
check "after repair: echo sees nat2" wait_eq 15 "$NAT2_IP" echo_ip

echo "== 4. client paused: fail-open, then back"
compose pause home2
check "paused: echo sees home1 after dead_peer_timeout" wait_eq $((DEAD_TIMEOUT + 45)) "$HOME1_IP" echo_ip
compose unpause home2
check "unpaused: echo sees nat2 again" wait_eq 90 "$NAT2_IP" echo_ip

echo "== 5. SIGTERM leaves nothing behind"
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
