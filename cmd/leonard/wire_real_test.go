package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
	"github.com/jasondillingham/leonard/internal/store"
)

// TestRealRuntime_InitPreservesCustomConfig covers bughunt-2 cli F1.
// Re-running `leonard init` over an existing project used to overwrite
// whatever the user had edited in config.toml. The Init contract now
// only writes defaults when no file exists; on re-init the user's
// custom values must survive verbatim.
func TestRealRuntime_InitPreservesCustomConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	rt := realRuntime{}

	// First init: defaults get written.
	if err := rt.Init(context.Background(), root, dataDir); err != nil {
		t.Fatalf("first init: %v", err)
	}
	cfgPath := filepath.Join(dataDir, config.Filename)

	// User edits the file. A non-default sentinel value is the easiest
	// way to detect overwrites — pick a field that's actually consumed
	// by the runtime so the test stays useful if the sentinel choice
	// later turns into a knob worth honoring elsewhere.
	custom := "inject_decisions_at_session_start = 99\n"
	if err := os.WriteFile(cfgPath, []byte(custom), 0o644); err != nil {
		t.Fatalf("write custom config: %v", err)
	}

	// Second init must NOT overwrite.
	if err := rt.Init(context.Background(), root, dataDir); err != nil {
		t.Fatalf("second init: %v", err)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config after re-init: %v", err)
	}
	if !strings.Contains(string(after), "inject_decisions_at_session_start = 99") {
		t.Errorf("custom value was overwritten by re-init.\nafter=%s", after)
	}
}

// TestRealRuntime_VerifySymbol_QualifiedName covers issue #5: the pre-edit deny
// message cites qualified names (e.g. "session.NewID") but FindSymbolsByName
// queries the bare-name column, so `leonard verify session.NewID` previously
// either found nothing or returned false positives from identically-named symbols
// in other packages. VerifySymbol now splits on the last dot and filters.
func TestRealRuntime_VerifySymbol_QualifiedName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	rt := realRuntime{}
	if err := rt.Init(context.Background(), root, dataDir); err != nil {
		t.Fatalf("init: %v", err)
	}

	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// Two files each exporting a symbol named "NewID" but in different packages.
	for _, f := range []store.File{
		{Path: "internal/session/session.go", Hash: "a", Language: "go", SizeBytes: 100},
		{Path: "internal/other/other.go", Hash: "b", Language: "go", SizeBytes: 100},
	} {
		if err := s.UpsertFile(f); err != nil {
			t.Fatalf("upsert %s: %v", f.Path, err)
		}
	}
	if err := s.ReplaceSymbols("internal/session/session.go", []store.Symbol{
		{Name: "NewID", QualifiedName: "session.NewID", Kind: "function", Exported: true},
	}); err != nil {
		t.Fatalf("replace symbols session: %v", err)
	}
	if err := s.ReplaceSymbols("internal/other/other.go", []store.Symbol{
		{Name: "NewID", QualifiedName: "other.NewID", Kind: "function", Exported: true},
	}); err != nil {
		t.Fatalf("replace symbols other: %v", err)
	}
	s.Close()

	ctx := context.Background()

	// Qualified lookup: only the matching package is returned.
	got, err := rt.VerifySymbol(ctx, dataDir, "session.NewID", "")
	if err != nil {
		t.Fatalf("verify session.NewID: %v", err)
	}
	if len(got) != 1 || got[0].QualifiedName != "session.NewID" {
		t.Errorf("verify session.NewID: want 1 match with QualifiedName=session.NewID, got %v", got)
	}

	// Qualified lookup for the other package does not bleed through.
	got, err = rt.VerifySymbol(ctx, dataDir, "other.NewID", "")
	if err != nil {
		t.Fatalf("verify other.NewID: %v", err)
	}
	if len(got) != 1 || got[0].QualifiedName != "other.NewID" {
		t.Errorf("verify other.NewID: want 1 match with QualifiedName=other.NewID, got %v", got)
	}

	// A qualified name that matches no package returns nothing.
	got, err = rt.VerifySymbol(ctx, dataDir, "missing.NewID", "")
	if err != nil {
		t.Fatalf("verify missing.NewID: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("verify missing.NewID: want 0 matches, got %v", got)
	}

	// Bare-name lookup still returns both symbols.
	got, err = rt.VerifySymbol(ctx, dataDir, "NewID", "")
	if err != nil {
		t.Fatalf("verify NewID: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("verify NewID: want 2 matches, got %d: %v", len(got), got)
	}
}

// TestRealRuntime_Doctor_BashExcludedFromParseSuspects covers issue #4: bash
// scripts that contain no function definitions legitimately have zero extracted
// symbols. Doctor was falsely flagging them as parse-failure suspects because the
// size-based heuristic did not account for narrow-capture languages.
func TestRealRuntime_Doctor_BashExcludedFromParseSuspects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	rt := realRuntime{}
	if err := rt.Init(context.Background(), root, dataDir); err != nil {
		t.Fatalf("init: %v", err)
	}

	s, err := store.Open(filepath.Join(dataDir, "leonard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// A bash script larger than docFileSizeCeiling (512 bytes) with no function
	// definitions → zero symbols, should NOT appear in EmptyFiles.
	// A Go file of the same size with no symbols → should appear in EmptyFiles.
	bigSize := int64(1024)
	for _, f := range []store.File{
		{Path: "scripts/deploy.sh", Hash: "s", Language: "bash", SizeBytes: bigSize},
		{Path: "internal/broken.go", Hash: "g", Language: "go", SizeBytes: bigSize},
	} {
		if err := s.UpsertFile(f); err != nil {
			t.Fatalf("upsert %s: %v", f.Path, err)
		}
		// Create the actual file on disk so Doctor doesn't mark it stale.
		abs := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, make([]byte, bigSize), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
	s.Close()

	rep, err := rt.Doctor(context.Background(), root, dataDir)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}

	for _, p := range rep.EmptyFiles {
		if p == "scripts/deploy.sh" {
			t.Errorf("bash script incorrectly flagged as parse-failure suspect: %s", p)
		}
	}
	found := false
	for _, p := range rep.EmptyFiles {
		if p == "internal/broken.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("zero-symbol Go file should appear in EmptyFiles, got %v", rep.EmptyFiles)
	}
}

// TestRealRuntime_InitWritesDefaultsWhenAbsent locks in the
// previously-correct behavior: on a fresh dir, init writes the
// default config. Bug-fix-2 changed Init to skip the write when a
// file exists, so the absent-file path needs an explicit test to
// prevent a future "skip always" regression.
func TestRealRuntime_InitWritesDefaultsWhenAbsent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, dataDirName)
	rt := realRuntime{}

	if err := rt.Init(context.Background(), root, dataDir); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(dataDir, config.Filename)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	// Default writes the InjectDecisionsAtSessionStart = 10 field. Any
	// recognisable default-shape substring is enough to confirm the
	// file got written rather than left empty.
	if !strings.Contains(string(data), "inject_decisions_at_session_start") {
		t.Errorf("default config not written; got %s", data)
	}
}
