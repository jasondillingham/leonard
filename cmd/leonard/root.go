package main

import (
	"context"

	"github.com/spf13/cobra"
)

// Runtime is the minimum collaborator surface the cobra subcommands need.
// Concrete implementations live in wire_real.go (uses internal/store +
// internal/index) and wire_stub.go (in-process fake for unit tests and for
// builds on lanes where the store/parser code has not yet merged).
type Runtime interface {
	// Init prepares a project: creates dataDir (.leonard), opens the store
	// once so the schema migration runs, writes a default config if missing.
	Init(ctx context.Context, projectRoot, dataDir string) error

	// IndexAll opens the store, drives a full re-index of projectRoot, and
	// reports the number of files touched.
	IndexAll(ctx context.Context, projectRoot, dataDir string) (filesIndexed int, err error)

	// VerifySymbol opens the store and returns matches by name. kind is
	// optional ("" means any).
	VerifySymbol(ctx context.Context, dataDir, name, kind string) ([]SymbolMatch, error)
}

// SymbolMatch is the small DTO the verify subcommand prints. It mirrors the
// fields documented for the MCP verify_symbol response — keeping it as a
// local type avoids dragging internal/store types into cmd/leonard's public
// test surface.
type SymbolMatch struct {
	File          string
	Line          int
	Signature     string
	Kind          string
	QualifiedName string
}

// newRootCmd assembles the cobra tree. The Runtime is injected so unit tests
// can substitute a fake.
func newRootCmd(rt Runtime) *cobra.Command {
	root := &cobra.Command{
		Use:           "leonard",
		Short:         "Project ground-truth indexer for Claude Code.",
		Long:          "Leonard maintains a local symbol index and a claims ledger that Claude Code consults via MCP and post-edit hooks. See DESIGN.md for the long form.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newInitCmd(rt))
	root.AddCommand(newIndexCmd(rt))
	root.AddCommand(newVerifyCmd(rt))
	root.AddCommand(newMCPCmd())
	return root
}
