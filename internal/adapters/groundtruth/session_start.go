package groundtruth

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jasondillingham/leonard/internal/adapters"
)

// SessionStart scans project .md files and emits a one-line summary of
// claim findings so operators know Leonard ran and whether any files need
// attention. Silent only when the truth tree is empty (no facts, no rules).
func (a *GroundTruthAdapter) SessionStart(_ context.Context, _ adapters.SessionStartPayload) (adapters.SessionStartResult, error) {
	a.mu.RLock()
	root := a.projectRoot
	facts := a.facts
	rules := a.rules
	stderr := a.stderr
	warnings := a.initWarnings
	a.mu.RUnlock()

	if root == "" {
		return adapters.SessionStartResult{}, nil
	}

	// Surface any partial-load warnings from Init prominently so the
	// operator sees them in-context, not just on MCP stderr.
	var warnPrefix string
	if len(warnings) > 0 {
		var b strings.Builder
		b.WriteString("**Leonard ground-truth: files loaded with errors — fix to restore full coverage:**\n")
		for _, w := range warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
		b.WriteString("\n")
		warnPrefix = b.String()
	}

	if (facts == nil || facts.IsEmpty()) && len(rules) == 0 {
		if warnPrefix != "" {
			return adapters.SessionStartResult{AdditionalContext: warnPrefix}, nil
		}
		return adapters.SessionStartResult{}, nil
	}

	targets, capped, err := walkMDFiles(root, ScanCap, nil)
	if err != nil || len(targets) == 0 {
		if warnPrefix != "" {
			return adapters.SessionStartResult{AdditionalContext: warnPrefix}, nil
		}
		return adapters.SessionStartResult{}, nil
	}
	if capped {
		fmt.Fprintf(stderr, "leonard ground-truth: capped scan at %d .md files; use `leonard list-stale-claims --scope=…` for full coverage\n", ScanCap)
	}

	var filesWithUnverified, filesWithForbidden int
	for _, path := range targets {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		res := a.Detect(string(content))
		if res.Summary.Forbidden > 0 {
			filesWithForbidden++
		} else if res.Summary.Unverified > 0 {
			filesWithUnverified++
		}
	}

	return adapters.SessionStartResult{
		AdditionalContext: warnPrefix + renderSessionStartSummary(len(targets), filesWithUnverified, filesWithForbidden),
	}, nil
}

func renderSessionStartSummary(total, unverified, forbidden int) string {
	if forbidden == 0 && unverified == 0 {
		return fmt.Sprintf("Leonard: %d .md files scanned, no claim findings.", total)
	}
	var parts []string
	if forbidden > 0 {
		parts = append(parts, fmt.Sprintf("%d with forbidden-claim risk", forbidden))
	}
	if unverified > 0 {
		parts = append(parts, fmt.Sprintf("%d with unverified claims", unverified))
	}
	return fmt.Sprintf("Leonard: %d .md files — %s. Run `leonard list-stale-claims` or `leonard check <path>` to investigate.",
		total, strings.Join(parts, ", "))
}
