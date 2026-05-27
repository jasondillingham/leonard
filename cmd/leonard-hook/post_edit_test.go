package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/hooks"
)

// runPostEdit is a test helper that fires the post-edit cobra command
// and returns the decoded HookResponse. args is appended after
// "post-edit" so callers can pass flags like "--verbose".
func runPostEdit(t *testing.T, root, resolved string, args ...string) hooks.HookResponse {
	t.Helper()
	envelope := hooks.PostToolUsePayload{
		SessionID:     "test-session",
		HookEventName: "PostToolUse",
		ToolName:      "Edit",
		ToolInput:     hooks.ToolInput{FilePath: filepath.Join(resolved, "x.go")},
		CWD:           resolved,
	}
	in, _ := json.Marshal(envelope)

	backend := newDefaultBackend()
	defer backend.Close()
	rootCmd := newRootCmd(backend)
	rootCmd.SetArgs(append([]string{"post-edit"}, args...))
	rootCmd.SetIn(bytes.NewReader(in))
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v\nstdout=%s", err, out.String())
	}
	var resp hooks.HookResponse
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, out.String())
	}
	return resp
}

// setupPostEditRoot creates a temp project root with a .leonard/ dir,
// chdirs into it, and registers a cleanup to chdir back.
func setupPostEditRoot(t *testing.T) (root, resolved string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".leonard"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, _ = filepath.EvalSymlinks(root)
	orig, _ := os.Getwd()
	if err := os.Chdir(resolved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return root, resolved
}

// TestPostEditCmd_RoutesThroughDispatcher smoke-tests the post-edit
// cobra wiring. Deep coverage of the index-refresh, verifier, and
// claim-ledger flows lives in internal/hooks/* and the code-adapter
// integration tests; here we only confirm the dispatcher path
// produces a well-formed response.
//
// Project has no DB (no `leonard init` run), so the code adapter
// degrades to a no-op. That's the right "fresh checkout" smoke
// scenario.
func TestPostEditCmd_RoutesThroughDispatcher(t *testing.T) {
	_, resolved := setupPostEditRoot(t)
	resp := runPostEdit(t, "", resolved)
	if !resp.Continue {
		t.Errorf("Continue should be true on a degraded-mode post-edit")
	}
}

// TestPostEditCmd_QuietByDefault verifies that a clean run (no DB, no
// failures) suppresses its systemMessage by default so per-edit status
// lines don't accumulate in the conversation surface.
func TestPostEditCmd_QuietByDefault(t *testing.T) {
	_, resolved := setupPostEditRoot(t)
	resp := runPostEdit(t, "", resolved)
	if !resp.SuppressOutput {
		t.Errorf("default (no --verbose): clean run should have suppressOutput=true, got false")
	}
}

// TestPostEditCmd_VerboseShowsSystemMessage verifies that --verbose
// restores the previous behavior: a systemMessage is emitted on every
// edit and suppressOutput is false.
func TestPostEditCmd_VerboseShowsSystemMessage(t *testing.T) {
	_, resolved := setupPostEditRoot(t)
	resp := runPostEdit(t, "", resolved, "--verbose")
	if resp.SuppressOutput {
		t.Errorf("--verbose: clean run should have suppressOutput=false, got true")
	}
}
