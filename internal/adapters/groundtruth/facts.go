package groundtruth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// Facts is the parsed facts.yaml tree. Root is the YAML root as a
// generic map so later issues' lookup logic ("facts.yaml#tech_stack.
// primary_language") can navigate it path-style without baking schema
// into v0.6.
//
// Path is the source file (absolute) for error messages.
//
// Empty (Path = "", Root = nil) is a valid "no facts.yaml present"
// state — the loader returns this rather than an error when the file
// is missing, so operators can ship a partial tree.
type Facts struct {
	Path string
	Root map[string]any
}

// loadFacts reads path and parses it as YAML into a Facts value. A
// missing file is NOT an error — returns an empty Facts. Parse errors
// from yaml.v3 already carry line numbers; we wrap them so the source
// path is visible in error chains.
func loadFacts(path string) (*Facts, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Facts{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &Facts{Path: path, Root: root}, nil
}

// IsEmpty reports whether the facts tree is unpopulated. Used by
// hook code to short-circuit lookups when there's nothing to check
// against. An explicit nil-receiver guard handles the "no facts.yaml
// at all" case without a separate flag.
func (f *Facts) IsEmpty() bool {
	return f == nil || len(f.Root) == 0
}
