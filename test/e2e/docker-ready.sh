#!/usr/bin/env bash
# Exit 0 when Docker can run the e2e tests; otherwise print why and exit 1.
skip() {
  echo "SKIP: $1, e2e test not run" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || skip "docker not installed"
docker info >/dev/null 2>&1 || skip "docker daemon not reachable (not running, or no permission)"
docker compose version >/dev/null 2>&1 || skip "docker compose plugin not installed"
