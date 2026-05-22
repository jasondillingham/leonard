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
func writeTrivialToken(t *testing.T, projectRoot, rel, reason string, ts time.Time) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	tokenDir := filepath.Join(resolved, ".leonard", "pending-trivial")
	if err := os.MkdirAll(tokenDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Mirror the CLI's filename derivation: sha256(rel)[:32].
	// Implementing it inline keeps test isolation from cmd/* code.
	body, err := json.Marshal(map[string]any{
		"file_path":      rel,
		"trivial_reason": reason,
		"ts":             ts.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, hashName(rel)), body, 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
}

// hashName mirrors selflog.tokenFilename and
// cmd/leonard.trivialTokenFilename. Reimplemented locally so the
// test doesn't reach into either package's unexported helpers.
func hashName(rel string) string {
	// crypto/sha256 + encoding/hex live in stdlib; inlining the
	// expression keeps the test self-contained.
	return shaHex(rel) + ".json"
}

func shaHex(s string) string {
	// Two-level helper so the import lives in one place per file.
	return shaHexImpl(s)
}

// (Tiny detour to keep test imports tight.)
func shaHexImpl(s string) string {
	// Defer to crypto/sha256 via a separate helper file would be
	// over-engineered. Use the stdlib here.
	return hexSha256Prefix32(s)
}

func hexSha256Prefix32(s string) string {
	// Implementation in trivial_helpers_test.go to keep this file
	// focused on test cases.
	return computeSha256Prefix32(s)
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
	tokenDir := filepath.Join(resolved, ".leonard", "pending-trivial")
	entries, _ := os.ReadDir(tokenDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("expired token not deleted: %s", e.Name())
		}
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
