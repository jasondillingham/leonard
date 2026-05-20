.PHONY: check test test-race vet build build-otel build-rust build-treesitter tidy clean install help

# Default target — what local development should run before a commit.
check: vet test-race

# Run tests in race mode, no cache. The race-detector catches
# concurrency bugs that the regular suite can miss (and several
# bug-hunt rounds have found exactly that).
test-race:
	go test -race ./... -count=1

# Faster cycle for tight inner loops.
test:
	go test ./...

# Clean go vet across the workspace. Enforced by the post-edit hook,
# but `make vet` lets you confirm before staging.
vet:
	go vet ./...

# Build all three binaries to repo root for quick local smoke. The
# canonical install target is `make install`, which puts them in
# $GOPATH/bin where the Claude Code hook wiring expects them.
build:
	go build ./cmd/leonard ./cmd/leonard-hook ./cmd/leonard-mcp

# OTel-instrumented build (build-tag gated). Default builds compile
# stub spans — this target opts into the real OpenTelemetry SDK on
# the leonard-hook hot paths. See README "Telemetry (optional)".
build-otel:
	go build -tags otel ./cmd/leonard ./cmd/leonard-hook ./cmd/leonard-mcp

# Build the syn-based Rust extractor (~9s on a cold target/, 55 MB
# cache). Discovered automatically by the indexer; override the path
# via LEONARD_RUST_EXTRACTOR.
build-rust:
	cd internal/parse/rust && cargo build --release

# Build the 29-grammar tree-sitter dispatcher (~15-20s on cold
# target/, ~400 MB cache). Override via LEONARD_TREESITTER_EXTRACTOR.
build-treesitter:
	cd internal/parse/treesitter && cargo build --release

# Install all three Go binaries to $GOPATH/bin. The Rust helpers
# remain discovered from their internal/parse/{rust,treesitter}/
# target/release/ paths.
install:
	go install ./cmd/leonard ./cmd/leonard-hook ./cmd/leonard-mcp

tidy:
	go mod tidy

clean:
	rm -f leonard leonard-hook leonard-mcp

help:
	@echo "Targets:"
	@echo "  check            vet + race-tests (default; what to run before staging)"
	@echo "  test             go test ./..."
	@echo "  test-race        go test -race ./..."
	@echo "  vet              go vet ./..."
	@echo "  build            build all three Go binaries to repo root"
	@echo "  build-otel       build with OpenTelemetry instrumentation enabled"
	@echo "  build-rust       cargo build the syn-based Rust extractor"
	@echo "  build-treesitter cargo build the 29-grammar tree-sitter dispatcher"
	@echo "  install          go install the three Go binaries to \$$GOPATH/bin"
	@echo "  tidy             go mod tidy"
	@echo "  clean            rm built binaries from repo root"
