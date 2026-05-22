package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// groundTruthDirName is the path under .leonard/ where the ground-
// truth adapter expects to find its five files. Matches the default
// `truth_dir` value baked into internal/adapters/groundtruth's
// defaultConfig.
const groundTruthDirName = "ground-truth"

// groundTruthTemplates is the empty-but-valid skeleton scaffolded by
// `leonard init --adapter=ground-truth`. Each file parses cleanly
// against the corresponding loader in internal/adapters/groundtruth.
//
// The bodies are intentionally minimal: a one-line header that names
// the file plus an HTML/YAML comment pointing the operator at the
// schema doc. Operators fill these in with real content; the
// scaffold's job is "what shape am I aiming at?", not "here's a
// long template".
var groundTruthTemplates = map[string]string{
	"facts.yaml": `# Facts — what IS true about the project's domain.
# See docs/ROADMAP-v1-ground-truth.md for schema.
#
# Top-level keys are arbitrary; the ground-truth adapter walks the
# tree to verify claims by path lookup. Example:
#
#   tech_stack:
#     primary_language: Go
#   product:
#     name: ExampleSaaS
`,
	"stories.md": `# Canonical stories

Add stories below using "## STORY: <name>" sections. Each story can
have "### Short version", "### Long version", "### Do NOT drift", and
"### Sensitivity" subsections.

## STORY: example

### Short version
Replace this with a 1–3 sentence canonical phrasing of a common claim.

### Long version
Longer paragraph form of the same story.

### Do NOT drift
- Replace this bullet with an anti-drift note about the story.

### Sensitivity
public-safe
`,
	"do-not-claim.md": `# Do Not Claim

Forbidden claims, grouped by category. Use ` + "`- ❌`" + ` bullets:

## Example category

- ❌ "<forbidden text>" — <reason this isn't true / not yet allowed>
`,
	"filters.yaml": `# Filters — strategic rules that affect what gets done.
# See docs/ROADMAP-v1-ground-truth.md for schema.
#
# Example:
#
#   customer_engagement:
#     forbidden_customers:
#       - id: acme-corp
#         reason: "Contract terminated"
#         forbidden_until: 2027-01-01
`,
	"audit-log.md": `# Audit log

Append-only ledger of claim verifications. Entries are written by
the post-edit hook starting in v0.9; v0.6 only checks that this file
is readable when present.
`,
}

// parseAdapterFlag decodes the --adapter value into a set of enabled
// adapter names. Empty input enables "code" (the v0.52 default so
// `leonard init` without the flag behaves as before).
//
// Accepted forms:
//
//	--adapter=code                     (just code, the default)
//	--adapter=ground-truth             (ground-truth only — no code DB)
//	--adapter=code,ground-truth        (both)
//
// Unknown names produce an error rather than silently dropping them
// so a typo doesn't ship an incomplete init.
func parseAdapterFlag(raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]bool{"code": true}, nil
	}
	out := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		switch name {
		case "code", "ground-truth":
			out[name] = true
		case "":
			// Empty segment from a stray comma — ignore.
		default:
			return nil, fmt.Errorf("unknown adapter %q (expected one of: code, ground-truth)", name)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--adapter resolved to an empty set after parsing %q", raw)
	}
	return out, nil
}

// scaffoldGroundTruth writes the five-file ground-truth skeleton
// under dataDir/ground-truth/. Existing files are preserved — the
// scaffold is idempotent so an operator can re-run `leonard init
// --adapter=ground-truth` without losing whatever they've already
// authored.
//
// Returns the list of files that were created (relative to dataDir)
// so the CLI can print a useful "wrote N files" line.
func scaffoldGroundTruth(dataDir string) ([]string, error) {
	gtDir := filepath.Join(dataDir, groundTruthDirName)
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", gtDir, err)
	}

	var created []string
	for name, body := range groundTruthTemplates {
		path := filepath.Join(gtDir, name)
		if _, err := os.Stat(path); err == nil {
			// Already exists — leave the operator's content alone.
			continue
		} else if !os.IsNotExist(err) {
			return created, fmt.Errorf("stat %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return created, fmt.Errorf("write %s: %w", path, err)
		}
		created = append(created, filepath.Join(groundTruthDirName, name))
	}
	return created, nil
}
