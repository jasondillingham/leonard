package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/jasondillingham/leonard/internal/store"
)

// ErrDatabaseReplaced is returned by adapter methods when WatchDatabase
// has been wired and the on-disk database file is gone or its inode no
// longer matches the open file handle. The error string is a JSON object
// so clients can parse the code+message even after the MCP layer wraps
// the error with a tool-name prefix.
var ErrDatabaseReplaced = errors.New(`{"code":"database-replaced","message":"the project store has been replaced; restart leonard-mcp"}`)

// StoreAdapter wraps *store.Store to satisfy SymbolStore. The store
// package's methods are context-free; the adapter accepts ctx for
// uniformity with the MCP-side interface and uses it only for short-circuit
// cancellation, never to abort an in-flight query.
//
// When WatchDatabase has been called, every adapter method stats the
// configured DB path first and returns ErrDatabaseReplaced on missing or
// swapped (different inode) files — without touching the now-ghost store.
type StoreAdapter struct {
	S       *store.Store
	dbPath  string
	dbInode uint64
}

func NewStoreAdapter(s *store.Store) *StoreAdapter { return &StoreAdapter{S: s} }

// WatchDatabase enables swap detection against path. The adapter records
// the file's current inode; subsequent method calls os.Stat the path and
// compare. Returns an error only if the initial stat fails — callers may
// proceed without watching by skipping this call.
func (a *StoreAdapter) WatchDatabase(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("watch database: %w", err)
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("watch database: unsupported FileInfo.Sys() type %T", fi.Sys())
	}
	a.dbPath = path
	a.dbInode = stat.Ino
	return nil
}

// CheckDatabase returns ErrDatabaseReplaced when the watched DB file has
// been removed or replaced under the running server. Returns nil when no
// path is configured (back-compat for tests that bypass WatchDatabase).
func (a *StoreAdapter) CheckDatabase() error {
	if a.dbPath == "" {
		return nil
	}
	fi, err := os.Stat(a.dbPath)
	if err != nil {
		return ErrDatabaseReplaced
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if stat.Ino != a.dbInode {
		return ErrDatabaseReplaced
	}
	return nil
}

// preflight checks ctx cancellation and DB swap-detection. Every adapter
// method runs this first so a ghost-inode store can't accept reads or
// writes after the on-disk DB has been removed or replaced.
func (a *StoreAdapter) preflight(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.CheckDatabase()
}

func (a *StoreAdapter) FindSymbolsByName(ctx context.Context, name string) ([]SymbolRecord, error) {
	if err := a.preflight(ctx); err != nil {
		return nil, err
	}
	syms, err := a.S.FindSymbolsByName(name)
	if err != nil {
		return nil, err
	}
	return symbolsToRecords(syms), nil
}

func (a *StoreAdapter) FindSymbolsByQuery(ctx context.Context, query string, limit int) ([]SymbolRecord, error) {
	if err := a.preflight(ctx); err != nil {
		return nil, err
	}
	syms, err := a.S.FindSymbolsByQuery(query, limit)
	if err != nil {
		return nil, err
	}
	return symbolsToRecords(syms), nil
}

func (a *StoreAdapter) ListFiles(ctx context.Context, pattern, language string) ([]FileRecord, error) {
	if err := a.preflight(ctx); err != nil {
		return nil, err
	}
	files, err := a.S.ListFiles(pattern, language)
	if err != nil {
		return nil, err
	}
	out := make([]FileRecord, len(files))
	for i, f := range files {
		out[i] = FileRecord{Path: f.Path, Language: f.Language, SizeBytes: f.SizeBytes}
	}
	return out, nil
}

func (a *StoreAdapter) RecordDecision(ctx context.Context, topic, choice, reasoning string, relatedFiles, relatedSymbols []string) (int64, error) {
	if err := a.preflight(ctx); err != nil {
		return 0, err
	}
	return a.S.RecordDecision(store.Decision{
		Topic:          topic,
		Choice:         choice,
		Reasoning:      reasoning,
		RelatedFiles:   relatedFiles,
		RelatedSymbols: relatedSymbols,
	})
}

func (a *StoreAdapter) GetDecisions(ctx context.Context, topic string, since int64, limit int) ([]DecisionRecord, error) {
	if err := a.preflight(ctx); err != nil {
		return nil, err
	}
	ds, err := a.S.GetDecisions(topic, since, limit)
	if err != nil {
		return nil, err
	}
	out := make([]DecisionRecord, len(ds))
	for i, d := range ds {
		out[i] = decisionToRecord(d)
	}
	return out, nil
}

func (a *StoreAdapter) GetStaleDecisions(ctx context.Context, limit int) ([]StaleDecisionRecord, error) {
	if err := a.preflight(ctx); err != nil {
		return nil, err
	}
	ss, err := a.S.GetStaleDecisions(limit)
	if err != nil {
		return nil, err
	}
	out := make([]StaleDecisionRecord, len(ss))
	for i, s := range ss {
		out[i] = StaleDecisionRecord{
			Decision:       decisionToRecord(s.Decision),
			MissingFiles:   s.MissingFiles,
			MissingSymbols: s.MissingSymbols,
		}
	}
	return out, nil
}

func decisionToRecord(d store.Decision) DecisionRecord {
	return DecisionRecord{
		ID:             d.ID,
		Topic:          d.Topic,
		Choice:         d.Choice,
		Reasoning:      d.Reasoning,
		RecordedAt:     d.RecordedAt,
		RelatedFiles:   d.RelatedFiles,
		RelatedSymbols: d.RelatedSymbols,
	}
}

func (a *StoreAdapter) SupersedeDecision(ctx context.Context, decisionID int64, newChoice, newReasoning string) (int64, error) {
	if err := a.preflight(ctx); err != nil {
		return 0, err
	}
	return a.S.SupersedeDecision(decisionID, newChoice, newReasoning)
}

func (a *StoreAdapter) RecordClaim(ctx context.Context, sessionID, claim, evidence, filePath string, verified bool) (int64, error) {
	if err := a.preflight(ctx); err != nil {
		return 0, err
	}
	return a.S.RecordClaim(store.Claim{
		SessionID: sessionID,
		Claim:     claim,
		Evidence:  evidence,
		FilePath:  filePath,
		Verified:  verified,
	})
}

func (a *StoreAdapter) GetUnverifiedClaims(ctx context.Context, sessionID string, includeSuperseded bool) ([]ClaimRecord, error) {
	if err := a.preflight(ctx); err != nil {
		return nil, err
	}
	var (
		cs  []store.Claim
		err error
	)
	if includeSuperseded {
		cs, err = a.S.GetUnverifiedClaimsAll(sessionID)
	} else {
		cs, err = a.S.GetUnverifiedClaims(sessionID)
	}
	if err != nil {
		return nil, err
	}
	out := make([]ClaimRecord, len(cs))
	for i, c := range cs {
		out[i] = ClaimRecord{
			ID:              c.ID,
			SessionID:       c.SessionID,
			Claim:           c.Claim,
			Evidence:        c.Evidence,
			RecordedAt:      c.RecordedAt,
			FilePath:        c.FilePath,
			Tool:            c.Tool,
			IndexOK:         c.IndexOK,
			VetOK:           c.VetOK,
			VetErrorSummary: c.VetErrorSummary,
		}
	}
	return out, nil
}

func (a *StoreAdapter) ListFilesIndexedSince(ctx context.Context, since int64, limit int) ([]FileRecord, error) {
	if err := a.preflight(ctx); err != nil {
		return nil, err
	}
	files, err := a.S.ListFilesIndexedSince(since, limit)
	if err != nil {
		return nil, err
	}
	out := make([]FileRecord, len(files))
	for i, f := range files {
		out[i] = FileRecord{
			Path:      f.Path,
			Language:  f.Language,
			SizeBytes: f.SizeBytes,
			IndexedAt: f.IndexedAt,
		}
	}
	return out, nil
}

func symbolsToRecords(syms []store.Symbol) []SymbolRecord {
	out := make([]SymbolRecord, len(syms))
	for i, s := range syms {
		out[i] = SymbolRecord{
			FilePath:      s.FilePath,
			Name:          s.Name,
			QualifiedName: s.QualifiedName,
			Kind:          s.Kind,
			Signature:     s.Signature,
			StartLine:     s.StartLine,
			EndLine:       s.EndLine,
			Exported:      s.Exported,
		}
	}
	return out
}
