package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/jasondillingham/leonard/internal/config"
)

// loadGroundTruthRaw returns the per-adapter config block for the
// ground-truth adapter, lifted from .leonard/config.toml's
// [[adapters]] array. Returns (nil, nil) when no config file exists
// or no ground-truth block is configured — callers should treat
// nil as "use the adapter's defaults."
//
// Used by `leonard check`, `leonard ground-truth lint`, and
// `leonard ground-truth stats` so the operator's truth_dir setting
// is honored. Without this, the CLIs default to the v0.6 hardcoded
// "ground-truth/" path and miss any operator who configured
// truth_dir elsewhere (e.g., "source-of-truth/" for the canonical
// personal-artifacts use case).
func loadGroundTruthRaw(dataDir string) (map[string]any, error) {
	path := filepath.Join(dataDir, config.Filename)
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	for _, a := range cfg.Adapters {
		if a.Type == "ground-truth" {
			return a.Raw, nil
		}
	}
	return nil, nil
}
