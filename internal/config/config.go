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

// Config mirrors the schema described in DESIGN.md §4.6. Fields are slices of
// strings so a missing key in the TOML round-trips to an empty slice rather
// than nil-vs-empty ambiguity at the call site.
type Config struct {
	Index     IndexConfig     `toml:"index"`
	Verifiers VerifiersConfig `toml:"verifiers"`
	Hooks     HooksConfig     `toml:"hooks"`
}

// IndexConfig controls which languages the walker dispatches and which paths
// it ignores in addition to .gitignore.
type IndexConfig struct {
	Languages []string `toml:"languages"`
	Ignore    []string `toml:"ignore"`
}

// VerifiersConfig holds the per-language commands the post-edit hook runs
// against the project. The phase-1 hook hard-codes "go vet ./..." but reads
// these for parity with the documented config surface.
type VerifiersConfig struct {
	Go         []string `toml:"go"`
	Python     []string `toml:"python"`
	TypeScript []string `toml:"typescript"`
}

// HooksConfig holds tunables for the Claude Code hook dispatchers.
type HooksConfig struct {
	InjectDecisionsAtSessionStart int  `toml:"inject_decisions_at_session_start"`
	BlockOnFabricatedSymbol       bool `toml:"block_on_fabricated_symbol"`
}

// Default returns the config Leonard writes during `leonard init`. The values
// match DESIGN.md §4.6 verbatim so the file on disk doubles as documentation.
func Default() Config {
	return Config{
		Index: IndexConfig{
			Languages: []string{"go", "python", "typescript"},
			Ignore:    []string{"vendor/", "node_modules/", "dist/", "build/"},
		},
		Verifiers: VerifiersConfig{
			Go:         []string{"go build ./...", "go vet ./..."},
			Python:     []string{"ruff check ."},
			TypeScript: []string{"tsc --noEmit"},
		},
		Hooks: HooksConfig{
			InjectDecisionsAtSessionStart: 10,
			BlockOnFabricatedSymbol:       true,
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
