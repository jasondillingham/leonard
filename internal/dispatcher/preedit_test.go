package dispatcher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
	"github.com/jasondillingham/leonard/internal/dispatcher"
	"github.com/jasondillingham/leonard/internal/hooks"
)

// TestHandlePreEdit_GroundTruthDenies covers the headline #46
// outcome: a forbidden-claim hit in the ground-truth adapter must
// surface as a deny in the wire response Claude Code parses.
func TestHandlePreEdit_GroundTruthDenies(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	dataDir := filepath.Join(root, ".leonard")
	gtDir := filepath.Join(dataDir, "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "tech_stack:\n  primary_language: Go\n",
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

	loaded, err := dispatcher.LoadEnabled(context.Background(), resolved, filepath.Join(resolved, ".leonard"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("LoadEnabled: %v", err)
	}
	defer loaded.Close()

	// Build a PreToolUse envelope where the proposed write content
	// triggers the forbidden rule.
	target := filepath.Join(resolved, "draft.md")
	envelope := hooks.PreToolUsePayload{
		SessionID:     "test-session",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput: hooks.PreEditToolInput{
			FilePath: target,
			Content:  "Our platform is HIPAA-compliant for healthcare customers.",
		},
		CWD: resolved,
	}
	in, _ := json.Marshal(envelope)

	var out bytes.Buffer
	if err := loaded.HandlePreEdit(context.Background(), bytes.NewReader(in), &out); err != nil {
		t.Fatalf("HandlePreEdit: %v", err)
	}

	var resp hooks.PreEditResponse
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, out.String())
	}
	if resp.HookSpecificOutput == nil {
		t.Fatalf("expected hookSpecificOutput on deny, got %+v", resp)
	}
	if resp.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("PermissionDecision: want %q, got %q", "deny", resp.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "HIPAA") {
		t.Errorf("reason should cite the rule, got %q", resp.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestHandlePreEdit_ContinueOnPass covers the happy path: no
// adapter denies → response is {"continue": true}.
func TestHandlePreEdit_ContinueOnPass(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	// Empty project — code adapter loads as the auto-detect fallback.
	dataDir := filepath.Join(root, ".leonard")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	loaded, err := dispatcher.LoadEnabled(context.Background(), root, dataDir, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("LoadEnabled: %v", err)
	}
	defer loaded.Close()

	envelope := hooks.PreToolUsePayload{
		SessionID:     "test-session",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput: hooks.PreEditToolInput{
			FilePath: filepath.Join(root, "fine.md"),
			Content:  "Some safe content.",
		},
		CWD: root,
	}
	in, _ := json.Marshal(envelope)

	var out bytes.Buffer
	if err := loaded.HandlePreEdit(context.Background(), bytes.NewReader(in), &out); err != nil {
		t.Fatalf("HandlePreEdit: %v", err)
	}

	var resp hooks.PreEditResponse
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("pass should not emit hookSpecificOutput, got %+v", resp.HookSpecificOutput)
	}
	if !resp.Continue {
		t.Errorf("pass should set Continue=true")
	}
}
