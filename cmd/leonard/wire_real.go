package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

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
	// Preserve a user-edited config.toml across re-init: only write the
	// defaults when the file doesn't already exist. Bughunt-2 cli F1 —
	// the prior code unconditionally overwrote, silently losing tunables.
	cfgPath := filepath.Join(dataDir, config.Filename)
	if _, statErr := os.Stat(cfgPath); errors.Is(statErr, fs.ErrNotExist) {
		return config.Save(config.Default(), cfgPath)
	} else if statErr != nil {
		return fmt.Errorf("stat %s: %w", cfgPath, statErr)
	}
	return nil
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
	// Bughunt-4 caps F3: the MCP-layer cap was the only enforcement
	// point; `leonard decisions add` used to bypass it entirely.
	// Validate here too, matching the MCP-side limits.
	if topic == "" {
		return 0, fmt.Errorf("decisions add: topic is required")
	}
	if len(topic) > store.MaxDecisionTopicBytes {
		return 0, fmt.Errorf("decisions add: topic exceeds %d bytes", store.MaxDecisionTopicBytes)
	}
	if len(choice) > store.MaxDecisionChoiceBytes {
		return 0, fmt.Errorf("decisions add: choice exceeds %d bytes", store.MaxDecisionChoiceBytes)
	}
	if len(reasoning) > store.MaxDecisionReasoningBytes {
		return 0, fmt.Errorf("decisions add: reasoning exceeds %d bytes", store.MaxDecisionReasoningBytes)
	}
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

func (realRuntime) GetTruthChanges(_ context.Context, dataDir, scope string, since int64, limit int) ([]TruthHistoryRow, error) {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	rows, err := s.GetTruthChanges(scope, since, limit)
	if err != nil {
		return nil, err
	}
	out := make([]TruthHistoryRow, 0, len(rows))
	for _, d := range rows {
		row := TruthHistoryRow{
			ID:         d.ID,
			Topic:      d.Topic,
			Choice:     d.Choice,
			Reasoning:  d.Reasoning,
			RecordedAt: d.RecordedAt,
		}
		if d.TruthChange != nil {
			row.Scope = d.TruthChange.Scope
			row.Files = d.TruthChange.Files
			row.DiffRef = d.TruthChange.DiffRef
			row.MotivatedBy = d.TruthChange.MotivatedBy
			row.Supersedes = d.TruthChange.Supersedes
			row.Trivial = d.TruthChange.Trivial
			row.TrivialReason = d.TruthChange.TrivialReason
		}
		out = append(out, row)
	}
	return out, nil
}

func (realRuntime) GetTruthHistory(_ context.Context, dataDir, filePath string, limit int) ([]TruthHistoryRow, error) {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	rows, err := s.GetTruthHistory(filePath, limit)
	if err != nil {
		return nil, err
	}
	out := make([]TruthHistoryRow, 0, len(rows))
	for _, d := range rows {
		row := TruthHistoryRow{
			ID:         d.ID,
			Topic:      d.Topic,
			Choice:     d.Choice,
			Reasoning:  d.Reasoning,
			RecordedAt: d.RecordedAt,
		}
		if d.TruthChange != nil {
			row.Scope = d.TruthChange.Scope
			row.Files = d.TruthChange.Files
			row.DiffRef = d.TruthChange.DiffRef
			row.MotivatedBy = d.TruthChange.MotivatedBy
			row.Supersedes = d.TruthChange.Supersedes
			row.Trivial = d.TruthChange.Trivial
			row.TrivialReason = d.TruthChange.TrivialReason
		}
		out = append(out, row)
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

func (realRuntime) Doctor(_ context.Context, projectRoot, dataDir string) (DoctorReport, error) {
	dbPath := filepath.Join(dataDir, "leonard.db")
	s, err := store.Open(dbPath)
	if err != nil {
		return DoctorReport{}, err
	}
	defer s.Close()

	files, err := s.ListFiles("", "")
	if err != nil {
		return DoctorReport{}, err
	}

	rep := DoctorReport{StorePath: dbPath, TotalFiles: len(files)}

	// Per-language file counts, max indexed_at, and stale detection
	// (file row in store, missing on disk).
	filesByLang := map[string]int{}
	pathsByLang := map[string]map[string]bool{}
	for _, f := range files {
		filesByLang[f.Language]++
		if pathsByLang[f.Language] == nil {
			pathsByLang[f.Language] = map[string]bool{}
		}
		pathsByLang[f.Language][f.Path] = true
		if f.IndexedAt > rep.LastIndexedAt {
			rep.LastIndexedAt = f.IndexedAt
		}
		// Stale check: file path is relative to projectRoot.
		// Bughunt-4 path-trust F4: filter the row through ResolveSafe
		// so a pre-v0.8 store row whose path escapes the project
		// root doesn't get stat'd outside the tree. Such rows are
		// stale by definition (the v0.14 indexer rejects them at
		// IndexFile time).
		abs, ok := index.ResolveSafe(projectRoot, filepath.FromSlash(f.Path))
		if !ok {
			rep.StaleFiles = append(rep.StaleFiles, f.Path)
			continue
		}
		if _, statErr := os.Stat(abs); errors.Is(statErr, fs.ErrNotExist) {
			rep.StaleFiles = append(rep.StaleFiles, f.Path)
		}
	}
	rep.FilesByLanguage = sortedLangCounts(filesByLang)

	// Per-language symbol counts, plus zero-symbol files. One aggregate
	// query gets us file_path → count for files that have any symbols;
	// files in the files table without an entry here are empty (and for
	// supported languages, almost certainly parse failures).
	counts, err := s.SymbolCountsByFile()
	if err != nil {
		return DoctorReport{}, err
	}
	// docFileSizeCeiling filters out package-comment-only files (e.g.
	// internal/<pkg>/doc.go) from the parse-failure suspect list — they
	// legitimately have zero symbols. 512 bytes is comfortably above the
	// largest doc.go in this repo (~290 bytes) without false-negativing
	// any real source file. A genuine parse failure on a meaningful file
	// is almost always thousands of bytes.
	const docFileSizeCeiling int64 = 512
	symsByLang := map[string]int{}
	for _, f := range files {
		n := counts[f.Path]
		symsByLang[f.Language] += n
		rep.TotalSymbols += n
		if n == 0 && f.SizeBytes > docFileSizeCeiling {
			rep.EmptyFiles = append(rep.EmptyFiles, f.Path)
		}
	}
	rep.SymbolsByLanguage = sortedLangCounts(symsByLang)

	// Decision + claim health.
	decisions, err := s.GetDecisions("", 0, 0)
	if err != nil {
		return DoctorReport{}, err
	}
	rep.DecisionCount = len(decisions)

	stale, err := s.GetStaleDecisions(0)
	if err != nil {
		return DoctorReport{}, err
	}
	rep.StaleDecisionCount = len(stale)

	unverified, err := s.GetUnverifiedClaims("")
	if err != nil {
		return DoctorReport{}, err
	}
	rep.UnverifiedClaims = len(unverified)

	sort.Strings(rep.EmptyFiles)
	sort.Strings(rep.StaleFiles)
	return rep, nil
}

func sortedLangCounts(m map[string]int) []LanguageCount {
	out := make([]LanguageCount, 0, len(m))
	for lang, n := range m {
		out = append(out, LanguageCount{Language: lang, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Language < out[j].Language })
	return out
}

func (realRuntime) ResolveClaim(_ context.Context, dataDir string, claimID int64, note string) error {
	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	return s.ResolveClaim(claimID, note)
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
