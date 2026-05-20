package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
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
