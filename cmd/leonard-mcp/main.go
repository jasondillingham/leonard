// Command leonard-mcp is the stdio MCP server exposing Leonard's
// ground-truth tools (verify_symbol, find_symbol, list_files) to Claude
// Code. It speaks newline-delimited JSON-RPC over stdin/stdout per the
// MCP stdio transport spec.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"
	"github.com/jasondillingham/leonard/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is overridable at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

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

	srv := leonardmcp.NewServer(leonardmcp.NewStoreAdapter(st), leonardmcp.Implementation{
		Name:    "leonard-mcp",
		Version: version,
	})

	return srv.Run(ctx, &mcp.StdioTransport{})
}
