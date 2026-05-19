package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jasondillingham/leonard/internal/hooks"
)

func TestExitCodeFor_NilIsZero(t *testing.T) {
	t.Parallel()
	if got := exitCodeFor(nil); got != 0 {
		t.Errorf("nil → %d, want 0", got)
	}
}

func TestExitCodeFor_PlainErrorIsOne(t *testing.T) {
	t.Parallel()
	// Per the Claude Code hook contract, exit 1 is a non-blocking error —
	// the right default for "Leonard problem, but the action is fine".
	if got := exitCodeFor(errors.New("disk on fire")); got != 1 {
		t.Errorf("plain error → %d, want 1", got)
	}
}

func TestBlockOnDecode_WrapsDecodeErrorsAsExitTwo(t *testing.T) {
	t.Parallel()
	// A handler error that wraps hooks.ErrDecode must end up with exit 2
	// after blockOnDecode — exit 2 is the only code Claude Code treats as
	// blocking, and a malformed payload to the fabrication guard must be
	// blocking (otherwise garbage-in is a free bypass).
	wrapped := fmt.Errorf("pre-edit: %w", fmt.Errorf("%w: garbled bytes", hooks.ErrDecode))
	mapped := blockOnDecode(wrapped)
	if got := exitCodeFor(mapped); got != 2 {
		t.Errorf("decode-wrapped error → %d, want 2 (block)", got)
	}
	// And the original error is preserved on the chain for diagnostics.
	if !errors.Is(mapped, hooks.ErrDecode) {
		t.Error("blockOnDecode should preserve ErrDecode on the error chain")
	}
}

func TestBlockOnDecode_PassesNonDecodeErrorsThrough(t *testing.T) {
	t.Parallel()
	plain := errors.New("disk on fire")
	mapped := blockOnDecode(plain)
	if mapped != plain {
		t.Errorf("non-decode error should pass through unchanged, got %v", mapped)
	}
	if got := exitCodeFor(mapped); got != 1 {
		t.Errorf("non-decode error exit = %d, want 1", got)
	}
}

func TestBlockOnDecode_NilStaysNil(t *testing.T) {
	t.Parallel()
	if got := blockOnDecode(nil); got != nil {
		t.Errorf("nil should stay nil, got %v", got)
	}
}
