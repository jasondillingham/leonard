package code

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/config"
	"github.com/jasondillingham/leonard/internal/index"
	leonardmcp "github.com/jasondillingham/leonard/internal/mcp"
	"github.com/jasondillingham/leonard/internal/store"
)

// Name is the registry key under which CodeAdapter registers itself.
// Use this constant rather than the literal string so a typo elsewhere
// in the codebase produces a compile error.
const Name = "code"

// CodeAdapter implements adapters.Adapter for Leonard's existing
// code-symbol surface. It is intentionally a thin shim over the
// internal/hooks and internal/mcp packages — see the package doc for
// the rationale.
//
// Lifecycle: Init opens the project's SQLite store (or sets up degraded
// "no DB" mode when leonard init hasn't run yet); subsequent hook
// methods reuse the cached handle; Close releases it.
//
// Concurrency: the hook methods are safe to call from multiple
// goroutines once Init has returned. Close acquires a write lock and
// nil's the cached handles so a stray call after shutdown returns a
// permissive no-op rather than a panic.
type CodeAdapter struct {
	mu sync.RWMutex

	projectRoot  string
	stderr       io.Writer
	cfg          config.Config
	modulePath   string
	hasGoMod     bool
	hasLeonardDB bool

	// storeHandle is the single SQLite handle shared by every hook
	// method. nil in degraded mode (no .leonard/leonard.db); the
	// individual hook methods substitute a permissive/no-op store in
	// that case.
	storeHandle  *store.Store
	storeAdapter *leonardmcp.StoreAdapter // built once for RegisterTools
}

// New returns an uninitialized CodeAdapter. Callers (the dispatcher,
// tests, or whatever wires the registry) MUST invoke Init before any
// hook or RegisterTools call.
//
// Factory shape matches adapters.Factory so registry-wired construction
// works.
func New() adapters.Adapter { return &CodeAdapter{} }

// Name returns the registry key. Required by adapters.Adapter.
func (a *CodeAdapter) Name() string { return Name }

// Init resolves the project root from cfg, stats .leonard/leonard.db,
// and opens the store when present. A missing DB is NOT an error — the
// code adapter degrades to a permissive mode in which:
//
//   - PreEdit reports every symbol as present (no fabrication-guard
//     coverage until the project is indexed)
//   - PostEdit / SessionStart / Stop emit a no-op response with a one-
//     line operator hint to stderr
//   - RegisterTools registers tools backed by a no-op store
//
// This matches the existing cmd/leonard-hook + cmd/leonard-mcp
// behavior on a fresh checkout that hasn't run `leonard init`.
func (a *CodeAdapter) Init(_ context.Context, cfg adapters.Config) error {
	if cfg.ProjectRoot == "" {
		return errors.New("code adapter: Init requires ProjectRoot")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.projectRoot = cfg.ProjectRoot
	a.cfg = cfg.Global
	a.stderr = cfg.Stderr
	if a.stderr == nil {
		a.stderr = io.Discard
	}
	a.modulePath = modulePathFromRoot(cfg.ProjectRoot)
	a.hasGoMod = a.modulePath != ""

	dbPath := filepath.Join(cfg.ProjectRoot, ".leonard", "leonard.db")
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		// Degraded mode: no DB yet. Hook methods will short-circuit.
		a.hasLeonardDB = false
		return nil
	} else if err != nil {
		return err
	}
	a.hasLeonardDB = true

	s, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	a.storeHandle = s
	a.storeAdapter = leonardmcp.NewStoreAdapter(s)
	if err := a.storeAdapter.WatchDatabase(dbPath); err != nil {
		// WatchDatabase failure means the inode is unreadable. Close
		// what we just opened and surface — the alternative is to
		// silently disable the swap-detection guard.
		_ = s.Close()
		a.storeHandle = nil
		a.storeAdapter = nil
		return err
	}
	return nil
}

// Close releases the cached store handle, triggering the
// wal_checkpoint(PASSIVE) in store.Close that bughunt-9 F3 added.
// Idempotent: a second or third call returns nil without touching the
// already-released handle.
func (a *CodeAdapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.storeHandle == nil {
		return nil
	}
	err := a.storeHandle.Close()
	a.storeHandle = nil
	a.storeAdapter = nil
	return err
}

// snapshot returns the bits hook methods need under a read lock so
// PreEdit/PostEdit/etc don't have to lock-and-juggle. The returned
// store handle is nil iff the adapter is in degraded mode.
func (a *CodeAdapter) snapshot() snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return snapshot{
		projectRoot:  a.projectRoot,
		stderr:       a.stderr,
		cfg:          a.cfg,
		modulePath:   a.modulePath,
		hasGoMod:     a.hasGoMod,
		hasLeonardDB: a.hasLeonardDB,
		storeHandle:  a.storeHandle,
		storeAdapter: a.storeAdapter,
	}
}

// snapshot is an unexported value type so internal/code's hook method
// files (pre_edit.go, post_edit.go, ...) can access the cached
// initialization state without re-locking. CodeAdapter.snapshot()
// publishes it under the read lock.
type snapshot struct {
	projectRoot  string
	stderr       io.Writer
	cfg          config.Config
	modulePath   string
	hasGoMod     bool
	hasLeonardDB bool
	storeHandle  *store.Store
	storeAdapter *leonardmcp.StoreAdapter
}

// indexer returns a hooks.Indexer backed by the cached store handle.
// In degraded mode (no DB) returns nil so PostEdit can short-circuit.
func (s snapshot) indexer() *index.Indexer {
	if s.storeHandle == nil {
		return nil
	}
	return index.New(s.storeHandle, s.projectRoot)
}

// modulePathFromRoot reads go.mod at projectRoot and returns the
// module path. Replicated from cmd/leonard-hook/pre_edit.go so the
// adapter doesn't pull cmd/* as a dependency. A missing or malformed
// go.mod yields "" — the pre-edit handler treats that as "no
// fabrication-guard module context" and degrades to permissive.
func modulePathFromRoot(projectRoot string) string {
	data, err := os.ReadFile(filepath.Join(projectRoot, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "module")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "" {
			continue
		}
		return strings.Trim(rest, "\"")
	}
	return ""
}
