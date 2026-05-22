package groundtruth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jasondillingham/leonard/internal/adapters"
)

// Stop satisfies adapters.Adapter. v0.9 (#30) emits a session
// summary by scanning .leonard/pending-audit.log for entries
// matching p.SessionID and rolling them into a short markdown
// digest.
//
// The summary is returned via SystemMessage so it surfaces in the
// operator's terminal (not the model context). Empty sessions
// produce a minimal "no claims this session" line rather than a
// wall of headers.
//
// Non-fatal: any read/parse error degrades to an empty result so
// the hook never blocks session end.
func (a *GroundTruthAdapter) Stop(_ context.Context, p adapters.StopPayload) (adapters.StopResult, error) {
	a.mu.RLock()
	root := a.projectRoot
	stderr := a.stderr
	a.mu.RUnlock()

	if root == "" {
		return adapters.StopResult{}, nil
	}

	entries, err := readPendingAuditEntries(root, p.SessionID)
	if err != nil {
		fmt.Fprintf(stderr, "leonard: ground-truth stop: read audit log: %v\n", err)
		return adapters.StopResult{}, nil
	}

	return adapters.StopResult{
		SystemMessage: renderStopSummary(entries),
	}, nil
}

// readPendingAuditEntries returns the entries in
// .leonard/pending-audit.log whose session_id matches sessionID.
// An empty sessionID returns all entries (useful for ad-hoc
// inspection but the Stop hook always passes one).
func readPendingAuditEntries(projectRoot, sessionID string) ([]pendingAuditEntry, error) {
	logPath := filepath.Join(projectRoot, ".leonard", pendingAuditLogName)
	f, err := os.Open(logPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []pendingAuditEntry
	sc := bufio.NewScanner(f)
	// pending-audit lines can carry long claim text; bump the
	// buffer ceiling so a single oversize entry doesn't trip
	// bufio.ErrTooLong.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var entry pendingAuditEntry
		if err := json.Unmarshal(sc.Bytes(), &entry); err != nil {
			// Skip malformed lines rather than failing the whole
			// summary — pending log is best-effort.
			continue
		}
		if sessionID != "" && entry.SessionID != sessionID {
			continue
		}
		out = append(out, entry)
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// renderStopSummary builds the markdown digest. Empty input yields
// a single line; populated input gets a sectioned summary with
// rollup counts, per-file breakdown, and a pointer to
// `leonard truth-story` for the full ledger.
func renderStopSummary(entries []pendingAuditEntry) string {
	if len(entries) == 0 {
		return "leonard ground-truth: no claims flagged this session.\n"
	}

	var (
		totalFindings int
		forbidden     int
		unverified    int
		filesSeen     = map[string]struct{}{}
	)
	// Per-file rollup: file → (forbidden, unverified)
	perFile := map[string][2]int{}

	for _, e := range entries {
		filesSeen[e.FilePath] = struct{}{}
		fc := perFile[e.FilePath]
		for _, h := range e.Findings {
			totalFindings++
			switch h.Verdict {
			case "forbidden":
				forbidden++
				fc[0]++
			case "unverified":
				unverified++
				fc[1]++
			}
		}
		perFile[e.FilePath] = fc
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Ground-truth session summary\n\n")
	fmt.Fprintf(&b, "- **%d** finding(s) across **%d** file(s): %d forbidden, %d unverified.\n\n",
		totalFindings, len(filesSeen), forbidden, unverified)

	if len(perFile) > 0 {
		b.WriteString("### By file\n\n")
		// Sort for stable rendering.
		paths := make([]string, 0, len(perFile))
		for p := range perFile {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fc := perFile[p]
			fmt.Fprintf(&b, "- `%s`: %d forbidden, %d unverified\n", p, fc[0], fc[1])
		}
		b.WriteString("\n")
	}

	b.WriteString("Run `leonard truth-story` for the full ledger narrative; `leonard truth-history <file>` for per-file history.\n")
	return b.String()
}
