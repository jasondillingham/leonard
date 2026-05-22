package selflog_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/selflog"
)

// newInitedAdapter sets up a fresh adapter against a tempdir.
func newInitedAdapter(t *testing.T) (adapters.Adapter, string) {
	t.Helper()
	tmp := t.TempDir()
	a := selflog.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, tmp
}

// readPendingDecisions returns one map per JSON line in
// .leonard/pending-decisions.log. Missing file returns nil.
func readPendingDecisions(t *testing.T, projectRoot string) []map[string]any {
	t.Helper()
	logPath := filepath.Join(projectRoot, ".leonard", "pending-decisions.log")
	f, err := os.Open(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(sc.Bytes(), &entry); err != nil {
			t.Fatalf("bad line: %v", err)
		}
		out = append(out, entry)
	}
	return out
}

func TestInit_RejectsEmptyProjectRoot(t *testing.T) {
	a := selflog.New()
	if err := a.Init(context.Background(), adapters.Config{}); err == nil {
		t.Error("expected error when ProjectRoot is empty")
	}
}

func TestPostEdit_LogsDomainTruthEdit(t *testing.T) {
	a, tmp := newInitedAdapter(t)

	// facts.yaml is the warn-tier domain-truth case.
	target := filepath.Join(tmp, ".leonard", "ground-truth", "facts.yaml")
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "s1",
		Tool:      "Edit",
		FilePath:  target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}

	entries := readPendingDecisions(t, tmp)
	if len(entries) != 1 {
		t.Fatalf("entry count: want 1, got %d", len(entries))
	}
	e := entries[0]
	if e["scope"] != "domain" {
		t.Errorf("scope: want %q, got %q", "domain", e["scope"])
	}
	if e["tier"] != "warn" {
		t.Errorf("tier: want %q, got %q", "warn", e["tier"])
	}
	if !strings.HasSuffix(e["file_path"].(string), "facts.yaml") {
		t.Errorf("file_path: %q", e["file_path"])
	}
}

func TestPostEdit_LogsRequireTierDomainEdit(t *testing.T) {
	a, tmp := newInitedAdapter(t)
	target := filepath.Join(tmp, ".leonard", "ground-truth", "do-not-claim.md")
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		FilePath: target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	entries := readPendingDecisions(t, tmp)
	if len(entries) != 1 {
		t.Fatalf("entry count: want 1, got %d", len(entries))
	}
	if entries[0]["tier"] != "require" {
		t.Errorf("tier: want %q, got %v", "require", entries[0]["tier"])
	}
}

func TestPostEdit_SkipsAuditLog(t *testing.T) {
	a, tmp := newInitedAdapter(t)
	// audit-log.md is skip-tier — should produce no log entry.
	target := filepath.Join(tmp, ".leonard", "ground-truth", "audit-log.md")
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		FilePath: target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if entries := readPendingDecisions(t, tmp); len(entries) != 0 {
		t.Errorf("skip-tier file should not log, got %+v", entries)
	}
}

func TestPostEdit_LogsToolkitTruthEdit(t *testing.T) {
	a, tmp := newInitedAdapter(t)
	target := filepath.Join(tmp, "internal", "adapters", "code", "adapter.go")
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		FilePath: target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	entries := readPendingDecisions(t, tmp)
	if len(entries) != 1 {
		t.Fatalf("entry count: want 1, got %d", len(entries))
	}
	if entries[0]["scope"] != "toolkit" {
		t.Errorf("scope: want %q, got %v", "toolkit", entries[0]["scope"])
	}
	if entries[0]["tier"] != "require" {
		t.Errorf("internal/adapters/ should be require-tier, got %v", entries[0]["tier"])
	}
}

func TestPostEdit_LogsRoadmapEdit(t *testing.T) {
	a, tmp := newInitedAdapter(t)
	target := filepath.Join(tmp, "docs", "ROADMAP-v1-ground-truth.md")
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		FilePath: target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	entries := readPendingDecisions(t, tmp)
	if len(entries) != 1 {
		t.Fatalf("entry count: want 1, got %d", len(entries))
	}
	if entries[0]["tier"] != "warn" {
		t.Errorf("docs/ROADMAP* should be warn-tier, got %v", entries[0]["tier"])
	}
}

func TestPostEdit_IgnoresNonTruthFile(t *testing.T) {
	a, tmp := newInitedAdapter(t)
	target := filepath.Join(tmp, "README.md")
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		FilePath: target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if entries := readPendingDecisions(t, tmp); len(entries) != 0 {
		t.Errorf("README.md is not policy-covered, should not log: %+v", entries)
	}
}

func TestPostEdit_OutsideProjectIsIgnored(t *testing.T) {
	a, tmp := newInitedAdapter(t)
	outside := filepath.Join(t.TempDir(), ".leonard", "ground-truth", "facts.yaml")
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		FilePath: outside,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if entries := readPendingDecisions(t, tmp); len(entries) != 0 {
		t.Errorf("outside-project edit should not log: %+v", entries)
	}
}

func TestPostEdit_AppendsAcrossCalls(t *testing.T) {
	a, tmp := newInitedAdapter(t)
	target := filepath.Join(tmp, ".leonard", "ground-truth", "facts.yaml")
	for i := 0; i < 3; i++ {
		if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{FilePath: target}); err != nil {
			t.Fatalf("PostEdit %d: %v", i, err)
		}
	}
	if entries := readPendingDecisions(t, tmp); len(entries) != 3 {
		t.Errorf("append: want 3, got %d", len(entries))
	}
}

func TestPostEdit_DraftCarriesPlaceholder(t *testing.T) {
	a, tmp := newInitedAdapter(t)
	target := filepath.Join(tmp, ".leonard", "ground-truth", "facts.yaml")
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{FilePath: target}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	entries := readPendingDecisions(t, tmp)
	draft, _ := entries[0]["draft"].(map[string]any)
	motivatedBy, _ := draft["motivated_by"].(string)
	if motivatedBy == "" {
		t.Error("motivated_by should carry a placeholder")
	}
	if !strings.Contains(motivatedBy, "Add rationale") {
		t.Errorf("placeholder should prompt operator: %q", motivatedBy)
	}
}

func TestPostEdit_NoOpForOtherHooks(t *testing.T) {
	a, _ := newInitedAdapter(t)
	ctx := context.Background()

	pre, err := a.PreEdit(ctx, adapters.PreEditPayload{Tool: "Edit"})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if pre.Decision != adapters.Pass {
		t.Errorf("PreEdit v0.6: want Pass, got %v", pre.Decision)
	}

	if _, err := a.SessionStart(ctx, adapters.SessionStartPayload{}); err != nil {
		t.Errorf("SessionStart: %v", err)
	}
	if _, err := a.Stop(ctx, adapters.StopPayload{}); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := a.RegisterTools(nil); err != nil {
		t.Errorf("RegisterTools v0.6: %v", err)
	}
}

func TestRegistry_SelfLogAdapterIsRegistered(t *testing.T) {
	got, err := adapters.New(selflog.Name)
	if err != nil {
		t.Fatalf("adapters.New(%q): %v", selflog.Name, err)
	}
	if got.Name() != selflog.Name {
		t.Errorf("Name: want %q, got %q", selflog.Name, got.Name())
	}
}
