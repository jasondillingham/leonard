package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Filename is the path of the project-local Leonard config, relative to a
// .leonard/ directory.
const Filename = "config.toml"

// Config holds the project-local Leonard tunables. Bughunt-2 Theme A
// trimmed this schema to only the fields actually consumed at runtime:
//
//   - [verifiers]: the post-edit hook hardcodes `go vet ./...`; per-
//     language verifier customization is a separate feature, not a
//     hidden default that vanishes on `leonard init` re-runs.
//   - [index].languages, [index].ignore: language dispatch is
//     extension-driven; ignore paths are handled by .gitignore +
//     .leonardignore at the project root.
//   - [hooks].block_on_fabricated_symbol: the fabrication guard is
//     core to the project's purpose; an opt-out knob is a footgun.
//
// What remains are the two knobs that actually wire through:
// inject_decisions_at_session_start (read by SessionStart) and
// surface_unverified_claims_at_stop (read by Stop).
type Config struct {
	Hooks HooksConfig `toml:"hooks"`
}

// HooksConfig holds tunables for the Claude Code hook dispatchers.
type HooksConfig struct {
	InjectDecisionsAtSessionStart int `toml:"inject_decisions_at_session_start"`
	SurfaceUnverifiedClaimsAtStop int `toml:"surface_unverified_claims_at_stop"`
}

// Default returns the config Leonard writes during `leonard init`.
func Default() Config {
	return Config{
		Hooks: HooksConfig{
			InjectDecisionsAtSessionStart: 10,
			SurfaceUnverifiedClaimsAtStop: 20,
		},
	}
}

// Load reads and decodes config.toml at path. A missing file is reported via
// fs.ErrNotExist (use errors.Is) so callers can distinguish "never inited"
// from "config is malformed".
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return c, nil
}

// LoadOrDefault returns Default() when path does not exist, decodes it
// otherwise. Used by hook handlers that must not abort if init hasn't been
// run yet.
func LoadOrDefault(path string) (Config, error) {
	c, err := Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	return c, err
}

// Save encodes c to path, creating the parent directory if necessary.
func Save(c Config, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}
