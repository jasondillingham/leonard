package adapters

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// minimalAdapter is the smallest viable Adapter for registry tests. It
// lives in the registry_test.go file (package adapters, not
// adapters_test) so tests can call the unexported reset helper to
// isolate state across cases.
type minimalAdapter struct{ name string }

func (m *minimalAdapter) Name() string                                  { return m.name }
func (m *minimalAdapter) Init(context.Context, Config) error            { return nil }
func (m *minimalAdapter) Close() error                                  { return nil }
func (m *minimalAdapter) PreEdit(context.Context, PreEditPayload) (PreEditResult, error) {
	return PreEditResult{}, nil
}
func (m *minimalAdapter) PostEdit(context.Context, PostEditPayload) (PostEditResult, error) {
	return PostEditResult{}, nil
}
func (m *minimalAdapter) SessionStart(context.Context, SessionStartPayload) (SessionStartResult, error) {
	return SessionStartResult{}, nil
}
func (m *minimalAdapter) Stop(context.Context, StopPayload) (StopResult, error) {
	return StopResult{}, nil
}
func (m *minimalAdapter) RegisterTools(*mcp.Server) error { return nil }

func TestRegister_RoundTrip(t *testing.T) {
	reset()
	t.Cleanup(reset)

	Register("alpha", func() Adapter { return &minimalAdapter{name: "alpha"} })

	got, err := New("alpha")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.Name() != "alpha" {
		t.Errorf("Name: want %q, got %q", "alpha", got.Name())
	}
}

func TestRegister_DuplicatePanics(t *testing.T) {
	reset()
	t.Cleanup(reset)

	Register("dup", func() Adapter { return &minimalAdapter{name: "dup"} })

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("duplicate Register did not panic")
		}
	}()
	Register("dup", func() Adapter { return &minimalAdapter{name: "dup"} })
}

func TestRegister_EmptyNamePanics(t *testing.T) {
	reset()
	t.Cleanup(reset)

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("empty-name Register did not panic")
		}
	}()
	Register("", func() Adapter { return &minimalAdapter{} })
}

func TestRegister_NilFactoryPanics(t *testing.T) {
	reset()
	t.Cleanup(reset)

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("nil-factory Register did not panic")
		}
	}()
	Register("nilf", nil)
}

func TestNew_UnknownAdapter(t *testing.T) {
	reset()
	t.Cleanup(reset)

	_, err := New("nope")
	if !errors.Is(err, ErrUnknownAdapter) {
		t.Fatalf("New(unknown) error: want errors.Is(ErrUnknownAdapter), got %v", err)
	}
}

func TestNames_SortedAndIncludesAll(t *testing.T) {
	reset()
	t.Cleanup(reset)

	Register("zebra", func() Adapter { return &minimalAdapter{name: "zebra"} })
	Register("alpha", func() Adapter { return &minimalAdapter{name: "alpha"} })
	Register("mango", func() Adapter { return &minimalAdapter{name: "mango"} })

	got := Names()
	want := []string{"alpha", "mango", "zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Names: want %v, got %v", want, got)
	}
}

func TestNew_FreshInstancePerCall(t *testing.T) {
	reset()
	t.Cleanup(reset)

	calls := 0
	Register("counter", func() Adapter {
		calls++
		return &minimalAdapter{name: "counter"}
	})

	a, err := New("counter")
	if err != nil {
		t.Fatalf("New 1: %v", err)
	}
	b, err := New("counter")
	if err != nil {
		t.Fatalf("New 2: %v", err)
	}
	if a == b {
		t.Errorf("New returned the same instance twice; factory must build fresh")
	}
	if calls != 2 {
		t.Errorf("factory call count: want 2, got %d", calls)
	}
}
