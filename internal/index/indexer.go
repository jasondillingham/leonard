// Package index walks the project tree, dispatches files to language-specific
// parsers in internal/parse, and persists extracted symbols into the store.
// Incremental: each file's sha256 is compared against the prior store entry,
// and parsing is skipped when the hash is unchanged.
package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ignore "github.com/sabhiram/go-gitignore"
	"golang.org/x/text/unicode/norm"

	"github.com/jasondillingham/leonard/internal/parse"
	"github.com/jasondillingham/leonard/internal/store"
	"github.com/jasondillingham/leonard/internal/telemetry"
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

// DefaultSkipDir reports whether name is in the indexer's default
// skip set. Exported so other packages (notably internal/hooks's
// pre-edit sibling-scan walker) can stay in lockstep with the
// indexer's view of "directories to avoid descending into."
// Bughunt-2 pre-edit F1: a divergent local skip list missed
// build-artifact dirs and wasted time scanning them.
func DefaultSkipDir(name string) bool {
	return defaultSkipDirs[name]
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
	".go":    {lang: "go", extract: parse.ExtractGo},
	".py":    {lang: "python", extract: parse.ExtractPython},
	".rs":    {lang: "rust", extract: parse.ExtractRust},
	".ts":    {lang: "typescript", extract: parse.ExtractTypeScript},
	".tsx":   {lang: "typescript", extract: parse.ExtractTypeScript},
	".java":  {lang: "java", extract: parse.ExtractJava},
	".rb":    {lang: "ruby", extract: parse.ExtractRuby},
	".cs":    {lang: "csharp", extract: parse.ExtractCSharp},
	".swift": {lang: "swift", extract: parse.ExtractSwift},
	".kt":    {lang: "kotlin", extract: parse.ExtractKotlin},
	".kts":   {lang: "kotlin", extract: parse.ExtractKotlin},
	".scala": {lang: "scala", extract: parse.ExtractScala},
	".dart":  {lang: "dart", extract: parse.ExtractDart},
	".c":     {lang: "c", extract: parse.ExtractC},
	".h":     {lang: "c", extract: parse.ExtractC},
	".cc":    {lang: "cpp", extract: parse.ExtractCpp},
	".cpp":   {lang: "cpp", extract: parse.ExtractCpp},
	".cxx":   {lang: "cpp", extract: parse.ExtractCpp},
	".hpp":   {lang: "cpp", extract: parse.ExtractCpp},
	".hh":    {lang: "cpp", extract: parse.ExtractCpp},
	".php":   {lang: "php", extract: parse.ExtractPHP},
	".lua":   {lang: "lua", extract: parse.ExtractLua},
	".sh":    {lang: "bash", extract: parse.ExtractBash},
	".bash":  {lang: "bash", extract: parse.ExtractBash},
	".zig":   {lang: "zig", extract: parse.ExtractZig},
	".nix":   {lang: "nix", extract: parse.ExtractNix},
	".ex":    {lang: "elixir", extract: parse.ExtractElixir},
	".exs":   {lang: "elixir", extract: parse.ExtractElixir},
	".sol":    {lang: "solidity", extract: parse.ExtractSolidity},
	".mk":     {lang: "make", extract: parse.ExtractMake},
	".cmake":  {lang: "cmake", extract: parse.ExtractCMake},
	".vue":     {lang: "vue", extract: parse.ExtractVue},
	".svelte":  {lang: "svelte", extract: parse.ExtractSvelte},
	".tf":      {lang: "hcl", extract: parse.ExtractHCL},
	".tfvars":  {lang: "hcl", extract: parse.ExtractHCL},
	".hcl":     {lang: "hcl", extract: parse.ExtractHCL},
	".graphql": {lang: "graphql", extract: parse.ExtractGraphQL},
	".gql":     {lang: "graphql", extract: parse.ExtractGraphQL},
	".proto":   {lang: "proto", extract: parse.ExtractProto},
	".sql":     {lang: "sql", extract: parse.ExtractSQL},
	".ipynb":   {lang: "jupyter", extract: parse.ExtractJupyter},
	".wit":     {lang: "wit", extract: parse.ExtractWIT},
	".astro":   {lang: "astro", extract: parse.ExtractAstro},
	// Solid.js is documented as "a semantic layer on TSX" — the
	// files are JSX/TSX and the existing TypeScript extractor
	// covers function-component declarations directly. Register
	// .jsx so Solid (and other JSX-emitting frameworks) get
	// indexed without a Solid-specific extractor.
	".jsx":     {lang: "typescript", extract: parse.ExtractTypeScript},
	".erl":     {lang: "erlang", extract: parse.ExtractErlang},
	".hrl":     {lang: "erlang", extract: parse.ExtractErlang},
	// .r covers both .r and .R — dispatchByExt lowercases the
	// extension before lookup, so the case distinction R uses by
	// convention vanishes here.
	".r":   {lang: "r", extract: parse.ExtractR},
	".bzl":   {lang: "starlark", extract: parse.ExtractStarlark},
	".bazel": {lang: "starlark", extract: parse.ExtractStarlark},
	".star":  {lang: "starlark", extract: parse.ExtractStarlark},
	// GLSL extensions: .glsl (generic), .vert/.frag/.geom/.comp/
	// .tesc/.tese (per stage — vertex/fragment/geometry/compute/
	// tessellation control/evaluation). All route through the same
	// grammar since the language is identical, only the entry-point
	// semantics differ.
	".glsl": {lang: "glsl", extract: parse.ExtractGLSL},
	".vert": {lang: "glsl", extract: parse.ExtractGLSL},
	".frag": {lang: "glsl", extract: parse.ExtractGLSL},
	".geom": {lang: "glsl", extract: parse.ExtractGLSL},
	".comp": {lang: "glsl", extract: parse.ExtractGLSL},
	".tesc": {lang: "glsl", extract: parse.ExtractGLSL},
	".tese": {lang: "glsl", extract: parse.ExtractGLSL},
	".hlsl": {lang: "hlsl", extract: parse.ExtractHLSL},
	".fx":   {lang: "hlsl", extract: parse.ExtractHLSL},
	".fxh":  {lang: "hlsl", extract: parse.ExtractHLSL},
}

// langExtractorsByName handles files whose basename (rather than
// extension) identifies the language. Makefiles have no extension;
// CMakeLists.txt has a non-trivial name. Lookup is case-insensitive.
// Add to this map for new no-extension languages.
var langExtractorsByName = map[string]struct {
	lang    string
	extract extractor
}{
	"makefile":      {lang: "make", extract: parse.ExtractMake},
	"gnumakefile":   {lang: "make", extract: parse.ExtractMake},
	"cmakelists.txt": {lang: "cmake", extract: parse.ExtractCMake},
	// OpenAPI / Swagger specs. Detected by filename because YAML
	// + JSON files are too generic to dispatch by extension.
	// Real-world projects ship these specs with one of these
	// conventional names.
	"openapi.yaml": {lang: "openapi", extract: parse.ExtractOpenAPI},
	"openapi.yml":  {lang: "openapi", extract: parse.ExtractOpenAPI},
	"openapi.json": {lang: "openapi", extract: parse.ExtractOpenAPI},
	"swagger.yaml": {lang: "openapi", extract: parse.ExtractOpenAPI},
	"swagger.yml":  {lang: "openapi", extract: parse.ExtractOpenAPI},
	"swagger.json": {lang: "openapi", extract: parse.ExtractOpenAPI},
	// Just recipe runner — `justfile` or `Justfile` at the repo root.
	"justfile": {lang: "just", extract: parse.ExtractJust},
	// Bazel build descriptions. The Starlark grammar covers .bzl,
	// BUILD, BUILD.bazel, WORKSPACE, WORKSPACE.bazel — all the
	// conventional names a Bazel-shaped repo emits.
	"build":          {lang: "starlark", extract: parse.ExtractStarlark},
	"build.bazel":    {lang: "starlark", extract: parse.ExtractStarlark},
	"workspace":      {lang: "starlark", extract: parse.ExtractStarlark},
	"workspace.bazel": {lang: "starlark", extract: parse.ExtractStarlark},
	// Manifest-aware dependency graph (v0.36). Each manifest file
	// type emits one Symbol per declared dependency — useful for
	// `verify_symbol("react")` style checks against the project's
	// real dependency set without grepping.
	"package.json": {lang: "manifest", extract: parse.ExtractPackageJSON},
	"cargo.toml":   {lang: "manifest", extract: parse.ExtractCargoToml},
	"go.mod":       {lang: "manifest", extract: parse.ExtractGoMod},
	"pom.xml":      {lang: "manifest", extract: parse.ExtractPomXml},
}

// dispatchByExt looks up an extractor for the given path. It tries
// the file extension first (the common case), then falls back to
// the basename for extension-less or specially-named build files.
// Returns ok=false when nothing matches.
func dispatchByExt(path string) (struct {
	lang    string
	extract extractor
}, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if spec, ok := langExtractors[ext]; ok {
		return spec, true
	}
	base := strings.ToLower(filepath.Base(path))
	if spec, ok := langExtractorsByName[base]; ok {
		return spec, true
	}
	return struct {
		lang    string
		extract extractor
	}{}, false
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
	// them in CLI/MCP output instead of having them silently swallowed.
	// v0.50.2 (bughunt-6 perf F4 PROMOTED) added a worker pool to IndexAll
	// so multiple goroutines call indexAbs concurrently — the mutex
	// protects parseFailures append from the data race.
	parseFailuresMu sync.Mutex
	parseFailures   []ParseFailure
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
	i.parseFailuresMu.Lock()
	defer i.parseFailuresMu.Unlock()
	out := make([]ParseFailure, len(i.parseFailures))
	copy(out, i.parseFailures)
	return out
}

// IndexAll walks the root, applying ignore rules, and indexes every
// supported file. Returns the first walk error encountered, but parse errors
// for individual files are swallowed so one bad file doesn't poison the run.
//
// Bughunt-6 perf F4 (PROMOTED): pre-v0.50.2 the walk was strictly
// sequential — one indexAbs call per file. On a polyglot project
// this was ~50 files/sec while parallel-Go was ~3000 files/sec. The
// dominant cost is the per-file subprocess fork for the tree-sitter
// + Rust + Python extractors. A bounded worker pool (NumCPU workers)
// cuts wall time ~10× on real workloads without overwhelming the
// filesystem or the SQLite writer (the store uses WAL with busy
// timeout so multiple inserts serialize cleanly).
func (i *Indexer) IndexAll() error {
	_, end := telemetry.Span(context.Background(), "leonard.index.all")
	defer end()
	matcher, err := loadIgnore(i.Root)
	if err != nil {
		return err
	}

	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > 16 {
		workers = 16
	}
	work := make(chan string, workers*4)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range work {
				_ = i.indexAbs(path) // errors captured on indexer; log-and-continue
			}
		}()
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

		// Skip symlinks. filepath.WalkDir doesn't recurse INTO
		// symlinked dirs, but it does still call this callback for
		// the symlink itself — and indexAbs would then os.ReadFile
		// through the link to whatever it points at, including
		// targets outside the project root. Security-1 F3.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		if matcher != nil && matcher.MatchesPath(rel) {
			return nil
		}

		if _, ok := dispatchByExt(path); !ok {
			return nil
		}

		work <- path
		return nil
	})
	close(work)
	wg.Wait()
	if walkErr != nil {
		return walkErr
	}

	// Prune file rows for paths that have vanished from disk. Without
	// this sweep, deleting a file and re-running `leonard index` left
	// the stale row in the store — verify_symbol kept returning
	// matches for code that no longer existed. Bughunt-2 cli F2.
	return i.pruneStaleFiles()
}

// pruneStaleFiles collects every file row that should be removed and
// hands the whole batch to Store.DeleteFiles for a single-transaction
// sweep. Two conditions trigger removal:
//
//  1. Rows whose path lives under a directory the indexer would now
//     skip — caught when v0.6.1's expanded defaultSkipDirs added
//     `.venv`, `target`, `__pycache__` etc. and a re-index found
//     thousands of prior rows from those dirs lingering in the store.
//  2. Rows whose path no longer resolves on disk (the original
//     bughunt-2 ground-truth-drift fix).
//
// v0.7 split the iteration from the deletes: the prior per-row
// DeleteFile loop averaged ~1.3s per row on a polluted index because
// each statement is its own transaction and triggers a FK cascade.
// Batching turns the same work into milliseconds.
func (i *Indexer) pruneStaleFiles() error {
	files, err := i.Store.AllFiles()
	if err != nil {
		return fmt.Errorf("prune: list files: %w", err)
	}
	var toDelete []string
	for _, f := range files {
		if pathHasSkippedComponent(f.Path) {
			toDelete = append(toDelete, f.Path)
			continue
		}
		// Bughunt-4 path-trust F4: a pre-v0.8 store could have rows
		// with paths like "../sneaky.go" from before path-trust
		// validation existed. Filter them through ResolveSafe so a
		// stat on a path that escapes the project root doesn't
		// happen — these rows get pruned along with the missing ones.
		abs, ok := ResolveSafe(i.Root, filepath.FromSlash(f.Path))
		if !ok {
			toDelete = append(toDelete, f.Path)
			continue
		}
		_, statErr := os.Stat(abs)
		if statErr == nil {
			continue
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("prune: stat %s: %w", f.Path, statErr)
		}
		toDelete = append(toDelete, f.Path)
	}
	if len(toDelete) == 0 {
		return nil
	}
	if _, err := i.Store.DeleteFiles(toDelete); err != nil {
		return err
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

	if _, ok := dispatchByExt(abs); !ok {
		return nil
	}

	return i.indexAbs(abs)
}

// ErrPathEscapesRoot is returned when an external caller supplies a file
// path that resolves outside the indexer's Root. Used to plug the
// confused-deputy reported as security-1 F1: a crafted PostToolUse
// payload with `file_path: /etc/hosts` or `../../other.go` used to be
// happily indexed into the project's store, breaking the ground-truth
// contract.
var ErrPathEscapesRoot = errors.New("index: path escapes project root")

// absPath resolves p to an absolute, cleaned path under Root, or
// returns ErrPathEscapesRoot when the resolution lands outside Root.
// Both relative paths (joined with Root) and absolute paths are
// accepted as INPUTS, but the OUTPUT is always rejected when it would
// step outside.
//
// Symlinks are evaluated when the target exists. Non-existent paths
// (legitimate for a Write that hasn't landed yet, or a delete-and-
// reindex race) fall back to a lexical containment check. Either
// way, no escape is silent.
func (i *Indexer) absPath(p string) (string, error) {
	resolved, ok := ResolveSafe(i.Root, p)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrPathEscapesRoot, p)
	}
	return resolved, nil
}

// isSymlink reports whether path itself is a symbolic link (the link
// itself — not whether it points at something that is, or whether the
// target exists). Used by ResolveSafe to reject dangling symlinks
// whose EvalSymlinks errors out but whose lexical path looks fine.
func isSymlink(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0
}

// ResolveSafe joins+cleans+symlink-evaluates claimed against root and
// returns (abs, true) iff the result has root as a prefix. Exported
// so internal/hooks can validate hook-payload file_path values with
// the same logic. See indexer.absPath for the in-package consumer.
//
// Behavior:
//   - Relative claimed: joined with root, then evaluated.
//   - Absolute claimed: cleaned + evaluated; rejected if outside root.
//   - Symlinks: filepath.EvalSymlinks resolves them; a symlink whose
//     target lies outside root returns (_, false).
//   - Non-existent target: fall back to lexical containment check
//     against the cleaned (un-evaluated) root + path. A path that
//     refers to a future write Claude proposes must still be safe.
//   - Empty root or claimed: rejected.
func ResolveSafe(root, claimed string) (string, bool) {
	if root == "" || claimed == "" {
		return "", false
	}
	abs := claimed
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)
	rootClean := filepath.Clean(root)

	contained := func(base, candidate string) bool {
		rel, err := filepath.Rel(base, candidate)
		if err != nil {
			return false
		}
		// Rel returns ".." or "../" prefix when candidate isn't under base.
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		return true
	}

	// Primary check: lexical containment. Mixing forms here would
	// produce false escapes when only one side is symlink-resolved.
	if contained(rootClean, abs) {
		// Lexical OK — also verify the resolved-symlinks form
		// catches a symlink INSIDE the project pointing OUTSIDE
		// (where lexical looks fine but the actual file isn't).
		if rAbs, errA := filepath.EvalSymlinks(abs); errA == nil {
			rRoot, errR := filepath.EvalSymlinks(rootClean)
			if errR != nil {
				rRoot = rootClean
			}
			if !contained(rRoot, rAbs) {
				return "", false
			}
		} else if isSymlink(abs) {
			// Bughunt-4 path-trust F2: dangling-symlink bypass.
			// EvalSymlinks errors on a symlink whose target doesn't
			// exist, which previously fell through to the lexical-
			// pass branch returning ok. A symlink (live or dangling)
			// can't be trusted to stay non-escaping — reject.
			return "", false
		}
		return abs, true
	}

	// Lexical containment failed — but root and abs may be in
	// different symlink states (the macOS /var → /private/var case:
	// os.Getwd() after Chdir returns the resolved form, but a
	// caller-supplied file_path may still be lexical, or vice
	// versa). Try the resolved forms before giving up.
	rAbs, errA := filepath.EvalSymlinks(abs)
	rRoot, errR := filepath.EvalSymlinks(rootClean)
	if errA == nil && errR == nil && contained(rRoot, rAbs) {
		// Keep the returned path in its caller-supplied lexical form so
		// storeKey + filepath.Rel downstream keep working with the
		// same path strings callers passed in.
		return abs, true
	}
	return "", false
}

// maxIndexedFileBytes caps the size of a single source file the
// indexer is willing to read. Bughunt-4 caps F4 measured a 172 MB
// fixture allocating ~480 MB RSS — pathological but easy to trigger
// (a checked-in vendored bundle, a generated parser, a build
// artifact that snuck past skip-dirs). 8 MiB is well above any
// realistic source file while keeping worst-case allocator pressure
// bounded.
// v0.44 lowered from 8 MiB to 4 MiB; v0.50.1 lowers to 2 MiB after
// security review #3 F4 measured 1.69 GiB peak RSS on a 3.9 MiB
// pathological input — ~5× worse than bughunt-5's "~400 MB
// worst-case" claim. The tree-sitter helper's amplification factor
// for deeply-nested or pathological grammars is much higher than
// observed on well-formed source. At 2 MiB the worst-case ceiling
// drops to ~1 GiB, which is recoverable on developer machines
// (most have ≥16 GiB RAM). Files between 2 and 8 MiB (rare:
// generated parsers, vendored bundles, minified JS) surface as
// ParseFailures with the cap message.
const maxIndexedFileBytes = 2 << 20

// indexAbs is the per-file workhorse: hash, decide whether to re-parse,
// extract, and persist. The path argument is always absolute.
func (i *Indexer) indexAbs(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() > maxIndexedFileBytes {
		rel := i.storeKey(path)
		i.parseFailuresMu.Lock()
		i.parseFailures = append(i.parseFailures, ParseFailure{
			Path:    rel,
			Message: fmt.Sprintf("file size %d bytes exceeds %d byte indexing cap", info.Size(), maxIndexedFileBytes),
		})
		i.parseFailuresMu.Unlock()
		return nil
	}
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

	spec, ok := dispatchByExt(path)
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
		i.parseFailuresMu.Lock()
		i.parseFailures = append(i.parseFailures, ParseFailure{Path: rel, Message: msg})
		i.parseFailuresMu.Unlock()
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
//
// Bughunt-4 path-trust F3: paths are also Unicode-normalized to NFC so
// the same on-disk file produces a single row regardless of the input
// form. macOS HFS+ and APFS normalize filenames internally but the
// userspace path string can arrive in either NFC or NFD; without this
// normalization, indexing the same file twice (once via a tool emitting
// NFC, once via NFD) created two rows with two complete symbol sets.
//
// F031/F032: on darwin, where APFS is case-insensitive but
// case-preserving, two path variants differing only in case refer to
// the same on-disk file. Lowercasing the key on darwin collapses
// these variants to a single store row, preventing duplicate symbol
// sets and double verify_symbol hits.
func (i *Indexer) storeKey(abs string) string {
	rel, err := filepath.Rel(i.Root, abs)
	if err != nil {
		return caseNormPath(norm.NFC.String(filepath.ToSlash(abs)))
	}
	return caseNormPath(norm.NFC.String(filepath.ToSlash(rel)))
}

// caseNormPath lowercases the path on darwin (APFS is case-insensitive)
// so a file whose path arrives in different capitalizations always
// produces the same store key. On other platforms the path is returned
// unchanged (Linux ext4, Windows NTFS may be case-sensitive).
func caseNormPath(p string) string {
	if runtime.GOOS == "darwin" {
		return strings.ToLower(p)
	}
	return p
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// LoadIgnore reads .gitignore and .leonardignore at root and compiles them
// into a single gitignore-style matcher. Returns nil (no-op matcher) if
// neither file exists or neither contains any non-empty lines. Exported so
// commands that walk the project tree (check, list-stale-claims) can skip
// the same paths the indexer skips.
func LoadIgnore(root string) (*ignore.GitIgnore, error) {
	return loadIgnore(root)
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
