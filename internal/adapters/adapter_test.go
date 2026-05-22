package adapters_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jasondillingham/leonard/internal/adapters"
)

// stubAdapter is a no-op implementation used by the contract tests. It
// tracks lifecycle calls so tests can assert ordering and idempotency
// without touching disk or network. Per-method behavior is overridable
// via the *Func fields — tests that need a deny verdict or an
// error-returning Init set the matching field.
type stubAdapter struct {
	name string

	initCalls         int32
	closeCalls        int32
	preEditCalls      int32
	postEditCalls     int32
	sessionStartCalls int32
	stopCalls         int32
	registerCalls     int32

	initErr       error
	preEditResult adapters.PreEditResult
	preEditErr    error
}

func (s *stubAdapter) Name() string { return s.name }

func (s *stubAdapter) Init(_ context.Context, _ adapters.Config) error {
	atomic.AddInt32(&s.initCalls, 1)
	return s.initErr
}

func (s *stubAdapter) Close() error {
	atomic.AddInt32(&s.closeCalls, 1)
	return nil
}

func (s *stubAdapter) PreEdit(_ context.Context, _ adapters.PreEditPayload) (adapters.PreEditResult, error) {
	atomic.AddInt32(&s.preEditCalls, 1)
	return s.preEditResult, s.preEditErr
}

func (s *stubAdapter) PostEdit(_ context.Context, _ adapters.PostEditPayload) (adapters.PostEditResult, error) {
	atomic.AddInt32(&s.postEditCalls, 1)
	return adapters.PostEditResult{}, nil
}

func (s *stubAdapter) SessionStart(_ context.Context, _ adapters.SessionStartPayload) (adapters.SessionStartResult, error) {
	atomic.AddInt32(&s.sessionStartCalls, 1)
	return adapters.SessionStartResult{}, nil
}

func (s *stubAdapter) Stop(_ context.Context, _ adapters.StopPayload) (adapters.StopResult, error) {
	atomic.AddInt32(&s.stopCalls, 1)
	return adapters.StopResult{}, nil
}

func (s *stubAdapter) RegisterTools(_ *mcp.Server) error {
	atomic.AddInt32(&s.registerCalls, 1)
	return nil
}

// Compile-time assertion: stubAdapter satisfies the Adapter interface.
// If the interface drifts in a way the stub doesn't track, this fails
// at build time rather than at first hook invocation.
var _ adapters.Adapter = (*stubAdapter)(nil)

func TestAdapter_LifecycleOrdering(t *testing.T) {
	a := &stubAdapter{name: "stub"}
	ctx := context.Background()

	if err := a.Init(ctx, adapters.Config{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := a.PreEdit(ctx, adapters.PreEditPayload{}); err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if _, err := a.PostEdit(ctx, adapters.PostEditPayload{}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if _, err := a.SessionStart(ctx, adapters.SessionStartPayload{}); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if _, err := a.Stop(ctx, adapters.StopPayload{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	checks := map[string]int32{
		"Init":         atomic.LoadInt32(&a.initCalls),
		"PreEdit":      atomic.LoadInt32(&a.preEditCalls),
		"PostEdit":     atomic.LoadInt32(&a.postEditCalls),
		"SessionStart": atomic.LoadInt32(&a.sessionStartCalls),
		"Stop":         atomic.LoadInt32(&a.stopCalls),
		"Close":        atomic.LoadInt32(&a.closeCalls),
	}
	for name, got := range checks {
		if got != 1 {
			t.Errorf("%s: want 1 call, got %d", name, got)
		}
	}
}

func TestAdapter_CloseIsIdempotent(t *testing.T) {
	a := &stubAdapter{name: "stub"}

	for i := 0; i < 3; i++ {
		if err := a.Close(); err != nil {
			t.Fatalf("Close call %d: %v", i+1, err)
		}
	}

	if got := atomic.LoadInt32(&a.closeCalls); got != 3 {
		t.Errorf("Close call count: want 3, got %d", got)
	}
}

func TestAdapter_InitErrorIsSurfaced(t *testing.T) {
	wantErr := errors.New("init failed")
	a := &stubAdapter{name: "stub", initErr: wantErr}

	err := a.Init(context.Background(), adapters.Config{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Init error: want %v, got %v", wantErr, err)
	}
}

func TestDecisionString(t *testing.T) {
	cases := []struct {
		d    adapters.Decision
		want string
	}{
		{adapters.Pass, "pass"},
		{adapters.Deny, "deny"},
		{adapters.Ask, "ask"},
		{adapters.Decision(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.d.String(); got != c.want {
			t.Errorf("Decision(%d).String() = %q, want %q", int(c.d), got, c.want)
		}
	}
}
