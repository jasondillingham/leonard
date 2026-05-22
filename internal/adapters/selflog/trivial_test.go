package selflog_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/selflog"
	"github.com/jasondillingham/leonard/internal/config"
)

// writeTrivialToken drops a JSON token file mirroring what
// cmd/leonard/truth-edit writes. Tests use this to set up the
// bypass-token state without invoking the CLI.
//
// v1.0 (bughunt-11 F1): token lives at $XDG_CONFIG_HOME/leonard/
// pending-trivial/, not under .leonard/. preEditFixture sets
// XDG_CONFIG_HOME to a tempdir so this writes to test-isolated
// state.
func writeTrivialToken(t *testing.T, projectRoot, rel, reason string, ts time.Time) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	tokenPath, err := config.PendingTokenPath("trivial", resolved, rel)
	if err != nil {
		t.Fatalf("PendingTokenPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"file_path":      rel,
		"trivial_reason": reason,
		"ts":             ts.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(tokenPath, body, 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
}

// TestConsumeTrivialToken_AllowsRequireTier covers the happy path:
// a fresh token bypasses require-tier denial.
func TestConsumeTrivialToken_AllowsRequireTier(t *testing.T) {
	a, tmp := preEditFixture(t)
	resolved, _ := filepath.EvalSymlinks(tmp)
	if err := config.WriteAdapterTrust(resolved, selflog.Name); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(".leonard", "ground-truth", "do-not-claim.md")
	writeTrivialToken(t, tmp, rel, "fix typo", time.Now())

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: filepath.Join(tmp, rel),
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("trivial token: want Pass, got %v", out.Decision)
	}
}

func TestConsumeTrivialToken_IsSingleUse(t *testing.T) {
	a, tmp := preEditFixture(t)
	resolved, _ := filepath.EvalSymlinks(tmp)
	if err := config.WriteAdapterTrust(resolved, selflog.Name); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(".leonard", "ground-truth", "do-not-claim.md")
	writeTrivialToken(t, tmp, rel, "fix typo", time.Now())

	// First call consumes the token → Pass.
	if _, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool: "Edit", FilePath: filepath.Join(tmp, rel),
	}); err != nil {
		t.Fatalf("first PreEdit: %v", err)
	}
	// Second call: no token → require-tier should Deny.
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool: "Edit", FilePath: filepath.Join(tmp, rel),
	})
	if err != nil {
		t.Fatalf("second PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("single-use: 2nd call want Deny, got %v", out.Decision)
	}
}

func TestConsumeTrivialToken_ExpiresAfterTTL(t *testing.T) {
	a, tmp := preEditFixture(t)
	resolved, _ := filepath.EvalSymlinks(tmp)
	if err := config.WriteAdapterTrust(resolved, selflog.Name); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(".leonard", "ground-truth", "do-not-claim.md")
	// Token from 10 minutes ago — past the 5-minute TTL.
	writeTrivialToken(t, tmp, rel, "stale", time.Now().Add(-10*time.Minute))

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool: "Edit", FilePath: filepath.Join(tmp, rel),
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("expired token: want Deny, got %v", out.Decision)
	}
	// Expired tokens are deleted on read so they don't accumulate.
	tokenPath, err := config.PendingTokenPath("trivial", resolved, rel)
	if err != nil {
		t.Fatalf("PendingTokenPath: %v", err)
	}
	if _, err := os.Stat(tokenPath); err == nil {
		t.Errorf("expired token not deleted: %s", tokenPath)
	}
}

// TestConsumeTrivialToken_RefusesSymlink covers bughunt-11 F2:
// a symlink planted at the canonical token path must be refused,
// not followed. The attack model: an actor who gets write access to
// $XDG_CONFIG_HOME/leonard/pending-trivial/ (multi-user box, mis-
// configured perms) plants a symlink to a forged token stored
// elsewhere. Without symlink refusal, consumeTrivialToken would
// read the forged content and grant bypass authority.
func TestConsumeTrivialToken_RefusesSymlink(t *testing.T) {
	a, tmp := preEditFixture(t)
	resolved, _ := filepath.EvalSymlinks(tmp)
	if err := config.WriteAdapterTrust(resolved, selflog.Name); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(".leonard", "ground-truth", "do-not-claim.md")

	// Plant a "real" forged token elsewhere with valid content +
	// fresh timestamp.
	stash := filepath.Join(t.TempDir(), "forged.json")
	body, _ := json.Marshal(map[string]any{
		"file_path":      rel,
		"trivial_reason": "attacker-supplied",
		"ts":             time.Now().UTC().Format(time.RFC3339),
	})
	if err := os.WriteFile(stash, body, 0o600); err != nil {
		t.Fatal(err)
	}
	// Symlink the canonical token path to the forged file.
	tokenPath, err := config.PendingTokenPath("trivial", resolved, rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stash, tokenPath); err != nil {
		t.Fatal(err)
	}

	// PreEdit on a require-tier file should Deny — the symlinked
	// token must not be honored.
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool: "Edit", FilePath: filepath.Join(tmp, rel),
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("symlinked token: want Deny, got %v", out.Decision)
	}
	// And the symlink should still be there — F2 says refuse without
	// auto-delete (operator visibility).
	if info, err := os.Lstat(tokenPath); err != nil {
		t.Errorf("symlink should still exist after refusal: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("token path no longer a symlink — was it auto-resolved?")
	}
}

func TestConsumeTrivialToken_AppendsDraftEntry(t *testing.T) {
	a, tmp := preEditFixture(t)
	resolved, _ := filepath.EvalSymlinks(tmp)
	if err := config.WriteAdapterTrust(resolved, selflog.Name); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(".leonard", "ground-truth", "do-not-claim.md")
	writeTrivialToken(t, tmp, rel, "fix typo", time.Now())

	if _, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool: "Edit", FilePath: filepath.Join(tmp, rel),
	}); err != nil {
		t.Fatalf("PreEdit: %v", err)
	}

	entries := readPendingDecisions(t, tmp)
	if len(entries) != 1 {
		t.Fatalf("want 1 pending-decisions entry, got %d", len(entries))
	}
	draft, _ := entries[0]["draft"].(map[string]any)
	motivated, _ := draft["motivated_by"].(string)
	if !strings.Contains(motivated, "Trivial bypass") {
		t.Errorf("draft should mark trivial: %q", motivated)
	}
	if !strings.Contains(motivated, "fix typo") {
		t.Errorf("draft should carry reason: %q", motivated)
	}
}
