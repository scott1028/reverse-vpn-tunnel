.DEFAULT_GOAL := help

.PHONY: release debug test clean help

release:
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/reverse-vpn-tunnel ./cmd/reverse-vpn-tunnel

debug:
	mkdir -p dist
	CGO_ENABLED=0 go build -gcflags="all=-N -l" -o dist/reverse-vpn-tunnel-debug ./cmd/reverse-vpn-tunnel

test:
	go test ./...

# Only the binaries: dist/ also holds local config files.
clean:
	rm -f dist/reverse-vpn-tunnel dist/reverse-vpn-tunnel-debug

help:
	@printf '%s\n' \
		'make release  Build a stripped binary at dist/reverse-vpn-tunnel' \
		'make debug    Build a binary with optimizations and inlining disabled at dist/reverse-vpn-tunnel-debug' \
		'make test     Run Go tests' \
		'make clean    Remove the binaries from dist/ (config files are kept)' \
		'make help     Show available targets (default)'
