.DEFAULT_GOAL := help

.PHONY: release debug test e2e-test e2e-full clean help

release:
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/reverse-vpn-tunnel ./cmd/reverse-vpn-tunnel

debug:
	mkdir -p dist
	CGO_ENABLED=0 go build -gcflags="all=-N -l" -o dist/reverse-vpn-tunnel-debug ./cmd/reverse-vpn-tunnel

test:
	go test ./...

# Both e2e targets skip with exit 0 when Docker is not usable.
e2e-test:
	@if bash test/e2e/docker-ready.sh; then bash test/e2e/smoke/run.sh; fi

e2e-full:
	@if bash test/e2e/docker-ready.sh; then bash test/e2e/run.sh; fi

# Only the binaries: dist/ also holds local config files.
clean:
	rm -f dist/reverse-vpn-tunnel dist/reverse-vpn-tunnel-debug

help:
	@printf '%s\n' \
		'make release   Build a stripped binary at dist/reverse-vpn-tunnel' \
		'make debug     Build a binary with optimizations and inlining disabled at dist/reverse-vpn-tunnel-debug' \
		'make test      Run Go tests' \
		'make e2e-test  Run the two-container Docker smoke test (skipped without Docker)' \
		'make e2e-full  Run the full Docker e2e test, takes minutes (skipped without Docker)' \
		'make clean     Remove the binaries from dist/ (config files are kept)' \
		'make help      Show available targets (default)'
