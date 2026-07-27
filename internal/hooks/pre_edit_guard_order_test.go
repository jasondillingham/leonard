package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestPreEdit_LeonardGuardRunsBeforeSizeCheck pins the ordering inside
// decidePreEdit. validateToolInputSizes used to run first, so an
// oversize payload returned ErrDecode before the path was ever
// inspected. Combined with the dispatcher treating that error as a soft
// failure, padding a Write to `.leonard/config.toml` past
// MaxSnippetBytes flipped the verdict from deny to allow — a bypass of
// the trust boundary the guard exists to hold.
//
// The guard must fire on payload shape alone, at any size.
func TestPreEdit_LeonardGuardRunsBeforeSizeCheck(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"under the snippet cap", strings.Repeat("x", 64)},
		{"over the snippet cap", strings.Repeat("x", MaxSnippetBytes+100)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := PreToolUsePayload{
				SessionID:     "test-session",
				HookEventName: "PreToolUse",
				ToolName:      "Write",
				ToolInput: PreEditToolInput{
					FilePath: ".leonard/config.toml",
					Content:  tc.content,
				},
			}
			in, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}

			var out bytes.Buffer
			err = HandlePreEdit(context.Background(), PreEditOptions{Store: newFakeSymStore()},
				bytes.NewReader(in), &out)
			if err != nil {
				t.Fatalf("HandlePreEdit: %v", err)
			}

			var resp PreEditResponse
			if decErr := json.NewDecoder(&out).Decode(&resp); decErr != nil {
				t.Fatalf("decode response: %v\nbody=%s", decErr, out.String())
			}
			if resp.HookSpecificOutput == nil {
				t.Fatalf("write to .leonard/ must deny regardless of size, got %+v", resp)
			}
			if got := resp.HookSpecificOutput.PermissionDecision; got != "deny" {
				t.Errorf("PermissionDecision: want %q, got %q", "deny", got)
			}
			if reason := resp.HookSpecificOutput.PermissionDecisionReason; !strings.Contains(reason, ".leonard") {
				t.Errorf("deny reason should name the guarded path, got %q", reason)
			}
		})
	}
}

// TestPreEdit_SizeCheckStillRejectsNonLeonardPaths confirms moving the
// guard did not weaken the size cap for ordinary paths: an oversize
// snippet outside `.leonard/` must still surface as ErrDecode, which
// cmd/leonard-hook maps to the blocking exit 2.
func TestPreEdit_SizeCheckStillRejectsNonLeonardPaths(t *testing.T) {
	payload := PreToolUsePayload{
		SessionID:     "test-session",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput: PreEditToolInput{
			FilePath: "notes.txt",
			Content:  strings.Repeat("x", MaxSnippetBytes+100),
		},
	}
	in, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var out bytes.Buffer
	err = HandlePreEdit(context.Background(), PreEditOptions{Store: newFakeSymStore()},
		bytes.NewReader(in), &out)
	if err == nil {
		t.Fatalf("oversize snippet must error, got nil (body=%q)", out.String())
	}
	if !errors.Is(err, ErrDecode) {
		t.Errorf("error must wrap ErrDecode so exit 2 blocks the call, got %v", err)
	}
}
