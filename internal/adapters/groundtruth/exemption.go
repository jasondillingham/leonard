package groundtruth

import (
	"path/filepath"
	"strings"
)

// isExemptFromMatcher reports whether the do-not-claim matcher should
// be skipped for the supplied file path.
//
// Two exemption sources, both #84 dogfood findings:
//
//  1. Auto-exempt: files INSIDE truth_dir. The truth tree's own files
//     (do-not-claim.md, facts.yaml, stories.md, audit-log.md) contain
//     the rules themselves as data. Running the matcher against them
//     creates a self-blocking loop — editing a rule's quoted phrase
//     trips on the rule the operator is trying to modify (#84 F1).
//
//  2. Operator-configured: paths matching any of exemptPaths globs.
//     Lets operators declare additional meta-files exempt — dogfood
//     logs, audit notes, project planning docs that legitimately
//     quote forbidden phrases as commentary (#8).
//
// absPath should be the resolved absolute path. projectRoot and
// truthDir should also be resolved (Init canonicalizes them through
// EvalSymlinks so the comparisons line up regardless of /var vs
// /private/var spelling on macOS).
//
// Glob matching uses filepath.Match against the project-relative
// path. Supports * and ?; ** is supported by interpreting any
// "**/" prefix as "match in any subdirectory."
func isExemptFromMatcher(absPath, projectRoot, truthDir string, exemptPaths []string) bool {
	if absPath == "" {
		return false
	}

	// (1) Inside the truth tree → exempt.
	if truthDir != "" {
		// HasPrefix is safe because both paths are canonical
		// absolutes (Init resolved them through EvalSymlinks).
		// Append separator before HasPrefix so /foo/bar doesn't
		// match /foo/barn.
		td := strings.TrimRight(truthDir, "/") + "/"
		if strings.HasPrefix(absPath, td) || absPath == strings.TrimRight(truthDir, "/") {
			return true
		}
	}

	// (2) Operator-configured allowlist. Match globs against the
	//     project-relative path so operators write
	//     "leonard-dogfood.md" not "/abs/path/.../leonard-dogfood.md".
	if len(exemptPaths) == 0 || projectRoot == "" {
		return false
	}
	rel, err := filepath.Rel(projectRoot, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	for _, pattern := range exemptPaths {
		if matchGlob(pattern, rel) {
			return true
		}
	}
	return false
}

// matchGlob extends filepath.Match with "**" path-segment support.
// "**" matches zero or more directory segments (so "docs/**/notes.md"
// matches "docs/notes.md", "docs/x/notes.md", "docs/x/y/notes.md",
// etc.). Without "**", behavior is plain filepath.Match — "audits/*.md"
// matches only direct children of audits/.
func matchGlob(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, _ := filepath.Match(pattern, name)
		return ok
	}
	// Split on "/" into segments, then walk pattern + name in
	// lockstep. "**" segment consumes 0+ name segments.
	pp := strings.Split(pattern, "/")
	np := strings.Split(name, "/")
	return matchSegs(pp, np)
}

// matchSegs is the recursive worker for matchGlob's "**" handling.
// Returns true when the pattern segments fully consume the name
// segments. "**" alternatives are tried by recursion: zero-match
// (skip the **) and one-or-more-match (consume name segment, retry).
func matchSegs(pp, np []string) bool {
	for len(pp) > 0 {
		head := pp[0]
		if head == "**" {
			// Try matching the rest of pp against any suffix of np.
			// "**" at the end matches everything remaining.
			if len(pp) == 1 {
				return true
			}
			for i := 0; i <= len(np); i++ {
				if matchSegs(pp[1:], np[i:]) {
					return true
				}
			}
			return false
		}
		if len(np) == 0 {
			return false
		}
		ok, _ := filepath.Match(head, np[0])
		if !ok {
			return false
		}
		pp, np = pp[1:], np[1:]
	}
	return len(np) == 0
}
