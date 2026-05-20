//go:build !otel

package telemetry

import (
	"context"
	"testing"
)

// TestEnabled_FalseInDefaultBuild locks in the contract: the default
// `go build` produces the no-op variant. If this fails, somebody
// accidentally wired the OTel impl into the default build path.
func TestEnabled_FalseInDefaultBuild(t *testing.T) {
	if Enabled() {
		t.Error("Enabled() returned true in a default build")
	}
}

// TestSpan_NoOpReturnsCtxUnchanged confirms the no-op Span doesn't
// secretly mutate the context. Hot-path call sites rely on this:
// they pass ctx in, get the same (or compatible) ctx back, and
// never have to think about whether telemetry rewired anything.
func TestSpan_NoOpReturnsCtxUnchanged(t *testing.T) {
	type ctxKey struct{}
	in := context.WithValue(context.Background(), ctxKey{}, "marker")
	out, end := Span(in, "irrelevant")
	defer end()
	if out.Value(ctxKey{}) != "marker" {
		t.Error("no-op Span dropped context value")
	}
}

// TestInit_NoOpReturnsNoError pins the cheap-no-error contract:
// the default build never reads env vars, never errors, never
// allocates anything that requires shutdown.
func TestInit_NoOpReturnsNoError(t *testing.T) {
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Errorf("Init in no-op build returned error: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown returned error: %v", err)
	}
}
