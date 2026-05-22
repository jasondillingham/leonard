package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/jasondillingham/leonard/internal/adapters/groundtruth/sync"
	"github.com/jasondillingham/leonard/internal/config"
)

// syncConfig is the cmd/leonard-side mirror of the sync plugin
// section of .leonard/config.toml. Decoded lazily on each
// `leonard sync` invocation; not stored elsewhere.
//
// Example config.toml block:
//
//	[sync.github]
//	command = "/usr/local/bin/leonard-sync-github"
//	[sync.github.config]
//	poll_interval = "daily"
type syncConfig struct {
	Sync map[string]syncPluginConfig `toml:"sync"`
}

type syncPluginConfig struct {
	Command string         `toml:"command"`
	Config  map[string]any `toml:"config"`
}

func newSyncCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync [plugin]",
		Short: "Run configured sync plugins to refresh facts.yaml.",
		Long: `Invokes external sync plugins that refresh facts.yaml against
authoritative sources (GitHub API, CRM, monitoring dashboards).

Without a plugin argument, runs every plugin configured in
.leonard/config.toml under [sync.<name>] blocks. With an argument,
runs only the named plugin.

Subcommands:
  list             Show configured plugins
  <plugin>         Run that plugin (or all when omitted)

Flags:
  --dry-run        Print proposed changes without writing facts.yaml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			cfg, err := loadSyncConfig(dataDir)
			if err != nil {
				return fmt.Errorf("sync: %w", err)
			}
			if len(cfg.Sync) == 0 {
				return errors.New("sync: no plugins configured (add [sync.<name>] blocks to .leonard/config.toml)")
			}

			var targets []string
			if len(args) == 1 {
				if _, ok := cfg.Sync[args[0]]; !ok {
					return fmt.Errorf("sync: unknown plugin %q (known: %s)", args[0], strings.Join(syncNames(cfg), ", "))
				}
				targets = []string{args[0]}
			} else {
				targets = syncNames(cfg)
			}

			truthDir := filepath.Join(dataDir, "ground-truth")
			factsPath := filepath.Join(truthDir, "facts.yaml")

			projectRoot := filepath.Dir(dataDir)
			out := cmd.OutOrStdout()
			for _, name := range targets {
				pc := cfg.Sync[name]
				// bughunt-11 F3: refuse to exec the plugin until the
				// operator has trusted its fingerprint via
				// `leonard config trust sync <name>`. Protects
				// against the bash-obfuscation attack class
				// planting a malicious command path.
				ok, err := config.VerifySyncPluginTrusted(projectRoot, name, pc.Command)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "sync: %s: trust check failed: %v\n", name, err)
					continue
				}
				if !ok {
					fmt.Fprintf(out, "sync: %s: command %q is not trusted; run `leonard config trust sync %s` to authorize.\n",
						name, pc.Command, name)
					continue
				}
				// bughunt-11 F5: re-read facts.yaml for each plugin.
				// If plugin A wrote updates earlier in the loop,
				// plugin B's input must reflect those changes —
				// otherwise plugin B's writeFactsAtomically would
				// overwrite plugin A's work with the stale tree.
				facts, err := readFactsYAML(factsPath)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "sync: %s: read facts.yaml: %v\n", name, err)
					continue
				}
				if err := runOneSyncPlugin(cmd, name, pc, facts, factsPath, dryRun, out); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "sync: %s: %v\n", name, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without writing facts.yaml")
	cmd.AddCommand(newSyncListCmd())
	return cmd
}

func newSyncListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured sync plugins.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			cfg, err := loadSyncConfig(dataDir)
			if err != nil {
				return fmt.Errorf("sync list: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(cfg.Sync) == 0 {
				fmt.Fprintln(out, "leonard: no sync plugins configured.")
				return nil
			}
			for _, name := range syncNames(cfg) {
				pc := cfg.Sync[name]
				fmt.Fprintf(out, "%-20s %s\n", name, pc.Command)
			}
			return nil
		},
	}
}

// loadSyncConfig reads .leonard/config.toml and decodes the
// [sync.*] subtree. Returns an empty syncConfig (not an error)
// when no sync block is present so `leonard sync list` works on
// projects that haven't configured any plugins yet.
func loadSyncConfig(dataDir string) (syncConfig, error) {
	path := filepath.Join(dataDir, "config.toml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return syncConfig{}, nil
	}
	if err != nil {
		return syncConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg syncConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return syncConfig{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return cfg, nil
}

func syncNames(cfg syncConfig) []string {
	names := make([]string, 0, len(cfg.Sync))
	for name := range cfg.Sync {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

// sortStrings is a tiny stdlib stand-in so we don't pull `sort`
// into a one-call site. Insertion sort is fine for the tiny
// plugin lists operators ship.
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// readFactsYAML returns the parsed facts.yaml tree. A missing or
// empty file is treated as an empty map so sync plugins running on
// a fresh project still work.
func readFactsYAML(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// runOneSyncPlugin invokes the named plugin and applies (or
// previews) its updated_facts. Operator messages go to out;
// plugin stderr is surfaced inline on errors.
func runOneSyncPlugin(cmd *cobra.Command, name string, pc syncPluginConfig, facts map[string]any, factsPath string, dryRun bool, out io.Writer) error {
	if pc.Command == "" {
		return errors.New("plugin has empty command")
	}
	fmt.Fprintf(out, "leonard sync: running %s (%s)\n", name, pc.Command)
	res, err := sync.Run(cmd.Context(), sync.Plugin{
		Name:    name,
		Command: pc.Command,
		Config:  pc.Config,
	}, facts)
	if err != nil {
		if res.Stderr != "" {
			fmt.Fprintf(out, "  stderr: %s\n", strings.TrimSpace(res.Stderr))
		}
		return err
	}
	if len(res.Changes) == 0 {
		fmt.Fprintf(out, "  no changes (took %s)\n", res.Elapsed.Truncate(1e6))
		return nil
	}
	fmt.Fprintf(out, "  %d change(s) in %s:\n", len(res.Changes), res.Elapsed.Truncate(1e6))
	for _, c := range res.Changes {
		fmt.Fprintf(out, "  - %s: %v -> %v", c.Path, c.Old, c.New)
		if c.Reason != "" {
			fmt.Fprintf(out, "  (%s)", c.Reason)
		}
		fmt.Fprintln(out)
	}

	if dryRun {
		fmt.Fprintln(out, "  --dry-run: facts.yaml NOT written")
		return nil
	}

	if err := writeFactsAtomically(factsPath, res.UpdatedFacts); err != nil {
		return fmt.Errorf("write facts.yaml: %w", err)
	}
	fmt.Fprintln(out, "  facts.yaml updated")
	return nil
}

// writeFactsAtomically renders facts as YAML and replaces factsPath
// via a temp-file rename. The rename is atomic on POSIX so a crash
// mid-write doesn't truncate facts.yaml.
func writeFactsAtomically(factsPath string, facts map[string]any) error {
	body, err := yaml.Marshal(facts)
	if err != nil {
		return err
	}
	dir := filepath.Dir(factsPath)
	tmp, err := os.CreateTemp(dir, "facts.yaml.tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // cleanup if rename fails
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, factsPath)
}
