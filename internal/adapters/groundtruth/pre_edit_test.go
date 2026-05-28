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

// preEditFixture sets up a tempdir with the valid ground-truth tree
// and points UserConfigDir at the same tempdir's parent so trust-
// marker grants stay isolated per test.
func preEditFixture(t *testing.T) (*groundtruth.GroundTruthAdapter, string, *bytes.Buffer) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", xdg)

	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"facts.yaml", "stories.md", "do-not-claim.md", "filters.yaml"} {
		data, err := os.ReadFile(filepath.Join("testdata", "valid", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(gtDir, name), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	stderr := &bytes.Buffer{}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Stderr:      stderr,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter), tmp, stderr
}

func grantTrust(t *testing.T, projectRoot string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if err := config.WriteAdapterTrust(resolved, "ground-truth"); err != nil {
		t.Fatalf("WriteAdapterTrust: %v", err)
	}
}

func TestPreEdit_DeniesForbiddenWhenTrusted(t *testing.T) {
	a, tmp, _ := preEditFixture(t)
	grantTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "page.md"),
		Content:  "Our platform: ExampleSaaS supports HIPAA-compliant workflows.",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Fatalf("Decision: want Deny, got %v", out.Decision)
	}
	if !strings.Contains(out.Reason, "HIPAA") {
		t.Errorf("Reason should cite the rule text, got %q", out.Reason)
	}
	if out.AdapterName != "ground-truth" {
		t.Errorf("AdapterName: want %q, got %q", "ground-truth", out.AdapterName)
	}
}

func TestPreEdit_WarnsWhenUntrusted(t *testing.T) {
	a, tmp, stderr := preEditFixture(t)
	// No trust grant.

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "page.md"),
		Content:  "Our platform: ExampleSaaS supports HIPAA-compliant workflows.",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("untrusted should not Deny, got %v", out.Decision)
	}
	if !strings.Contains(stderr.String(), "config trust ground-truth") {
		t.Errorf("stderr should hint at the trust grant, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "HIPAA") {
		t.Errorf("stderr should cite the offending claim, got %q", stderr.String())
	}
}

func TestPreEdit_PassesContentWithoutForbidden(t *testing.T) {
	a, tmp, _ := preEditFixture(t)
	grantTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "page.md"),
		Content:  "Today we shipped a minor refactor with no claims to verify.",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("Decision: want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_PassesEmptyContent(t *testing.T) {
	a, _, _ := preEditFixture(t)
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool: "Write",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("empty content: want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_SkipsFilesOutsideProject(t *testing.T) {
	a, _, _ := preEditFixture(t)
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(t.TempDir(), "elsewhere.md"),
		Content:  "ExampleSaaS supports HIPAA-compliant workflows",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("outside-project edit: want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_SkipsFilesNotInVerifyTargets(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", xdg)

	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"facts.yaml", "do-not-claim.md", "filters.yaml", "stories.md"} {
		data, err := os.ReadFile(filepath.Join("testdata", "valid", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gtDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := groundtruth.New()
	// Restrict verify_targets to *.md only.
	if err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Raw:         map[string]any{"verify_targets": []any{"*.md"}},
	}); err != nil {
		t.Fatal(err)
	}
	gta := a.(*groundtruth.GroundTruthAdapter)
	resolved, _ := filepath.EvalSymlinks(tmp)
	if err := config.WriteAdapterTrust(resolved, "ground-truth"); err != nil {
		t.Fatal(err)
	}

	// .py file with forbidden text — should pass because *.py isn't
	// in verify_targets.
	out, err := gta.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "x.py"),
		Content:  "ExampleSaaS supports HIPAA-compliant workflows",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf(".py file should bypass guard, got %v", out.Decision)
	}

	// .md file with the same text — should Deny.
	out, err = gta.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "x.md"),
		Content:  "ExampleSaaS supports HIPAA-compliant workflows",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf(".md file should be Denied, got %v", out.Decision)
	}
}

func TestPreEdit_SkipsWhenNoRulesLoaded(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", xdg)
	tmp := t.TempDir()

	// Init against a project with NO ground-truth tree.
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatal(err)
	}
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(tmp, "x.md"),
		Content:  "anything",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("no rules → want Pass, got %v", out.Decision)
	}
}

func TestPreEdit_BashCommandStillChecked(t *testing.T) {
	a, tmp, _ := preEditFixture(t)
	grantTrust(t, tmp)

	// Bash commands have no FilePath but DO have Command + Content.
	// In our payload shape Content carries the substantive text the
	// guard inspects. A bash command containing the forbidden text
	// should still trip the guard.
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:    "Bash",
		Command: "echo 'ExampleSaaS supports HIPAA-compliant workflows'",
		Content: "echo 'ExampleSaaS supports HIPAA-compliant workflows'",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("Bash with forbidden text: want Deny, got %v", out.Decision)
	}
}

// TestPreEdit_BashRedirectionDeniesWhenContentEmpty covers bughunt-12
// F046: the real Bash tool payload sets Command only (Content is empty).
// The prior guard short-circuited on empty Content before scanning,
// letting `echo "forbidden" > file.txt` bypass every ground-truth rule.
func TestPreEdit_BashRedirectionDeniesWhenContentEmpty(t *testing.T) {
	a, tmp, _ := preEditFixture(t)
	grantTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:    "Bash",
		Command: `echo "ExampleSaaS supports HIPAA-compliant workflows" > page.md`,
		// Content intentionally empty — mirrors the real Bash tool payload.
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("Bash redirection with forbidden text: want Deny, got %v (reason=%q)", out.Decision, out.Reason)
	}
}

func TestPreEdit_BashSedMutationDeniesWhenContentEmpty(t *testing.T) {
	a, tmp, _ := preEditFixture(t)
	grantTrust(t, tmp)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:    "Bash",
		Command: `sed -i 's/old text/ExampleSaaS supports HIPAA-compliant workflows/' page.md`,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("Bash sed -i with forbidden text: want Deny, got %v (reason=%q)", out.Decision, out.Reason)
	}
}

func TestPreEdit_BashReadOnlyCommandPasses(t *testing.T) {
	a, tmp, _ := preEditFixture(t)
	grantTrust(t, tmp)

	// cat / grep / ls — no redirection, no mutation: should pass even
	// if the command text contains a forbidden phrase.
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:    "Bash",
		Command: `grep "HIPAA" page.md`,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("read-only Bash: want Pass, got %v", out.Decision)
	}
}
