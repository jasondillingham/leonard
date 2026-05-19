package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/hooks"
	"github.com/jasondillingham/leonard/internal/store"
)

// preEditOpener returns a SymbolStore wired to the given project root, plus
// a close function that releases its underlying handle. Tests inject a stub
// opener so the cobra layer can be exercised without touching SQLite.
type preEditOpener func(projectRoot string) (hooks.SymbolStore, func() error, error)

// modulePathReader resolves the host module's import path (typically by
// reading go.mod at the project root). Tests substitute a fixed value.
type modulePathReader func(projectRoot string) string

func newPreEditCmd() *cobra.Command {
	return newPreEditCmdWithDeps(defaultPreEditOpener, defaultModulePath)
}

func newPreEditCmdWithDeps(open preEditOpener, readModule modulePathReader) *cobra.Command {
	return &cobra.Command{
		Use:   "pre-edit",
		Short: "Handle a Claude Code PreToolUse event (Edit/Write).",
		Long:  "Reads the PreToolUse hook envelope from stdin, parses the proposed Edit/Write content with go/parser, and blocks the tool call when it adds references to tracked-package symbols that aren't in the symbol index. Tools other than Edit/Write, non-Go files, and snippets whose references resolve only to stdlib or external packages all pass through.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot()
			if err != nil {
				return err
			}
			symStore, closer, err := open(root)
			if err != nil {
				return fmt.Errorf("pre-edit: open store: %w", err)
			}
			defer func() { _ = closer() }()

			opts := hooks.PreEditOptions{
				Store:      symStore,
				ModulePath: readModule(root),
			}
			if err := hooks.HandlePreEdit(cmd.Context(), opts, cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return fmt.Errorf("pre-edit: %w", err)
			}
			return nil
		},
	}
}

// defaultPreEditOpener opens the project's SQLite store. If the DB file
// doesn't exist yet (typically a project that hasn't run `leonard init`), we
// fall back to a permissive store that reports every symbol as present so
// the hook degrades to a no-op instead of blocking every edit on a fresh
// checkout.
func defaultPreEditOpener(projectRoot string) (hooks.SymbolStore, func() error, error) {
	dbPath := filepath.Join(projectRoot, ".leonard", "leonard.db")
	if _, err := os.Stat(dbPath); err != nil {
		return permissiveStore{}, func() error { return nil }, nil
	}
	s, err := store.Open(dbPath)
	if err != nil {
		return nil, func() error { return nil }, err
	}
	return preEditSymbolAdapter{s: s}, s.Close, nil
}

// preEditSymbolAdapter adapts hooks.SymbolStore onto *store.Store. The pre-edit
// hook only cares whether a name exists in the index — kind, file, and
// signature aren't read at v0.
type preEditSymbolAdapter struct{ s *store.Store }

func (a preEditSymbolAdapter) HasSymbol(name string) (bool, error) {
	syms, err := a.s.FindSymbolsByName(name)
	if err != nil {
		return false, err
	}
	return len(syms) > 0, nil
}

// permissiveStore is the fallback used when no .leonard/leonard.db exists
// yet. Reporting every symbol as present means the handler never blocks,
// which is the right behavior before the index has anything to compare
// against.
type permissiveStore struct{}

func (permissiveStore) HasSymbol(string) (bool, error) { return true, nil }

// defaultModulePath reads go.mod at projectRoot and returns the module path.
// A missing file or a malformed `module` directive yields "" — which makes
// every import look external and disables blocking, the right safety net for
// a misconfigured project.
func defaultModulePath(projectRoot string) string {
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
