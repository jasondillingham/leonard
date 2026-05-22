package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
	"github.com/jasondillingham/leonard/internal/hooks"
)

// TestPreEditCmd_RoutesThroughDispatcher exercises the cobra
// subcommand wiring end-to-end with a ground-truth adapter
// configured to deny a forbidden claim. Confirms #46's pre-edit
// path actually reaches the adapters and surfaces their verdicts
// in the wire response.
//
// The deep-coverage tests for the adapter logic live in
// internal/adapters/{code,groundtruth}/; this file only checks
// that the leonard-hook binary's CLI wiring connects them
// correctly via internal/dispatcher.
func TestPreEditCmd_RoutesThroughDispatcher(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	dataDir := filepath.Join(root, ".leonard")
	gtDir := filepath.Join(dataDir, "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "x: 1\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolved, _ := filepath.EvalSymlinks(root)
	if err := config.WriteAdapterTrust(resolved, "ground-truth"); err != nil {
		t.Fatalf("WriteAdapterTrust: %v", err)
	}

	// Chdir so resolveProjectRoot uses our tempdir.
	orig, _ := os.Getwd()
	if err := os.Chdir(resolved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	envelope := hooks.PreToolUsePayload{
		SessionID:     "test-session",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput: hooks.PreEditToolInput{
			FilePath: filepath.Join(resolved, "draft.md"),
			Content:  "We are HIPAA-compliant for healthcare.",
		},
		CWD: resolved,
	}
	in, _ := json.Marshal(envelope)

	backend := newDefaultBackend()
	defer backend.Close()
	rootCmd := newRootCmd(backend)
	rootCmd.SetArgs([]string{"pre-edit"})
	rootCmd.SetIn(bytes.NewReader(in))
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})

	// The pre-edit RunE returns a "blockOnDecode" error on deny so
	// main exits non-zero — that's expected here. The wire response
	// body on stdout is what we assert against.
	_ = rootCmd.Execute()

	var resp hooks.PreEditResponse
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, out.String())
	}
	if resp.HookSpecificOutput == nil {
		t.Fatalf("expected hookSpecificOutput on deny, got %+v", resp)
	}
	if resp.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("PermissionDecision: want deny, got %q", resp.HookSpecificOutput.PermissionDecision)
	}
}
