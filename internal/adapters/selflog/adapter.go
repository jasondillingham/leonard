package selflog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/config"
)

// Name is the registry key under which SelfLogAdapter registers.
const Name = "self-logging"

// pendingDecisionsLogName is the file under .leonard/ where v0.6's
// advisory draft entries land. Lives alongside leonard.db and
// pending-audit.log — both intermediate operator-facing logs that
// later issues promote into structured stores.
const pendingDecisionsLogName = "pending-decisions.log"

// SelfLogAdapter implements adapters.Adapter. v0.6: PostEdit
// classifies the touched file against the tier policy; for any file
// matching a non-skip tier, drafts a TruthChange entry and appends
// it to .leonard/pending-decisions.log. Other hook methods are
// no-ops — see doc.go.
//
// The classification is purely path-based: no diff inspection, no
// reading the file contents. The draft motivated_by is a placeholder
// pointing the operator at next steps. Real rationale capture comes
// when #25 wires require-tier blocking + operator prompts.
type SelfLogAdapter struct {
	mu          sync.RWMutex
	projectRoot string
	stderr      io.Writer
}

// New returns an uninitialized SelfLogAdapter.
func New() adapters.Adapter { return &SelfLogAdapter{} }

// Name returns the registry key.
func (a *SelfLogAdapter) Name() string { return Name }

// Init records the project root and stderr destination. No on-disk
// resources are opened in v0.6 — the pending-decisions.log is
// O_APPEND-created on each write.
func (a *SelfLogAdapter) Init(_ context.Context, cfg adapters.Config) error {
	if cfg.ProjectRoot == "" {
		return errors.New("self-logging adapter: Init requires ProjectRoot")
	}
	// Canonicalize the project root so trust-marker lookups hash
	// the same path no matter how the caller spelled it. Mirrors
	// the groundtruth adapter's fix.
	canonRoot := cfg.ProjectRoot
	if resolved, err := filepath.EvalSymlinks(cfg.ProjectRoot); err == nil {
		canonRoot = resolved
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.projectRoot = canonRoot
	a.stderr = cfg.Stderr
	if a.stderr == nil {
		a.stderr = io.Discard
	}
	return nil
}

// Close is a no-op. Future versions may hold an open log file
// handle; for now the adapter holds nothing.
func (a *SelfLogAdapter) Close() error { return nil }

// PreEdit classifies the touched file against the tier policy
// (#25):
//
//   - skip      → Pass silently
//   - warn      → Pass + stderr warning
//   - require   → Deny when trusted AND no rationale has been
//                 confirmed; otherwise advisory (warn-style Pass)
//
// "Trusted" means `leonard config trust self-logging` has granted
// the adapter authority to block. Without trust the require tier
// degrades to a warning so an unconfigured install never accidentally
// blocks an operator from working.
//
// "Confirmed" means an operator has either:
//   - Marked the edit trivial via the env var
//     LEONARD_TRUTH_TRIVIAL set to the file path (#26's mechanism),
//     OR
//   - Recorded a rationale via the LEONARD_TRUTH_CONFIRMED_FILES
//     env var (comma-separated paths). v0.7 keeps the confirmation
//     mechanism env-var-based so the CLI surface stays minimal;
//     v0.8 promotes it to a persistent confirmation log.
//
// Each branch's behavior is non-blocking for the file paths the
// guard doesn't apply to (classify() returns ok=false) so edits to
// non-truth files always pass through cleanly.
func (a *SelfLogAdapter) PreEdit(_ context.Context, p adapters.PreEditPayload) (adapters.PreEditResult, error) {
	a.mu.RLock()
	root := a.projectRoot
	stderr := a.stderr
	a.mu.RUnlock()

	if p.FilePath == "" {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	absPath := p.FilePath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(root, absPath)
	}
	rel, ok := projectRelative(root, absPath)
	if !ok {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	_, tier, ok := classify(rel)
	if !ok || tier == TierSkip {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	if tier == TierWarn {
		fmt.Fprintf(stderr,
			"leonard: self-logging: edit to %s is warn-tier — a TruthChange entry will be drafted on completion\n",
			rel,
		)
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	// tier == TierRequire from here on.
	if tok, ok := consumeTrivialToken(root, rel); ok {
		fmt.Fprintf(stderr,
			"leonard: self-logging: trivial bypass consumed for %s (reason: %s)\n",
			rel, tok.TrivialReason,
		)
		if err := appendTrivialDraftEntry(root, p, rel, tok); err != nil {
			fmt.Fprintf(stderr, "leonard: self-logging: append trivial entry: %v\n", err)
		}
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}
	if pathConfirmed(rel, "LEONARD_TRUTH_TRIVIAL") {
		fmt.Fprintf(stderr,
			"leonard: self-logging: edit to %s allowed via LEONARD_TRUTH_TRIVIAL\n",
			rel,
		)
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}
	if pathConfirmed(rel, "LEONARD_TRUTH_CONFIRMED_FILES") {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	trusted, err := config.AdapterTrusted(root, Name)
	if err != nil {
		fmt.Fprintf(stderr,
			"leonard: self-logging: trust check failed, falling through to Pass: %v\n",
			err,
		)
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}
	if !trusted {
		fmt.Fprintf(stderr,
			"leonard: self-logging: %s is REQUIRE-tier; rationale would have been required. Run `leonard config trust self-logging` to enable blocking. Proceeding.\n",
			rel,
		)
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	reason := fmt.Sprintf("Self-logging: edit to %s is require-tier. Record a rationale via `leonard truth-edit %s` or bypass with `LEONARD_TRUTH_TRIVIAL=%s` for trivial fixes.", rel, rel, rel)
	return adapters.PreEditResult{
		Decision:    adapters.Deny,
		Reason:      reason,
		AdapterName: Name,
	}, nil
}

// pathConfirmed reports whether rel appears in the comma-separated
// env var named by varName. Case-sensitive; paths must match exactly
// (the v0.7 env-var mechanism is intentionally strict — no glob, no
// normalization, so the operator's intent is unambiguous).
func pathConfirmed(rel, varName string) bool {
	raw := os.Getenv(varName)
	if raw == "" {
		return false
	}
	for _, p := range strings.Split(raw, ",") {
		if strings.TrimSpace(p) == rel {
			return true
		}
	}
	return false
}

// PostEdit classifies the touched file. When the file matches a
// non-skip tier from the default policy, drafts a TruthChange entry
// and appends one JSON line to .leonard/pending-decisions.log.
//
// The hook is non-blocking: any classification miss, encoding
// error, or write error is logged to stderr and swallowed.
// Production wiring waits for #46 (cmd-binary dispatcher rewiring).
func (a *SelfLogAdapter) PostEdit(_ context.Context, p adapters.PostEditPayload) (adapters.PostEditResult, error) {
	a.mu.RLock()
	root := a.projectRoot
	stderr := a.stderr
	a.mu.RUnlock()

	if p.FilePath == "" {
		return adapters.PostEditResult{}, nil
	}

	absPath := p.FilePath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(root, absPath)
	}
	rel, ok := projectRelative(root, absPath)
	if !ok {
		// Outside project root — not a truth edit we care about.
		return adapters.PostEditResult{}, nil
	}

	scope, tier, ok := classify(rel)
	if !ok {
		return adapters.PostEditResult{}, nil
	}
	if tier == TierSkip {
		return adapters.PostEditResult{}, nil
	}

	entry := pendingDecisionEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		SessionID: p.SessionID,
		Tool:      p.Tool,
		FilePath:  rel,
		Scope:     string(scope),
		Tier:      string(tier),
		Draft: pendingDecisionDraft{
			MotivatedBy: fmt.Sprintf("Auto-drafted on edit to %s. Add rationale before promoting.", rel),
		},
	}

	if err := appendPendingDecision(root, entry); err != nil {
		fmt.Fprintf(stderr, "leonard: self-logging post-edit: %v\n", err)
	}

	return adapters.PostEditResult{}, nil
}

// SessionStart is a no-op in v0.6.
func (a *SelfLogAdapter) SessionStart(_ context.Context, _ adapters.SessionStartPayload) (adapters.SessionStartResult, error) {
	return adapters.SessionStartResult{}, nil
}

// Stop is a no-op in v0.6.
func (a *SelfLogAdapter) Stop(_ context.Context, _ adapters.StopPayload) (adapters.StopResult, error) {
	return adapters.StopResult{}, nil
}

// RegisterTools is a no-op in v0.6. The truth-history MCP tools are
// in #28's scope.
func (a *SelfLogAdapter) RegisterTools(_ *mcp.Server) error { return nil }

// pendingDecisionEntry is the wire shape for one log line. The
// shape mirrors store.TruthChange but adds operator-context (Tier,
// Draft, Tool) so the v0.6 log is self-contained without needing a
// separate decisions DB read.
type pendingDecisionEntry struct {
	Timestamp string               `json:"ts"`
	SessionID string               `json:"session_id,omitempty"`
	Tool      string               `json:"tool,omitempty"`
	FilePath  string               `json:"file_path"`
	Scope     string               `json:"scope"`
	Tier      string               `json:"tier"`
	Draft     pendingDecisionDraft `json:"draft"`
}

// pendingDecisionDraft is the placeholder TruthChange a v0.6
// edit produces. Fields match store.TruthChange field names so an
// operator review (in #28) can promote the entry into the DB
// without remapping.
type pendingDecisionDraft struct {
	MotivatedBy string `json:"motivated_by,omitempty"`
}

func appendPendingDecision(root string, entry pendingDecisionEntry) error {
	if root == "" {
		return errors.New("selflog: projectRoot is empty")
	}
	dataDir := filepath.Join(root, ".leonard")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dataDir, err)
	}
	logPath := filepath.Join(dataDir, pendingDecisionsLogName)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", logPath, err)
	}
	return nil
}

func projectRelative(projectRoot, absPath string) (string, bool) {
	if projectRoot == "" {
		return "", false
	}
	cleanRoot := canonicalizePath(projectRoot)
	cleanPath := canonicalizePath(absPath)
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}

// canonicalizePath returns EvalSymlinks(p) if possible. For paths
// whose leaf or intermediate dirs don't exist yet (PreEdit on a
// Write of a new file), walks up until it finds an existing
// ancestor, resolves that, and rejoins the missing tail components.
// Falls back to Abs(p) only when no ancestor exists.
func canonicalizePath(p string) string {
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
