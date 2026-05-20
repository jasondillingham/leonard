package parse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jasondillingham/leonard/internal/store"
)

// defaultRustExtractorName is the basename of the compiled syn-based
// AST walker shipped in internal/parse/rust/. The release binary lives
// at internal/parse/rust/target/release/leonard-extract-rust by
// default; users with a system-wide install can override via
// LEONARD_RUST_EXTRACTOR.
const defaultRustExtractorName = "leonard-extract-rust"

// rustTimeout caps the subprocess. Same shape as pythonTimeout —
// generous (well above the ~30ms a single file actually takes) but
// bounded so a runaway helper can't freeze the indexer.
var rustTimeout = 30 * time.Second

// rustExtractorOverride is set in tests; production reads
// LEONARD_RUST_EXTRACTOR from the env. Keeping the override as a
// package-level var (rather than threading it through every call)
// matches the pythonInterpreter pattern.
var rustExtractorOverride string

// ErrRustExtractorUnavailable means the compiled helper isn't on
// PATH, in the LEONARD_RUST_EXTRACTOR env var, or in the
// internal/parse/rust/target/release/ build output dir relative to
// the running binary's source. The indexer surfaces this as a per-
// file ParseFailure rather than a hard error so a project with one
// bad Rust file doesn't tank the whole walk.
var ErrRustExtractorUnavailable = errors.New(
	"parse: leonard-extract-rust binary not found " +
		"(build via `cargo build --release` inside internal/parse/rust/, " +
		"set LEONARD_RUST_EXTRACTOR, or install the binary on PATH)",
)

// rustSymbolRecord mirrors the JSON shape emitted by main.rs.
type rustSymbolRecord struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Signature     string `json:"signature"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	Exported      bool   `json:"exported"`
}

// rustExtractorPath returns the path to the helper binary.
// Precedence: explicit test override, then $LEONARD_RUST_EXTRACTOR,
// then "leonard-extract-rust" on $PATH, then the cargo target dir
// inside the source tree (most useful during development — the
// helper just got built, no install step needed).
func rustExtractorPath() (string, error) {
	if rustExtractorOverride != "" {
		return rustExtractorOverride, nil
	}
	if env := strings.TrimSpace(os.Getenv("LEONARD_RUST_EXTRACTOR")); env != "" {
		// Stat-check so a typo in the env var surfaces as a clean
		// ErrRustExtractorUnavailable (the indexer logs it as a
		// per-file ParseFailure) rather than a confusing fork/exec
		// "no such file or directory" later in cmd.Run().
		if _, err := os.Stat(env); err != nil {
			return "", ErrRustExtractorUnavailable
		}
		return env, nil
	}
	if bin, err := exec.LookPath(defaultRustExtractorName); err == nil {
		return bin, nil
	}
	// Source-tree fallback: this file is at internal/parse/rust.go.
	// The cargo crate at internal/parse/rust/ builds its binary to
	// internal/parse/rust/target/release/. Find this file via
	// runtime.Caller so the lookup works whether we're running tests
	// from the package dir or from elsewhere.
	if _, here, _, ok := runtime.Caller(0); ok {
		candidate := filepath.Join(filepath.Dir(here), "rust", "target", "release", defaultRustExtractorName)
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", ErrRustExtractorUnavailable
}

// ExtractRust extracts module-level Rust symbols (fn, struct, enum,
// trait, type alias, const, static, plus methods one level deep
// inside impl blocks). Calls out to a small syn-based helper binary
// shipped in internal/parse/rust/ — same dependency model as Python
// (where we exec python3) and `go vet`.
//
// path is the indexer-relative path used to derive the module
// qualifier. ID and ParentID are left zero — the store assigns them
// on insert.
func ExtractRust(path string, src []byte) ([]store.Symbol, error) {
	bin, err := rustExtractorPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), rustTimeout)
	defer cancel()

	prefix := moduleQualifier(path)
	cmd := exec.CommandContext(ctx, bin, prefix)
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Stdin = bytes.NewReader(src)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("rust extract timed out after %s", rustTimeout)
		}
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "parse error"
			}
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("rust extract failed: %v: %s", runErr, strings.TrimSpace(stderr.String()))
	}

	var records []rustSymbolRecord
	if err := json.Unmarshal(stdout.Bytes(), &records); err != nil {
		return nil, fmt.Errorf("decode extract-rust output: %w", err)
	}
	out := make([]store.Symbol, 0, len(records))
	for _, r := range records {
		out = append(out, store.Symbol{
			FilePath:      path,
			Name:          r.Name,
			QualifiedName: r.QualifiedName,
			Kind:          r.Kind,
			Signature:     r.Signature,
			StartLine:     r.StartLine,
			EndLine:       r.EndLine,
			Exported:      r.Exported,
		})
	}
	return out, nil
}
