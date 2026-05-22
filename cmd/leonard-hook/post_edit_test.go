package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/hooks"
)

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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".leonard"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(root)
	orig, _ := os.Getwd()
	if err := os.Chdir(resolved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

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
	rootCmd.SetArgs([]string{"post-edit"})
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
	if !resp.Continue {
		t.Errorf("Continue should be true on a degraded-mode post-edit")
	}
}
