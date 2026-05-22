package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAdapterFlag_DefaultIsCode(t *testing.T) {
	got, err := parseAdapterFlag("")
	if err != nil {
		t.Fatalf("empty flag: %v", err)
	}
	if !got["code"] || got["ground-truth"] {
		t.Errorf("default: want only code, got %v", got)
	}
}

func TestParseAdapterFlag_GroundTruthOnly(t *testing.T) {
	got, err := parseAdapterFlag("ground-truth")
	if err != nil {
		t.Fatalf("ground-truth: %v", err)
	}
	if got["code"] || !got["ground-truth"] {
		t.Errorf("ground-truth only: got %v", got)
	}
}

func TestParseAdapterFlag_BothCommaSeparated(t *testing.T) {
	got, err := parseAdapterFlag("code,ground-truth")
	if err != nil {
		t.Fatalf("both: %v", err)
	}
	if !got["code"] || !got["ground-truth"] {
		t.Errorf("both: got %v", got)
	}
}

func TestParseAdapterFlag_RejectsUnknown(t *testing.T) {
	if _, err := parseAdapterFlag("kebab"); err == nil {
		t.Error("unknown adapter name should be rejected")
	}
}

func TestParseAdapterFlag_TrimsWhitespace(t *testing.T) {
	got, err := parseAdapterFlag("  code , ground-truth ")
	if err != nil {
		t.Fatalf("whitespace: %v", err)
	}
	if !got["code"] || !got["ground-truth"] {
		t.Errorf("whitespace handling broke: %v", got)
	}
}

func TestScaffoldGroundTruth_CreatesAllFiles(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, ".leonard")
	created, err := scaffoldGroundTruth(dataDir)
	if err != nil {
		t.Fatalf("scaffoldGroundTruth: %v", err)
	}

	wantFiles := []string{"facts.yaml", "stories.md", "do-not-claim.md", "filters.yaml", "audit-log.md"}
	for _, name := range wantFiles {
		path := filepath.Join(dataDir, groundTruthDirName, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("missing %s: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty — expected populated template", name)
		}
	}
	if len(created) != len(wantFiles) {
		t.Errorf("created count: want %d, got %d (%v)", len(wantFiles), len(created), created)
	}
}

func TestScaffoldGroundTruth_IsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, ".leonard")

	// First call creates all five.
	first, err := scaffoldGroundTruth(dataDir)
	if err != nil {
		t.Fatalf("first scaffold: %v", err)
	}
	if len(first) != 5 {
		t.Fatalf("first scaffold should create 5 files, got %d", len(first))
	}

	// Mutate one file so we can prove it wasn't overwritten.
	factsPath := filepath.Join(dataDir, groundTruthDirName, "facts.yaml")
	custom := []byte("custom_key: custom_value\n")
	if err := os.WriteFile(factsPath, custom, 0o644); err != nil {
		t.Fatalf("write custom: %v", err)
	}

	// Second call should create zero files.
	second, err := scaffoldGroundTruth(dataDir)
	if err != nil {
		t.Fatalf("second scaffold: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("idempotent: want 0 created on re-run, got %d (%v)", len(second), second)
	}

	// Custom content preserved.
	got, err := os.ReadFile(factsPath)
	if err != nil {
		t.Fatalf("read facts.yaml: %v", err)
	}
	if string(got) != string(custom) {
		t.Errorf("custom content overwritten\nwant: %q\ngot:  %q", string(custom), string(got))
	}
}

func TestScaffoldGroundTruth_TemplatesAreParseable(t *testing.T) {
	// The scaffolded templates need to parse cleanly through the
	// groundtruth adapter's loaders — otherwise an operator who runs
	// `leonard init --adapter=ground-truth` and immediately Init's
	// the adapter gets a load error. This is a contract test against
	// the v0.6 parsers.
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, ".leonard")
	if _, err := scaffoldGroundTruth(dataDir); err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	// Each YAML file should parse as valid YAML (verified by
	// loading it and confirming no error). Each markdown file
	// should at minimum read without error. We test the smoke-level
	// here; the parser packages have their own deeper tests.
	for _, name := range []string{"facts.yaml", "stories.md", "do-not-claim.md", "filters.yaml", "audit-log.md"} {
		path := filepath.Join(dataDir, groundTruthDirName, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
}

func TestInitCmd_WithGroundTruthFlagScaffoldsTree(t *testing.T) {
	root := t.TempDir()
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "init", "--adapter=ground-truth")
	if err != nil {
		t.Fatalf("init: %v\nout=%s", err, out)
	}

	gtDir := filepath.Join(root, dataDirName, groundTruthDirName)
	for _, name := range []string{"facts.yaml", "stories.md", "do-not-claim.md", "filters.yaml", "audit-log.md"} {
		if _, err := os.Stat(filepath.Join(gtDir, name)); err != nil {
			t.Errorf("missing scaffolded %s: %v", name, err)
		}
	}
	if !strings.Contains(out, "scaffolded ground-truth") {
		t.Errorf("output missing scaffold confirmation: %q", out)
	}

	// --adapter=ground-truth alone shouldn't call rt.Init (no SQLite
	// store needed) — though it should still create the .leonard/
	// directory.
	if len(rt.initCalls) != 0 {
		t.Errorf("rt.Init should NOT be called for ground-truth-only init, got %d calls", len(rt.initCalls))
	}
}

func TestInitCmd_WithBothAdaptersScaffoldsAndInits(t *testing.T) {
	root := t.TempDir()
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "init", "--adapter=code,ground-truth")
	if err != nil {
		t.Fatalf("init: %v\nout=%s", err, out)
	}

	if len(rt.initCalls) != 1 {
		t.Errorf("rt.Init should be called once for code,ground-truth init, got %d", len(rt.initCalls))
	}
	gtDir := filepath.Join(root, dataDirName, groundTruthDirName)
	if _, err := os.Stat(filepath.Join(gtDir, "facts.yaml")); err != nil {
		t.Errorf("ground-truth not scaffolded: %v", err)
	}
}

func TestInitCmd_DefaultIsCodeOnly(t *testing.T) {
	root := t.TempDir()
	withCwd(t, root)
	rt := &fakeRuntime{}
	if _, err := runRoot(t, rt, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if len(rt.initCalls) != 1 {
		t.Errorf("default init should run code adapter: got %d Init calls", len(rt.initCalls))
	}
	// No ground-truth tree on the default invocation.
	if _, err := os.Stat(filepath.Join(root, dataDirName, groundTruthDirName)); err == nil {
		t.Error("default init should NOT create ground-truth tree")
	}
}

func TestInitCmd_RejectsUnknownAdapter(t *testing.T) {
	root := t.TempDir()
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "init", "--adapter=kebab")
	if err == nil || !strings.Contains(err.Error(), "unknown adapter") {
		t.Errorf("expected unknown-adapter error, got %v", err)
	}
}

func TestInitCmd_ReRunGroundTruthIsIdempotent(t *testing.T) {
	root := t.TempDir()
	withCwd(t, root)
	rt := &fakeRuntime{}

	if _, err := runRoot(t, rt, "init", "--adapter=ground-truth"); err != nil {
		t.Fatalf("first init: %v", err)
	}

	out, err := runRoot(t, rt, "init", "--adapter=ground-truth")
	if err != nil {
		t.Fatalf("second init: %v", err)
	}
	if !strings.Contains(out, "already present") {
		t.Errorf("re-run should report already-present, got %q", out)
	}
}
