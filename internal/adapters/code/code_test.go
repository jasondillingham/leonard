package code_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/code"
)

// newDegradedAdapter constructs a CodeAdapter and Init's it against a
// tempdir that has NO .leonard/leonard.db — exercising the "fresh
// checkout, leonard init hasn't run yet" path that the production
// hook binaries hit on every new project.
func newDegradedAdapter(t *testing.T) (adapters.Adapter, *bytes.Buffer) {
	t.Helper()
	tmp := t.TempDir()
	stderr := &bytes.Buffer{}
	a := code.New()
	if err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Stderr:      stderr,
	}); err != nil {
		t.Fatalf("Init in degraded mode: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, stderr
}

func TestNew_ReturnsUninitializedAdapter(t *testing.T) {
	a := code.New()
	if a == nil {
		t.Fatal("New returned nil")
	}
	if a.Name() != code.Name {
		t.Errorf("Name: want %q, got %q", code.Name, a.Name())
	}
}

func TestInit_RejectsEmptyProjectRoot(t *testing.T) {
	a := code.New()
	err := a.Init(context.Background(), adapters.Config{})
	if err == nil {
		t.Fatal("expected error when ProjectRoot is empty")
	}
}

func TestInit_DegradedModeWhenNoDB(t *testing.T) {
	a, _ := newDegradedAdapter(t)
	_ = a // smoke: Init returned without error against a bare tempdir
}

func TestClose_Idempotent(t *testing.T) {
	a, _ := newDegradedAdapter(t)
	for i := 0; i < 3; i++ {
		if err := a.Close(); err != nil {
			t.Fatalf("Close call %d: %v", i+1, err)
		}
	}
}

func TestPreEdit_PassesNonGoFile(t *testing.T) {
	a, _ := newDegradedAdapter(t)
	got, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		SessionID: "s1",
		Tool:      "Edit",
		FilePath:  filepath.Join(t.TempDir(), "README.md"),
		Content:   "# hi\nthis is markdown",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if got.Decision != adapters.Pass {
		t.Errorf("non-Go file: want Pass, got %v (reason=%q)", got.Decision, got.Reason)
	}
}

func TestPreEdit_PassesInDegradedMode(t *testing.T) {
	a, _ := newDegradedAdapter(t)
	// Even .go content passes in degraded mode because the permissive
	// store reports every symbol as present.
	got, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		SessionID: "s1",
		Tool:      "Edit",
		FilePath:  filepath.Join(t.TempDir(), "main.go"),
		Content:   "package main\n\nfunc main() {\n\tFoo()\n}\n",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if got.Decision != adapters.Pass {
		t.Errorf("degraded mode: want Pass, got %v (reason=%q)", got.Decision, got.Reason)
	}
}

func TestPreEdit_BlocksLeonardSelfEdit(t *testing.T) {
	a, _ := newDegradedAdapter(t)
	// Security review #2 F1: edits under .leonard/ are blocked
	// regardless of file type. Confirms CodeAdapter routes the request
	// through the existing guard (the deny path returns Deny here).
	tmp := t.TempDir()
	got, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		SessionID: "s1",
		Tool:      "Edit",
		FilePath:  filepath.Join(tmp, ".leonard", "config.toml"),
		Content:   "[hooks]\n",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if got.Decision != adapters.Deny {
		t.Errorf(".leonard/ edit: want Deny, got %v", got.Decision)
	}
	if got.Reason == "" {
		t.Error("Deny reason is empty; the .leonard/ guard should cite the rule")
	}
	if got.AdapterName != code.Name {
		t.Errorf("AdapterName: want %q, got %q", code.Name, got.AdapterName)
	}
}

func TestPostEdit_NoOpInDegradedMode(t *testing.T) {
	a, stderr := newDegradedAdapter(t)
	got, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "s1",
		Tool:      "Edit",
		FilePath:  filepath.Join(t.TempDir(), "main.go"),
	})
	if err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if got.AdditionalContext != "" || got.SystemMessage != "" || len(got.Claims) != 0 {
		t.Errorf("degraded mode should emit empty result, got %+v", got)
	}
	if !strings.Contains(stderr.String(), "leonard init") {
		t.Errorf("expected operator hint to mention `leonard init`, got %q", stderr.String())
	}
}

func TestSessionStart_NoOpInDegradedMode(t *testing.T) {
	a, _ := newDegradedAdapter(t)
	got, err := a.SessionStart(context.Background(), adapters.SessionStartPayload{
		SessionID: "s1",
		Source:    "startup",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if got.AdditionalContext != "" {
		t.Errorf("degraded mode should emit no context, got %q", got.AdditionalContext)
	}
}

func TestStop_NoOpInDegradedMode(t *testing.T) {
	a, _ := newDegradedAdapter(t)
	got, err := a.Stop(context.Background(), adapters.StopPayload{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got.SystemMessage != "" {
		t.Errorf("degraded mode should emit empty message, got %q", got.SystemMessage)
	}
}

func TestRegisterTools_NoOpInDegradedMode(t *testing.T) {
	a, _ := newDegradedAdapter(t)
	// nil server is acceptable in degraded mode because RegisterTools
	// short-circuits before touching it. If the short-circuit ever
	// regresses this test will panic on the nil deref, surfacing the
	// regression cleanly.
	if err := a.RegisterTools(nil); err != nil {
		t.Errorf("RegisterTools in degraded mode: want nil error, got %v", err)
	}
}

func TestRegistry_CodeAdapterIsRegistered(t *testing.T) {
	// The init() in register.go calls adapters.Register("code", New).
	// Importing this package (which the test does implicitly via the
	// `code` import) is enough to register. Confirms a fresh New()
	// from the registry returns a CodeAdapter shape.
	got, err := adapters.New(code.Name)
	if err != nil {
		t.Fatalf("adapters.New(%q): %v", code.Name, err)
	}
	if got.Name() != code.Name {
		t.Errorf("registry-returned adapter Name: want %q, got %q", code.Name, got.Name())
	}
}

func TestParseVerifyTimeout_Defaults(t *testing.T) {
	// Indirect test of the helper via a configured-verifier PostEdit
	// would require a real store; the helper's behavior is otherwise
	// identical to the version in cmd/leonard-hook/post_edit.go which
	// has its own tests. Confirming the helper exists by exercising
	// it via the public surface would tighten coupling we don't need
	// right now.
	t.Skip("covered by cmd/leonard-hook/post_edit_test.go's parseVerifyTimeout tests; equivalent logic mirrored in code adapter")
}
