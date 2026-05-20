package main

import (
	"context"

	"github.com/spf13/cobra"
)

// Version is the leonard CLI version string. cobra wires it into
// the auto-generated `--version` flag once Command.Version is set.
// Kept in sync with cmd/leonard-mcp/main.go's `version` var; each
// minor release bumps both via the same commit (see CHANGELOG.md).
const Version = "0.48.0"

// Runtime is the minimum collaborator surface the cobra subcommands need.
// Concrete implementations live in wire_real.go (uses internal/store +
// internal/index) and wire_stub.go (in-process fake for unit tests and for
// builds on lanes where the store/parser code has not yet merged).
type Runtime interface {
	// Init prepares a project: creates dataDir (.leonard), opens the store
	// once so the schema migration runs, writes a default config if missing.
	Init(ctx context.Context, projectRoot, dataDir string) error

	// IndexAll opens the store, drives a full re-index of projectRoot, and
	// reports the number of files touched plus any per-file parse failures
	// surfaced by the extractors. A non-nil error indicates the walk itself
	// failed (couldn't open the store, etc.) — individual parse failures
	// land in the ParseFailures slice instead.
	IndexAll(ctx context.Context, projectRoot, dataDir string) (IndexResult, error)

	// VerifySymbol opens the store and returns matches by name. kind is
	// optional ("" means any).
	VerifySymbol(ctx context.Context, dataDir, name, kind string) ([]SymbolMatch, error)

	// RecordDecision opens the store and persists a decision row. Mirrors
	// the MCP record_decision tool. Returns the assigned row ID.
	RecordDecision(ctx context.Context, dataDir, topic, choice, reasoning string) (int64, error)

	// GetDecisions opens the store and returns decisions newest-first.
	// topic is optional (empty = all topics); since is a unix-second lower
	// bound (0 = no bound); limit caps the slice (0 = use the same default
	// the MCP tool publishes).
	GetDecisions(ctx context.Context, dataDir, topic string, since int64, limit int) ([]DecisionRow, error)

	// GetStaleDecisions returns decisions whose related_files or
	// related_symbols no longer resolve against the live index.
	GetStaleDecisions(ctx context.Context, dataDir string, limit int) ([]StaleDecisionRow, error)

	// GetUnverifiedClaims returns claim rows where verified=false.
	// sessionID is optional (empty = all sessions).
	GetUnverifiedClaims(ctx context.Context, dataDir, sessionID string) ([]ClaimRow, error)

	// ResolveClaim marks a single claim as manually resolved by the
	// operator. v0.38 escape hatch for stale claims that the
	// auto-supersede (SupersedeOutstandingFailures on vet=ok)
	// doesn't catch. note is optional audit text appended to the
	// claim's evidence.
	ResolveClaim(ctx context.Context, dataDir string, claimID int64, note string) error

	// Doctor inspects store + filesystem state and returns a health report.
	// Used by `leonard doctor` — opens the store once and collects every
	// data-point the command surfaces.
	Doctor(ctx context.Context, projectRoot, dataDir string) (DoctorReport, error)
}

// DoctorReport is the structured output of `leonard doctor`. Counts and
// per-language breakdowns mirror what the store can answer cheaply; the
// EmptyFiles + StaleFiles slices are the "things worth your attention"
// surface.
type DoctorReport struct {
	StorePath        string
	LastIndexedAt    int64
	FilesByLanguage  []LanguageCount
	SymbolsByLanguage []LanguageCount
	TotalFiles       int
	TotalSymbols     int

	// EmptyFiles are paths that have a file row but zero symbols. For
	// supported languages this almost always means a parse failure;
	// for unsupported languages it just means we don't extract anything.
	EmptyFiles []string

	// StaleFiles are paths that exist in the store but no longer on disk.
	// A `leonard index` after a delete/rename should clear these.
	StaleFiles []string

	DecisionCount      int
	StaleDecisionCount int
	UnverifiedClaims   int
}

// LanguageCount is the per-language tally used in the doctor report's
// breakdown sections. Sorted by Language ascending by the runtime impl.
type LanguageCount struct {
	Language string
	Count    int
}

// DecisionRow is the small DTO the decisions subcommand prints. Mirrors
// store.Decision, declared locally so cmd/leonard stays decoupled.
type DecisionRow struct {
	ID         int64
	Topic      string
	Choice     string
	Reasoning  string
	RecordedAt int64
}

// StaleDecisionRow carries the decision plus the specific refs that no
// longer resolve. Mirrors store.StaleDecision.
type StaleDecisionRow struct {
	Decision       DecisionRow
	MissingFiles   []string
	MissingSymbols []string
}

// ClaimRow is the projection of store.Claim the claims subcommand prints.
type ClaimRow struct {
	ID         int64
	SessionID  string
	Claim      string
	Evidence   string
	FilePath   string
	RecordedAt int64
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

// IndexResult summarizes one IndexAll invocation. ParseFailures is the
// per-file extractor errors — important to expose because silently
// dropping symbols for files we couldn't parse is the exact category of
// "false done claim" Leonard exists to prevent.
type IndexResult struct {
	FilesIndexed  int
	ParseFailures []ParseFailure
}

// ParseFailure mirrors index.ParseFailure but is re-declared here so the
// cmd/leonard surface stays decoupled from internal/index.
type ParseFailure struct {
	Path    string
	Message string
}

// newRootCmd assembles the cobra tree. The Runtime is injected so unit tests
// can substitute a fake.
func newRootCmd(rt Runtime) *cobra.Command {
	root := &cobra.Command{
		Use:           "leonard",
		Short:         "Project ground-truth indexer for Claude Code.",
		Long:          "Leonard maintains a local symbol index and a claims ledger that Claude Code consults via MCP and post-edit hooks. See DESIGN.md for the long form.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newInitCmd(rt))
	root.AddCommand(newIndexCmd(rt))
	root.AddCommand(newVerifyCmd(rt))
	root.AddCommand(newDecisionsCmd(rt))
	root.AddCommand(newClaimsCmd(rt))
	root.AddCommand(newDoctorCmd(rt))
	root.AddCommand(newMCPCmd())
	return root
}
