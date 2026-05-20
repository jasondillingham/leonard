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
//   - [index].languages, [index].ignore: language dispatch is
//     extension-driven; ignore paths are handled by .gitignore +
//     .leonardignore at the project root.
//   - [hooks].block_on_fabricated_symbol: the fabrication guard is
//     core to the project's purpose; an opt-out knob is a footgun.
//
// What remains are the knobs that actually wire through:
//   - [hooks].inject_decisions_at_session_start (read by SessionStart)
//   - [hooks].surface_unverified_claims_at_stop (read by Stop)
//   - [post_edit.verify] (read by PostToolUse) — opt-in, explicit per-
//     project verifier command. When unset, the hook falls back to the
//     `go vet ./...` auto-detection that has been there since v0.1. This
//     satisfies the original Bughunt-2 concern (no hidden defaults that
//     vanish on `leonard init` re-runs) because the command is project-
//     authored TOML, not a discovered default.
type Config struct {
	Hooks    HooksConfig    `toml:"hooks"`
	PostEdit PostEditConfig `toml:"post_edit"`
}

// HooksConfig holds tunables for the Claude Code hook dispatchers.
type HooksConfig struct {
	InjectDecisionsAtSessionStart int `toml:"inject_decisions_at_session_start"`
	SurfaceUnverifiedClaimsAtStop int `toml:"surface_unverified_claims_at_stop"`
}

// PostEditConfig groups PostToolUse-hook tunables.
type PostEditConfig struct {
	Verify VerifyConfig `toml:"verify"`
}

// VerifyConfig opts into a project-defined post-edit verifier. When
// Command is set, the PostToolUse hook runs it through `sh -c` after
// every Edit/Write and records the outcome in the claims ledger,
// replacing the default `go vet ./...` auto-detection. When Command
// is empty (the zero value), the hook keeps its v0.1 behavior:
// `go vet ./...` if a `go.mod` is present at the project root,
// "skipped (no go.mod)" otherwise.
//
// The command is passed verbatim to `sh -c` so standard quoting and
// composition (`&&`, `|`, env vars) work. It runs with cwd =
// WorkingDir if set, else the project root.
type VerifyConfig struct {
	// Command is the verifier invoked after each Edit/Write. Examples:
	//   command = "cargo check --workspace"
	//   command = "pnpm tsc --noEmit"
	//   command = "ruff check . && mypy ."
	// Empty disables the feature.
	Command string `toml:"command"`

	// WorkingDir overrides the directory the verifier runs in. Most
	// projects want this empty (= project root). Useful when the
	// verifier lives in a subdirectory of a polyrepo.
	WorkingDir string `toml:"working_dir"`

	// Timeout caps how long the verifier may run. Parsed via
	// time.ParseDuration ("30s", "2m", etc.). Empty defaults to 60s —
	// generous enough for `cargo check` cold builds while still
	// bounding pathological hangs.
	Timeout string `toml:"timeout"`
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
