package groundtruth_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
	"github.com/jasondillingham/leonard/internal/config"
)

// filterFixture sets up a tempdir with a custom filters.yaml +
// minimal stories/facts/do-not-claim shells. Returns the adapter,
// the project root, and a stderr buffer.
func filterFixture(t *testing.T, filtersYAML string) (*groundtruth.GroundTruthAdapter, string, *bytes.Buffer) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", xdg)

	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gtDir, "filters.yaml"), []byte(filtersYAML), 0o644); err != nil {
		t.Fatalf("write filters: %v", err)
	}
	// Empty rules so the forbidden-claim guard doesn't fire and
	// obscure the filter test outcome.
	if err := os.WriteFile(filepath.Join(gtDir, "do-not-claim.md"), []byte("# empty\n"), 0o644); err != nil {
		t.Fatalf("write rules: %v", err)
	}

	stderr := &bytes.Buffer{}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Stderr:      stderr,
		Raw: map[string]any{
			// Make every path verifiable so path_filters check fires
			// regardless of the default *.md/*.txt/*.yaml allowlist.
			"verify_targets": []any{"**", "*"},
		},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter), tmp, stderr
}

func grantGroundTruthTrust(t *testing.T, projectRoot string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if err := config.WriteAdapterTrust(resolved, "ground-truth"); err != nil {
		t.Fatalf("WriteAdapterTrust: %v", err)
	}
}

func TestPathFilters_DeniesForbiddenCaptureWhenTrusted(t *testing.T) {
	a, tmp, _ := filterFixture(t, `path_filters:
  - path_pattern: "applications/([^/]+)/"
    forbidden_values:
      - acme-corp
      - beta-co
    reason: "Contract terminated"
`)
	grantGroundTruthTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "applications/acme-corp/cover-letter.md"),
		Content:  "Dear Acme,",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Fatalf("forbidden path: want Deny, got %v (reason=%q)", out.Decision, out.Reason)
	}
	if !strings.Contains(out.Reason, "Contract terminated") {
		t.Errorf("Reason should carry rule's reason: %q", out.Reason)
	}
	if !strings.Contains(out.Reason, "leonard override --once") {
		t.Errorf("Reason should hint at override CLI: %q", out.Reason)
	}
}

func TestPathFilters_AllowsNonForbiddenCapture(t *testing.T) {
	a, tmp, _ := filterFixture(t, `path_filters:
  - path_pattern: "applications/([^/]+)/"
    forbidden_values:
      - acme-corp
`)
	grantGroundTruthTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "applications/different-co/cover-letter.md"),
		Content:  "Body",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("non-forbidden capture: want Pass, got %v", out.Decision)
	}
}

func TestPathFilters_WarnsWhenUntrusted(t *testing.T) {
	a, tmp, stderr := filterFixture(t, `path_filters:
  - path_pattern: "applications/([^/]+)/"
    forbidden_values:
      - acme-corp
    reason: "Contract terminated"
`)
	// No trust grant.

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "applications/acme-corp/cover-letter.md"),
		Content:  "Body",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("untrusted: want Pass (warn), got %v", out.Decision)
	}
	if !strings.Contains(stderr.String(), "path_filters") {
		t.Errorf("stderr should cite the filter rule: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "leonard config trust ground-truth") {
		t.Errorf("stderr should hint at trust grant: %q", stderr.String())
	}
}

// TestPathFilters_OverrideToken_RefusesSymlink covers bughunt-11
// F2: a symlink planted at the canonical override-token path must
// be refused, not followed. Same attack shape as the trivial-token
// symlink-refusal test in internal/adapters/selflog.
func TestPathFilters_OverrideToken_RefusesSymlink(t *testing.T) {
	a, tmp, _ := filterFixture(t, `path_filters:
  - path_pattern: "applications/([^/]+)/"
    forbidden_values:
      - acme-corp
`)
	grantGroundTruthTrust(t, tmp)
	resolved, _ := filepath.EvalSymlinks(tmp)
	rel := "applications/acme-corp/cover-letter.md"

	// Plant a forged token elsewhere with a fresh timestamp.
	stash := filepath.Join(t.TempDir(), "forged.json")
	body := []byte(`{"file_path":"` + rel + `","reason":"attacker","ts":"` + nowRFC3339() + `"}`)
	if err := os.WriteFile(stash, body, 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath, err := config.PendingTokenPath("override", resolved, rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stash, tokenPath); err != nil {
		t.Fatal(err)
	}

	// PreEdit should Deny — the symlinked override must not bypass
	// the path_filter rule.
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, rel),
		Content:  "Body",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("symlinked override: want Deny, got %v", out.Decision)
	}
	// Symlink should still be there.
	if info, err := os.Lstat(tokenPath); err != nil {
		t.Errorf("symlink should still exist after refusal: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("token path no longer a symlink — was it auto-resolved?")
	}
}

func TestPathFilters_BypassedByOverrideToken(t *testing.T) {
	a, tmp, _ := filterFixture(t, `path_filters:
  - path_pattern: "applications/([^/]+)/"
    forbidden_values:
      - acme-corp
`)
	grantGroundTruthTrust(t, tmp)

	// Write an override token at the path the adapter computes.
	rel := "applications/acme-corp/cover-letter.md"
	writeOverrideToken(t, tmp, rel, "Acme is back; legal cleared")

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, rel),
		Content:  "Body",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("override token: want Pass, got %v", out.Decision)
	}
	// Token consumed — second call denies again.
	out, err = a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, rel),
		Content:  "Body",
	})
	if err != nil {
		t.Fatalf("PreEdit 2nd: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("single-use override: 2nd call want Deny, got %v", out.Decision)
	}
}

func TestPathFilters_EmptyForbiddenValuesMatchesAll(t *testing.T) {
	a, tmp, _ := filterFixture(t, `path_filters:
  - path_pattern: "secrets/"
    reason: "Bare blanket ban"
`)
	grantGroundTruthTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "secrets/api-keys.md"),
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("blanket-ban path: want Deny, got %v (reason=%q)", out.Decision, out.Reason)
	}
}

func TestContentFilters_DeniesWhenDisclosureMissing(t *testing.T) {
	a, tmp, _ := filterFixture(t, `content_filters:
  - content_pattern: "(?i)bid on contract"
    required: "see disclosures.md"
`)
	grantGroundTruthTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "proposal.md"),
		Content:  "We are pleased to bid on contract A-100 next quarter.",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("disclosure missing: want Deny, got %v", out.Decision)
	}
	if !strings.Contains(out.Reason, "see disclosures.md") {
		t.Errorf("Reason should carry required disclosure: %q", out.Reason)
	}
}

func TestContentFilters_PassesWhenDisclosurePresent(t *testing.T) {
	a, tmp, _ := filterFixture(t, `content_filters:
  - content_pattern: "(?i)bid on contract"
    required: "see disclosures.md"
`)
	grantGroundTruthTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "proposal.md"),
		Content:  "We are pleased to bid on contract A-100. See disclosures.md.",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("disclosure present: want Pass, got %v", out.Decision)
	}
}

func TestContentFilters_DisclosureCaseInsensitive(t *testing.T) {
	a, tmp, _ := filterFixture(t, `content_filters:
  - content_pattern: "(?i)bid on contract"
    required: "See Disclosures"
`)
	grantGroundTruthTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "proposal.md"),
		// Disclosure in different case should still satisfy.
		Content: "Bid on contract A-100. see disclosures.md.",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("case-insensitive disclosure: want Pass, got %v", out.Decision)
	}
}

func TestPathFilters_RejectsMalformedYAML(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", xdg)

	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gtDir, "filters.yaml"), []byte(`path_filters:
  - path_pattern: "[invalid regex("
    forbidden_values: ["x"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := groundtruth.New()
	err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp})
	if err == nil {
		t.Error("expected regex-compile error")
	}
	if !strings.Contains(err.Error(), "path_pattern") {
		t.Errorf("error should cite path_pattern: %v", err)
	}
}

// writeOverrideToken drops a JSON token mirroring what `leonard
// override --once` writes.
//
// v1.0 (bughunt-11 F1): token lives at $XDG_CONFIG_HOME/leonard/
// pending-override/, not under .leonard/. The enclosing test
// fixtures set XDG_CONFIG_HOME to a tempdir so this writes to
// test-isolated state.
func writeOverrideToken(t *testing.T, projectRoot, rel, reason string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	tokenPath, err := config.PendingTokenPath("override", resolved, rel)
	if err != nil {
		t.Fatalf("PendingTokenPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte(`{"file_path":"` + rel + `","reason":"` + reason + `","ts":"` + nowRFC3339() + `"}`)
	if err := os.WriteFile(tokenPath, body, 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
}
