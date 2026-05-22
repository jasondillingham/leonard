package selflog_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/selflog"
	"github.com/jasondillingham/leonard/internal/config"
)

// preEditFixture creates a tempdir + selflog adapter + XDG home
// pointed at a separate tempdir (so trust grants don't pollute the
// user's real config dir).
func preEditFixture(t *testing.T) (adapters.Adapter, string) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", xdg)

	tmp := t.TempDir()
	a := selflog.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, tmp
}

func grantSelfLogTrust(t *testing.T, projectRoot string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if err := config.WriteAdapterTrust(resolved, selflog.Name); err != nil {
		t.Fatalf("WriteAdapterTrust: %v", err)
	}
}

func TestPreEdit_DeniesRequireTierWhenTrusted(t *testing.T) {
	a, tmp := preEditFixture(t)
	grantSelfLogTrust(t, tmp)

	target := filepath.Join(tmp, ".leonard", "ground-truth", "do-not-claim.md")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: target,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("require-tier + trusted: want Deny, got %v", out.Decision)
	}
	if !strings.Contains(out.Reason, "rationale") {
		t.Errorf("Reason should mention rationale, got %q", out.Reason)
	}
	if out.AdapterName != selflog.Name {
		t.Errorf("AdapterName: want %q, got %q", selflog.Name, out.AdapterName)
	}
}

func TestPreEdit_WarnsRequireTierWhenUntrusted(t *testing.T) {
	a, tmp := preEditFixture(t)
	// No trust grant.
	target := filepath.Join(tmp, ".leonard", "ground-truth", "filters.yaml")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: target,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("require-tier + untrusted: want Pass (warn), got %v", out.Decision)
	}
}

func TestPreEdit_AllowsWarnTier(t *testing.T) {
	a, tmp := preEditFixture(t)
	grantSelfLogTrust(t, tmp)
	target := filepath.Join(tmp, ".leonard", "ground-truth", "facts.yaml")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: target,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("warn-tier: want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_AllowsSkipTier(t *testing.T) {
	a, tmp := preEditFixture(t)
	grantSelfLogTrust(t, tmp)
	target := filepath.Join(tmp, ".leonard", "ground-truth", "audit-log.md")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: target,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("skip-tier: want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_AllowsNonTruthFile(t *testing.T) {
	a, tmp := preEditFixture(t)
	grantSelfLogTrust(t, tmp)
	target := filepath.Join(tmp, "README.md")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: target,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("non-truth file: want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_TrivialBypassAllows(t *testing.T) {
	a, tmp := preEditFixture(t)
	grantSelfLogTrust(t, tmp)
	target := filepath.Join(tmp, ".leonard", "ground-truth", "do-not-claim.md")
	rel := filepath.Join(".leonard", "ground-truth", "do-not-claim.md")

	t.Setenv("LEONARD_TRUTH_TRIVIAL", rel)
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: target,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("trivial bypass: want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_ConfirmedFilesAllows(t *testing.T) {
	a, tmp := preEditFixture(t)
	grantSelfLogTrust(t, tmp)
	target := filepath.Join(tmp, ".leonard", "ground-truth", "do-not-claim.md")
	rel := filepath.Join(".leonard", "ground-truth", "do-not-claim.md")

	t.Setenv("LEONARD_TRUTH_CONFIRMED_FILES", rel)
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: target,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("confirmed-files bypass: want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_ConfirmedFilesNonMatchStillDenies(t *testing.T) {
	a, tmp := preEditFixture(t)
	grantSelfLogTrust(t, tmp)
	target := filepath.Join(tmp, ".leonard", "ground-truth", "do-not-claim.md")

	t.Setenv("LEONARD_TRUTH_CONFIRMED_FILES", "some/other/file.md")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: target,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("non-matching confirmed-files: want Deny, got %v", out.Decision)
	}
}

func TestPreEdit_OutsideProjectIsAllowed(t *testing.T) {
	a, _ := preEditFixture(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.md")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: outside,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("outside-project: want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_EmptyFilePath(t *testing.T) {
	a, _ := preEditFixture(t)
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool: "Bash",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("empty FilePath: want Pass, got %v", out.Decision)
	}
}
