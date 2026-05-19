package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
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

// TestOSExitConfinedToMain guards the exit-code contract from regression.
// Per the Claude Code hook contract, exit 1 = non-blocking and exit 2 =
// blocking. The whole fabrication guard is load-bearing on this distinction,
// so all process exits in the hook tree route through exitCodeFor in main.go.
// A stray os.Exit anywhere else would silently break the contract — this test
// fails the build instead.
func TestOSExitConfinedToMain(t *testing.T) {
	t.Parallel()
	// roots are relative to the test's package directory (cmd/leonard-hook).
	roots := []string{".", "../../internal/hooks"}
	// The single sanctioned process-exit site. Match by suffix so the check
	// is portable across absolute-path resolutions and OS path separators.
	const allowedSuffix = "cmd/leonard-hook/main.go"
	var offenders []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			abs, absErr := filepath.Abs(path)
			if absErr != nil {
				return fmt.Errorf("abs %s: %w", path, absErr)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if ident.Name != "os" || sel.Sel.Name != "Exit" {
					return true
				}
				if strings.HasSuffix(filepath.ToSlash(abs), allowedSuffix) {
					return true
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d", path, fset.Position(call.Pos()).Line))
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("os.Exit called outside cmd/leonard-hook/main.go — route exits through exitCodeFor instead:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
