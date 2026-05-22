package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/config"
)

// Loaded carries the set of adapters the dispatcher brought up for a
// single hook or MCP-process lifetime. The Close field releases all
// per-adapter state in reverse order; it MUST be called (defer is
// the typical pattern).
//
// Failures during Init for any single adapter are non-fatal: the
// dispatcher logs to stderr and continues with the adapters that
// did initialize. This matches Leonard's "graceful degradation"
// posture — losing one adapter (e.g., truth files are malformed)
// shouldn't take down the rest of the pipeline.
type Loaded struct {
	Adapters []adapters.Adapter

	// ProjectRoot is the resolved project root (parent of .leonard/).
	// Carried in Loaded so callers don't have to recompute it.
	ProjectRoot string

	// Config is the parsed .leonard/config.toml (or zero-value when
	// no file exists). Hooks read [hooks] tunables from here.
	Config config.Config

	// Close releases all adapter state. Idempotent — safe to call
	// multiple times.
	Close func() error
}

// LoadEnabled instantiates every adapter the project configuration
// asks for, calls Init on each, and returns a Loaded that callers
// must Close.
//
//	projectRoot — absolute path to the project (parent of .leonard/)
//	dataDir     — projectRoot/.leonard
//	stderr      — where each adapter's operational logs go (typically os.Stderr)
//
// The returned Loaded's Close func is safe to defer even when err is
// non-nil — it's set to a no-op in that case.
func LoadEnabled(ctx context.Context, projectRoot, dataDir string, stderr io.Writer) (*Loaded, error) {
	cfg, err := loadConfig(dataDir)
	if err != nil {
		return &Loaded{Close: noopClose}, err
	}

	enabled := cfg.EnabledAdapters(projectRoot)

	out := &Loaded{
		ProjectRoot: projectRoot,
		Config:      cfg,
		Close:       noopClose,
	}
	var initialized []adapters.Adapter
	closeAll := func() error {
		var firstErr error
		for i := len(initialized) - 1; i >= 0; i-- {
			if err := initialized[i].Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	for _, name := range enabled {
		a, err := adapters.New(name)
		if err != nil {
			fmt.Fprintf(stderr, "leonard dispatcher: skip %q: %v\n", name, err)
			continue
		}
		acfg := adapters.Config{
			ProjectRoot: projectRoot,
			Stderr:      stderr,
			Global:      cfg,
		}
		if err := a.Init(ctx, acfg); err != nil {
			fmt.Fprintf(stderr, "leonard dispatcher: %q init failed: %v\n", name, err)
			_ = a.Close() // defensive — Init failure may have partial state
			continue
		}
		initialized = append(initialized, a)
	}

	out.Adapters = initialized
	out.Close = closeAll
	return out, nil
}

// loadConfig reads .leonard/config.toml. Missing file is OK — returns
// a zero Config so auto-detection can still run against the project
// shape. Other I/O / parse errors propagate.
func loadConfig(dataDir string) (config.Config, error) {
	path := filepath.Join(dataDir, config.Filename)
	cfg, err := config.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return config.Config{}, nil
	}
	if err != nil {
		return config.Config{}, fmt.Errorf("dispatcher: load %s: %w", path, err)
	}
	return cfg, nil
}

func noopClose() error { return nil }
