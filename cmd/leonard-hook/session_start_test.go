package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/hooks"
)

// TestSessionStartCmd_CompactShortCircuits confirms the dispatcher
// short-circuits "compact" / "clear" sources without running
// adapters. Deep adapter coverage lives in internal/adapters/*.
func TestSessionStartCmd_CompactShortCircuits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".leonard"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	resolved, _ := filepath.EvalSymlinks(root)
	if err := os.Chdir(resolved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	envelope := hooks.SessionStartPayload{
		SessionID:     "test-session",
		HookEventName: "SessionStart",
		Source:        "compact",
		CWD:           resolved,
	}
	in, _ := json.Marshal(envelope)

	backend := newDefaultBackend()
	defer backend.Close()
	rootCmd := newRootCmd(backend)
	rootCmd.SetArgs([]string{"session-start"})
	rootCmd.SetIn(bytes.NewReader(in))
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var resp hooks.SessionStartResponse
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, out.String())
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("compact source should short-circuit; got %+v", resp.HookSpecificOutput)
	}
	if !resp.Continue {
		t.Errorf("Continue should be true on compact")
	}
}
