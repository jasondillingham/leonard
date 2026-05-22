package groundtruth

import (
	"fmt"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/config"
)

// evalPathFilters runs the v0.8 path_filters check (#15). Returns a
// verdict + true when a rule fired. Returns false when no rule
// applied so the caller proceeds to the next guard.
//
// Trust gate: like the forbidden-claim guard, path_filters only
// hard-block when the adapter is trusted via `leonard config trust
// ground-truth`. Untrusted projects see a stderr warning and
// proceed.
//
// Override token: a `leonard override --once` token (#17) is
// honored by short-circuiting the path filter. The token is
// consumed regardless of which filter would have triggered.
func evalPathFilters(snap postEditSnapshot, p adapters.PreEditPayload) (adapters.PreEditResult, bool) {
	if snap.filters == nil || len(snap.filters.PathFilters) == 0 {
		return adapters.PreEditResult{}, false
	}
	if p.FilePath == "" {
		// Bash commands etc. have no FilePath; path_filters don't
		// apply.
		return adapters.PreEditResult{}, false
	}

	// Build a project-relative form so the regex patterns in
	// filters.yaml can be written against project structure
	// ("applications/.../...") rather than absolute paths.
	rel := relForFilters(snap.projectRoot, p.FilePath)

	for i, pf := range snap.filters.PathFilters {
		captured, matched := pf.MatchPath(rel)
		if !matched {
			continue
		}
		if len(pf.ForbiddenValues) > 0 && !pf.IsForbidden(captured) {
			// Captured value isn't on the forbidden list — this
			// rule doesn't apply.
			continue
		}
		if len(pf.ForbiddenValues) == 0 {
			// No forbidden_values specified: rule is "any match
			// is forbidden." Useful for blanket bans on a path
			// prefix.
		}

		if consumeOverrideToken(snap.projectRoot, rel) {
			fmt.Fprintf(snap.stderr,
				"leonard: ground-truth: path filter bypassed for %s via `leonard override --once`\n",
				rel,
			)
			return adapters.PreEditResult{}, false
		}

		reason := pf.Reason
		if reason == "" {
			reason = fmt.Sprintf("path %q matches forbidden value %q", rel, captured)
		}

		trusted, err := config.AdapterTrusted(snap.projectRoot, Name)
		if err != nil {
			fmt.Fprintf(snap.stderr,
				"leonard: ground-truth: path_filters[%d] trust check failed, falling through to Pass: %v\n",
				i, err,
			)
			return adapters.PreEditResult{}, false
		}
		if !trusted {
			fmt.Fprintf(snap.stderr,
				"leonard: ground-truth: path %q would be blocked by path_filters[%d] (%s); run `leonard config trust ground-truth` to enable blocking.\n",
				rel, i, reason,
			)
			return adapters.PreEditResult{}, false
		}

		denyReason := fmt.Sprintf("Path %q is forbidden by path_filters[%d]: %s. Use `leonard override --once --reason \"...\"` to bypass for a single edit.",
			rel, i, reason)
		return adapters.PreEditResult{
			Decision:    adapters.Deny,
			Reason:      denyReason,
			AdapterName: Name,
		}, true
	}
	return adapters.PreEditResult{}, false
}

// evalContentFilters runs the v0.8 content_filters check (#16). For
// each filter whose content_pattern matches p.Content, the required
// disclosure must also be present (case-insensitive). Missing
// disclosure → Deny.
func evalContentFilters(snap postEditSnapshot, p adapters.PreEditPayload) (adapters.PreEditResult, bool) {
	if snap.filters == nil || len(snap.filters.ContentFilters) == 0 {
		return adapters.PreEditResult{}, false
	}
	if p.Content == "" {
		return adapters.PreEditResult{}, false
	}
	rel := relForFilters(snap.projectRoot, p.FilePath)

	for i, cf := range snap.filters.ContentFilters {
		if !cf.MatchContent(p.Content) {
			continue
		}
		if cf.DisclosureSatisfied(p.Content) {
			continue
		}

		if consumeOverrideToken(snap.projectRoot, rel) {
			fmt.Fprintf(snap.stderr,
				"leonard: ground-truth: content filter bypassed for %s via `leonard override --once`\n",
				rel,
			)
			return adapters.PreEditResult{}, false
		}

		trusted, err := config.AdapterTrusted(snap.projectRoot, Name)
		if err != nil {
			fmt.Fprintf(snap.stderr,
				"leonard: ground-truth: content_filters[%d] trust check failed, falling through to Pass: %v\n",
				i, err,
			)
			return adapters.PreEditResult{}, false
		}
		if !trusted {
			fmt.Fprintf(snap.stderr,
				"leonard: ground-truth: content matches content_filters[%d]; required disclosure missing: %q. Run `leonard config trust ground-truth` to enable blocking.\n",
				i, cf.Required,
			)
			return adapters.PreEditResult{}, false
		}

		denyReason := fmt.Sprintf("Content matches content_filters[%d] but the required disclosure is missing: %q. Add the disclosure verbatim to the content, or use `leonard override --once --reason \"...\"` to bypass for a single edit.",
			i, cf.Required)
		return adapters.PreEditResult{
			Decision:    adapters.Deny,
			Reason:      denyReason,
			AdapterName: Name,
		}, true
	}
	return adapters.PreEditResult{}, false
}

// relForFilters returns the project-relative path used by the
// filter checks. Falls back to the input when canonicalization
// fails (e.g., FilePath is empty for Bash payloads).
func relForFilters(projectRoot, filePath string) string {
	if filePath == "" {
		return ""
	}
	if projectRoot == "" {
		return filePath
	}
	cleanRoot := canonicalize(projectRoot)
	cleanPath := canonicalize(filePath)
	if rel, err := relPathSafe(cleanRoot, cleanPath); err == nil {
		return rel
	}
	return filePath
}
