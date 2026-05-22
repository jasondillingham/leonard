package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTruthStory_RendersMarkdown(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		getTruthChangesOut: []TruthHistoryRow{
			{ID: 1, Topic: "a", RecordedAt: 1000, Scope: "domain", MotivatedBy: "first"},
			{ID: 2, Topic: "b", RecordedAt: 2000, Scope: "toolkit", MotivatedBy: "second"},
		},
	}
	out, err := runRoot(t, rt, "truth-story")
	if err != nil {
		t.Fatalf("truth-story: %v\nout=%s", err, out)
	}
	for _, want := range []string{
		"# Truth-source change narrative",
		"2 entries shown",
		"#1",
		"first",
		"#2",
		"second",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestTruthStory_EmptyResult(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "truth-story")
	if err != nil {
		t.Fatalf("truth-story: %v", err)
	}
	if !strings.Contains(out, "no truth-change entries") {
		t.Errorf("empty: %s", out)
	}
}

func TestTruthStory_ScopeFlag(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	if _, err := runRoot(t, rt, "truth-story", "--scope=domain"); err != nil {
		t.Fatalf("truth-story: %v", err)
	}
	if len(rt.getTruthChangesCalls) != 1 || rt.getTruthChangesCalls[0].Scope != "domain" {
		t.Errorf("scope not propagated: %+v", rt.getTruthChangesCalls)
	}
}

func TestTruthStory_RejectsInvalidScope(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "truth-story", "--scope=kebab")
	if err == nil || !strings.Contains(err.Error(), "--scope") {
		t.Errorf("want scope error, got %v", err)
	}
}

func TestTruthStory_SinceFlag(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	if _, err := runRoot(t, rt, "truth-story", "--since=2026-01-01"); err != nil {
		t.Fatalf("truth-story: %v", err)
	}
	if len(rt.getTruthChangesCalls) != 1 || rt.getTruthChangesCalls[0].Since == 0 {
		t.Errorf("since not propagated: %+v", rt.getTruthChangesCalls)
	}
}

func TestTruthStory_RejectsInvalidSince(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "truth-story", "--since=yesterday")
	if err == nil || !strings.Contains(err.Error(), "YYYY-MM-DD") {
		t.Errorf("want date format error, got %v", err)
	}
}

func TestTruthStory_JSONFormat(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		getTruthChangesOut: []TruthHistoryRow{
			{ID: 1, Topic: "a", Scope: "domain", MotivatedBy: "x"},
		},
	}
	out, err := runRoot(t, rt, "truth-story", "--format=json")
	if err != nil {
		t.Fatalf("truth-story: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output isn't valid JSON: %v\n%s", err, out)
	}
	if _, ok := payload["entries"]; !ok {
		t.Errorf("JSON payload missing 'entries': %v", payload)
	}
}

func TestTruthStory_PlainFormat(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		getTruthChangesOut: []TruthHistoryRow{
			{ID: 7, Topic: "release", Scope: "domain", RecordedAt: 1700000000, MotivatedBy: "tighten rule"},
		},
	}
	out, err := runRoot(t, rt, "truth-story", "--format=plain")
	if err != nil {
		t.Fatalf("truth-story: %v", err)
	}
	// Plain format: tab-separated, one entry per line.
	if !strings.Contains(out, "\t") {
		t.Errorf("plain format should be tab-separated, got %q", out)
	}
	if !strings.Contains(out, "release") {
		t.Errorf("plain format missing topic: %q", out)
	}
}

func TestTruthStory_TrivialCollapsedByDefault(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		getTruthChangesOut: []TruthHistoryRow{
			{ID: 1, Topic: "real", Scope: "domain", MotivatedBy: "real change"},
			{ID: 2, Topic: "typo-fix", Scope: "domain", Trivial: true, TrivialReason: "typo"},
		},
	}
	out, err := runRoot(t, rt, "truth-story")
	if err != nil {
		t.Fatalf("truth-story: %v", err)
	}
	if !strings.Contains(out, "real change") {
		t.Error("non-trivial entry should appear")
	}
	if strings.Contains(out, "typo-fix") {
		t.Error("trivial entry should be collapsed by default")
	}
	if !strings.Contains(out, "1 trivial") {
		t.Errorf("collapsed-trivial summary missing: %s", out)
	}
}

func TestTruthStory_IncludeTrivial(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		getTruthChangesOut: []TruthHistoryRow{
			{ID: 1, Topic: "real", Scope: "domain"},
			{ID: 2, Topic: "typo-fix", Scope: "domain", Trivial: true, TrivialReason: "typo"},
		},
	}
	out, err := runRoot(t, rt, "truth-story", "--include-trivial")
	if err != nil {
		t.Fatalf("truth-story: %v", err)
	}
	if !strings.Contains(out, "real") || !strings.Contains(out, "typo-fix") {
		t.Errorf("both entries should appear with --include-trivial:\n%s", out)
	}
}

func TestTruthStory_RejectsInvalidFormat(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "truth-story", "--format=yaml")
	if err == nil || !strings.Contains(err.Error(), "--format") {
		t.Errorf("want format error, got %v", err)
	}
}
