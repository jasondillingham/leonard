package selflog

import (
	"path/filepath"
	"strings"
)

// Tier is what the self-logging amendment calls a per-file
// enforcement level. v0.6 honors only the "skip" tier in code (it
// causes PostEdit to return without writing a draft); "warn" and
// "require" both log a draft. The strict require-tier blocking
// behavior lands in #25.
type Tier string

const (
	// TierRequire would block the edit without a rationale (when
	// #25 wires it up). v0.6 logs a draft and continues.
	TierRequire Tier = "require"

	// TierWarn logs a draft but does not block. Default for
	// frequently-churning files (facts.yaml, stories.md).
	TierWarn Tier = "warn"

	// TierSkip drops the edit silently — no draft, no log line.
	// Default for audit-log.md (an append-only artifact log that
	// isn't itself truth).
	TierSkip Tier = "skip"
)

// Scope is the truth scope an edit belongs to. Matches the field
// shape used by store.TruthChange (#21).
type Scope string

const (
	// ScopeDomain is .leonard/ground-truth/* — the truth the
	// project verifies artifacts against.
	ScopeDomain Scope = "domain"

	// ScopeToolkit is Leonard's own source / docs / roadmap — the
	// truth Leonard itself codifies.
	ScopeToolkit Scope = "toolkit"
)

// rule is one entry in the tier policy. Order matters: rules are
// evaluated top-down and the first match wins.
type rule struct {
	// match is a glob applied to the project-relative file path.
	// "**" segments use filepath.Match-style globbing. Patterns
	// expand at adapter-init time into compiled forms.
	match string
	scope Scope
	tier  Tier
}

// defaultPolicy is the tier policy from the roadmap amendment's
// "Tiered enforcement policy" section. Operators can override by
// editing the [truth_change_log.policy] block in .leonard/config.
// toml — config decoding lands in #25's enforcement work.
var defaultPolicy = []rule{
	// Domain truth — most-specific paths first.
	{match: ".leonard/ground-truth/audit-log.md", scope: ScopeDomain, tier: TierSkip},
	{match: ".leonard/ground-truth/do-not-claim.md", scope: ScopeDomain, tier: TierRequire},
	{match: ".leonard/ground-truth/filters.yaml", scope: ScopeDomain, tier: TierRequire},
	{match: ".leonard/ground-truth/facts.yaml", scope: ScopeDomain, tier: TierWarn},
	{match: ".leonard/ground-truth/stories.md", scope: ScopeDomain, tier: TierWarn},

	// Toolkit truth.
	{match: "internal/adapters/", scope: ScopeToolkit, tier: TierRequire},
	{match: "internal/trust/", scope: ScopeToolkit, tier: TierRequire},
	{match: "cmd/", scope: ScopeToolkit, tier: TierWarn},
	{match: "docs/ROADMAP", scope: ScopeToolkit, tier: TierWarn},
}

// classify reports the scope + tier for relPath (project-relative).
// Returns ok=false when the path is not under any policy rule, in
// which case PostEdit treats it as not-a-truth-edit and skips
// logging. The match is prefix-based for directory rules (paths
// ending in "/") and substring-based for filename rules — see the
// rule comments in defaultPolicy for the intent of each.
func classify(relPath string) (Scope, Tier, bool) {
	relPath = filepath.ToSlash(relPath)
	for _, r := range defaultPolicy {
		if matches(r.match, relPath) {
			return r.scope, r.tier, true
		}
	}
	return "", "", false
}

// matches reports whether relPath is covered by pat. Three forms:
//
//   - Exact path: pat == relPath
//   - Directory prefix: pat ends in "/" and relPath starts with pat
//   - Substring: pat is a filename or fragment present in relPath
//
// Intentionally simple — operators who want regex-flavored matching
// will get it when #25 lands real config-driven policy.
func matches(pat, relPath string) bool {
	if pat == relPath {
		return true
	}
	if strings.HasSuffix(pat, "/") {
		return strings.HasPrefix(relPath, pat)
	}
	return strings.Contains(relPath, pat)
}
