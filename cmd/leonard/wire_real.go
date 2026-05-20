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

func (realRuntime) IndexAll(_ context.Context, projectRoot, dataDir string) (IndexResult, error) {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return IndexResult{}, err
	}
	defer s.Close()
	idx := index.New(s, projectRoot)
	if err := idx.IndexAll(); err != nil {
		return IndexResult{}, err
	}
	files, err := s.ListFiles("", "")
	if err != nil {
		return IndexResult{}, err
	}
	rawFailures := idx.ParseFailures()
	failures := make([]ParseFailure, len(rawFailures))
	for i, f := range rawFailures {
		failures[i] = ParseFailure{Path: f.Path, Message: f.Message}
	}
	return IndexResult{FilesIndexed: len(files), ParseFailures: failures}, nil
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

func (realRuntime) RecordDecision(_ context.Context, dataDir, topic, choice, reasoning string) (int64, error) {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return 0, err
	}
	defer s.Close()
	return s.RecordDecision(store.Decision{Topic: topic, Choice: choice, Reasoning: reasoning})
}

func (realRuntime) GetDecisions(_ context.Context, dataDir, topic string, since int64, limit int) ([]DecisionRow, error) {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	rows, err := s.GetDecisions(topic, since, limit)
	if err != nil {
		return nil, err
	}
	out := make([]DecisionRow, len(rows))
	for i, d := range rows {
		out[i] = DecisionRow{ID: d.ID, Topic: d.Topic, Choice: d.Choice, Reasoning: d.Reasoning, RecordedAt: d.RecordedAt}
	}
	return out, nil
}

func (realRuntime) GetStaleDecisions(_ context.Context, dataDir string, limit int) ([]StaleDecisionRow, error) {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	rows, err := s.GetStaleDecisions(limit)
	if err != nil {
		return nil, err
	}
	out := make([]StaleDecisionRow, len(rows))
	for i, r := range rows {
		out[i] = StaleDecisionRow{
			Decision: DecisionRow{
				ID: r.Decision.ID, Topic: r.Decision.Topic, Choice: r.Decision.Choice,
				Reasoning: r.Decision.Reasoning, RecordedAt: r.Decision.RecordedAt,
			},
			MissingFiles:   r.MissingFiles,
			MissingSymbols: r.MissingSymbols,
		}
	}
	return out, nil
}

func (realRuntime) GetUnverifiedClaims(_ context.Context, dataDir, sessionID string) ([]ClaimRow, error) {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	rows, err := s.GetUnverifiedClaims(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]ClaimRow, len(rows))
	for i, c := range rows {
		out[i] = ClaimRow{
			ID: c.ID, SessionID: c.SessionID, Claim: c.Claim,
			Evidence: c.Evidence, FilePath: c.FilePath, RecordedAt: c.RecordedAt,
		}
	}
	return out, nil
}
