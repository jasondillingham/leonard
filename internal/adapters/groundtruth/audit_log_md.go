package groundtruth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// auditLogFileName is the operator-facing markdown ledger. Lives
// inside the ground-truth tree (alongside facts/stories/etc.) so
// operators inspecting the truth dir find it naturally.
const auditLogFileName = "audit-log.md"

// appendMarkdownAudit writes one structured section to
// <truthDir>/audit-log.md for the supplied pending-audit entry.
// Per-section shape matches docs/ROADMAP-v1-ground-truth.md's
// "Audit log entries" example:
//
//	## YYYY-MM-DD — <artifact> — <status>
//
//	**Claims made:**
//	- [claim] — VERDICT (provenance)
//	- ...
//
//	**Tool:** Edit
//	**Session:** <session-id>
//
// Empty findings → no section emitted (caller already short-
// circuited but this is defensive).
//
// File is created on first call (with a one-line header above any
// content) and opened O_APPEND on subsequent calls so concurrent
// hook invocations don't clobber each other.
func appendMarkdownAudit(truthDir string, entry pendingAuditEntry) error {
	if truthDir == "" {
		return errors.New("groundtruth: truthDir is empty")
	}
	if len(entry.Findings) == 0 {
		return nil
	}
	if err := os.MkdirAll(truthDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", truthDir, err)
	}
	path := filepath.Join(truthDir, auditLogFileName)

	// Bootstrap the file with a header line if it's missing or
	// empty. Subsequent writes are pure append.
	if info, err := os.Stat(path); errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() == 0) {
		header := "# Audit log\n\nAppend-only ledger of claim verifications. Each section is one Edit/Write.\n\n"
		if err := os.WriteFile(path, []byte(header), 0o644); err != nil {
			return fmt.Errorf("write header to %s: %w", path, err)
		}
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.WriteString(renderAuditMarkdown(entry)); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// renderAuditMarkdown formats one entry as a markdown section. See
// the package-level appendMarkdownAudit doc for the shape.
func renderAuditMarkdown(entry pendingAuditEntry) string {
	var b strings.Builder

	// Section header
	day := entry.Timestamp
	if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
		day = t.UTC().Format("2006-01-02")
	}
	artifact := entry.FilePath
	if artifact == "" {
		artifact = "(unknown)"
	}
	status := summaryStatus(entry.Summary)
	fmt.Fprintf(&b, "## %s — %s — %s\n\n", day, artifact, status)

	if len(entry.Findings) > 0 {
		b.WriteString("**Claims made:**\n")
		for _, h := range entry.Findings {
			b.WriteString("- ")
			b.WriteString(escapeMarkdown(h.Text))
			b.WriteString(" — ")
			b.WriteString(strings.ToUpper(h.Verdict))
			switch h.Verdict {
			case "verified":
				if h.EvidencePath != "" {
					fmt.Fprintf(&b, " (facts.yaml#%s)", h.EvidencePath)
				}
			case "forbidden":
				if h.RulePath != "" {
					fmt.Fprintf(&b, " (do-not-claim.md#%s)", h.RulePath)
				}
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if entry.Tool != "" || entry.SessionID != "" {
		if entry.Tool != "" {
			fmt.Fprintf(&b, "**Tool:** %s  \n", entry.Tool)
		}
		if entry.SessionID != "" {
			fmt.Fprintf(&b, "**Session:** %s  \n", entry.SessionID)
		}
		fmt.Fprintf(&b, "**Timestamp:** %s\n", entry.Timestamp)
		b.WriteString("\n")
	}

	return b.String()
}

// summaryStatus reduces a per-verdict rollup to one of:
//
//	"clean"            — no findings (shouldn't reach here)
//	"flagged"          — at least one unverified
//	"forbidden hit"    — at least one forbidden
func summaryStatus(s pendingAuditRollup) string {
	switch {
	case s.Forbidden > 0:
		return "FORBIDDEN HIT"
	case s.Unverified > 0:
		return "flagged"
	default:
		return "clean"
	}
}

// escapeMarkdown does a minimal pass to keep arbitrary claim text
// from breaking markdown rendering: pipes (table separators), and
// trailing newlines. Keeps the rest verbatim — operators read this
// file so brutalist literalism is fine.
func escapeMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}
