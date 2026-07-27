package groundtruth

import (
	"context"
	"errors"
	"fmt"
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

	// initWarnings holds non-fatal parse errors from Init (malformed
	// facts.yaml, stories.md, etc.). The adapter still loads with empty
	// values for the affected files; warnings are surfaced via
	// SessionStart's AdditionalContext so Claude Code sees them.
	initWarnings []string

	// watchStop / watchDone are the lifecycle channels for the
	// hot-reload polling goroutine (#27). nil before Init starts
	// the watcher; close(watchStop) signals shutdown and
	// <-watchDone confirms the goroutine has exited.
	watchStop chan struct{}
	watchDone chan struct{}
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
// adapter loads with empty structures and the hook methods treat it
// as "nothing to verify."
//
// Parse errors in ground-truth files (malformed YAML, empty story
// names, etc.) are non-fatal: the affected file loads as empty, a
// warning is emitted to stderr, and the warning is stored for
// SessionStart to surface via AdditionalContext. This prevents a
// single typo in stories.md from silently dropping all MCP tools.
func (a *GroundTruthAdapter) Init(_ context.Context, cfg adapters.Config) error {
	if cfg.ProjectRoot == "" {
		return errors.New("ground-truth adapter: Init requires ProjectRoot")
	}

	parsed, err := decodeConfig(cfg.Raw)
	if err != nil {
		return err
	}

	// Canonicalize the project root once, so trust lookups (which
	// hash projectRoot to derive the trust-file path) match no
	// matter how the caller spelled the directory. On macOS
	// t.TempDir() returns the unresolved /var/folders form while
	// os.Getwd() after chdir returns the resolved /private/var/
	// folders form; without canonicalization the two hashes diverge
	// and a trust grant from the CLI doesn't satisfy the adapter's
	// trust check.
	canonRoot := cfg.ProjectRoot
	if resolved, err := filepath.EvalSymlinks(cfg.ProjectRoot); err == nil {
		canonRoot = resolved
	}

	// truth_dir is project-relative by default. The historical
	// implementation force-prepended ".leonard/" before joining,
	// which trapped operators who wanted their truth tree at e.g.
	// "source-of-truth/" at the project root. v0.53 makes the
	// path operator-controlled: relative paths join directly to
	// projectRoot, absolute paths pass through unchanged.
	//
	// Resolved against canonRoot (and symlink-canonicalized itself)
	// so path comparisons against walk results — the session-start
	// truth-dir exclusion — hold on macOS where /var and
	// /private/var name the same directory.
	truthDir := parsed.TruthDir
	if !filepath.IsAbs(truthDir) {
		truthDir = filepath.Join(canonRoot, truthDir)
	}
	if resolved, err := filepath.EvalSymlinks(truthDir); err == nil {
		truthDir = resolved
	}

	// Resolve stderr early so we can write warnings during loading.
	errOut := cfg.Stderr
	if errOut == nil {
		errOut = io.Discard
	}

	// warnLoad calls the loader and, on error, warns to stderr and
	// returns nil so the caller uses the zero value instead of failing.
	var warnings []string
	warnLoad := func(label string, loadErr error) bool {
		if loadErr == nil {
			return false
		}
		msg := fmt.Sprintf("ground-truth partial load: %s: %v — continuing with empty content", label, loadErr)
		fmt.Fprintf(errOut, "leonard: %s\n", msg)
		warnings = append(warnings, msg)
		return true
	}

	facts, err := loadFacts(filepath.Join(truthDir, "facts.yaml"))
	if warnLoad("facts.yaml", err) {
		facts = &Facts{}
	}
	filters, err := loadFilters(filepath.Join(truthDir, "filters.yaml"))
	if warnLoad("filters.yaml", err) {
		filters = &Filters{}
	}
	stories, err := loadStories(filepath.Join(truthDir, "stories.md"))
	if warnLoad("stories.md", err) {
		stories = Stories{}
	}
	rules, err := loadRules(filepath.Join(truthDir, "do-not-claim.md"))
	if warnLoad("do-not-claim.md", err) {
		rules = Rules{}
	}
	hasAudit, err := auditLogPresent(filepath.Join(truthDir, "audit-log.md"))
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.projectRoot = canonRoot
	a.truthDir = truthDir
	a.stderr = errOut
	a.cfg = parsed
	a.facts = facts
	a.filters = filters
	a.stories = stories
	a.rules = rules
	a.auditLogExists = hasAudit
	a.initWarnings = warnings
	a.mu.Unlock()

	// Start the hot-reload watcher (#27). The goroutine polls
	// mtimes every reloadInterval and re-parses on change. Stops
	// when Close is called.
	a.startWatch(context.Background())

	return nil
}

// Close releases any resources held by the adapter. v0.8 (#27)
// stops the hot-reload polling goroutine. Idempotent — a second
// call after the goroutine has already exited is a no-op.
func (a *GroundTruthAdapter) Close() error {
	a.mu.Lock()
	stop := a.watchStop
	done := a.watchDone
	a.watchStop = nil
	a.watchDone = nil
	a.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
	return nil
}

// InitWarnings returns non-fatal parse errors collected during Init
// (e.g. malformed facts.yaml, empty story name). The adapter loaded
// with empty content for each affected file. Callers such as
// `leonard ground-truth lint` should treat a non-empty slice as a
// lint failure.
func (a *GroundTruthAdapter) InitWarnings() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.initWarnings
}

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

// PreEdit is implemented in pre_edit.go. v0.7: hard-deny on forbidden-
// claim matches when the adapter is trusted; warning-to-stderr
// otherwise.

// PostEdit is implemented in post_edit.go. v0.6: advisory writes to
// .leonard/pending-audit.log when the just-written file produces
// UNVERIFIED or FORBIDDEN findings.

// SessionStart is implemented in session_start.go.

// Stop is implemented in stop.go. v0.9 (#30): scans pending-audit.log
// for this session and emits a markdown digest via SystemMessage.

// RegisterTools is implemented in mcp.go. The signature there
// satisfies the adapters.Adapter contract by attaching verify_claim,
// list_facts, and get_story to srv.
