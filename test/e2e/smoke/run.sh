#!/usr/bin/env bash
# Two-container smoke test: Entry sends its default traffic out through Exit.
# Needs Docker access only, no sudo on the host.
set -euo pipefail

SMOKE_DIR=$(cd "$(dirname "$0")" && pwd)
E2E_DIR=$(cd "$SMOKE_DIR/.." && pwd)
ROOT=$(cd "$E2E_DIR/../.." && pwd)
OUT=$ROOT/.tmp/e2e-smoke
export RVT_BIN_DIR=$OUT/bin

API_KEY=smoke-api-key-0123456789abcdef
ENTRY_IP=10.202.0.10
# Nothing answers here; the default route only picks the tunnel's interface.
GATEWAY_IP=10.202.0.1
# Echo server in a netns inside Exit, so only Exit can reach it.
TARGET_IP=198.51.100.1
# Exit's end of the veth: the source the target sees after Exit's NAT.
EXIT_NAT_IP=198.51.100.254
TARGET_URL=http://$TARGET_IP/cgi-bin/ip

compose() { docker compose -p rvt-e2e-smoke -f "$SMOKE_DIR/docker-compose.yml" "$@"; }
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
# target_seen_from <service>: the source address the target sees from that service.
target_seen_from() { dx "$1" curl -s -m 2 "$TARGET_URL"; }

cleanup() {
  dx entry cat /work/server.log >"$OUT/server.log" 2>/dev/null || true
  dx exit cat /work/client.log >"$OUT/client.log" 2>/dev/null || true
  compose down -v -t 1 >/dev/null 2>&1 || true
  echo "logs: $OUT/server.log $OUT/client.log"
}

echo "== setup"
mkdir -p "$RVT_BIN_DIR"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$RVT_BIN_DIR/reverse-vpn-tunnel" ./cmd/reverse-vpn-tunnel)
docker build -q -t rvt-e2e:latest "$E2E_DIR" >/dev/null
compose down -v -t 1 >/dev/null 2>&1 || true
trap cleanup EXIT
compose up -d

# The tunnel binds to the default route's interface, so keep it on the shared network.
for svc in entry exit; do
  dx "$svc" sh -c "ip route replace default via $GATEWAY_IP && mkdir -p /work"
done
dx exit sh -s -- "$TARGET_IP" "$EXIT_NAT_IP" <<'EOF'
set -eu
ip netns add inet
ip link add veth-inet type veth peer name veth-out netns inet
ip addr add "$2/24" dev veth-inet
ip link set veth-inet up
ip -n inet addr add "$1/24" dev veth-out
ip -n inet link set veth-out up
ip -n inet link set lo up
# Replies with the caller's address, as seen by the target.
mkdir -p /www/cgi-bin
printf '#!/bin/sh\nprintf "Content-Type: text/plain\\r\\n\\r\\n%%s\\n" "$REMOTE_ADDR"\n' >/www/cgi-bin/ip
chmod +x /www/cgi-bin/ip
ip netns exec inet busybox httpd -p "$1:80" -h /www
EOF

dx entry sh -c 'cat >/work/reverse-vpn-tunnel.server.jsonc' <<EOF
{
  "api_key": "$API_KEY",
  "listen_port": 51820,
  // Short retry for the test.
  "reconnect": { "retry_interval_sec": 5 }
}
EOF
dx exit sh -c 'cat >/work/reverse-vpn-tunnel.client.jsonc' <<EOF
{
  "api_key": "$API_KEY",
  "server_address": "$ENTRY_IP:51820",
  "reconnect": { "retry_interval_sec": 5 }
}
EOF

echo "== 1. baseline without the tunnel"
check "exit reaches the target itself (sees $EXIT_NAT_IP)" wait_eq 10 "$EXIT_NAT_IP" target_seen_from exit
check "entry cannot reach the target" not target_seen_from entry

echo "== 2. tunnel up"
compose exec -d -T entry sh -c "cd /work && exec /opt/rvt/reverse-vpn-tunnel --server --debug >>/work/server.log 2>&1"
compose exec -d -T exit sh -c "cd /work && exec /opt/rvt/reverse-vpn-tunnel --client --debug >>/work/client.log 2>&1"
check "entry routes the target into the tunnel (dev rvt0)" \
  wait_ok 60 dx entry sh -c "ip route get $TARGET_IP | grep -q ' dev rvt0 '"

echo "== 3. gate: Exit forwards Entry's default traffic"
check "entry reaches the target through exit (sees $EXIT_NAT_IP)" \
  wait_eq 30 "$EXIT_NAT_IP" target_seen_from entry

echo
if [ "$failures" -ne 0 ]; then
  echo "SMOKE FAILED: $failures check(s)"
  exit 1
fi
echo "SMOKE PASSED"
