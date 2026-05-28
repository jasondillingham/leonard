package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
	"github.com/jasondillingham/leonard/internal/index"
)

func newListStaleClaimsCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "list-stale-claims",
		Short: "Scan files for unverified or forbidden claims.",
		Long: `Walks the project tree (or a --scope glob) and runs the
ground-truth adapter's claim detector on each file. Reports every
unverified or forbidden claim with its line number and excerpt.

Exit code:
  0  no findings
  1  unverified findings
  2  forbidden findings (takes priority over unverified)
  >2 error

Examples:
  leonard list-stale-claims
  leonard list-stale-claims --scope="docs/**/*.md"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			projectRoot := filepath.Dir(dataDir)

			a := groundtruth.New()
			raw, err := loadGroundTruthRaw(dataDir)
			if err != nil {
				return fmt.Errorf("list-stale-claims: %w", err)
			}
			if err := a.Init(context.Background(), adapters.Config{
				ProjectRoot: projectRoot,
				Stderr:      cmd.ErrOrStderr(),
				Raw:         raw,
			}); err != nil {
				return fmt.Errorf("list-stale-claims: init ground-truth adapter: %w", err)
			}
			defer a.Close()
			gta := a.(*groundtruth.GroundTruthAdapter)

			targets, err := collectTargets(projectRoot, scope)
			if err != nil {
				return fmt.Errorf("list-stale-claims: %w", err)
			}
			if len(targets) == 0 && scope != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "list-stale-claims: no files matched scope %q\n", scope)
				return nil
			}

			maxCode := 0
			for _, path := range targets {
				content, err := os.ReadFile(path)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "list-stale-claims: read %s: %v\n", path, err)
					continue
				}
				res := gta.Detect(string(content))
				if res.Summary.Unverified == 0 && res.Summary.Forbidden == 0 && res.Summary.Contradiction == 0 {
					continue
				}

				rel, _ := filepath.Rel(projectRoot, path)
				renderCheckResult(cmd.OutOrStdout(), rel, content, res, "plain")

				if res.Summary.Forbidden > 0 && maxCode < 2 {
					maxCode = 2
				} else if (res.Summary.Unverified > 0 || res.Summary.Contradiction > 0) && maxCode < 1 {
					maxCode = 1
				}
			}

			if maxCode > 0 {
				return &exitCode{code: maxCode}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "glob pattern relative to project root (e.g. \"docs/**/*.md\"); defaults to all .md files")
	return cmd
}

// collectTargets returns the list of files to scan. When scope is set it is
// treated as a glob pattern relative to projectRoot (** is supported for
// recursive matching); when empty, every .md file under projectRoot
// (excluding .leonard/, hidden dirs, and paths matched by .gitignore /
// .leonardignore) is returned.
func collectTargets(projectRoot, scope string) ([]string, error) {
	if scope != "" {
		matches, err := expandGlob(projectRoot, scope)
		if err != nil {
			return nil, fmt.Errorf("scope glob %q: %w", scope, err)
		}
		return matches, nil
	}

	ig, err := index.LoadIgnore(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("load ignore rules: %w", err)
	}

	var out []string
	err = filepath.WalkDir(projectRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(projectRoot, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".leonard" || name == ".git" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if ig != nil && ig.MatchesPath(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if ig != nil && ig.MatchesPath(rel) {
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) == ".md" {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// expandGlob resolves a scope glob relative to root. Patterns without **
// use filepath.Glob; patterns with ** walk the tree and match each relative
// path against a translated regexp so that ** crosses directory boundaries.
func expandGlob(root, pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(filepath.Join(root, pattern))
	}
	re, err := scopeGlobToRE(pattern)
	if err != nil {
		return nil, err
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkerr error) error {
		if walkerr != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if re.MatchString(filepath.ToSlash(rel)) {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// scopeGlobToRE translates a glob pattern with ** support into a regexp.
// Rules: ** matches any sequence of characters including path separators,
// * matches any sequence of non-separator characters, ? matches one
// non-separator character.
func scopeGlobToRE(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteByte('^')
	i := 0
	for i < len(pattern) {
		switch {
		case pattern[i] == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			b.WriteString(`.*`)
			i += 2
			if i < len(pattern) && pattern[i] == '/' {
				i++ // consume the slash following **
			}
		case pattern[i] == '*':
			b.WriteString(`[^/]*`)
			i++
		case pattern[i] == '?':
			b.WriteString(`[^/]`)
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	b.WriteByte('$')
	return regexp.Compile(b.String())
}
