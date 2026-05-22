package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/hooks"
)

// TestStopCmd_RoutesThroughDispatcher smoke-tests the stop cobra
// wiring. Deep adapter coverage lives in internal/adapters/*.
func TestStopCmd_RoutesThroughDispatcher(t *testing.T) {
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

	envelope := hooks.StopPayload{
		SessionID:     "test-session",
		HookEventName: "Stop",
		CWD:           resolved,
	}
	in, _ := json.Marshal(envelope)

	backend := newDefaultBackend()
	defer backend.Close()
	rootCmd := newRootCmd(backend)
	rootCmd.SetArgs([]string{"stop"})
	rootCmd.SetIn(bytes.NewReader(in))
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute: %v\nstdout=%s", err, out.String())
	}

	var resp hooks.StopResponse
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, out.String())
	}
	if !resp.Continue {
		t.Errorf("Continue should be true on Stop")
	}
}
