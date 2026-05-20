package parse

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jasondillingham/leonard/internal/store"
)

// extractScript is the bundled Python AST walker shelled out per-file.
// Embedded so the binary is self-contained — no separate scripts to
// install. See extract_python.py for the contract.
//
//go:embed extract_python.py
var extractScript string

// defaultPythonInterpreter is the binary name we exec when the
// LEONARD_PYTHON env var is empty.
const defaultPythonInterpreter = "python3"

// pythonTimeout caps how long the indexer waits for a single python3
// extract before giving up and killing the subprocess. Set generously
// (well above real-world parse times, which run ~40ms per file) so a
// pathological file or environment glitch doesn't tank the whole walk
// — but bounded so a hung interpreter (recursive pyenv shim, NFS
// stall) doesn't freeze the indexer indefinitely.
//
// Override in tests via the same var.
var pythonTimeout = 30 * time.Second

// pythonInterpreter returns the binary the parser will exec for this
// invocation. Reads the LEONARD_PYTHON env var on every call so a
// user can change Python toolchains between sessions without
// rebuilding Leonard.
func pythonInterpreter() string {
	if v := strings.TrimSpace(os.Getenv("LEONARD_PYTHON")); v != "" {
		return v
	}
	return defaultPythonInterpreter
}

// ErrPythonUnavailable means the configured interpreter (LEONARD_PYTHON
// or the python3 fallback) isn't on PATH. The indexer logs this as a
// per-file parse failure and moves on — the rest of the index (Go,
// TypeScript) still gets built.
var ErrPythonUnavailable = errors.New("parse: python interpreter not found (set LEONARD_PYTHON to override the python3 default)")

// pythonSymbolRecord mirrors the JSON shape extract_python.py emits.
// Field names use json tags so the wire shape stays decoupled from
// store.Symbol's Go field names.
type pythonSymbolRecord struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Signature     string `json:"signature"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	Exported      bool   `json:"exported"`
}

// ExtractPython extracts module-level Python symbols (functions,
// classes + their methods, vars) from src. Calls out to the host
// python3's ast module so every Python version the user has installed
// is supported — replaces gpython, which capped at Python 3.4 syntax
// and rejected f-strings, PEP 585/604 generics, walrus, match, and
// PEP 526 annotated assignments common in modern code.
//
// Honors $LEONARD_PYTHON for alternate interpreter paths (uv, pyenv,
// etc.). Caps each subprocess at pythonTimeout so a hung interpreter
// doesn't freeze the indexer.
//
// path is the indexer-relative path used to derive the module
// qualifier (see moduleQualifier). ID and ParentID are left zero —
// the store assigns them on insert.
func ExtractPython(path string, src []byte) ([]store.Symbol, error) {
	bin, err := exec.LookPath(pythonInterpreter())
	if err != nil {
		return nil, ErrPythonUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), pythonTimeout)
	defer cancel()

	prefix := moduleQualifier(path)
	cmd := exec.CommandContext(ctx, bin, "-c", extractScript, prefix)
	// WaitDelay lets exec.Cmd reap the I/O goroutines (stdin writer in
	// particular) after the context fires and the subprocess is sent
	// SIGKILL. Without it, Run() blocks on those goroutines until the
	// subprocess actually closes its pipes, which on a hung interpreter
	// is forever. 500ms is a generous grace for the goroutines to notice.
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Stdin = bytes.NewReader(src)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil {
		// Timeout has the highest priority: the subprocess was killed
		// because we ran out of time, regardless of any exit code it
		// might have produced on the way down.
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("python extract timed out after %s (set LEONARD_PYTHON or raise pythonTimeout)", pythonTimeout)
		}
		// extract_python.py exit-codes 2 on SyntaxError; anything else
		// is the interpreter itself crashing (or the file being unreadable).
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "syntax error"
			}
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("python extract failed: %v: %s", runErr, strings.TrimSpace(stderr.String()))
	}

	var records []pythonSymbolRecord
	if err := json.Unmarshal(stdout.Bytes(), &records); err != nil {
		return nil, fmt.Errorf("decode extract_python output: %w", err)
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
