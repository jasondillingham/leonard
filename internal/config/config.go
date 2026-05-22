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
	Hooks    HooksConfig     `toml:"hooks"`
	PostEdit PostEditConfig  `toml:"post_edit"`
	Adapters []AdapterConfig `toml:"adapters"`

	// GroundTruth holds settings for the ground-truth adapter when
	// it's enabled (either via an explicit [[adapters]] block of
	// type "ground-truth" OR via auto-detection when truth_dir is
	// set or .leonard/ground-truth/ exists). Decoded from
	// [adapters.ground-truth].
	GroundTruth GroundTruthConfig `toml:"adapters.ground-truth"`
}

// AdapterConfig is one [[adapters]] entry. v1.0 ships three adapter
// types: "code", "ground-truth", "self-logging". The Type field is
// the lookup key into the adapter registry; if no [[adapters]]
// blocks are present, the dispatcher auto-detects (go.mod or
// post_edit.verify → code; truth_dir set or .leonard/ground-truth/
// exists → ground-truth).
type AdapterConfig struct {
	Type string `toml:"type"`
}

// EnabledAdapters returns the list of adapter type names the
// dispatcher should load for projectRoot, based on the parsed
// config. When [[adapters]] blocks are present, returns their Type
// values in declared order. Otherwise auto-detects:
//
//   - "code" when go.mod exists at projectRoot OR
//     [post_edit.verify].command is set
//   - "ground-truth" when [adapters.ground-truth].truth_dir is set
//     OR .leonard/ground-truth/ exists
//
// Self-logging is never auto-enabled; operators opt in via an
// explicit [[adapters]] block (it's a discipline, not a default).
//
// Returns at minimum []string{"code"} so a project with no config
// and no go.mod still keeps the v0.52 fabrication-guard surface.
// The "code" adapter degrades gracefully on a missing DB (see its
// Init for the permissive-store fallback) so the no-go-mod case
// just no-ops every hook rather than blocking edits.
func (c *Config) EnabledAdapters(projectRoot string) []string {
	if len(c.Adapters) > 0 {
		out := make([]string, 0, len(c.Adapters))
		seen := map[string]bool{}
		for _, a := range c.Adapters {
			if a.Type == "" || seen[a.Type] {
				continue
			}
			seen[a.Type] = true
			out = append(out, a.Type)
		}
		if len(out) == 0 {
			return []string{"code"}
		}
		return out
	}

	var enabled []string

	// "code" — auto when go.mod is present at root OR an operator-
	// trusted verifier is configured. Also the always-on fallback
	// so the v0.52 surface keeps working on projects that haven't
	// opted into anything.
	hasGoMod := false
	if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err == nil {
		hasGoMod = true
	}
	hasVerifier := c.PostEdit.Verify.Command != ""
	if hasGoMod || hasVerifier {
		enabled = append(enabled, "code")
	}

	// "ground-truth" — auto when the operator has set truth_dir OR
	// the default truth dir exists on disk. Don't auto-enable when
	// neither signal is present so projects that haven't opted into
	// ground-truth don't pay the parse cost.
	gtDir := c.GroundTruth.TruthDir
	if gtDir == "" {
		// Use the default location for the auto-detect probe.
		gtDir = filepath.Join(".leonard", "ground-truth")
	}
	if _, err := os.Stat(filepath.Join(projectRoot, gtDir)); err == nil {
		enabled = append(enabled, "ground-truth")
	} else if c.GroundTruth.TruthDir != "" {
		// Operator explicitly configured a truth_dir that doesn't
		// exist (yet). Still enable — the adapter handles missing
		// files gracefully and the operator presumably plans to
		// populate it.
		enabled = append(enabled, "ground-truth")
	}

	if len(enabled) == 0 {
		return []string{"code"}
	}
	return enabled
}

// GroundTruthConfig mirrors what internal/adapters/groundtruth/config.go
// reads off the [adapters.ground-truth] block. Duplicated here so the
// dispatcher can decide whether ground-truth should auto-enable
// without importing the adapter package (cycle avoidance — config
// is imported BY adapter packages, so it can't import them).
type GroundTruthConfig struct {
	// TruthDir is the project-relative directory where the truth
	// tree lives. Empty means "use the default .leonard/ground-truth/".
	TruthDir string `toml:"truth_dir"`
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
