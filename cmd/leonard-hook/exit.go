package main

import (
	"errors"

	"github.com/jasondillingham/leonard/internal/hooks"
)

// exitErr is returned by a cobra RunE when the binary should exit with a
// non-default status. main.go inspects it after Execute() and maps it to the
// process exit code. Wrapping (rather than calling os.Exit directly inside the
// command) keeps the RunE testable.
type exitErr struct {
	err  error
	code int
}

func (e *exitErr) Error() string { return e.err.Error() }
func (e *exitErr) Unwrap() error { return e.err }

// blockOnDecode wraps a handler error so the cobra layer signals "block this
// tool call" (exit 2) to Claude Code when the payload was un-decodable. Per
// the documented hook contract: exit 1 = non-blocking, exit 2 = block. The
// pre-edit and post-edit guards must use exit 2 for decode failures —
// otherwise a malformed payload would let the would-be-fabricated edit
// through. All other errors keep the default exit-1 behavior.
func blockOnDecode(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, hooks.ErrDecode) {
		return &exitErr{err: err, code: 2}
	}
	return err
}

// exitCodeFor reports the desired process exit code for err. Returns 0 for
// nil. Returns the embedded code for an *exitErr. Returns 1 otherwise — the
// "non-blocking error" code per the Claude Code hook contract.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitErr
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}
