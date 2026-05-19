package mcp

import "context"

// SymbolRecord is the subset of internal/store.Symbol that the MCP layer
// needs to translate into the wire format. Field names mirror the store
// types in DESIGN.md §4.2 / phase-1-brief.md so the real store satisfies
// this interface without a translation layer.
type SymbolRecord struct {
	FilePath      string
	Name          string
	QualifiedName string
	Kind          string
	Signature     string
	StartLine     int
	EndLine       int
	Exported      bool
}

// FileRecord is the subset of internal/store.File the MCP layer surfaces
// through list_files.
type FileRecord struct {
	Path      string
	Language  string
	SizeBytes int64
}

// SymbolStore is the read-side surface the MCP handlers depend on. The
// real internal/store.Store will satisfy this interface; tests use the
// in-memory implementation in mem_store.go.
type SymbolStore interface {
	FindSymbolsByName(ctx context.Context, name string) ([]SymbolRecord, error)
	FindSymbolsByQuery(ctx context.Context, query string, limit int) ([]SymbolRecord, error)
	ListFiles(ctx context.Context, pattern, language string) ([]FileRecord, error)
}

// DecisionRecord is the subset of internal/store.Decision that the MCP
// layer surfaces through the decision tools. RecordedAt is unix seconds.
type DecisionRecord struct {
	ID         int64
	Topic      string
	Choice     string
	Reasoning  string
	RecordedAt int64
}

// DecisionStore is the read/write surface the decision tools depend on.
//
// Kept as a sibling interface to SymbolStore (rather than embedded into
// it) so the in-memory phase-1 test fixture in mem_store.go and the
// errStore in mcp_test.go don't have to grow decision methods. register()
// type-asserts the SymbolStore it receives and only wires the decision
// tools when the assertion succeeds — the real StoreAdapter satisfies
// both interfaces, so leonard-mcp always exposes the full surface.
type DecisionStore interface {
	RecordDecision(ctx context.Context, topic, choice, reasoning string) (int64, error)
	GetDecisions(ctx context.Context, topic string, since int64, limit int) ([]DecisionRecord, error)
	SupersedeDecision(ctx context.Context, decisionID int64, newChoice, newReasoning string) (int64, error)
}

// ClaimRecord is the subset of internal/store.Claim that the MCP layer
// surfaces through the claim tools. RecordedAt is unix seconds.
type ClaimRecord struct {
	ID         int64
	SessionID  string
	Claim      string
	Evidence   string
	RecordedAt int64
}

// ClaimStore is the read/write surface the claim tools depend on. Sibling
// to SymbolStore / DecisionStore — register() type-asserts the SymbolStore
// it receives and only wires the claim tools when the assertion succeeds.
// The real StoreAdapter satisfies all three so leonard-mcp exposes the
// full surface; in-memory test fixtures pick and choose.
type ClaimStore interface {
	RecordClaim(ctx context.Context, sessionID, claim, evidence string, verified bool) (int64, error)
	GetUnverifiedClaims(ctx context.Context, sessionID string) ([]ClaimRecord, error)
}
