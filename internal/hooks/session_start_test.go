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

type fakeDecisionReader struct {
	mu     sync.Mutex
	calls  []decisionCall
	rows   []store.Decision
	err    error
}

type decisionCall struct {
	Topic string
	Since int64
	Limit int
}

func (f *fakeDecisionReader) GetDecisions(topic string, since int64, limit int) ([]store.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, decisionCall{Topic: topic, Since: since, Limit: limit})
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func (f *fakeDecisionReader) Calls() []decisionCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]decisionCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func encodeSessionStart(t *testing.T, p SessionStartPayload) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func TestHandleSessionStart_InjectsDecisions(t *testing.T) {
	t.Parallel()
	reader := &fakeDecisionReader{
		rows: []store.Decision{
			{ID: 2, Topic: "toml lib", Choice: "pelletier/go-toml/v2", Reasoning: "actively maintained; zero-alloc decode", RecordedAt: 200},
			{ID: 1, Topic: "store", Choice: "modernc.org/sqlite", Reasoning: "pure-Go,\nno CGo", RecordedAt: 100},
		},
	}
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{
		SessionID:     "sess-1",
		HookEventName: "SessionStart",
		Source:        "startup",
		CWD:           "/tmp/proj",
	}))
	var stdout bytes.Buffer

	err := HandleSessionStart(context.Background(), SessionStartOptions{
		Decisions: reader,
	}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandleSessionStart: %v", err)
	}

	calls := reader.Calls()
	if len(calls) != 1 {
		t.Fatalf("GetDecisions calls = %d, want 1", len(calls))
	}
	if calls[0].Topic != "" || calls[0].Since != 0 || calls[0].Limit != DefaultDecisionsLimit {
		t.Errorf("GetDecisions called with %+v, want topic=\"\" since=0 limit=%d", calls[0], DefaultDecisionsLimit)
	}

	var resp SessionStartResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if !resp.Continue {
		t.Error("Continue should be true")
	}
	if resp.HookSpecificOutput == nil {
		t.Fatal("HookSpecificOutput is nil — expected injection")
	}
	if resp.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q", resp.HookSpecificOutput.HookEventName)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	if !strings.HasPrefix(ctx, "## Prior decisions (from Leonard)") {
		t.Errorf("injected context missing heading: %q", ctx)
	}
	if !strings.Contains(ctx, "toml lib") || !strings.Contains(ctx, "pelletier/go-toml/v2") {
		t.Errorf("first decision missing from injection: %q", ctx)
	}
	if !strings.Contains(ctx, "store") || !strings.Contains(ctx, "modernc.org/sqlite") {
		t.Errorf("second decision missing from injection: %q", ctx)
	}
	// Reasoning should be flattened to a single line.
	if strings.Contains(ctx, "no CGo") && strings.Contains(ctx[strings.Index(ctx, "modernc"):], "\nno CGo") {
		t.Errorf("multi-line reasoning leaked through unflattened: %q", ctx)
	}
}

func TestHandleSessionStart_NilReaderEmitsNoInjection(t *testing.T) {
	t.Parallel()
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{
		HookEventName: "SessionStart",
		Source:        "startup",
	}))
	var stdout bytes.Buffer

	err := HandleSessionStart(context.Background(), SessionStartOptions{Decisions: nil}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandleSessionStart: %v", err)
	}

	var resp SessionStartResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if !resp.Continue {
		t.Error("Continue should be true even for no-op response")
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no injection, got %+v", resp.HookSpecificOutput)
	}
}

func TestHandleSessionStart_EmptyDecisionsEmitsNoInjection(t *testing.T) {
	t.Parallel()
	reader := &fakeDecisionReader{rows: nil}
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{HookEventName: "SessionStart"}))
	var stdout bytes.Buffer

	if err := HandleSessionStart(context.Background(), SessionStartOptions{Decisions: reader}, stdin, &stdout); err != nil {
		t.Fatalf("HandleSessionStart: %v", err)
	}

	var resp SessionStartResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no injection for empty store, got %+v", resp.HookSpecificOutput)
	}
	if calls := reader.Calls(); len(calls) != 1 || calls[0].Limit != DefaultDecisionsLimit {
		t.Errorf("calls = %+v, want one call with default limit", calls)
	}
}

func TestHandleSessionStart_ExplicitLimitForwarded(t *testing.T) {
	t.Parallel()
	reader := &fakeDecisionReader{rows: []store.Decision{{Topic: "t", Choice: "c"}}}
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{HookEventName: "SessionStart"}))
	var stdout bytes.Buffer

	err := HandleSessionStart(context.Background(), SessionStartOptions{Decisions: reader, Limit: 3}, stdin, &stdout)
	if err != nil {
		t.Fatalf("HandleSessionStart: %v", err)
	}
	calls := reader.Calls()
	if len(calls) != 1 || calls[0].Limit != 3 {
		t.Errorf("limit = %+v, want 3", calls)
	}
}

func TestHandleSessionStart_ZeroLimitFallsBackToDefault(t *testing.T) {
	t.Parallel()
	reader := &fakeDecisionReader{rows: nil}
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{HookEventName: "SessionStart"}))
	var stdout bytes.Buffer

	if err := HandleSessionStart(context.Background(), SessionStartOptions{Decisions: reader, Limit: 0}, stdin, &stdout); err != nil {
		t.Fatalf("HandleSessionStart: %v", err)
	}
	calls := reader.Calls()
	if len(calls) != 1 || calls[0].Limit != DefaultDecisionsLimit {
		t.Errorf("limit = %+v, want default %d", calls, DefaultDecisionsLimit)
	}
}

func TestHandleSessionStart_NegativeLimitFallsBackToDefault(t *testing.T) {
	t.Parallel()
	reader := &fakeDecisionReader{}
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{HookEventName: "SessionStart"}))
	var stdout bytes.Buffer

	if err := HandleSessionStart(context.Background(), SessionStartOptions{Decisions: reader, Limit: -5}, stdin, &stdout); err != nil {
		t.Fatalf("HandleSessionStart: %v", err)
	}
	if calls := reader.Calls(); len(calls) != 1 || calls[0].Limit != DefaultDecisionsLimit {
		t.Errorf("limit = %+v, want default %d", calls, DefaultDecisionsLimit)
	}
}

func TestHandleSessionStart_EmptyStdin(t *testing.T) {
	t.Parallel()
	err := HandleSessionStart(context.Background(), SessionStartOptions{}, bytes.NewReader(nil), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty payload error, got %v", err)
	}
}

func TestHandleSessionStart_MalformedJSON(t *testing.T) {
	t.Parallel()
	err := HandleSessionStart(context.Background(), SessionStartOptions{},
		strings.NewReader("not json at all"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestHandleSessionStart_ReaderErrorBubblesUp(t *testing.T) {
	t.Parallel()
	reader := &fakeDecisionReader{err: errors.New("db locked")}
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{HookEventName: "SessionStart"}))
	err := HandleSessionStart(context.Background(), SessionStartOptions{Decisions: reader}, stdin, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "db locked") {
		t.Fatalf("expected reader error, got %v", err)
	}
}

func TestHandleSessionStart_SkipsInjectionForCompactSource(t *testing.T) {
	t.Parallel()
	reader := &fakeDecisionReader{
		rows: []store.Decision{{Topic: "t", Choice: "c", Reasoning: "r"}},
	}
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{
		HookEventName: "SessionStart",
		Source:        "compact",
	}))
	var stdout bytes.Buffer

	if err := HandleSessionStart(context.Background(), SessionStartOptions{Decisions: reader}, stdin, &stdout); err != nil {
		t.Fatalf("HandleSessionStart: %v", err)
	}

	var resp SessionStartResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if !resp.Continue {
		t.Error("Continue should be true on skip-injection path")
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no injection on source=compact, got %+v", resp.HookSpecificOutput)
	}
	if calls := reader.Calls(); len(calls) != 0 {
		t.Errorf("expected zero GetDecisions calls on source=compact, got %d (%+v)", len(calls), calls)
	}
}

func TestHandleSessionStart_SkipsInjectionForClearSource(t *testing.T) {
	t.Parallel()
	reader := &fakeDecisionReader{
		rows: []store.Decision{{Topic: "t", Choice: "c", Reasoning: "r"}},
	}
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{
		HookEventName: "SessionStart",
		Source:        "clear",
	}))
	var stdout bytes.Buffer

	if err := HandleSessionStart(context.Background(), SessionStartOptions{Decisions: reader}, stdin, &stdout); err != nil {
		t.Fatalf("HandleSessionStart: %v", err)
	}

	var resp SessionStartResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if !resp.Continue {
		t.Error("Continue should be true on skip-injection path")
	}
	if resp.HookSpecificOutput != nil {
		t.Errorf("expected no injection on source=clear, got %+v", resp.HookSpecificOutput)
	}
	if calls := reader.Calls(); len(calls) != 0 {
		t.Errorf("expected zero GetDecisions calls on source=clear, got %d (%+v)", len(calls), calls)
	}
}

func TestHandleSessionStart_InjectsForResumeSource(t *testing.T) {
	t.Parallel()
	reader := &fakeDecisionReader{
		rows: []store.Decision{{Topic: "t", Choice: "c", Reasoning: "r"}},
	}
	stdin := bytes.NewReader(encodeSessionStart(t, SessionStartPayload{
		HookEventName: "SessionStart",
		Source:        "resume",
	}))
	var stdout bytes.Buffer

	if err := HandleSessionStart(context.Background(), SessionStartOptions{Decisions: reader}, stdin, &stdout); err != nil {
		t.Fatalf("HandleSessionStart: %v", err)
	}

	var resp SessionStartResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nstdout=%q", err, stdout.String())
	}
	if resp.HookSpecificOutput == nil {
		t.Fatal("expected injection on source=resume, got nil HookSpecificOutput")
	}
	if !strings.Contains(resp.HookSpecificOutput.AdditionalContext, "**t**") {
		t.Errorf("injected context missing decision: %q", resp.HookSpecificOutput.AdditionalContext)
	}
}

func TestFormatDecisions_OmitsEmptyReasoning(t *testing.T) {
	t.Parallel()
	out := formatDecisions([]store.Decision{
		{Topic: "naming", Choice: "snake_case", Reasoning: ""},
	})
	if !strings.Contains(out, "- **naming** → snake_case") {
		t.Errorf("bullet line missing or malformed: %q", out)
	}
	if strings.Contains(out, " — ") {
		t.Errorf("expected no em-dash separator without reasoning: %q", out)
	}
}
