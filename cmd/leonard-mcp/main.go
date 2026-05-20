// Command leonard-mcp is the stdio MCP server exposing Leonard's
// ground-truth tools (verify_symbol, find_symbol, list_files) to Claude
// Code. It speaks newline-delimited JSON-RPC over stdin/stdout per the
// MCP stdio transport spec.
package main

import (
	"context"
	"errors"
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
var version = "0.6.0"

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

	go watchDatabase(ctx, stop, adapter, dbPath, os.Stderr, dbWatchInterval)

	srv := leonardmcp.NewServer(adapter, leonardmcp.Implementation{
		Name:    "leonard-mcp",
		Version: version,
	})

	// Filter stdin through a JSON-RPC envelope screen so that a stray
	// non-JSON line from a misbehaving parent process doesn't crash the
	// server. Use IOTransport directly rather than StdioTransport so we
	// can substitute the reader. Writer side mirrors what StdioTransport
	// does internally (no-op Close around os.Stdout). Bughunt-2 mcp F1.
	transport := &mcp.IOTransport{
		Reader: newJSONLineFilter(os.Stdin, os.Stderr),
		Writer: nopWriteCloser{os.Stdout},
	}
	return translateExitErr(srv.Run(ctx, transport))
}

// translateExitErr maps the MCP SDK's run-loop result onto exit semantics.
// SIGINT, SIGTERM, and the db-watcher's stop() all cancel the run context,
// which surfaces as context.Canceled — those are clean shutdowns and must
// exit 0 so process supervisors don't treat them as crashes. Any other
// error propagates unchanged.
func translateExitErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// watchDatabase polls the DB path on a ticker and triggers a clean
// shutdown if the file disappears or its inode no longer matches the
// open handle. Lazy detection on each tool call (in StoreAdapter) is the
// primary guard; this watcher tears the server down so a session that
// goes idle after the swap doesn't keep returning stale reads.
//
// interval is parameterized so tests can poll at millisecond speed
// without changing the production cadence.
func watchDatabase(ctx context.Context, stop func(), a *leonardmcp.StoreAdapter, dbPath string, errOut io.Writer, interval time.Duration) {
	ticker := time.NewTicker(interval)
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
