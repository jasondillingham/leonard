package groundtruth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// Filters is the parsed filters.yaml content. v0.6 holds it as a
// generic map; #15 / #16 add strongly-typed accessors for path_filters
// and content_filters when filter enforcement lands.
type Filters struct {
	Path string
	Root map[string]any
}

// loadFilters reads path and parses it as YAML. Missing file → empty
// Filters (not an error).
func loadFilters(path string) (*Filters, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Filters{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &Filters{Path: path, Root: root}, nil
}

// IsEmpty reports whether the filters tree is unpopulated.
func (f *Filters) IsEmpty() bool {
	return f == nil || len(f.Root) == 0
}
