package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jasondillingham/leonard/internal/store"
)

type fakeClaimsReader struct {
	mu    sync.Mutex
	calls []string
	rows  []store.Claim
	err   error
}

func (f *fakeClaimsReader) GetUnverifiedClaims(sessionID string) ([]store.Claim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sessionID)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func (f *fakeClaimsReader) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func encodeStop(t *testing.T, p StopPayload) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func TestHandleStop_SurfacesUnverifiedClaims(t *testing.T) {
	t.Parallel()
	reader := &fakeClaimsReader{
		rows: []store.Claim{
			{ID: 2, SessionID: "sess-1", Claim: "Refactored handler; tests pass.", RecordedAt: 200},
			{ID: 1, SessionID: "sess-1", Claim: "Renamed Foo to Bar.\nDouble-check call sites.", RecordedAt: 100},
		},
	}
	stdin := bytes.NewReader(encodeStop(t, StopPayload{
		SessionID:     "sess-1",
		HookEventName: "Stop",
		CWD:           "/tmp/proj",
	}))
	var stdout bytes.Buffer

	err := HandleStop(context.Background(), StopOptions{Claims: reader}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandleStop: %v", err)
	}

	calls := reader.Calls()
	if len(calls) != 1 || calls[0] != "sess-1" {
		t.Errorf("GetUnverifiedClaims sessionID forwarding: got %v, want [sess-1]", calls)
	}

	var resp StopResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if !resp.Continue {
		t.Error("Continue must be true — Stop hook is advisory")
	}
	if resp.HookSpecificOutput == nil {
		t.Fatal("HookSpecificOutput is nil — expected surfacing")
	}
	if resp.HookSpecificOutput.HookEventName != "Stop" {
		t.Errorf("hookEventName = %q", resp.HookSpecificOutput.HookEventName)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	if !strings.HasPrefix(ctx, "## Unverified claims (from Leonard)") {
		t.Errorf("surfaced context missing heading: %q", ctx)
	}
	if !strings.Contains(ctx, "Refactored handler") {
		t.Errorf("first claim missing from surfacing: %q", ctx)
	}
	if !strings.Contains(ctx, "Renamed Foo to Bar.") {
		t.Errorf("second claim missing from surfacing: %q", ctx)
	}
	// Second claim's reasoning is multi-line; only the first line should
	// surface — "Double-check call sites." is on line two and must not appear.
	if strings.Contains(ctx, "Double-check call sites") {
		t.Errorf("multi-line claim leaked beyond first line: %q", ctx)
	}
}

func TestHandleStop_NilReaderEmitsNoSurfacing(t *testing.T) {
	t.Parallel()
	stdin := bytes.NewReader(encodeStop(t, StopPayload{HookEventName: "Stop"}))
	var stdout bytes.Buffer

	if err := HandleStop(context.Background(), StopOptions{Claims: nil}, stdin, &stdout); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}

	var resp StopResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if !resp.Continue {
		t.Error("Continue should be true for nil reader")
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no surfacing, got %+v", resp.HookSpecificOutput)
	}
}

func TestHandleStop_EmptyClaimsEmitsNoSurfacing(t *testing.T) {
	t.Parallel()
	reader := &fakeClaimsReader{rows: nil}
	stdin := bytes.NewReader(encodeStop(t, StopPayload{HookEventName: "Stop", SessionID: "sess-2"}))
	var stdout bytes.Buffer

	if err := HandleStop(context.Background(), StopOptions{Claims: reader}, stdin, &stdout); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}

	var resp StopResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no surfacing for empty claims, got %+v", resp.HookSpecificOutput)
	}
	if !resp.Continue {
		t.Error("Continue should be true")
	}
	if calls := reader.Calls(); len(calls) != 1 || calls[0] != "sess-2" {
		t.Errorf("sessionID forwarding: got %v, want [sess-2]", calls)
	}
}

func TestHandleStop_ExplicitLimitTruncates(t *testing.T) {
	t.Parallel()
	reader := &fakeClaimsReader{
		rows: []store.Claim{
			{Claim: "a"}, {Claim: "b"}, {Claim: "c"}, {Claim: "d"}, {Claim: "e"},
		},
	}
	stdin := bytes.NewReader(encodeStop(t, StopPayload{HookEventName: "Stop"}))
	var stdout bytes.Buffer

	if err := HandleStop(context.Background(), StopOptions{Claims: reader, Limit: 2}, stdin, &stdout); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}
	var resp StopResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.HookSpecificOutput == nil {
		t.Fatal("expected surfacing")
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	if got := strings.Count(ctx, "\n- "); got != 2 {
		t.Errorf("expected exactly 2 bullets, got %d:\n%s", got, ctx)
	}
	if !strings.Contains(ctx, "- a") || !strings.Contains(ctx, "- b") {
		t.Errorf("first two claims missing:\n%s", ctx)
	}
	for _, dropped := range []string{"- c", "- d", "- e"} {
		if strings.Contains(ctx, dropped) {
			t.Errorf("limit not honored — found %q in: %s", dropped, ctx)
		}
	}
}

func TestHandleStop_ZeroLimitFallsBackToDefault(t *testing.T) {
	t.Parallel()
	rows := make([]store.Claim, 0, DefaultStopClaimLimit+5)
	for i := 0; i < DefaultStopClaimLimit+5; i++ {
		rows = append(rows, store.Claim{Claim: "claim"})
	}
	reader := &fakeClaimsReader{rows: rows}
	stdin := bytes.NewReader(encodeStop(t, StopPayload{HookEventName: "Stop"}))
	var stdout bytes.Buffer

	if err := HandleStop(context.Background(), StopOptions{Claims: reader, Limit: 0}, stdin, &stdout); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}
	var resp StopResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.HookSpecificOutput == nil {
		t.Fatal("expected surfacing")
	}
	if got := strings.Count(resp.HookSpecificOutput.AdditionalContext, "\n- "); got != DefaultStopClaimLimit {
		t.Errorf("expected %d bullets at default limit, got %d", DefaultStopClaimLimit, got)
	}
}

func TestHandleStop_NegativeLimitFallsBackToDefault(t *testing.T) {
	t.Parallel()
	rows := make([]store.Claim, 0, DefaultStopClaimLimit+3)
	for i := 0; i < DefaultStopClaimLimit+3; i++ {
		rows = append(rows, store.Claim{Claim: "claim"})
	}
	reader := &fakeClaimsReader{rows: rows}
	stdin := bytes.NewReader(encodeStop(t, StopPayload{HookEventName: "Stop"}))
	var stdout bytes.Buffer

	if err := HandleStop(context.Background(), StopOptions{Claims: reader, Limit: -7}, stdin, &stdout); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}
	var resp StopResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := strings.Count(resp.HookSpecificOutput.AdditionalContext, "\n- "); got != DefaultStopClaimLimit {
		t.Errorf("expected %d bullets at default limit, got %d", DefaultStopClaimLimit, got)
	}
}

func TestHandleStop_EmptyStdin(t *testing.T) {
	t.Parallel()
	err := HandleStop(context.Background(), StopOptions{}, bytes.NewReader(nil), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty payload error, got %v", err)
	}
}

func TestHandleStop_MalformedJSON(t *testing.T) {
	t.Parallel()
	err := HandleStop(context.Background(), StopOptions{},
		strings.NewReader("not json at all"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestHandleStop_ReaderErrorBubblesUp(t *testing.T) {
	t.Parallel()
	reader := &fakeClaimsReader{err: errors.New("db locked")}
	stdin := bytes.NewReader(encodeStop(t, StopPayload{HookEventName: "Stop"}))
	err := HandleStop(context.Background(), StopOptions{Claims: reader}, stdin, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "db locked") {
		t.Fatalf("expected reader error, got %v", err)
	}
}

func TestHandleStop_LongClaimTruncatedWithEllipsis(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", stopClaimPrefixMax*2)
	reader := &fakeClaimsReader{rows: []store.Claim{{Claim: long}}}
	stdin := bytes.NewReader(encodeStop(t, StopPayload{HookEventName: "Stop"}))
	var stdout bytes.Buffer

	if err := HandleStop(context.Background(), StopOptions{Claims: reader}, stdin, &stdout); err != nil {
		t.Fatalf("HandleStop: %v", err)
	}
	var resp StopResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "…") {
		t.Errorf("expected ellipsis on truncated long claim: %q", ctx)
	}
	// The bullet line itself (everything after "- " up to the next newline)
	// must not exceed the configured rune cap.
	for _, line := range strings.Split(ctx, "\n") {
		bullet, ok := strings.CutPrefix(line, "- ")
		if !ok {
			continue
		}
		if got := len([]rune(bullet)); got > stopClaimPrefixMax {
			t.Errorf("bullet exceeded cap: %d runes", got)
		}
	}
}
