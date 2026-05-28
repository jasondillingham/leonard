package groundtruth

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// FactLeaf is one resolved scalar from a facts.yaml key path. When the
// path resolves to a map or array, ResolveFactKey returns one FactLeaf
// per contained scalar so callers can search for each value separately.
type FactLeaf struct {
	// Path is the dotted key path to this scalar
	// ("tech_stack.primary_language", "bosun.tools[0]").
	Path string
	// Value is the scalar value as it would appear in a string search
	// (already formatted via fmt.Sprint for numeric/bool types).
	Value string
}

// ResolveFactKey navigates facts.Root using keyPath (dot-separated,
// with optional [N] array indexing). If the resolved node is a map or
// array it returns all leaf scalars beneath it. Returns an error if the
// path doesn't exist.
func ResolveFactKey(facts *Facts, keyPath string) ([]FactLeaf, error) {
	if facts.IsEmpty() {
		return nil, fmt.Errorf("facts.yaml is empty")
	}
	segments := parseKeyPath(keyPath)
	node, err := navigateFacts(facts.Root, segments)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyPath, err)
	}
	var leaves []FactLeaf
	collectLeaves(node, keyPath, &leaves)
	if len(leaves) == 0 {
		return nil, fmt.Errorf("%s: no scalar values found at path", keyPath)
	}
	return leaves, nil
}

// ImpactResult is one file that contains a reference to a fact value.
type ImpactResult struct {
	// Path is the absolute path to the file.
	Path string
	// FirstLine is the 1-based line number of the first strong hit, or the
	// first hit of any kind when there are no strong hits.
	FirstLine int
	// Hits is the total number of occurrences in the file.
	Hits int
	// StrongHits is the subset of Hits where the surrounding context contains
	// a word matching a key-path segment, indicating the reference is likely
	// discussing this specific fact rather than coincidentally sharing a value.
	StrongHits int
}

// FindImpactedFiles walks .md files under projectRoot and returns those that
// contain valueStr (word-boundary matched, case-insensitive). keyPath is the
// dotted facts key (e.g. "team.engineers") used to classify each hit as strong
// (context mentions a key-path segment) or value-only. ig, when non-nil,
// prunes paths that match .gitignore / .leonardignore rules.
func FindImpactedFiles(projectRoot, valueStr, keyPath string, ig *ignore.GitIgnore) ([]ImpactResult, error) {
	if valueStr == "" {
		return nil, nil
	}
	targets, _, err := walkMDFiles(projectRoot, ScanCap, ig)
	if err != nil {
		return nil, err
	}
	var out []ImpactResult
	for _, path := range targets {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		text := string(content)
		spans := findAllOccurrences(text, valueStr)
		if len(spans) == 0 {
			continue
		}
		var strongHits int
		firstStrongLine := 0
		for _, s := range spans {
			ctx := numberContext(text, s.start, s.end)
			if contextMatchesFact(ctx, keyPath) {
				strongHits++
				if firstStrongLine == 0 {
					firstStrongLine = byteOffsetToLine(content, s.start)
				}
			}
		}
		firstLine := byteOffsetToLine(content, spans[0].start)
		if firstStrongLine > 0 {
			firstLine = firstStrongLine
		}
		out = append(out, ImpactResult{
			Path:       path,
			FirstLine:  firstLine,
			Hits:       len(spans),
			StrongHits: strongHits,
		})
	}
	return out, nil
}

// byteOffsetToLine returns the 1-based line number for the given byte
// offset in src.
func byteOffsetToLine(src []byte, offset int) int {
	if offset <= 0 || len(src) == 0 {
		return 1
	}
	if offset > len(src) {
		offset = len(src)
	}
	line := 1
	for i := 0; i < offset; i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}

// parseKeyPath splits a dot-separated key path into segments, handling
// array notation: "bosun.tools[0]" → ["bosun", "tools", "[0]"].
func parseKeyPath(keyPath string) []string {
	// Replace [ with .[ so we can split cleanly on dots.
	keyPath = strings.ReplaceAll(keyPath, "[", ".[")
	parts := strings.Split(keyPath, ".")
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// navigateFacts walks a decoded YAML tree following segments. Returns
// the node at the path or an error if any segment is missing.
func navigateFacts(node any, segments []string) (any, error) {
	if len(segments) == 0 {
		return node, nil
	}
	seg := segments[0]
	rest := segments[1:]

	if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
		idxStr := seg[1 : len(seg)-1]
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			return nil, fmt.Errorf("invalid array index %q", seg)
		}
		arr, ok := node.([]any)
		if !ok {
			return nil, fmt.Errorf("expected array at %q, got %T", seg, node)
		}
		if idx < 0 || idx >= len(arr) {
			return nil, fmt.Errorf("index %d out of range (len %d)", idx, len(arr))
		}
		return navigateFacts(arr[idx], rest)
	}

	m, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected map at %q, got %T", seg, node)
	}
	child, exists := m[seg]
	if !exists {
		return nil, fmt.Errorf("key %q not found", seg)
	}
	return navigateFacts(child, rest)
}

// collectLeaves recursively gathers all scalar values from node,
// appending a FactLeaf for each.
func collectLeaves(node any, path string, out *[]FactLeaf) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			collectLeaves(child, childPath, out)
		}
	case []any:
		for i, child := range v {
			collectLeaves(child, fmt.Sprintf("%s[%d]", path, i), out)
		}
	case nil:
		// skip
	default:
		*out = append(*out, FactLeaf{Path: path, Value: fmt.Sprint(v)})
	}
}
