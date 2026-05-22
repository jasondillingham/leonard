package groundtruth

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"

	"github.com/jasondillingham/leonard/internal/adapters"
)

// Name is the registry key under which this adapter registers itself.
const Name = "ground-truth"

// GroundTruthAdapter implements adapters.Adapter. v0.6 ships the
// parser layer only: Init reads .leonard/ground-truth/{facts,stories,
// do-not-claim,filters,audit-log}.* into in-memory structures the
// later issues' hook + MCP code will consume. The hook methods are
// no-ops in this version — see the package doc for the issue-by-issue
// breakdown of what gets wired when.
//
// Concurrency: the parser is single-threaded at Init; the hook methods
// only read the parsed structures, never mutate them. Mutation
// (audit-log appends from PostEdit) lands in #29 and will use the
// existing mu.
type GroundTruthAdapter struct {
	mu sync.RWMutex

	projectRoot string
	truthDir    string // absolute, resolved at Init
	stderr      io.Writer
	cfg         Config

	facts          *Facts
	filters        *Filters
	stories        Stories
	rules          Rules
	auditLogExists bool
}

// New returns an uninitialized GroundTruthAdapter. Callers MUST invoke
// Init before any hook or RegisterTools call. Factory shape matches
// adapters.Factory so registry-wired construction works.
func New() adapters.Adapter { return &GroundTruthAdapter{} }

// Name returns the registry key.
func (a *GroundTruthAdapter) Name() string { return Name }

// Init decodes adapter-specific config from cfg.Raw, resolves the
// truth_dir against cfg.ProjectRoot, and parses every file present.
// A completely missing ground-truth/ directory is acceptable — the
// adapter loads with empty structures and the hook methods (once they
// gain behavior in later issues) treat it as "nothing to verify."
//
// Errors are returned for malformed YAML / Markdown — operators need a
// clear line-numbered message when a typo breaks the tree, not a
// silent fall-through.
func (a *GroundTruthAdapter) Init(_ context.Context, cfg adapters.Config) error {
	if cfg.ProjectRoot == "" {
		return errors.New("ground-truth adapter: Init requires ProjectRoot")
	}

	parsed, err := decodeConfig(cfg.Raw)
	if err != nil {
		return err
	}

	truthDir := parsed.TruthDir
	if !filepath.IsAbs(truthDir) {
		truthDir = filepath.Join(cfg.ProjectRoot, ".leonard", truthDir)
	}

	facts, err := loadFacts(filepath.Join(truthDir, "facts.yaml"))
	if err != nil {
		return err
	}
	filters, err := loadFilters(filepath.Join(truthDir, "filters.yaml"))
	if err != nil {
		return err
	}
	stories, err := loadStories(filepath.Join(truthDir, "stories.md"))
	if err != nil {
		return err
	}
	rules, err := loadRules(filepath.Join(truthDir, "do-not-claim.md"))
	if err != nil {
		return err
	}
	hasAudit, err := auditLogPresent(filepath.Join(truthDir, "audit-log.md"))
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.projectRoot = cfg.ProjectRoot
	a.truthDir = truthDir
	a.stderr = cfg.Stderr
	if a.stderr == nil {
		a.stderr = io.Discard
	}
	a.cfg = parsed
	a.facts = facts
	a.filters = filters
	a.stories = stories
	a.rules = rules
	a.auditLogExists = hasAudit
	a.mu.Unlock()

	return nil
}

// Close releases any resources held by the adapter. v0.6 holds none
// (parsed structures live on the GC heap), so this is a no-op. The
// method is here so the contract holds; future versions that hold an
// open audit-log file handle (e.g., #29's append path) will close it
// here.
func (a *GroundTruthAdapter) Close() error { return nil }

// Facts returns the parsed facts.yaml tree. Used by tests and by
// later issues' MCP tools (list_facts in #11, verify_claim in #10).
// Returns nil only before Init.
func (a *GroundTruthAdapter) Facts() *Facts {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.facts
}

// Filters returns the parsed filters.yaml tree.
func (a *GroundTruthAdapter) Filters() *Filters {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.filters
}

// Stories returns the parsed stories.md map. Used by get_story (#12).
func (a *GroundTruthAdapter) Stories() Stories {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.stories
}

// Rules returns the parsed do-not-claim.md rules. Used by check_forbidden
// (later) and by the pre-edit hard guard (#23).
func (a *GroundTruthAdapter) Rules() Rules {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.rules
}

// ResolvedConfig returns the effective adapter config (defaults merged
// with operator overrides). Exposed for tests and for the dispatcher
// to read post-Init.
func (a *GroundTruthAdapter) ResolvedConfig() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

// Detect runs the heuristic claim detector against text using the
// loaded Facts + Rules. Thin wrapper around the package-level Detect
// function — exposed as a method so future call sites (the
// verify_claim MCP tool in #10, the pre-edit hard guard in #23, the
// post-edit advisory logger in #18) only need a *GroundTruthAdapter
// reference, not the underlying Facts / Rules slices.
//
// Returns an empty DetectionResult before Init.
func (a *GroundTruthAdapter) Detect(text string) DetectionResult {
	a.mu.RLock()
	facts := a.facts
	rules := a.rules
	a.mu.RUnlock()
	return Detect(text, facts, rules)
}

// PreEdit is a no-op in v0.6. Hard-deny behavior lands in #23 (forbidden-
// claim guard); advisory pending-audit log lands in #18.
func (a *GroundTruthAdapter) PreEdit(_ context.Context, _ adapters.PreEditPayload) (adapters.PreEditResult, error) {
	return adapters.PreEditResult{Decision: adapters.Pass}, nil
}

// PostEdit is a no-op in v0.6. Advisory pending-audit log lands in
// #18; auto-append to audit-log.md lands in #29.
func (a *GroundTruthAdapter) PostEdit(_ context.Context, _ adapters.PostEditPayload) (adapters.PostEditResult, error) {
	return adapters.PostEditResult{}, nil
}

// SessionStart is a no-op in v0.6. Later issues may inject a
// "loaded N facts / M rules" summary at session start.
func (a *GroundTruthAdapter) SessionStart(_ context.Context, _ adapters.SessionStartPayload) (adapters.SessionStartResult, error) {
	return adapters.SessionStartResult{}, nil
}

// Stop is a no-op in v0.6. Stop hook session summary lands in #30.
func (a *GroundTruthAdapter) Stop(_ context.Context, _ adapters.StopPayload) (adapters.StopResult, error) {
	return adapters.StopResult{}, nil
}

// RegisterTools is implemented in mcp.go. The signature there
// satisfies the adapters.Adapter contract by attaching verify_claim,
// list_facts, and get_story to srv.
