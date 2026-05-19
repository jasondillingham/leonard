// Command leonard-mcp is the stdio MCP server exposing Leonard's
// ground-truth tools (verify_symbol, find_symbol, list_files) to Claude
// Code. It speaks newline-delimited JSON-RPC over stdin/stdout per the
// MCP stdio transport spec.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"
	"github.com/jasondillingham/leonard/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is overridable at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

// dbWatchInterval is how often the active watcher polls the DB path for a
// swap. Two seconds is a generous balance: well under the human latency of
// "did my init succeed?" while keeping the syscall load trivial.
const dbWatchInterval = 2 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "leonard-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cwd: %w", err)
	}
	dbPath := filepath.Join(cwd, ".leonard", "leonard.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("leonard store not found at %s — run `leonard init` first", dbPath)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	adapter := leonardmcp.NewStoreAdapter(st)
	if err := adapter.WatchDatabase(dbPath); err != nil {
		return fmt.Errorf("watch database: %w", err)
	}

	go watchDatabase(ctx, stop, adapter, dbPath, os.Stderr)

	srv := leonardmcp.NewServer(adapter, leonardmcp.Implementation{
		Name:    "leonard-mcp",
		Version: version,
	})

	return srv.Run(ctx, &mcp.StdioTransport{})
}

// watchDatabase polls the DB path on a ticker and triggers a clean
// shutdown if the file disappears or its inode no longer matches the
// open handle. Lazy detection on each tool call (in StoreAdapter) is the
// primary guard; this watcher tears the server down so a session that
// goes idle after the swap doesn't keep returning stale reads.
func watchDatabase(ctx context.Context, stop func(), a *leonardmcp.StoreAdapter, dbPath string, errOut io.Writer) {
	ticker := time.NewTicker(dbWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.CheckDatabase(); err != nil {
				fmt.Fprintf(errOut, "leonard-mcp: database at %s was removed or replaced — shutting down\n", dbPath)
				stop()
				return
			}
		}
	}
}
