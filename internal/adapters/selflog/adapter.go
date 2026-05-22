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
	a.mu.Lock()
	defer a.mu.Unlock()
	a.projectRoot = cfg.ProjectRoot
	a.stderr = cfg.Stderr
	if a.stderr == nil {
		a.stderr = io.Discard
	}
	return nil
}

// Close is a no-op. Future versions may hold an open log file
// handle; for now the adapter holds nothing.
func (a *SelfLogAdapter) Close() error { return nil }

// PreEdit is a no-op in v0.6. #25 wires require-tier blocking.
func (a *SelfLogAdapter) PreEdit(_ context.Context, _ adapters.PreEditPayload) (adapters.PreEditResult, error) {
	return adapters.PreEditResult{Decision: adapters.Pass}, nil
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
	cleanRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", false
	}
	cleanPath, err := filepath.Abs(absPath)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}
