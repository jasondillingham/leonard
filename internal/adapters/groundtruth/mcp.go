package groundtruth

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterTools satisfies adapters.Adapter. v0.6 attaches three tools
// to srv:
//
//   - verify_claim(text)            #10 — heuristic claim detection
//   - list_facts(category?)         #11 — facts.yaml navigation
//   - get_story(name)               #12 — canonical phrasings
//
// The handlers close over the adapter so they read the current
// in-memory ground-truth state. If the adapter has not been Init'd
// the registered tools still respond — verify_claim returns an empty
// result, list_facts returns an empty tree, get_story returns a
// not-found error — so an MCP client gets predictable behavior even
// in pathological wire-up scenarios.
func (a *GroundTruthAdapter) RegisterTools(srv *mcp.Server) error {
	if srv == nil {
		return errors.New("ground-truth adapter: RegisterTools requires non-nil server")
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "verify_claim",
		Description: "Detect claims in the supplied text and classify each against facts.yaml + do-not-claim.md. Returns per-claim verdicts (verified | unverified | forbidden | opinion) with provenance.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in VerifyClaimInput) (*mcp.CallToolResult, VerifyClaimOutput, error) {
		return nil, a.verifyClaim(in), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_facts",
		Description: "Return facts from facts.yaml. With no category, returns the top-level keys. With a dotted category (e.g., 'tech_stack'), returns the matching subtree. Private entries (sensitivity: private) are filtered unless include_private is true.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in ListFactsInput) (*mcp.CallToolResult, ListFactsOutput, error) {
		return nil, a.listFacts(in), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_story",
		Description: "Return the canonical text for a story from stories.md. Names are matched case-insensitively. Returns an error if the story is not found (rather than an empty success).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in GetStoryInput) (*mcp.CallToolResult, GetStoryOutput, error) {
		out, err := a.getStory(in)
		if err != nil {
			return nil, GetStoryOutput{}, err
		}
		return nil, out, nil
	})

	return nil
}

// ----- verify_claim -----

// VerifyClaimInput is the wire shape of the verify_claim tool's input.
type VerifyClaimInput struct {
	Text string `json:"text"`
}

// VerifyClaimOutput is the wire shape of the verify_claim response.
// Mirrors DetectionResult / Claim / Summary but with explicit JSON
// tags and string-typed verdict so the wire schema doesn't shift if
// the Verdict enum gains new values.
type VerifyClaimOutput struct {
	Claims  []VerifyClaimEntry `json:"claims"`
	Summary VerifyClaimSummary `json:"summary"`
}

// VerifyClaimEntry is one Claim on the wire.
type VerifyClaimEntry struct {
	Text          string `json:"text"`
	Category      string `json:"category"`
	Verdict       string `json:"verdict"`
	EvidencePath  string `json:"evidence_path,omitempty"`
	EvidenceValue any    `json:"evidence_value,omitempty"`
	RulePath      string `json:"rule_path,omitempty"`
	RuleText      string `json:"rule_text,omitempty"`
	Note          string `json:"note,omitempty"`
	StartByte     int    `json:"start_byte"`
	EndByte       int    `json:"end_byte"`
}

// VerifyClaimSummary is the per-verdict rollup.
type VerifyClaimSummary struct {
	Total      int `json:"total"`
	Verified   int `json:"verified"`
	Unverified int `json:"unverified"`
	Forbidden  int `json:"forbidden"`
	Opinion    int `json:"opinion"`
}

func (a *GroundTruthAdapter) verifyClaim(in VerifyClaimInput) VerifyClaimOutput {
	res := a.Detect(in.Text)
	out := VerifyClaimOutput{
		Claims: make([]VerifyClaimEntry, 0, len(res.Claims)),
		Summary: VerifyClaimSummary{
			Total:      res.Summary.Total,
			Verified:   res.Summary.Verified,
			Unverified: res.Summary.Unverified,
			Forbidden:  res.Summary.Forbidden,
			Opinion:    res.Summary.Opinion,
		},
	}
	for _, c := range res.Claims {
		out.Claims = append(out.Claims, VerifyClaimEntry{
			Text:          c.Text,
			Category:      c.Category,
			Verdict:       c.Verdict.String(),
			EvidencePath:  c.EvidencePath,
			EvidenceValue: c.EvidenceValue,
			RulePath:      c.RulePath,
			RuleText:      c.RuleText,
			Note:          c.Note,
			StartByte:     c.StartByte,
			EndByte:       c.EndByte,
		})
	}
	return out
}

// ----- list_facts -----

// ListFactsInput is the wire shape. Category is optional (empty =
// return top-level keys). IncludePrivate opts caller into receiving
// entries marked sensitivity: private.
type ListFactsInput struct {
	Category       string `json:"category,omitempty"`
	IncludePrivate bool   `json:"include_private,omitempty"`
}

// ListFactsOutput is the wire shape. Path echoes the resolved
// category (empty when the caller asked for top-level keys). Facts
// is the (sensitivity-filtered) tree at that path; Keys is the
// sorted list of top-level keys at that path for callers that just
// want navigation hints.
type ListFactsOutput struct {
	Path  string         `json:"path"`
	Facts map[string]any `json:"facts"`
	Keys  []string       `json:"keys"`
}

func (a *GroundTruthAdapter) listFacts(in ListFactsInput) ListFactsOutput {
	a.mu.RLock()
	facts := a.facts
	a.mu.RUnlock()

	out := ListFactsOutput{
		Path:  in.Category,
		Facts: map[string]any{},
		Keys:  []string{},
	}
	if facts == nil || len(facts.Root) == 0 {
		return out
	}

	node, ok := walkToCategory(facts.Root, in.Category)
	if !ok {
		// Unknown category. Return empty Facts + Keys with the
		// path the caller asked for so they can distinguish
		// "category not found" from "category found but empty".
		return out
	}

	filtered := filterPrivate(node, in.IncludePrivate)
	if asMap, ok := filtered.(map[string]any); ok {
		out.Facts = asMap
		out.Keys = sortedKeys(asMap)
	} else {
		// Scalar / list at the requested path. Wrap so the
		// output shape stays a map (clients have an easier
		// time with a stable shape than with a polymorphic
		// response).
		out.Facts = map[string]any{"value": filtered}
		out.Keys = []string{"value"}
	}
	return out
}

// walkToCategory descends a facts tree by a dotted category path.
// Empty category returns the root unchanged. Returns (nil, false)
// when any path segment is missing.
func walkToCategory(root map[string]any, category string) (any, bool) {
	if category == "" {
		return root, true
	}
	var node any = root
	for _, seg := range splitDots(category) {
		m, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := m[seg]
		if !ok {
			return nil, false
		}
		node = next
	}
	return node, true
}

func splitDots(s string) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// filterPrivate recursively walks node and drops any map entry whose
// `sensitivity` field equals "private". Top-level only — nested
// objects inside a private parent are filtered transitively.
// includePrivate=true short-circuits and returns the input unchanged.
func filterPrivate(node any, includePrivate bool) any {
	if includePrivate {
		return node
	}
	switch v := node.(type) {
	case map[string]any:
		// If THIS map is itself private, drop it (return nil; the
		// caller decides whether to omit the key).
		if isPrivate(v) {
			return nil
		}
		out := make(map[string]any, len(v))
		for k, child := range v {
			filtered := filterPrivate(child, includePrivate)
			if filtered == nil {
				continue
			}
			out[k] = filtered
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, child := range v {
			filtered := filterPrivate(child, includePrivate)
			if filtered == nil {
				continue
			}
			out = append(out, filtered)
		}
		return out
	default:
		return v
	}
}

// isPrivate reports whether m has a sensitivity:private marker.
func isPrivate(m map[string]any) bool {
	s, ok := m["sensitivity"].(string)
	if !ok {
		return false
	}
	return s == "private"
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ----- get_story -----

// GetStoryInput is the wire shape.
type GetStoryInput struct {
	Name string `json:"name"`
}

// GetStoryOutput mirrors Story with JSON tags.
type GetStoryOutput struct {
	Name        string   `json:"name"`
	Short       string   `json:"short"`
	Long        string   `json:"long"`
	DoNotDrift  []string `json:"do_not_drift"`
	Sensitivity string   `json:"sensitivity"`
	Line        int      `json:"line"`
}

// ErrStoryNotFound is returned by getStory when the requested story
// is absent from stories.md. Callers (the MCP layer) propagate it as
// a tool-call error so Claude sees "no such story 'X'" rather than
// an empty success.
var ErrStoryNotFound = errors.New("ground-truth adapter: story not found")

func (a *GroundTruthAdapter) getStory(in GetStoryInput) (GetStoryOutput, error) {
	a.mu.RLock()
	stories := a.stories
	a.mu.RUnlock()

	story, ok := stories.Get(in.Name)
	if !ok {
		return GetStoryOutput{}, fmt.Errorf("%w: %q", ErrStoryNotFound, in.Name)
	}
	return GetStoryOutput{
		Name:        story.Name,
		Short:       story.Short,
		Long:        story.Long,
		DoNotDrift:  story.DoNotDrift,
		Sensitivity: story.Sensitivity,
		Line:        story.Line,
	}, nil
}
