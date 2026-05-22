package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTruthHistory_RendersEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)

	supersedes := int64(7)
	rt := &fakeRuntime{
		getTruthHistoryOut: []TruthHistoryRow{
			{
				ID:          7,
				Topic:       "initial",
				RecordedAt:  1700000000,
				Scope:       "domain",
				Files:       []string{"ground-truth/do-not-claim.md"},
				DiffRef:     "git:a1b2c3d",
				MotivatedBy: "Initial HIPAA rule",
			},
			{
				ID:          12,
				Topic:       "tighten",
				RecordedAt:  1700100000,
				Scope:       "domain",
				Files:       []string{"ground-truth/do-not-claim.md"},
				DiffRef:     "git:e8eced8",
				MotivatedBy: "Tighten wording per legal feedback",
				Supersedes:  &supersedes,
			},
		},
	}

	out, err := runRoot(t, rt, "truth-history", "ground-truth/do-not-claim.md")
	if err != nil {
		t.Fatalf("truth-history: %v\nout=%s", err, out)
	}

	for _, want := range []string{
		"Truth history for ground-truth/do-not-claim.md",
		"#7",
		"initial",
		"Initial HIPAA rule",
		"git:a1b2c3d",
		"#12",
		"supersedes=#7",
		"Tighten wording per legal feedback",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q in:\n%s", want, out)
		}
	}

	if len(rt.getTruthHistoryCalls) != 1 {
		t.Errorf("GetTruthHistory called %d times", len(rt.getTruthHistoryCalls))
	}
	if rt.getTruthHistoryCalls[0].FilePath != "ground-truth/do-not-claim.md" {
		t.Errorf("FilePath: got %q", rt.getTruthHistoryCalls[0].FilePath)
	}
}

func TestTruthHistory_EmptyResultPrintsHint(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{getTruthHistoryOut: nil}
	out, err := runRoot(t, rt, "truth-history", "unused/path.md")
	if err != nil {
		t.Fatalf("truth-history: %v", err)
	}
	if !strings.Contains(out, "no truth-change entries") {
		t.Errorf("empty result should print hint, got: %s", out)
	}
}

func TestTruthHistory_TrivialEntriesCollapsedByDefault(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		getTruthHistoryOut: []TruthHistoryRow{
			{ID: 1, Topic: "first", RecordedAt: 1, Scope: "domain", MotivatedBy: "non-trivial"},
			{ID: 2, Topic: "fix-typo", RecordedAt: 2, Scope: "domain", Trivial: true, TrivialReason: "typo"},
			{ID: 3, Topic: "ws", RecordedAt: 3, Scope: "domain", Trivial: true, TrivialReason: "whitespace"},
		},
	}
	out, err := runRoot(t, rt, "truth-history", "x.md")
	if err != nil {
		t.Fatalf("truth-history: %v", err)
	}
	if !strings.Contains(out, "non-trivial") {
		t.Error("non-trivial entry should be shown")
	}
	if strings.Contains(out, "fix-typo") || strings.Contains(out, "ws") {
		t.Error("trivial entries should be collapsed by default")
	}
	if !strings.Contains(out, "trivial") || !strings.Contains(out, "collapsed") {
		t.Errorf("output should mention collapsed-trivial summary: %s", out)
	}
}

func TestTruthHistory_IncludeTrivialFlag(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{
		getTruthHistoryOut: []TruthHistoryRow{
			{ID: 1, Topic: "non-trivial", RecordedAt: 1, Scope: "domain"},
			{ID: 2, Topic: "fix-typo", RecordedAt: 2, Scope: "domain", Trivial: true, TrivialReason: "typo"},
		},
	}
	out, err := runRoot(t, rt, "truth-history", "x.md", "--include-trivial")
	if err != nil {
		t.Fatalf("truth-history: %v", err)
	}
	if !strings.Contains(out, "non-trivial") || !strings.Contains(out, "fix-typo") {
		t.Errorf("both entries should be shown with --include-trivial: %s", out)
	}
	if !strings.Contains(out, "TRIVIAL") {
		t.Error("trivial-flag marker should be shown")
	}
}

func TestTruthHistory_RequiresPathArg(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "truth-history")
	if err == nil {
		t.Error("missing path arg should error")
	}
}

func TestTruthHistory_PassesLimitFlag(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	if _, err := runRoot(t, rt, "truth-history", "x.md", "--limit", "5"); err != nil {
		t.Fatalf("truth-history: %v", err)
	}
	if len(rt.getTruthHistoryCalls) != 1 || rt.getTruthHistoryCalls[0].Limit != 5 {
		t.Errorf("limit not propagated: %+v", rt.getTruthHistoryCalls)
	}
}
