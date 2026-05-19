package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jasondillingham/leonard/internal/config"
	"github.com/jasondillingham/leonard/internal/index"
	"github.com/jasondillingham/leonard/internal/store"
)

// realRuntime wires the cobra subcommands to the production store + indexer.
type realRuntime struct{}

func newDefaultRuntime() Runtime { return realRuntime{} }

func (realRuntime) Init(_ context.Context, projectRoot, dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dataDir, err)
	}
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return err
	}
	if cerr := s.Close(); cerr != nil {
		return cerr
	}
	cfgPath := filepath.Join(dataDir, config.Filename)
	return config.Save(config.Default(), cfgPath)
}

func (realRuntime) IndexAll(_ context.Context, projectRoot, dataDir string) (int, error) {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return 0, err
	}
	defer s.Close()
	idx := index.New(s, projectRoot)
	if err := idx.IndexAll(); err != nil {
		return 0, err
	}
	files, err := s.ListFiles("", "")
	if err != nil {
		return 0, err
	}
	return len(files), nil
}

func (realRuntime) VerifySymbol(_ context.Context, dataDir, name, kind string) ([]SymbolMatch, error) {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	syms, err := s.FindSymbolsByName(name)
	if err != nil {
		return nil, err
	}
	out := make([]SymbolMatch, 0, len(syms))
	for _, sym := range syms {
		if kind != "" && sym.Kind != kind {
			continue
		}
		out = append(out, SymbolMatch{
			File:          sym.FilePath,
			Line:          sym.StartLine,
			Signature:     sym.Signature,
			Kind:          sym.Kind,
			QualifiedName: sym.QualifiedName,
		})
	}
	return out, nil
}
