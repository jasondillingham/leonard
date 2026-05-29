package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jasondillingham/leonard/internal/telemetry"
)

// Implementation reports the leonard-mcp server identity advertised over
// the wire. Version is overridable so the binary can stamp build info.
type Implementation struct {
	Name    string
	Version string
}

// DefaultImplementation is the server identity used when a caller doesn't
// supply one (e.g. tests).
func DefaultImplementation() Implementation {
	return Implementation{Name: "leonard-mcp", Version: "0.54.0"}
}

// NewServer constructs an MCP server with Leonard's v1 tools registered
// against the supplied SymbolStore.
func NewServer(store SymbolStore, impl Implementation) *mcp.Server {
	if impl.Name == "" {
		impl = DefaultImplementation()
	}
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    impl.Name,
		Version: impl.Version,
	}, nil)
	register(srv, store)
	return srv
}

// NewBareServer constructs an MCP server with NO tools registered.
// Used by the v1.0 dispatcher-based leonard-mcp main: each loaded
// adapter calls its own RegisterTools onto the returned server, so
// the bare-server step is just the identity advertisement.
//
// Tests and embedded callers that want the v0.52 code-symbol tool
// set should call NewServer instead (or call RegisterTools manually
// after NewBareServer).
func NewBareServer(impl Implementation) *mcp.Server {
	if impl.Name == "" {
		impl = DefaultImplementation()
	}
	return mcp.NewServer(&mcp.Implementation{
		Name:    impl.Name,
		Version: impl.Version,
	}, nil)
}

// RegisterTools wires Leonard's v1 tool set onto srv with the supplied
// store. v0.6+ exposes this so the adapter system can attach tools to
// an already-constructed *mcp.Server (see internal/adapters/code.
// CodeAdapter.RegisterTools); cmd/leonard-mcp continues to use
// NewServer, which calls this internally.
func RegisterTools(srv *mcp.Server, store SymbolStore) {
	register(srv, store)
}

// register wires the v1 tool set onto srv. Split out so tests can assert
// the tool list without rebuilding the server constructor.
func register(srv *mcp.Server, store SymbolStore) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "verify_symbol",
		Description: "Verify whether a symbol with the given name exists in the indexed project. Returns matching declarations with file/line/signature.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in VerifySymbolInput) (*mcp.CallToolResult, VerifySymbolOutput, error) {
		out, err := verifySymbol(ctx, store, in)
		if err != nil {
			return nil, VerifySymbolOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "find_symbol",
		Description: "Search the symbol index for declarations whose name or qualified name contains the query (case-insensitive).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in FindSymbolInput) (*mcp.CallToolResult, FindSymbolOutput, error) {
		out, err := findSymbol(ctx, store, in)
		if err != nil {
			return nil, FindSymbolOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_files",
		Description: "List files known to the symbol index, optionally filtered by glob pattern and/or language.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListFilesInput) (*mcp.CallToolResult, ListFilesOutput, error) {
		out, err := listFiles(ctx, store, in)
		if err != nil {
			return nil, ListFilesOutput{}, err
		}
		return nil, out, nil
	})

	// Decision tools are only wired when the store also implements the
	// DecisionStore surface. The real StoreAdapter satisfies both — the
	// phase-1 in-memory test fixture (MemStore) intentionally doesn't, so
	// existing tests don't have to grow decision plumbing.
	if ds, ok := store.(DecisionStore); ok {
		registerDecisionTools(srv, ds)
	}

	// Claim tools follow the same opt-in pattern as the decision tools.
	if cs, ok := store.(ClaimStore); ok {
		registerClaimTools(srv, cs)
	}

	// Same gated wiring for recent_changes: only the real StoreAdapter
	// (and changes-aware test fixtures) implement ChangesStore.
	if cs, ok := store.(ChangesStore); ok {
		registerChangesTool(srv, cs)
	}

	// get_truth_history (#28): wired only when the store satisfies
	// TruthHistoryStore. Matches the gated-tool pattern above.
	if ts, ok := store.(TruthHistoryStore); ok {
		registerTruthHistoryTool(srv, ts)
	}
}

// verifySymbolSuggestionsLimit caps the number of fuzzy candidates returned
// in the suggestions field when verify_symbol finds no exact match.
const verifySymbolSuggestionsLimit = 5

// maxSymbolQueryBytes caps find_symbol.query and verify_symbol.name.
// 4 KiB is well above any realistic symbol name while staying far below
// SQLite's LIKE/GLOB pattern-length limit (~50 KB), which would otherwise
// surface as a raw "LIKE or GLOB pattern too complex" SQL error (F007).
const maxSymbolQueryBytes = 4096

// verifySymbol is the thin shim: name lookup + optional kind/language
// filter + record-to-wire translation. On an exact miss it runs a fuzzy
// query and returns up to verifySymbolSuggestionsLimit candidates in the
// suggestions field so callers can correct a misspelling without a
// separate find_symbol round-trip.
func verifySymbol(ctx context.Context, store SymbolStore, in VerifySymbolInput) (VerifySymbolOutput, error) {
	ctx, end := telemetry.Span(ctx, "leonard.mcp.verify_symbol")
	defer end()
	if len(in.Name) > maxSymbolQueryBytes {
		return VerifySymbolOutput{}, fmt.Errorf("verify_symbol: name exceeds %d-byte cap (got %d bytes)", maxSymbolQueryBytes, len(in.Name))
	}
	syms, err := store.FindSymbolsByName(ctx, in.Name)
	if err != nil {
		return VerifySymbolOutput{}, err
	}
	matches := filterAndConvert(syms, in.Kind, in.Language, 0)
	if len(matches) > 0 {
		return VerifySymbolOutput{Exists: true, Matches: matches}, nil
	}
	// Exact miss — run a fuzzy query for "did you mean" suggestions.
	fuzzy, ferr := store.FindSymbolsByQuery(ctx, in.Name, verifySymbolSuggestionsLimit)
	if ferr != nil {
		// Fuzzy failure is non-fatal; return the miss without suggestions.
		return VerifySymbolOutput{Exists: false, Matches: matches}, nil
	}
	suggestions := filterAndConvert(fuzzy, in.Kind, in.Language, verifySymbolSuggestionsLimit)
	return VerifySymbolOutput{Exists: false, Matches: matches, Suggestions: suggestions}, nil
}

func findSymbol(ctx context.Context, store SymbolStore, in FindSymbolInput) (FindSymbolOutput, error) {
	ctx, end := telemetry.Span(ctx, "leonard.mcp.find_symbol")
	defer end()
	if in.Query == "" {
		return FindSymbolOutput{}, fmt.Errorf("find_symbol: query must not be empty")
	}
	if len(in.Query) > maxSymbolQueryBytes {
		return FindSymbolOutput{}, fmt.Errorf("find_symbol: query exceeds %d-byte cap (got %d bytes)", maxSymbolQueryBytes, len(in.Query))
	}
	syms, err := store.FindSymbolsByQuery(ctx, in.Query, int(in.Limit))
	if err != nil {
		return FindSymbolOutput{}, err
	}
	return FindSymbolOutput{Matches: filterAndConvert(syms, in.Kind, in.Language, int(in.Limit))}, nil
}

// listFilesDefaultLimit + listFilesMaxLimit cap the response size.
// Bughunt-4 caps F7: ListFilesInput previously had no limit at all,
// so on a 10k-file project list_files returned every row.
const (
	listFilesDefaultLimit = 200
	listFilesMaxLimit     = 1000
)

func listFiles(ctx context.Context, store SymbolStore, in ListFilesInput) (ListFilesOutput, error) {
	ctx, end := telemetry.Span(ctx, "leonard.mcp.list_files")
	defer end()
	limit := int(in.Limit)
	if limit <= 0 {
		limit = listFilesDefaultLimit
	}
	if limit > listFilesMaxLimit {
		limit = listFilesMaxLimit
	}
	files, err := store.ListFiles(ctx, in.Pattern, in.Language)
	if err != nil {
		return ListFilesOutput{}, err
	}
	if len(files) > limit {
		files = files[:limit]
	}
	out := make([]FileEntry, 0, len(files))
	for _, f := range files {
		out = append(out, FileEntry{
			Path:      f.Path,
			Language:  f.Language,
			SizeBytes: f.SizeBytes,
		})
	}
	return ListFilesOutput{Files: out}, nil
}

// MaxSymbolResults caps the number of SymbolMatch entries any one
// verify_symbol or find_symbol call can return. Bughunt-6 mcp F2:
// the v0.1 path had no SQL LIMIT and no MCP-layer ceiling, so a
// caller passing `limit=10000000` could materialize the entire
// symbol table before any cap took effect. 500 is well above every
// real consumer (Claude's reasoning loops are bounded by their own
// context budget) while preventing runaway materialization.
const MaxSymbolResults = 500

// filterAndConvert applies kind/language filters to a slice of store
// symbol records and converts them to the wire format. The MCP layer
// (not the store) owns language filtering because store.Symbol doesn't
// carry language directly — we derive it from the file extension here.
//
// limit semantics:
//   - limit <= 0 → use MaxSymbolResults
//   - limit > 0 → clamp to min(limit, MaxSymbolResults)
//
// This protects against the bughunt-6 mcp F2 case where a caller
// passes a giant limit.
func filterAndConvert(syms []SymbolRecord, kind, language string, limit int) []SymbolMatch {
	if limit <= 0 || limit > MaxSymbolResults {
		limit = MaxSymbolResults
	}
	out := make([]SymbolMatch, 0, limit)
	for _, s := range syms {
		if kind != "" && s.Kind != kind {
			continue
		}
		if language != "" && languageFromPath(s.FilePath) != language {
			continue
		}
		out = append(out, SymbolMatch{
			File:          s.FilePath,
			Line:          s.StartLine,
			Signature:     s.Signature,
			Kind:          s.Kind,
			QualifiedName: s.QualifiedName,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}
