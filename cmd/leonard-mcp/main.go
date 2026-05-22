// Command leonard-mcp is the stdio MCP server exposing Leonard's
// adapter tool surfaces (code: verify_symbol / find_symbol /
// list_files / record_decision / etc; ground-truth: verify_claim /
// list_facts / get_story / get_truth_history) to Claude Code.
//
// v1.0 (#46): routes through internal/dispatcher so the same
// adapter selection rules apply as in leonard-hook. Each enabled
// adapter calls its own RegisterTools onto the MCP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jasondillingham/leonard/internal/dispatcher"
	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"
)

// version is overridable at build time via -ldflags "-X main.version=...".
var version = "0.53.0"

func main() {
	// Bughunt-5 launch-readiness B4: `leonard-mcp --version` used to
	// exit 0 with no output because the binary never goes through
	// cobra. Check argv before falling into the MCP run loop.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			fmt.Println("leonard-mcp", version)
			return
		case "--help", "-h", "help":
			fmt.Fprintln(os.Stderr, "leonard-mcp — stdio MCP server for Leonard's adapter tool surfaces.")
			fmt.Fprintln(os.Stderr, "Run with no arguments to start the server (expects MCP JSON-RPC on stdin/stdout).")
			fmt.Fprintln(os.Stderr, "Version:", version)
			return
		}
	}
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
	dataDir := filepath.Join(cwd, ".leonard")
	if _, err := os.Stat(dataDir); err != nil {
		return fmt.Errorf("leonard not initialized at %s — run `leonard init` first", dataDir)
	}

	dispatcher.SetGlobalStderr(os.Stderr)
	loaded, err := dispatcher.LoadEnabled(ctx, cwd, dataDir, os.Stderr)
	if err != nil {
		return fmt.Errorf("load adapters: %w", err)
	}
	defer loaded.Close()

	srv := leonardmcp.NewBareServer(leonardmcp.Implementation{
		Name:    "leonard-mcp",
		Version: version,
	})

	// Each loaded adapter contributes its own tool surface. The code
	// adapter registers the v0.52 code-symbol + decisions/claims
	// tools; the ground-truth adapter registers verify_claim /
	// list_facts / get_story (+ get_truth_history when the store
	// satisfies the right interface).
	for _, a := range loaded.Adapters {
		if err := a.RegisterTools(srv); err != nil {
			fmt.Fprintf(os.Stderr, "leonard-mcp: %s RegisterTools: %v\n", a.Name(), err)
		}
	}

	// Filter stdin through a JSON-RPC envelope screen so a stray
	// non-JSON line from a misbehaving parent process doesn't crash
	// the server. Bughunt-2 mcp F1 defense, kept from v0.52.
	transport := &mcp.IOTransport{
		Reader: newJSONLineFilter(os.Stdin, os.Stderr),
		Writer: nopWriteCloser{os.Stdout},
	}
	return translateExitErr(srv.Run(ctx, transport))
}

// translateExitErr maps the MCP SDK's run-loop result onto exit semantics.
// SIGINT / SIGTERM cancel the run context, which surfaces as
// context.Canceled — those are clean shutdowns and must exit 0 so
// process supervisors don't treat them as crashes.
func translateExitErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
