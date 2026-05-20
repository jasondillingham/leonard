// Package index walks the project tree, dispatches files to language-specific
// parsers in internal/parse, and persists extracted symbols into the store.
// Incremental: each file's sha256 is compared against the prior store entry,
// and parsing is skipped when the hash is unchanged.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	ignore "github.com/sabhiram/go-gitignore"

	"github.com/jasondillingham/leonard/internal/parse"
	"github.com/jasondillingham/leonard/internal/store"
)

// defaultSkipDirs are directory names always skipped, regardless of ignore
// files. These exist before .gitignore can be read on most projects.
// defaultSkipDirs are directory names skipped during IndexAll regardless of
// any .gitignore / .leonardignore content. Three categories:
//
//   - Version control: .git
//   - Dependency vendoring: vendor (Go), node_modules (JS/TS)
//   - Build artifacts + tool caches by ecosystem convention:
//     dist, build (generic);
//     .venv, venv, __pycache__, .mypy_cache, .ruff_cache,
//     .pytest_cache, .tox (Python);
//     target (Rust/Maven/sbt);
//     .next, .nuxt (JS/TS framework build dirs).
//
// Added in v0.6.1 after a Leonard-on-Leonard reindex picked up
// ~160k symbols from .venv site-packages and Cargo's target/, which
// would have tanked the Inspect eval. The .gitignore lane already
// handles these for projects that ignore them at the root, but
// subdir .gitignores (which is where these usually live) aren't
// honored — so the default has to be defensive.
var defaultSkipDirs = map[string]bool{
	".git":          true,
	".mypy_cache":   true,
	".next":         true,
	".nuxt":         true,
	".pytest_cache": true,
	".ruff_cache":   true,
	".tox":          true,
	".venv":         true,
	"__pycache__":   true,
	"build":         true,
	"dist":          true,
	"node_modules":  true,
	"target":        true,
	"vendor":        true,
	"venv":          true,
}

// extractor extracts symbols for a single file's source. Returning an error
// is treated as a parse failure for that file — the indexer logs and moves on.
type extractor func(path string, src []byte) ([]store.Symbol, error)

// langExtractors maps a file extension (with leading dot) to the language tag
// stored on the file row and the extractor used to pull symbols. Phase 1 is
// Go-only; phase 2 adds python and typescript here.
var langExtractors = map[string]struct {
	lang    string
	extract extractor
}{
	".go":  {lang: "go", extract: parse.ExtractGo},
	".py":  {lang: "python", extract: parse.ExtractPython},
	".rs":  {lang: "rust", extract: parse.ExtractRust},
	".ts":  {lang: "typescript", extract: parse.ExtractTypeScript},
	".tsx": {lang: "typescript", extract: parse.ExtractTypeScript},
}

// ParseFailure is a per-file record of an extractor returning a non-nil
// error. The path is project-relative (the same form used as a store key)
// and Message is the parse error's first line — enough for a CLI summary,
// not so much that a chatty parser floods the output.
type ParseFailure struct {
	Path    string
	Message string
}

// Indexer walks Root and persists symbols into Store. Construct with New.
type Indexer struct {
	Store *store.Store
	Root  string

	// parseCount tracks how many files actually triggered a re-parse since
	// the indexer was created. Exposed via ParseCount() — tests assert that
	// a second IndexAll on an unchanged tree increments the counter by zero.
	parseCount atomic.Int64

	// parseFailures collects per-file parser errors so callers can surface
	// them in CLI/MCP output instead of having them silently swallowed. The
	// indexer walks sequentially so a plain slice with no mutex is fine.
	parseFailures []ParseFailure
}

// New returns an Indexer rooted at root. The root is cleaned and converted
// to an absolute path so subsequent walk results are stored consistently
// regardless of how the caller spelled it.
func New(s *store.Store, root string) *Indexer {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &Indexer{Store: s, Root: filepath.Clean(abs)}
}

// ParseCount returns the number of files re-parsed since construction.
// Intended for tests; cheap enough to call in prod if useful for metrics.
func (i *Indexer) ParseCount() int64 { return i.parseCount.Load() }

// ParseFailures returns a copy of the per-file parse errors accumulated
// since construction. Empty slice when every file parsed cleanly. Callers
// own the result and can format/log it however they like.
func (i *Indexer) ParseFailures() []ParseFailure {
	out := make([]ParseFailure, len(i.parseFailures))
	copy(out, i.parseFailures)
	return out
}

// IndexAll walks the root, applying ignore rules, and indexes every
// supported file. Returns the first walk error encountered, but parse errors
// for individual files are swallowed so one bad file doesn't poison the run.
func (i *Indexer) IndexAll() error {
	matcher, err := loadIgnore(i.Root)
	if err != nil {
		return err
	}

	walkErr := filepath.WalkDir(i.Root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}

		rel, err := filepath.Rel(i.Root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}

		if d.IsDir() {
			if defaultSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if matcher != nil && matcher.MatchesPath(rel+"/") {
				return filepath.SkipDir
			}
			return nil
		}

		if matcher != nil && matcher.MatchesPath(rel) {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if _, ok := langExtractors[ext]; !ok {
			return nil
		}

		if err := i.indexAbs(path); err != nil {
			// Log-and-continue: a parse failure on one file shouldn't abort
			// the whole walk. The error is captured on the indexer (see
			// indexAbs) so the CLI can surface it after the walk instead of
			// silently swallowing it.
			_ = err
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	// Prune file rows for paths that have vanished from disk. Without
	// this sweep, deleting a file and re-running `leonard index` left
	// the stale row in the store — verify_symbol kept returning
	// matches for code that no longer existed. Bughunt-2 cli F2.
	return i.pruneStaleFiles()
}

// pruneStaleFiles iterates every known file row and removes:
//
//  1. Rows whose path no longer resolves on disk (the original
//     bughunt-2 ground-truth-drift fix).
//  2. Rows whose path lives under a directory the indexer would now
//     skip — caught when v0.6.1's expanded defaultSkipDirs added
//     `.venv`, `target`, `__pycache__` etc. and a re-index found
//     thousands of prior rows from those dirs lingering in the store.
//
// Symbols cascade via FK on either deletion path.
func (i *Indexer) pruneStaleFiles() error {
	files, err := i.Store.ListFiles("", "")
	if err != nil {
		return fmt.Errorf("prune: list files: %w", err)
	}
	for _, f := range files {
		if pathHasSkippedComponent(f.Path) {
			if err := i.Store.DeleteFile(f.Path); err != nil {
				return err
			}
			continue
		}
		abs := filepath.Join(i.Root, filepath.FromSlash(f.Path))
		_, statErr := os.Stat(abs)
		if statErr == nil {
			continue
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("prune: stat %s: %w", f.Path, statErr)
		}
		if err := i.Store.DeleteFile(f.Path); err != nil {
			return err
		}
	}
	return nil
}

// pathHasSkippedComponent returns true when any directory component
// of rel matches a defaultSkipDirs entry. Used by the prune sweep
// (and could be reused by the walk itself, but the walk uses
// filepath.SkipDir at the d.IsDir() check which is more efficient
// for full-tree scans).
func pathHasSkippedComponent(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if defaultSkipDirs[part] {
			return true
		}
	}
	return false
}

// IndexFile indexes a single file. The path may be absolute or relative to
// the indexer's root. Files outside the root, missing files, or files whose
// extension has no registered extractor are all silent no-ops returning nil,
// matching the brief's loose contract for the post-edit hook caller.
func (i *Indexer) IndexFile(path string) error {
	abs, err := i.absPath(path)
	if err != nil {
		return err
	}

	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return nil
	}

	ext := strings.ToLower(filepath.Ext(abs))
	if _, ok := langExtractors[ext]; !ok {
		return nil
	}

	return i.indexAbs(abs)
}

// absPath resolves p to an absolute, cleaned path under Root if it's relative.
func (i *Indexer) absPath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	return filepath.Clean(filepath.Join(i.Root, p)), nil
}

// indexAbs is the per-file workhorse: hash, decide whether to re-parse,
// extract, and persist. The path argument is always absolute.
func (i *Indexer) indexAbs(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	hash := hashBytes(data)

	rel := i.storeKey(path)
	prior, found, err := i.Store.GetFile(rel)
	if err != nil {
		return fmt.Errorf("store.GetFile: %w", err)
	}
	if found && prior.Hash == hash {
		return nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	spec, ok := langExtractors[ext]
	if !ok {
		return nil
	}

	i.parseCount.Add(1)
	syms, err := spec.extract(rel, data)
	if err != nil {
		// Persist the file row so an unchanged-but-broken file is not re-parsed
		// every walk; clear out any prior symbols for it.
		_ = i.Store.ReplaceSymbols(rel, nil)
		_ = i.Store.UpsertFile(store.File{
			Path:      rel,
			Hash:      hash,
			Language:  spec.lang,
			SizeBytes: int64(len(data)),
			IndexedAt: time.Now().Unix(),
		})
		// Record the failure so the CLI can surface it. Keep just the first
		// line of the error to stay terse — parsers like gpython emit
		// multi-line diagnostics that would otherwise spam the summary.
		msg := err.Error()
		if nl := strings.Index(msg, "\n"); nl >= 0 {
			msg = msg[:nl]
		}
		i.parseFailures = append(i.parseFailures, ParseFailure{Path: rel, Message: msg})
		return fmt.Errorf("extract %s: %w", rel, err)
	}

	if err := i.Store.UpsertFile(store.File{
		Path:      rel,
		Hash:      hash,
		Language:  spec.lang,
		SizeBytes: int64(len(data)),
		IndexedAt: time.Now().Unix(),
	}); err != nil {
		return fmt.Errorf("store.UpsertFile: %w", err)
	}
	if err := i.Store.ReplaceSymbols(rel, syms); err != nil {
		return fmt.Errorf("store.ReplaceSymbols: %w", err)
	}
	return nil
}

// storeKey is the path key written to the store. Paths under Root are stored
// as forward-slash relative paths so the index is portable across platforms
// and stable when the root is moved.
func (i *Indexer) storeKey(abs string) string {
	rel, err := filepath.Rel(i.Root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// loadIgnore reads .gitignore and .leonardignore at root and compiles them
// into a single matcher. Returns nil (no-op matcher) if neither file exists.
func loadIgnore(root string) (*ignore.GitIgnore, error) {
	var lines []string
	for _, name := range []string{".gitignore", ".leonardignore"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			lines = append(lines, strings.TrimRight(line, "\r"))
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return ignore.CompileIgnoreLines(lines...), nil
}
