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
