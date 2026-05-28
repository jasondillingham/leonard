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
// through list_files and recent_changes. IndexedAt is unix seconds; it is
// populated by the recent_changes path and left zero by list_files (which
// has no need for it). Adding the field is a back-compat additive change.
type FileRecord struct {
	Path      string
	Language  string
	SizeBytes int64
	IndexedAt int64
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
// RelatedFiles / RelatedSymbols are decoded from the JSON columns added in
// schema v4; nil for decisions recorded before that migration.
// TruthChange is decoded from the JSON column added in schema v8 (#21);
// nil for decisions that aren't truth-tracking entries.
type DecisionRecord struct {
	ID             int64
	Topic          string
	Choice         string
	Reasoning      string
	RecordedAt     int64
	SupersededBy   *int64 // non-nil when this decision has been replaced by another
	RelatedFiles   []string
	RelatedSymbols []string
	TruthChange    *TruthChangeRecord
}

// TruthChangeRecord mirrors internal/store.TruthChange on the MCP
// wire. Fields use snake_case JSON tags so the MCP client sees the
// same shape that lands in audit/audit-log.md.
type TruthChangeRecord struct {
	Scope         string   `json:"scope,omitempty"`
	Files         []string `json:"files,omitempty"`
	DiffRef       string   `json:"diff_ref,omitempty"`
	MotivatedBy   string   `json:"motivated_by,omitempty"`
	Supersedes    *int64   `json:"supersedes,omitempty"`
	Trivial       bool     `json:"trivial,omitempty"`
	TrivialReason string   `json:"trivial_reason,omitempty"`
}

// StaleDecisionRecord wraps DecisionRecord with the specific refs that no
// longer resolve against the live index, as returned by get_stale_decisions.
type StaleDecisionRecord struct {
	Decision       DecisionRecord
	MissingFiles   []string
	MissingSymbols []string
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
	RecordDecision(ctx context.Context, topic, choice, reasoning string, relatedFiles, relatedSymbols []string) (int64, error)
	GetDecisions(ctx context.Context, topic string, since int64, limit int) ([]DecisionRecord, error)
	SupersedeDecision(ctx context.Context, decisionID int64, newChoice, newReasoning string) (int64, error)
	GetStaleDecisions(ctx context.Context, limit int) ([]StaleDecisionRecord, error)
}

// TruthHistoryStore is the read surface the get_truth_history tool
// (#28) depends on. Sibling to DecisionStore so test fixtures that
// don't track truth changes don't have to grow this method. The
// real StoreAdapter satisfies both; register() gates the tool on
// this interface assertion.
type TruthHistoryStore interface {
	GetTruthHistory(ctx context.Context, filePath string, limit int) ([]DecisionRecord, error)
}

// ClaimRecord is the subset of internal/store.Claim that the MCP layer
// surfaces through the claim tools. RecordedAt is unix seconds. FilePath
// is empty for claims recorded before migration v2 or by record_claim
// callers that didn't supply one. Tool / IndexOK / VetOK / VetErrorSummary
// are populated by the post-edit hook (v3+); record_claim from a model
// leaves them empty. IndexOK and VetOK are *bool because v3 distinguishes
// "didn't run" (nil) from "ran and failed" (pointer to false).
type ClaimRecord struct {
	ID              int64
	SessionID       string
	Claim           string
	Evidence        string
	RecordedAt      int64
	FilePath        string
	Tool            string
	IndexOK         *bool
	VetOK           *bool
	VetErrorSummary string
}

// ClaimStore is the read/write surface the claim tools depend on. Sibling
// to SymbolStore / DecisionStore — register() type-asserts the SymbolStore
// it receives and only wires the claim tools when the assertion succeeds.
// The real StoreAdapter satisfies all three so leonard-mcp exposes the
// full surface; in-memory test fixtures pick and choose.
type ClaimStore interface {
	RecordClaim(ctx context.Context, sessionID, claim, evidence, filePath string, verified bool) (int64, error)
	// GetUnverifiedClaims returns claims still flagged as unverified. When
	// includeSuperseded is false (the default for the get_unverified_claims
	// tool), rows resolved by a later vet=ok run on the same file are
	// hidden so stop-time output focuses on outstanding failures.
	GetUnverifiedClaims(ctx context.Context, sessionID string, includeSuperseded bool) ([]ClaimRecord, error)
}

// ChangesStore is the read surface the recent_changes tool depends on.
// Sibling to SymbolStore/DecisionStore so test fixtures that don't care
// about the changes tool aren't forced to implement it — register() type
// asserts and only wires the tool when the assertion succeeds.
type ChangesStore interface {
	ListFilesIndexedSince(ctx context.Context, since int64, limit int) ([]FileRecord, error)
}
