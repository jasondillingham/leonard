package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkDataDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dataDirName, err)
	}
	withCwd(t, root)
	return root
}

func TestDecisionsList_PrintsRows(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{
		getDecisionsOut: []DecisionRow{
			{ID: 2, Topic: "toml lib", Choice: "pelletier/go-toml/v2", Reasoning: "actively maintained\nzero-alloc decode", RecordedAt: 1700000000},
			{ID: 1, Topic: "store", Choice: "modernc.org/sqlite", Reasoning: "pure Go", RecordedAt: 1690000000},
		},
	}
	out, err := runRoot(t, rt, "decisions", "list")
	if err != nil {
		t.Fatalf("decisions list: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "2 decision(s)") {
		t.Errorf("expected count summary: %q", out)
	}
	if !strings.Contains(out, "toml lib") || !strings.Contains(out, "pelletier/go-toml/v2") {
		t.Errorf("first row missing: %q", out)
	}
	if !strings.Contains(out, "actively maintained") {
		t.Errorf("reasoning first-line missing: %q", out)
	}
	if strings.Contains(out, "zero-alloc decode") {
		t.Errorf("reasoning should be flattened to first line: %q", out)
	}
}

func TestDecisionsList_EmptyHasFriendlyMessage(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{getDecisionsOut: nil}
	out, err := runRoot(t, rt, "decisions", "list")
	if err != nil {
		t.Fatalf("decisions list: %v", err)
	}
	if !strings.Contains(out, "no decisions recorded") {
		t.Errorf("expected friendly empty message: %q", out)
	}
}

func TestDecisionsList_PassesFlags(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "decisions", "list", "--topic", "store", "--since", "100", "--limit", "5")
	if err != nil {
		t.Fatalf("decisions list: %v", err)
	}
	if len(rt.getDecisionsCalls) != 1 {
		t.Fatalf("GetDecisions called %d times", len(rt.getDecisionsCalls))
	}
	c := rt.getDecisionsCalls[0]
	if c.Topic != "store" || c.Since != 100 || c.Limit != 5 {
		t.Errorf("flags not passed through: %+v", c)
	}
}

func TestDecisionsList_RequiresInit(t *testing.T) {
	withCwd(t, t.TempDir()) // no .leonard/
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "decisions", "list")
	if err == nil || !strings.Contains(err.Error(), "leonard init") {
		t.Fatalf("expected init-required error, got %v", err)
	}
	if len(rt.getDecisionsCalls) != 0 {
		t.Errorf("GetDecisions should not be called without .leonard/")
	}
}

func TestDecisionsAdd_ForwardsArgs(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{recordDecisionID: 42}
	out, err := runRoot(t, rt, "decisions", "add", "auth lib", "Authentik", "OIDC", "support", "is", "first-class")
	if err != nil {
		t.Fatalf("decisions add: %v\nout=%s", err, out)
	}
	if len(rt.recordDecisionCalls) != 1 {
		t.Fatalf("RecordDecision called %d times", len(rt.recordDecisionCalls))
	}
	c := rt.recordDecisionCalls[0]
	if c.Topic != "auth lib" {
		t.Errorf("topic = %q", c.Topic)
	}
	if c.Choice != "Authentik" {
		t.Errorf("choice = %q", c.Choice)
	}
	if c.Reasoning != "OIDC support is first-class" {
		t.Errorf("reasoning should be joined args[2:] with spaces, got %q", c.Reasoning)
	}
	if !strings.Contains(out, "recorded decision #42") {
		t.Errorf("expected new-id message in output: %q", out)
	}
}

func TestDecisionsAdd_RequiresThreeArgs(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{}
	// cobra reports its own usage error here; we just need to confirm it fails
	// and didn't reach the runtime.
	_, err := runRoot(t, rt, "decisions", "add", "topic-only")
	if err == nil {
		t.Fatal("expected error on too-few args")
	}
	if len(rt.recordDecisionCalls) != 0 {
		t.Errorf("RecordDecision should not be called when args check fails")
	}
}

func TestDecisionsStale_PrintsMissingRefs(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{
		getStaleOut: []StaleDecisionRow{
			{
				Decision:       DecisionRow{ID: 7, Topic: "schema", Choice: "v3"},
				MissingFiles:   []string{"internal/store/migrate.go"},
				MissingSymbols: []string{"ApplyV3", "RollbackV2"},
			},
		},
	}
	out, err := runRoot(t, rt, "decisions", "stale")
	if err != nil {
		t.Fatalf("decisions stale: %v", err)
	}
	if !strings.Contains(out, "1 stale decision(s)") {
		t.Errorf("missing summary: %q", out)
	}
	if !strings.Contains(out, "internal/store/migrate.go") {
		t.Errorf("missing-files list absent: %q", out)
	}
	if !strings.Contains(out, "ApplyV3") || !strings.Contains(out, "RollbackV2") {
		t.Errorf("missing-symbols list absent: %q", out)
	}
}

func TestDecisionsStale_EmptyHasFriendlyMessage(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{getStaleOut: nil}
	out, err := runRoot(t, rt, "decisions", "stale")
	if err != nil {
		t.Fatalf("decisions stale: %v", err)
	}
	if !strings.Contains(out, "no stale decisions") {
		t.Errorf("expected empty-message: %q", out)
	}
}

func TestClaimsUnverified_PrintsRows(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{
		getUnverifiedOut: []ClaimRow{
			{
				ID: 5, SessionID: "sess-abc", Claim: "vet ok on internal/store/store.go",
				Evidence: "go vet: failed\nexit: 1\nstore.go:42 declared and not used: x",
				FilePath: "internal/store/store.go", RecordedAt: 1700000500,
			},
		},
	}
	out, err := runRoot(t, rt, "claims", "unverified")
	if err != nil {
		t.Fatalf("claims unverified: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "1 unverified claim(s)") {
		t.Errorf("missing summary line: %q", out)
	}
	if !strings.Contains(out, "internal/store/store.go") {
		t.Errorf("file path missing: %q", out)
	}
	if !strings.Contains(out, "sess-abc") {
		t.Errorf("session id missing: %q", out)
	}
	if !strings.Contains(out, "go vet: failed") {
		t.Errorf("first evidence line missing: %q", out)
	}
}

func TestClaimsUnverified_PassesSessionFilter(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "claims", "unverified", "--session", "sess-xyz")
	if err != nil {
		t.Fatalf("claims unverified: %v", err)
	}
	if len(rt.getUnverifiedCalls) != 1 || rt.getUnverifiedCalls[0].SessionID != "sess-xyz" {
		t.Errorf("session filter not forwarded: %+v", rt.getUnverifiedCalls)
	}
}

func TestClaimsUnverified_EmptyHasFriendlyMessage(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "claims", "unverified")
	if err != nil {
		t.Fatalf("claims unverified: %v", err)
	}
	if !strings.Contains(out, "no unverified claims") {
		t.Errorf("expected friendly empty message: %q", out)
	}
}

func TestClaimsPurge_DefaultAge(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{purgeSupersededOut: 3}
	out, err := runRoot(t, rt, "claims", "purge")
	if err != nil {
		t.Fatalf("claims purge: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "purged 3 superseded") {
		t.Errorf("want purge count in output, got %q", out)
	}
	if len(rt.purgeSupersededCalls) != 1 {
		t.Fatalf("want 1 purge call, got %d", len(rt.purgeSupersededCalls))
	}
	// Default --age is 30 days; cutoff should be ~30 days ago (unix seconds).
	cutoff := rt.purgeSupersededCalls[0]
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Unix()
	delta := thirtyDaysAgo - cutoff
	if delta < -5 || delta > 5 {
		t.Errorf("cutoff not ~30 days ago: want ~%d, got %d (delta=%d)", thirtyDaysAgo, cutoff, delta)
	}
}

func TestClaimsPurge_NothingToDelete(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{purgeSupersededOut: 0}
	out, err := runRoot(t, rt, "claims", "purge")
	if err != nil {
		t.Fatalf("claims purge: %v", err)
	}
	if !strings.Contains(out, "no superseded claims") {
		t.Errorf("expected friendly empty message: %q", out)
	}
}
