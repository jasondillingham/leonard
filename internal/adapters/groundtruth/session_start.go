package groundtruth

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jasondillingham/leonard/internal/adapters"
)

const scanCap = 500

var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".next":        true,
	"out":          true,
}

// SessionStart scans project .md files and emits a one-line summary of
// claim findings so operators know Leonard ran and whether any files need
// attention. Silent only when the truth tree is empty (no facts, no rules).
func (a *GroundTruthAdapter) SessionStart(_ context.Context, _ adapters.SessionStartPayload) (adapters.SessionStartResult, error) {
	a.mu.RLock()
	root := a.projectRoot
	facts := a.facts
	rules := a.rules
	stderr := a.stderr
	a.mu.RUnlock()

	if root == "" {
		return adapters.SessionStartResult{}, nil
	}
	if (facts == nil || facts.IsEmpty()) && len(rules) == 0 {
		return adapters.SessionStartResult{}, nil
	}

	targets, capped, err := walkMDFiles(root, scanCap)
	if err != nil || len(targets) == 0 {
		return adapters.SessionStartResult{}, nil
	}
	if capped {
		fmt.Fprintf(stderr, "leonard ground-truth: capped scan at %d .md files; use `leonard list-stale-claims --scope=…` for full coverage\n", scanCap)
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
		AdditionalContext: renderSessionStartSummary(len(targets), filesWithUnverified, filesWithForbidden),
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

func walkMDFiles(root string, cap int) (targets []string, capped bool, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkerr error) error {
		if walkerr != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) == ".md" {
			targets = append(targets, path)
			if len(targets) >= cap {
				capped = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return targets, capped, err
}
