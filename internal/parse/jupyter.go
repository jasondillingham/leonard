package parse

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jasondillingham/leonard/internal/store"
)

// jupyterNotebook is the minimal shape Leonard cares about from
// an .ipynb file. The schema is much richer (metadata, outputs,
// kernel info, attachments) but Leonard's job is symbol
// extraction — code cells are all that matter.
type jupyterNotebook struct {
	Cells []jupyterCell `json:"cells"`
}

type jupyterCell struct {
	CellType string          `json:"cell_type"`
	Source   json.RawMessage `json:"source"`
}

// ExtractJupyter parses a Jupyter notebook (.ipynb JSON), extracts
// the source of every code cell, concatenates them with cell
// separators, and routes the result through ExtractPython. Line
// numbers in returned Symbols correspond to the position in the
// concatenated stream — approximate but useful for navigation
// (clients reading raw .ipynb JSON wouldn't have meaningful line
// numbers anyway since they're per-cell).
//
// Markdown / raw cells are skipped. A cell separator line is
// inserted between consecutive code cells so Python's parser
// doesn't see two unrelated symbol decls glued together.
func ExtractJupyter(path string, src []byte) ([]store.Symbol, error) {
	var nb jupyterNotebook
	if err := json.Unmarshal(src, &nb); err != nil {
		return nil, fmt.Errorf("jupyter: parse notebook: %w", err)
	}

	var py strings.Builder
	for _, cell := range nb.Cells {
		if cell.CellType != "code" {
			continue
		}
		cellSrc, err := decodeCellSource(cell.Source)
		if err != nil {
			return nil, fmt.Errorf("jupyter: decode cell source: %w", err)
		}
		py.WriteString(cellSrc)
		// Separator: blank line between cells so Python's parser
		// treats them as independent top-level scopes.
		if !strings.HasSuffix(cellSrc, "\n") {
			py.WriteString("\n")
		}
		py.WriteString("\n")
	}
	if py.Len() == 0 {
		return nil, nil
	}
	syms, err := ExtractPython(path, []byte(py.String()))
	if err != nil {
		return syms, err
	}
	return syms, nil
}

// decodeCellSource accepts the two shapes Jupyter notebooks use
// for cell content: a single string (rare, older notebooks) or an
// array of strings (the modern convention — each entry typically
// is one line WITH its trailing newline). Returns the
// concatenated source.
func decodeCellSource(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	// Try string first.
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return asStr, nil
	}
	var asArr []string
	if err := json.Unmarshal(raw, &asArr); err != nil {
		return "", fmt.Errorf("expected string or array, got: %s", string(raw))
	}
	return strings.Join(asArr, ""), nil
}
