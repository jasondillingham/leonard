package mcp

import (
	"context"

	"github.com/jasondillingham/leonard/internal/store"
)

// StoreAdapter wraps *store.Store to satisfy SymbolStore. The store
// package's methods are context-free; the adapter accepts ctx for
// uniformity with the MCP-side interface and uses it only for short-circuit
// cancellation, never to abort an in-flight query.
type StoreAdapter struct{ S *store.Store }

func NewStoreAdapter(s *store.Store) *StoreAdapter { return &StoreAdapter{S: s} }

func (a *StoreAdapter) FindSymbolsByName(ctx context.Context, name string) ([]SymbolRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	syms, err := a.S.FindSymbolsByName(name)
	if err != nil {
		return nil, err
	}
	return symbolsToRecords(syms), nil
}

func (a *StoreAdapter) FindSymbolsByQuery(ctx context.Context, query string, limit int) ([]SymbolRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	syms, err := a.S.FindSymbolsByQuery(query, limit)
	if err != nil {
		return nil, err
	}
	return symbolsToRecords(syms), nil
}

func (a *StoreAdapter) ListFiles(ctx context.Context, pattern, language string) ([]FileRecord, error) {
	if err := ctx.Err(); err != nil {
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
