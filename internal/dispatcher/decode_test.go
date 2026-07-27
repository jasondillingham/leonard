package dispatcher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/dispatcher"
	"github.com/jasondillingham/leonard/internal/hooks"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// stubAdapter is a minimal Adapter whose PreEdit returns a caller-
// supplied error, so the dispatcher's error-classification branch can
// be exercised without a symbol store or truth files on disk.
type stubAdapter struct {
	name    string
	preErr  error
	preCall int
}

func (s *stubAdapter) Name() string                                { return s.name }
func (s *stubAdapter) Init(context.Context, adapters.Config) error { return nil }
func (s *stubAdapter) Close() error                                { return nil }

func (s *stubAdapter) PreEdit(context.Context, adapters.PreEditPayload) (adapters.PreEditResult, error) {
	s.preCall++
	return adapters.PreEditResult{}, s.preErr
}

func (s *stubAdapter) PostEdit(context.Context, adapters.PostEditPayload) (adapters.PostEditResult, error) {
	return adapters.PostEditResult{}, nil
}

func (s *stubAdapter) SessionStart(context.Context, adapters.SessionStartPayload) (adapters.SessionStartResult, error) {
	return adapters.SessionStartResult{}, nil
}

func (s *stubAdapter) Stop(context.Context, adapters.StopPayload) (adapters.StopResult, error) {
	return adapters.StopResult{}, nil
}

func (s *stubAdapter) RegisterTools(*mcpsdk.Server) error { return nil }

func preEditEnvelope(t *testing.T, path, content string) []byte {
	t.Helper()
	in, err := json.Marshal(hooks.PreToolUsePayload{
		SessionID:     "test-session",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput:     hooks.PreEditToolInput{FilePath: path, Content: content},
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return in
}

// TestHandlePreEdit_AdapterDecodeErrorDenies pins the fail-closed
// contract: an adapter returning hooks.ErrDecode is rejecting the
// payload, not malfunctioning. The dispatcher previously logged it and
// continued, which produced zero results and aggregated to Pass — so
// padding a Write past MaxSnippetBytes turned a deny into an allow.
func TestHandlePreEdit_AdapterDecodeErrorDenies(t *testing.T) {
	stub := &stubAdapter{
		name:   "code",
		preErr: fmt.Errorf("%w: Write content exceeds 1048576 bytes", hooks.ErrDecode),
	}
	loaded := &dispatcher.Loaded{Adapters: []adapters.Adapter{stub}}

	var out bytes.Buffer
	in := preEditEnvelope(t, "notes.txt", "irrelevant")
	if err := loaded.HandlePreEdit(context.Background(), bytes.NewReader(in), &out); err != nil {
		t.Fatalf("HandlePreEdit: %v", err)
	}

	var resp hooks.PreEditResponse
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, out.String())
	}
	if resp.HookSpecificOutput == nil {
		t.Fatalf("expected a deny verdict, got pass-through %+v", resp)
	}
	if got := resp.HookSpecificOutput.PermissionDecision; got != "deny" {
		t.Errorf("PermissionDecision: want %q, got %q", "deny", got)
	}
	reason := resp.HookSpecificOutput.PermissionDecisionReason
	if !strings.Contains(reason, "code") {
		t.Errorf("reason should attribute the deny to an adapter, got %q", reason)
	}
	// The raw error must stay on stderr: Reason is rendered to the model
	// in a Leonard-branded block, so echoing decode-error text there lets
	// payload bytes impersonate Leonard's ground-truth voice.
	if strings.Contains(reason, "1048576") {
		t.Errorf("reason must not echo raw decode-error detail, got %q", reason)
	}
}

// TestHandlePreEdit_TrailingContent pins a boundary that shifted when
// the entry points moved from json.Decoder (stops at the first complete
// value, ignores the rest) to Unmarshal (rejects trailing bytes).
// Trailing whitespace must stay benign — Claude Code newline-terminates
// its envelopes. A second concatenated envelope must be rejected: which
// envelope the guard inspected would otherwise be ambiguous, and the old
// behavior silently inspected the first while discarding the second.
// Commit 03c5641 made the same call for concatenated MCP frames.
func TestHandlePreEdit_TrailingContent(t *testing.T) {
	envelope := preEditEnvelope(t, "notes.txt", "hello")

	cases := []struct {
		name      string
		body      []byte
		wantError bool
	}{
		{"trailing newline", append(append([]byte{}, envelope...), '\n'), false},
		{"trailing whitespace", append(append([]byte{}, envelope...), []byte("\n\n \t\n")...), false},
		{"concatenated envelopes", append(append([]byte{}, envelope...), envelope...), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubAdapter{name: "code"}
			loaded := &dispatcher.Loaded{Adapters: []adapters.Adapter{stub}}

			var out bytes.Buffer
			err := loaded.HandlePreEdit(context.Background(), bytes.NewReader(tc.body), &out)
			if tc.wantError {
				if err == nil {
					t.Fatalf("want rejection, got nil (body=%q)", out.String())
				}
				if !errors.Is(err, hooks.ErrDecode) {
					t.Errorf("rejection must wrap hooks.ErrDecode, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("trailing whitespace must decode cleanly, got %v", err)
			}
			if stub.preCall != 1 {
				t.Errorf("adapter should run once, got %d", stub.preCall)
			}
		})
	}
}

// TestHandlePreEdit_AdapterSoftErrorStillPasses is the other half of
// the contract: a non-ErrDecode failure (malformed truth file, unusable
// adapter state) must stay advisory so one broken adapter cannot block
// every edit in the project.
func TestHandlePreEdit_AdapterSoftErrorStillPasses(t *testing.T) {
	stub := &stubAdapter{name: "ground-truth", preErr: errors.New("truth file unreadable")}
	loaded := &dispatcher.Loaded{Adapters: []adapters.Adapter{stub}}

	var out bytes.Buffer
	in := preEditEnvelope(t, "notes.txt", "hello")
	if err := loaded.HandlePreEdit(context.Background(), bytes.NewReader(in), &out); err != nil {
		t.Fatalf("HandlePreEdit: %v", err)
	}

	var resp hooks.PreEditResponse
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, out.String())
	}
	if resp.HookSpecificOutput != nil {
		t.Fatalf("soft adapter error must not deny, got %+v", resp.HookSpecificOutput)
	}
}

// TestDispatcherEntryPoints_RejectOversizeEnvelope pins the payload cap
// on every dispatcher entry point. These used json.NewDecoder(r).Decode
// with no bound, so hooks.MaxHookPayloadBytes was never enforced on the
// production path: a 200 MB payload drove ~2.45 GB peak RSS before
// anything rejected it. Errors must wrap hooks.ErrDecode so
// cmd/leonard-hook maps them to exit 2 rather than letting an
// uninspectable payload read as approval.
func TestDispatcherEntryPoints_RejectOversizeEnvelope(t *testing.T) {
	oversize := append(bytes.Repeat([]byte("x"), hooks.MaxHookPayloadBytes+1), '\n')

	entryPoints := map[string]func(*dispatcher.Loaded, context.Context, *bytes.Reader, *bytes.Buffer) error{
		"pre-edit": func(l *dispatcher.Loaded, ctx context.Context, r *bytes.Reader, w *bytes.Buffer) error {
			return l.HandlePreEdit(ctx, r, w)
		},
		"post-edit": func(l *dispatcher.Loaded, ctx context.Context, r *bytes.Reader, w *bytes.Buffer) error {
			return l.HandlePostEdit(ctx, r, w)
		},
		"session-start": func(l *dispatcher.Loaded, ctx context.Context, r *bytes.Reader, w *bytes.Buffer) error {
			return l.HandleSessionStart(ctx, r, w)
		},
		"stop": func(l *dispatcher.Loaded, ctx context.Context, r *bytes.Reader, w *bytes.Buffer) error {
			return l.HandleStop(ctx, r, w)
		},
	}

	for name, call := range entryPoints {
		t.Run(name, func(t *testing.T) {
			stub := &stubAdapter{name: "code"}
			loaded := &dispatcher.Loaded{Adapters: []adapters.Adapter{stub}}

			var out bytes.Buffer
			err := call(loaded, context.Background(), bytes.NewReader(oversize), &out)
			if err == nil {
				t.Fatalf("oversize envelope must error, got nil (body=%q)", out.String())
			}
			if !errors.Is(err, hooks.ErrDecode) {
				t.Errorf("error must wrap hooks.ErrDecode so exit 2 blocks the call, got %v", err)
			}
			if !strings.Contains(err.Error(), "exceeds") {
				t.Errorf("error should name the cap, got %v", err)
			}
			if stub.preCall != 0 {
				t.Errorf("adapters must not run on a rejected envelope, PreEdit called %d times", stub.preCall)
			}
		})
	}
}

// TestDispatcherEntryPoints_AcceptAtCap guards the boundary from the
// other side, so tightening the cap later cannot silently start
// rejecting payloads that are exactly at the documented limit.
func TestDispatcherEntryPoints_AcceptAtCap(t *testing.T) {
	body := preEditEnvelope(t, "notes.txt", strings.Repeat("y", 4096))
	if len(body) > hooks.MaxHookPayloadBytes {
		t.Fatalf("fixture unexpectedly exceeds the cap: %d", len(body))
	}

	stub := &stubAdapter{name: "code"}
	loaded := &dispatcher.Loaded{Adapters: []adapters.Adapter{stub}}

	var out bytes.Buffer
	if err := loaded.HandlePreEdit(context.Background(), bytes.NewReader(body), &out); err != nil {
		t.Fatalf("in-cap envelope must decode, got %v", err)
	}
	if stub.preCall != 1 {
		t.Errorf("adapter should run exactly once for an in-cap envelope, got %d", stub.preCall)
	}
}
