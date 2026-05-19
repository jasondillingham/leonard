package mcp

import (
	"context"
	"path"
	"strings"
	"sync"
)

// MemStore is an in-memory SymbolStore. It is the default store used by
// the leonard-mcp binary until internal/store lands, and the harness for
// the package's integration tests.
type MemStore struct {
	mu      sync.RWMutex
	files   []FileRecord
	symbols []SymbolRecord
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{}
}

// AddFile inserts or updates a file record by Path.
func (m *MemStore) AddFile(f FileRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.files {
		if existing.Path == f.Path {
			m.files[i] = f
			return
		}
	}
	m.files = append(m.files, f)
}

// AddSymbol appends a symbol record. Tests typically load a fixture once
// so we don't bother deduping here.
func (m *MemStore) AddSymbol(s SymbolRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.symbols = append(m.symbols, s)
}

func (m *MemStore) FindSymbolsByName(ctx context.Context, name string) ([]SymbolRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []SymbolRecord
	for _, s := range m.symbols {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *MemStore) FindSymbolsByQuery(ctx context.Context, query string, limit int) ([]SymbolRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	q := strings.ToLower(query)
	var out []SymbolRecord
	for _, s := range m.symbols {
		if q == "" || strings.Contains(strings.ToLower(s.Name), q) || strings.Contains(strings.ToLower(s.QualifiedName), q) {
			out = append(out, s)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemStore) ListFiles(ctx context.Context, pattern, language string) ([]FileRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []FileRecord
	for _, f := range m.files {
		if language != "" && f.Language != language {
			continue
		}
		if pattern != "" {
			ok, err := path.Match(pattern, f.Path)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		out = append(out, f)
	}
	return out, nil
}
