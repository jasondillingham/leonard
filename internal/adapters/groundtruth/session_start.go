package groundtruth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jasondillingham/leonard/internal/adapters"
)

// sessionStartScanBudget bounds the wall-clock time the session-start
// claim scan may spend across all files. incident-1: an unbounded scan
// over a project with 95 fuzzy rules and a 834 KB machine-generated
// .md file ran for hours at full CPU, orphaned past Claude Code's hook
// timeout. The budget is checked between files; a partial scan reports
// how far it got.
const sessionStartScanBudget = 5 * time.Second

// maxScanFileBytes caps the size of a single .md file the session-start
// scan will read. Files past the cap are almost always generated
// artifacts (logs, exports), not operator prose, and they dominate
// detector cost. Skipped files are counted and surfaced on stderr.
const maxScanFileBytes = 1 << 20 // 1 MiB

// SessionStart scans project .md files and emits a one-line summary of
// claim findings so operators know Leonard ran and whether any files need
// attention. Silent only when the truth tree is empty (no facts, no rules).
func (a *GroundTruthAdapter) SessionStart(ctx context.Context, _ adapters.SessionStartPayload) (adapters.SessionStartResult, error) {
	a.mu.RLock()
	root := a.projectRoot
	truthDir := a.truthDir
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

	// The truth dir is excluded from the scan: it is the evidence the
	// detector checks claims against, so scanning it is circular
	// (do-not-claim.md matches its own rules by definition) — and its
	// audit-log.md grows without bound (incident-1).
	targets, capped, err := walkMDFiles(root, ScanCap, nil, truthDir)
	if err != nil || len(targets) == 0 {
		if warnPrefix != "" {
			return adapters.SessionStartResult{AdditionalContext: warnPrefix}, nil
		}
		return adapters.SessionStartResult{}, nil
	}
	if capped {
		fmt.Fprintf(stderr, "leonard ground-truth: capped scan at %d .md files; use `leonard list-stale-claims --scope=…` for full coverage\n", ScanCap)
	}

	deadline := time.Now().Add(sessionStartScanBudget)
	var filesWithUnverified, filesWithForbidden, scanned, oversize int
	truncated := false
	for _, path := range targets {
		if ctx.Err() != nil || time.Now().After(deadline) {
			truncated = true
			break
		}
		if info, statErr := os.Stat(path); statErr == nil && info.Size() > maxScanFileBytes {
			oversize++
			continue
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		res := a.Detect(string(content))
		scanned++
		if res.Summary.Forbidden > 0 {
			filesWithForbidden++
		} else if res.Summary.Unverified > 0 {
			filesWithUnverified++
		}
	}
	if oversize > 0 {
		fmt.Fprintf(stderr, "leonard ground-truth: skipped %d .md file(s) over %d bytes; use `leonard check <path>` to scan them explicitly\n", oversize, maxScanFileBytes)
	}

	summary := renderSessionStartSummary(scanned, filesWithUnverified, filesWithForbidden)
	if truncated {
		fmt.Fprintf(stderr, "leonard ground-truth: session-start scan budget (%s) exhausted after %d of %d files\n", sessionStartScanBudget, scanned, len(targets))
		summary += fmt.Sprintf(" (partial: %d of %d files scanned within the %s session-start budget)", scanned, len(targets), sessionStartScanBudget)
	}

	return adapters.SessionStartResult{
		AdditionalContext: warnPrefix + summary,
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
