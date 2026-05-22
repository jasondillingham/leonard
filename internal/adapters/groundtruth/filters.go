package groundtruth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Filters is the parsed filters.yaml content. v0.6 held it as a
// generic map; v0.8 (#15) adds strongly-typed accessors for
// path_filters. content_filters (#16) layers on the same shape.
type Filters struct {
	Path string
	Root map[string]any

	// PathFilters is the parsed top-level `path_filters:` slice
	// from filters.yaml. Each entry's path_pattern is compiled at
	// load time so the pre-edit hook doesn't pay the regex-compile
	// cost on every call.
	PathFilters []PathFilter

	// ContentFilters is the parsed top-level `content_filters:`
	// slice (v0.8 #16). Each pattern compiled at load time.
	ContentFilters []ContentFilter
}

// PathFilter blocks Write operations into paths matching path_pattern
// when the first capture group resolves to a value in
// forbidden_values. The reason is surfaced verbatim in the deny
// message so operators get context for what the rule is protecting.
//
// Example filters.yaml entry:
//
//	path_filters:
//	  - path_pattern: "applications/([^/]+)/"
//	    forbidden_values:
//	      - acme-corp
//	      - beta-co
//	    reason: "Contract terminated 2026-03-15; no further outreach"
//
// Compiled regex lives in `re`; the source pattern stays in
// PathPattern for error messages.
type PathFilter struct {
	PathPattern     string
	ForbiddenValues []string
	Reason          string

	// re is the compiled regex; populated by loadFilters at parse
	// time. Nil only if PathPattern was empty (in which case the
	// rule is dropped during parsing).
	re *regexp.Regexp
}

// ContentFilter rejects edits whose Content matches content_pattern
// unless the required disclosure text is present verbatim in the
// same Content. Used for "if you say X, you MUST also say Y"
// disclosure scenarios.
type ContentFilter struct {
	ContentPattern string
	Required       string

	re *regexp.Regexp
}

// MatchPath returns the first capture group's value when path
// matches PathPattern, plus a flag indicating whether ANY match
// occurred. When the value is in ForbiddenValues, callers should
// reject the edit.
func (f PathFilter) MatchPath(path string) (string, bool) {
	if f.re == nil {
		return "", false
	}
	m := f.re.FindStringSubmatch(path)
	if m == nil {
		return "", false
	}
	if len(m) >= 2 {
		return m[1], true
	}
	return "", true
}

// IsForbidden reports whether captured value v is in ForbiddenValues
// (case-insensitive match).
func (f PathFilter) IsForbidden(v string) bool {
	for _, fv := range f.ForbiddenValues {
		if strings.EqualFold(fv, v) {
			return true
		}
	}
	return false
}

// MatchContent reports whether body matches the content_pattern.
func (f ContentFilter) MatchContent(body string) bool {
	if f.re == nil {
		return false
	}
	return f.re.MatchString(body)
}

// DisclosureSatisfied reports whether body contains the required
// disclosure text (case-insensitive). When MatchContent returns
// true AND DisclosureSatisfied is false, the edit should be
// rejected.
func (f ContentFilter) DisclosureSatisfied(body string) bool {
	if f.Required == "" {
		return true
	}
	return strings.Contains(strings.ToLower(body), strings.ToLower(f.Required))
}

// loadFilters reads path and parses it as YAML. Missing file → empty
// Filters (not an error). Also extracts path_filters and
// content_filters into strongly-typed slices for the v0.8 pre-edit
// guard (#15 / #16). Malformed entries inside path_filters /
// content_filters are reported as errors with line context.
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

	pathFilters, err := parsePathFilters(path, root)
	if err != nil {
		return nil, err
	}
	contentFilters, err := parseContentFilters(path, root)
	if err != nil {
		return nil, err
	}
	return &Filters{
		Path:           path,
		Root:           root,
		PathFilters:    pathFilters,
		ContentFilters: contentFilters,
	}, nil
}

func parsePathFilters(srcPath string, root map[string]any) ([]PathFilter, error) {
	raw, ok := root["path_filters"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: path_filters must be a list, got %T", srcPath, raw)
	}
	out := make([]PathFilter, 0, len(list))
	for i, e := range list {
		entry, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: path_filters[%d] must be a map, got %T", srcPath, i, e)
		}
		pattern, _ := entry["path_pattern"].(string)
		if pattern == "" {
			return nil, fmt.Errorf("%s: path_filters[%d]: path_pattern is required and must be a string", srcPath, i)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("%s: path_filters[%d]: compile path_pattern %q: %w", srcPath, i, pattern, err)
		}
		reason, _ := entry["reason"].(string)

		var values []string
		if rawValues, ok := entry["forbidden_values"]; ok {
			vs, ok := rawValues.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: path_filters[%d]: forbidden_values must be a list, got %T", srcPath, i, rawValues)
			}
			for j, v := range vs {
				s, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("%s: path_filters[%d].forbidden_values[%d] must be a string, got %T", srcPath, i, j, v)
				}
				values = append(values, s)
			}
		}
		out = append(out, PathFilter{
			PathPattern:     pattern,
			ForbiddenValues: values,
			Reason:          reason,
			re:              re,
		})
	}
	return out, nil
}

func parseContentFilters(srcPath string, root map[string]any) ([]ContentFilter, error) {
	raw, ok := root["content_filters"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: content_filters must be a list, got %T", srcPath, raw)
	}
	out := make([]ContentFilter, 0, len(list))
	for i, e := range list {
		entry, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: content_filters[%d] must be a map, got %T", srcPath, i, e)
		}
		pattern, _ := entry["content_pattern"].(string)
		if pattern == "" {
			return nil, fmt.Errorf("%s: content_filters[%d]: content_pattern is required and must be a string", srcPath, i)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("%s: content_filters[%d]: compile content_pattern %q: %w", srcPath, i, pattern, err)
		}
		required, _ := entry["required"].(string)
		out = append(out, ContentFilter{
			ContentPattern: pattern,
			Required:       required,
			re:             re,
		})
	}
	return out, nil
}

// IsEmpty reports whether the filters tree is unpopulated.
func (f *Filters) IsEmpty() bool {
	return f == nil || len(f.Root) == 0
}
