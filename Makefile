.PHONY: check test vet build build-real tidy clean

# `make check` is what bosun expects each lane to run before declaring done.
# Until the store and parser lanes merge, we only test against this lane's
# owned packages — the rest of ./... has no test files yet.
check: vet test

test:
	go test -race ./cmd/leonard/... ./cmd/leonard-hook/... ./internal/hooks/... ./internal/config/... -count=1

vet:
	go vet ./cmd/leonard/... ./cmd/leonard-hook/... ./internal/hooks/... ./internal/config/...

# Default `go build` uses the stub wire-up so the cmd binaries compile on this
# lane in isolation. After the store and parser branches merge to main, swap
# `make build` for `make build-real` (or drop the build tag entirely).
build:
	go build ./...

build-real:
	go build -tags leonardreal ./cmd/leonard ./cmd/leonard-hook

tidy:
	go mod tidy

clean:
	rm -f leonard leonard-hook leonard-mcp
