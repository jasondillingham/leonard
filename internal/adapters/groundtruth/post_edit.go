package groundtruth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jasondillingham/leonard/internal/adapters"
)

// pendingAuditLogName is the file under .leonard/ where v0.6's
// advisory hook records UNVERIFIED + FORBIDDEN findings. Lives
// alongside leonard.db rather than inside ground-truth/ because it's
// an intermediate operator-facing log, not part of the canonical
// truth tree. v0.9 promotes the writing path to audit-log.md.
const pendingAuditLogName = "pending-audit.log"

// maxAuditPayloadBytes caps the size of file content read for
// detection. A 1 MiB ceiling matches Leonard's existing snippet cap;
// claim detection on multi-MB binaries would be wasteful and
// produces no useful signal.
const maxAuditPayloadBytes = 1 << 20

// PostEdit reads the just-written file, runs Detect against it, and
// appends one JSON-per-line entry to .leonard/pending-audit.log when
// the result has any UNVERIFIED or FORBIDDEN findings. Verified and
// Opinion verdicts are dropped — the log is for findings that need
// operator attention.
//
// The hook is non-blocking: any read / detect / write error is
// logged to stderr and swallowed. PostEdit must never reject; the
// hard pre-edit guard for forbidden claims is #23's job.
//
// In v0.6 the cmd/leonard-hook binary doesn't yet dispatch through
// the adapter (#46 deferred). Until then PostEdit is exercised by
// tests; production wiring lands when #46 merges.
func (a *GroundTruthAdapter) PostEdit(_ context.Context, p adapters.PostEditPayload) (adapters.PostEditResult, error) {
	snap := a.snapshot()
	if snap.facts == nil && len(snap.rules) == 0 {
		return adapters.PostEditResult{}, nil
	}
	if p.FilePath == "" {
		return adapters.PostEditResult{}, nil
	}

	absPath := p.FilePath
	if !filepath.IsAbs(absPath) {
		root := snap.projectRoot
		if root == "" {
			return adapters.PostEditResult{}, nil
		}
		absPath = filepath.Join(root, absPath)
	}

	if !insideProject(snap.projectRoot, absPath) {
		fmt.Fprintf(snap.stderr, "leonard: ground-truth post-edit skipped — %s is outside project root\n", absPath)
		return adapters.PostEditResult{}, nil
	}

	if !matchesAnyGlob(absPath, snap.cfg.VerifyTargets) {
		return adapters.PostEditResult{}, nil
	}

	content, err := readCappedFile(absPath, maxAuditPayloadBytes)
	if err != nil {
		fmt.Fprintf(snap.stderr, "leonard: ground-truth post-edit: read %s: %v\n", absPath, err)
		return adapters.PostEditResult{}, nil
	}

	res := Detect(string(content), snap.facts, snap.rules)
	findings := filterFindings(res.Claims)
	if len(findings) == 0 {
		return adapters.PostEditResult{}, nil
	}

	entry := pendingAuditEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		SessionID: p.SessionID,
		Tool:      p.Tool,
		FilePath:  relativeOrAbs(snap.projectRoot, absPath),
		Summary:   summaryFromResult(res),
		Findings:  findings,
	}

	if err := appendPendingAudit(snap.projectRoot, entry); err != nil {
		fmt.Fprintf(snap.stderr, "leonard: ground-truth post-edit: append audit log: %v\n", err)
	}

	return adapters.PostEditResult{}, nil
}

// snapshot returns a read-locked copy of the adapter's load-time
// state. The post-edit path reads this once at entry to avoid
// holding the lock during disk I/O.
type postEditSnapshot struct {
	projectRoot string
	stderr      io.Writer
	cfg         Config
	facts       *Facts
	rules       Rules
}

func (a *GroundTruthAdapter) snapshot() postEditSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return postEditSnapshot{
		projectRoot: a.projectRoot,
		stderr:      a.stderr,
		cfg:         a.cfg,
		facts:       a.facts,
		rules:       a.rules,
	}
}

// pendingAuditEntry is the JSON shape of one log line. Mirrors what
// v0.9's audit-log.md entries will eventually carry — keeping them
// aligned now means the v0.9 promotion is a path/file rename rather
// than a schema rewrite.
type pendingAuditEntry struct {
	Timestamp string             `json:"ts"`
	SessionID string             `json:"session_id,omitempty"`
	Tool      string             `json:"tool,omitempty"`
	FilePath  string             `json:"file_path"`
	Summary   pendingAuditRollup `json:"summary"`
	Findings  []pendingAuditHit  `json:"findings"`
}

type pendingAuditRollup struct {
	Total      int `json:"total"`
	Verified   int `json:"verified"`
	Unverified int `json:"unverified"`
	Forbidden  int `json:"forbidden"`
	Opinion    int `json:"opinion"`
}

type pendingAuditHit struct {
	Text         string `json:"text"`
	Category     string `json:"category"`
	Verdict      string `json:"verdict"`
	EvidencePath string `json:"evidence_path,omitempty"`
	RulePath     string `json:"rule_path,omitempty"`
	RuleText     string `json:"rule_text,omitempty"`
	StartByte    int    `json:"start_byte"`
	EndByte      int    `json:"end_byte"`
}

// filterFindings drops Verified and Opinion verdicts. The audit log
// is for operator-actionable findings — verified claims and opinion
// statements aren't.
func filterFindings(claims []Claim) []pendingAuditHit {
	out := make([]pendingAuditHit, 0, len(claims))
	for _, c := range claims {
		if c.Verdict != VerdictUnverified && c.Verdict != VerdictForbidden {
			continue
		}
		out = append(out, pendingAuditHit{
			Text:         c.Text,
			Category:     c.Category,
			Verdict:      c.Verdict.String(),
			EvidencePath: c.EvidencePath,
			RulePath:     c.RulePath,
			RuleText:     c.RuleText,
			StartByte:    c.StartByte,
			EndByte:      c.EndByte,
		})
	}
	return out
}

func summaryFromResult(r DetectionResult) pendingAuditRollup {
	return pendingAuditRollup{
		Total:      r.Summary.Total,
		Verified:   r.Summary.Verified,
		Unverified: r.Summary.Unverified,
		Forbidden:  r.Summary.Forbidden,
		Opinion:    r.Summary.Opinion,
	}
}

// appendPendingAudit writes one JSON line to .leonard/pending-audit.log.
// The file is created on first write and opened O_APPEND on each
// subsequent call so concurrent hook invocations don't clobber each
// other.
func appendPendingAudit(projectRoot string, entry pendingAuditEntry) error {
	if projectRoot == "" {
		return errors.New("groundtruth: projectRoot is empty")
	}
	dataDir := filepath.Join(projectRoot, ".leonard")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dataDir, err)
	}
	logPath := filepath.Join(dataDir, pendingAuditLogName)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal audit entry: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", logPath, err)
	}
	return nil
}

// insideProject reports whether absPath resolves under projectRoot.
// Both paths are passed through filepath.EvalSymlinks before
// comparison so an unresolved-vs-resolved spelling of the same
// directory (the macOS /var ↔ /private/var case) lines up rather
// than diverging through filepath.Rel. Falls back to filepath.Abs
// for paths that don't exist on disk yet (Write of a new file).
func insideProject(projectRoot, absPath string) bool {
	if projectRoot == "" {
		return false
	}
	cleanRoot := canonicalize(projectRoot)
	cleanPath := canonicalize(absPath)
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}

// canonicalize returns EvalSymlinks(p) if possible. For paths whose
// leaf or intermediate dirs don't exist yet (PreEdit on a Write of a
// new file in a subdirectory), walks up until it finds an existing
// ancestor, resolves that, and rejoins the missing tail components.
// Used by insideProject / relativeOrAbs so they line up regardless
// of whether the file is on disk yet.
func canonicalize(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	var tail []string
	cur := abs
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// matchesAnyGlob reports whether absPath matches any pattern in
// patterns. Empty patterns slice means "match everything" so a
// minimally-configured adapter still produces audit entries.
func matchesAnyGlob(absPath string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	base := filepath.Base(absPath)
	for _, pat := range patterns {
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
		if ok, _ := filepath.Match(pat, absPath); ok {
			return true
		}
	}
	return false
}

func readCappedFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

func relativeOrAbs(projectRoot, absPath string) string {
	if projectRoot == "" {
		return absPath
	}
	cleanRoot := canonicalize(projectRoot)
	cleanPath := canonicalize(absPath)
	if rel, err := filepath.Rel(cleanRoot, cleanPath); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return absPath
}
