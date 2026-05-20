package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	return Implementation{Name: "leonard-mcp", Version: "0.39.0"}
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
}

// verifySymbol is the thin shim: name lookup + optional kind/language
// filter + record-to-wire translation.
func verifySymbol(ctx context.Context, store SymbolStore, in VerifySymbolInput) (VerifySymbolOutput, error) {
	syms, err := store.FindSymbolsByName(ctx, in.Name)
	if err != nil {
		return VerifySymbolOutput{}, err
	}
	matches := filterAndConvert(syms, in.Kind, in.Language, 0)
	return VerifySymbolOutput{Exists: len(matches) > 0, Matches: matches}, nil
}

func findSymbol(ctx context.Context, store SymbolStore, in FindSymbolInput) (FindSymbolOutput, error) {
	syms, err := store.FindSymbolsByQuery(ctx, in.Query, in.Limit)
	if err != nil {
		return FindSymbolOutput{}, err
	}
	return FindSymbolOutput{Matches: filterAndConvert(syms, in.Kind, in.Language, in.Limit)}, nil
}

// listFilesDefaultLimit + listFilesMaxLimit cap the response size.
// Bughunt-4 caps F7: ListFilesInput previously had no limit at all,
// so on a 10k-file project list_files returned every row.
const (
	listFilesDefaultLimit = 200
	listFilesMaxLimit     = 1000
)

func listFiles(ctx context.Context, store SymbolStore, in ListFilesInput) (ListFilesOutput, error) {
	limit := in.Limit
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

// filterAndConvert applies kind/language filters to a slice of store
// symbol records and converts them to the wire format. The MCP layer
// (not the store) owns language filtering because store.Symbol doesn't
// carry language directly — we derive it from the file extension here.
func filterAndConvert(syms []SymbolRecord, kind, language string, limit int) []SymbolMatch {
	out := make([]SymbolMatch, 0, len(syms))
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
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
