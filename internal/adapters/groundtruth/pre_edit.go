package groundtruth

import (
	"context"
	"fmt"
	"strings"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/config"
)

// PreEdit implements the v0.7 hard guard. When the proposed edit
// would write content matching a do-not-claim.md rule, the adapter
// returns Deny with the rule citation — IF the operator has granted
// trust via `leonard config trust ground-truth`. Untrusted projects
// see a warning to stderr but the edit proceeds (so an unconfigured
// install never accidentally blocks an operator from working).
//
// Guards (mirror PostEdit):
//   - empty Content → Pass (no claim to check)
//   - no rules loaded → Pass (truth tree empty)
//   - file outside project root → Pass
//   - file path doesn't match verify_targets → Pass
//
// Hook errors are returned to the dispatcher so a panic-class
// failure (config trust I/O blowing up) is visible upstream rather
// than silently degrading the guard.
func (a *GroundTruthAdapter) PreEdit(_ context.Context, p adapters.PreEditPayload) (adapters.PreEditResult, error) {
	snap := a.snapshot()

	// #15 path_filters: check the proposed file path against
	// forbidden-target rules BEFORE checking content. A blocked
	// path makes the content-based check moot.
	if verdict, ok := evalPathFilters(snap, p); ok {
		return verdict, nil
	}

	// #16 content_filters: check whether content triggers a required-
	// disclosure rule. Runs before the do-not-claim hard guard so a
	// disclosure miss is the verdict rather than a forbidden-claim
	// hit on the same text.
	if verdict, ok := evalContentFilters(snap, p); ok {
		return verdict, nil
	}

	if len(snap.rules) == 0 {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}
	if p.Content == "" {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	// File path is optional for some tool types (Bash carries
	// Command instead). When set, apply the same path-trust +
	// verify-target gating as PostEdit. When empty, fall through
	// to detection — Bash commands containing forbidden text still
	// deserve the guard.
	if p.FilePath != "" {
		absPath := p.FilePath
		if !pathIsAbsolute(absPath) {
			absPath = joinUnderRoot(snap.projectRoot, absPath)
		}
		if !insideProject(snap.projectRoot, absPath) {
			return adapters.PreEditResult{Decision: adapters.Pass}, nil
		}
		if !matchesAnyGlob(absPath, snap.cfg.VerifyTargets) {
			return adapters.PreEditResult{Decision: adapters.Pass}, nil
		}
		// #84 / #8: files inside truth_dir, plus operator-
		// configured exempt_paths globs, skip the matcher. The
		// truth tree's own files contain the rule bodies as data
		// — running the matcher against them creates a self-
		// blocking loop on the operator's own rule content.
		if isExemptFromMatcher(absPath, snap.projectRoot, snap.truthDir, snap.cfg.ExemptPaths) {
			return adapters.PreEditResult{Decision: adapters.Pass}, nil
		}
	}

	res := Detect(p.Content, snap.facts, snap.rules)
	if res.Summary.Forbidden == 0 {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	// Find the first forbidden hit to surface in the deny reason.
	var hit *Claim
	for i := range res.Claims {
		if res.Claims[i].Verdict == VerdictForbidden {
			hit = &res.Claims[i]
			break
		}
	}

	trusted, err := config.AdapterTrusted(snap.projectRoot, Name)
	if err != nil {
		// Trust check failed (symlink defense, I/O error). Fail-
		// closed: don't block on a check we can't perform, but
		// surface the error to the dispatcher so the operator
		// notices.
		fmt.Fprintf(snap.stderr, "leonard: ground-truth pre-edit: trust check failed, falling through to Pass: %v\n", err)
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}
	if !trusted {
		fmt.Fprintf(snap.stderr,
			"leonard: ground-truth pre-edit: forbidden claim %q would have been rejected; run `leonard config trust ground-truth` to enable blocking. Proceeding.\n",
			hit.Text,
		)
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	reason := buildDenyReason(hit)
	return adapters.PreEditResult{
		Decision:    adapters.Deny,
		Reason:      reason,
		AdapterName: Name,
	}, nil
}

// buildDenyReason renders a deny message that includes both the
// matched claim AND the source rule body (#88). Pre-fix messages
// only carried the rule reference, which forced operators to open
// do-not-claim.md and find the cited rule manually before they could
// understand the block. Including the rule body inline gives Claude
// (and the operator) enough context to choose a rewrite without a
// context switch.
//
// The rule body is truncated to ruleSnippetMax chars to keep the
// permissionDecisionReason field at a reasonable size. Most rules
// are well under that limit; the cap is a safety belt for the
// occasional verbose entry.
func buildDenyReason(hit *Claim) string {
	reason := strings.TrimSpace(hit.RuleReason)
	if len(reason) > ruleSnippetMax {
		reason = reason[:ruleSnippetMax-1] + "…"
	}
	if reason == "" {
		// Defensive: rule has no reason after the em-dash. Fall
		// back to the v0.7 shape with just the citation.
		return fmt.Sprintf("Forbidden claim %q matches rule %s in do-not-claim.md. Rewrite to avoid the claim or update the rule if it is wrong.",
			hit.Text, hit.RulePath)
	}
	return fmt.Sprintf("Forbidden claim %q matches rule %s in do-not-claim.md — %s. Rewrite to avoid the claim or update the rule if it is wrong.",
		hit.Text, hit.RulePath, reason)
}

// ruleSnippetMax caps how much of the rule body we splice into the
// deny reason. 200 chars handles most rules (typical do-not-claim
// entries are short bullets) while keeping the wire-level reason
// field reasonable.
const ruleSnippetMax = 200

// pathIsAbsolute is a tiny local helper to avoid a filepath import
// in this file. The post_edit.go file already imports filepath for
// its own absPath logic; we keep this file lean.
func pathIsAbsolute(p string) bool {
	return len(p) > 0 && (p[0] == '/' || (len(p) > 1 && p[1] == ':'))
}

// joinUnderRoot joins a relative path under root. Pulled out so the
// PreEdit path-resolution mirrors PostEdit's shape without sharing
// import lines.
func joinUnderRoot(root, rel string) string {
	if root == "" {
		return rel
	}
	return root + "/" + rel
}
