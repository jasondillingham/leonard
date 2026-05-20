package parse

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jasondillingham/leonard/internal/store"
)

// extractScript is the bundled Python AST walker shelled out per-file.
// Embedded so the binary is self-contained — no separate scripts to
// install. See extract_python.py for the contract.
//
//go:embed extract_python.py
var extractScript string

// pythonInterpreter is the binary we exec. Resolved once via PATH lookup
// at first use so each parse call doesn't re-stat /usr/bin/python3. A
// missing interpreter is surfaced as a clear ParseFailure (not a hard
// indexer error) so a project with one bad Python file doesn't tank
// the whole walk.
//
// Override via the LEONARD_PYTHON environment variable when the host
// has python3 under a non-standard name (uv, pyenv shims, etc.).
var pythonInterpreter = "python3"

// ErrPythonUnavailable means the host has no python3 on PATH. The
// indexer logs this as a per-file parse failure and moves on — the
// rest of the index (Go, TypeScript) still gets built.
var ErrPythonUnavailable = errors.New("parse: python3 not found on PATH (set LEONARD_PYTHON to override)")

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
// path is the indexer-relative path used to derive the module
// qualifier (see moduleQualifier). ID and ParentID are left zero —
// the store assigns them on insert.
func ExtractPython(path string, src []byte) ([]store.Symbol, error) {
	bin, err := exec.LookPath(pythonInterpreter)
	if err != nil {
		return nil, ErrPythonUnavailable
	}
	prefix := moduleQualifier(path)
	cmd := exec.Command(bin, "-c", extractScript, prefix)
	cmd.Stdin = bytes.NewReader(src)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil {
		// extract_python.py exit-codes 2 on SyntaxError; anything else
		// is the interpreter itself crashing (or the file being unreadable).
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "syntax error"
			}
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("python3 extract failed: %v: %s", runErr, strings.TrimSpace(stderr.String()))
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
