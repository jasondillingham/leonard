package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jasondillingham/leonard/internal/telemetry"
)

// SymbolStore is the minimum surface internal/store.Store must satisfy for the
// pre-edit handler. The hook only asks whether a name appears anywhere in the
// index — it doesn't care about kind, file, or how many copies exist. Keeping
// the contract local lets us unit-test without pulling the store package in
// as a dependency.
type SymbolStore interface {
	HasSymbol(name string) (bool, error)
}

// PreToolUsePayload mirrors the Claude Code PreToolUse hook envelope. The
// fields the handler actually reads are ToolName, ToolInput.FilePath, and
// either ToolInput.NewString (Edit) or ToolInput.Content (Write); everything
// else is decoded loosely.
type PreToolUsePayload struct {
	SessionID     string           `json:"session_id"`
	HookEventName string           `json:"hook_event_name"`
	ToolName      string           `json:"tool_name"`
	ToolInput     PreEditToolInput `json:"tool_input"`
	CWD           string           `json:"cwd"`
}

// PreEditToolInput captures the subset of tool_input we read for the
// write-shaped tools the fabrication guard covers:
//
//   - Edit  — uses file_path + new_string
//   - Write — uses file_path + content
//   - MultiEdit — uses file_path + edits[], each carrying its own
//     new_string; we concatenate them into a single snippet so a
//     reference fabricated in any one edit still trips the guard
//   - NotebookEdit — uses notebook_path + new_source. Notebooks aren't
//     Go files, so the `.go` suffix gate below will short-circuit
//     them, but we still decode the payload cleanly so the handler
//     doesn't fail open or crash on a malformed envelope.
//
// Unknown fields (old_string, replace_all, etc.) deserialize harmlessly
// and we drop them.
type PreEditToolInput struct {
	FilePath     string             `json:"file_path"`
	NotebookPath string             `json:"notebook_path"`
	NewString    string             `json:"new_string"`
	Content      string             `json:"content"`
	NewSource    string             `json:"new_source"`
	Edits        []PreEditMultiEdit `json:"edits"`
}

// PreEditMultiEdit mirrors one entry of the MultiEdit `edits` array.
// Only new_string is load-bearing for the fabrication guard — the rest is
// decoded for completeness so a future check (e.g., warning when
// replace_all=true on a tracked file) has the fields available.
type PreEditMultiEdit struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// PreEditResponse is the JSON document the pre-edit hook emits on stdout.
// PreToolUse responses have their own shape per the Claude Code hook docs —
// the deny path lives inside hookSpecificOutput, not in the top-level
// decision/reason fields that PostToolUse uses. The allow path emits
// {"continue": true} and omits hookSpecificOutput; the deny path emits a
// hookSpecificOutput with permissionDecision=deny and omits the top-level
// continue field entirely so the session keeps running after the single
// tool call is rejected.
type PreEditResponse struct {
	Continue           bool                      `json:"continue,omitempty"`
	SuppressOutput     bool                      `json:"suppressOutput,omitempty"`
	HookSpecificOutput *PreToolUseSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// PreToolUseSpecificOutput carries the PreToolUse-specific deny payload.
// HookEventName self-identifies the response to Claude Code; PermissionDecision
// is the documented "allow"/"deny"/"ask" enum (the pre-edit hook only emits
// "deny" — allows skip hookSpecificOutput entirely); PermissionDecisionReason
// is surfaced to the model so it can choose a different edit.
type PreToolUseSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// PreEditOptions wires the handler to its collaborators. ModulePath is the
// current go.mod module (e.g., "github.com/jasondillingham/leonard"); imports
// that fall outside that prefix are treated as external and skipped. An empty
// ModulePath disables blocking entirely — a useful safety net when go.mod
// can't be located.
//
// ModuleRoot is the absolute filesystem path containing go.mod, used by the
// F8 sibling-package scan to enumerate in-module packages whose symbols
// the snippet might reference without an explicit import. An empty
// ModuleRoot skips the scan and preserves the pre-F8 alias-only behavior.
type PreEditOptions struct {
	Store      SymbolStore
	ModulePath string
	ModuleRoot string
}

// HandlePreEdit reads a PreToolUse JSON payload from stdin, decides whether
// the proposed Edit/Write introduces a reference to a tracked symbol that
// doesn't exist in the index, and writes a hook response to stdout. Tool
// calls other than Edit/Write, non-Go files, and snippets whose references
// resolve only to stdlib or external packages all pass through.
func HandlePreEdit(ctx context.Context, opts PreEditOptions, stdin io.Reader, stdout io.Writer) error {
	ctx, end := telemetry.Span(ctx, "leonard.pre-edit")
	defer end()

	if opts.Store == nil {
		return errors.New("hooks: SymbolStore is required")
	}
	payload, err := decodePreToolUsePayload(stdin)
	if err != nil {
		return err
	}
	decision, err := decidePreEdit(ctx, opts, payload)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(decision); err != nil {
		return fmt.Errorf("hooks: encode pre-edit response: %w", err)
	}
	return nil
}

func decodePreToolUsePayload(r io.Reader) (PreToolUsePayload, error) {
	var p PreToolUsePayload
	body, err := readPayloadBytes(r)
	if err != nil {
		return p, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return p, fmt.Errorf("%w: empty PreToolUse payload on stdin", ErrDecode)
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("%w: decode PreToolUse payload: %v", ErrDecode, err)
	}
	return p, nil
}

func decidePreEdit(ctx context.Context, opts PreEditOptions, p PreToolUsePayload) (PreEditResponse, error) {
	// Bughunt-4 caps F5/F6: reject oversize snippets and over-count
	// MultiEdit BEFORE invoking the fabrication guard. The previous
	// capSnippets/truncate behavior silently dropped the offending
	// snippets, which let a fabricated reference at e.g. MultiEdit
	// position 150 sneak past the cap. Wrapping in ErrDecode maps
	// to exit-code 2 so Claude sees a clear "edit rejected" signal.
	if err := validateToolInputSizes(p.ToolName, p.ToolInput); err != nil {
		return PreEditResponse{}, err
	}
	snippets, targeted := snippetsForTool(p.ToolName, p.ToolInput)
	if !targeted {
		return allowResponse(), nil
	}
	filePath := strings.TrimSpace(p.ToolInput.FilePath)
	if filePath == "" {
		// NotebookEdit names the target as notebook_path. Fall back to it so
		// future non-.go work (or any caller swapping field names) doesn't
		// silently bypass the guard.
		filePath = strings.TrimSpace(p.ToolInput.NotebookPath)
	}
	if filePath == "" || !strings.HasSuffix(filePath, ".go") {
		return allowResponse(), nil
	}
	// Pre-load the target file's imports once so each snippet can resolve
	// aliases declared elsewhere in the same source file (Edit-to-a-body
	// snippets typically don't carry their own import block).
	fileImports := readFileImports(filePath)
	// Pre-load sibling packages in the same module so a snippet referencing
	// `pkg.Name` for an in-module package without an explicit import in the
	// target file still hits the fabrication check (hooks F8). Cheap walk
	// (PackageClauseOnly parse) — empty result on missing ModuleRoot.
	// Wrapped in its own span because bughunt-2 pre-edit F3 measured this
	// as the dominant cost (480ms warm at 10k files); a tagged build lets
	// operators see the cost per call instead of guessing.
	_, endSiblings := telemetry.Span(ctx, "leonard.pre-edit.sibling-scan")
	siblings := readSiblingPackages(opts.ModuleRoot, opts.ModulePath)
	endSiblings()
	seen := make(map[string]bool)
	var fabricated []string
	for _, snippet := range snippets {
		if strings.TrimSpace(snippet) == "" {
			continue
		}
		snippetFile := parseSnippet(filePath, snippet)
		if snippetFile == nil {
			// Unparseable snippet — let downstream tooling (go vet, go build)
			// catch it; the pre-edit hook deliberately doesn't fabricate
			// syntax errors of its own.
			continue
		}
		imports := collectImportsFromFile(snippetFile)
		for alias, path := range fileImports {
			if _, exists := imports[alias]; !exists {
				imports[alias] = path
			}
		}
		// Sibling map is the last-resort layer: only kicks in when neither
		// the snippet's wrapped imports nor the target file's imports
		// supplied an alias. An explicit import always wins, so a snippet
		// that does `import "external/lib"` and writes `lib.X` doesn't get
		// mis-resolved against an in-module `lib` package.
		for alias, path := range siblings {
			if _, exists := imports[alias]; !exists {
				imports[alias] = path
			}
		}
		refs, err := findFabricatedReferences(snippetFile, imports, opts)
		if err != nil {
			return PreEditResponse{}, err
		}
		for _, r := range refs {
			if !seen[r] {
				seen[r] = true
				fabricated = append(fabricated, r)
			}
		}
	}
	if len(fabricated) == 0 {
		return allowResponse(), nil
	}
	sort.Strings(fabricated)
	return blockResponse(fabricated), nil
}

// snippetsForTool returns the proposed source slices the handler has to
// scan for each write-shaped tool: one snippet per Edit/Write/NotebookEdit
// call, one per element of MultiEdit.edits. Tools the fabrication guard
// doesn't cover (Bash, Read, …) get targeted=false and pass through.
//
// MultiEdit returns each edit as its own snippet rather than a single
// concatenation: edits often target different syntactic positions in the
// file (one a top-level decl, another a function body), and gluing them
// together produces text that doesn't parse — letting the fabrication
// guard silently fail open. Per-edit snippets let parseSnippet's wrapper
// fallback do its job on each piece independently.
//
// Without MultiEdit coverage the guard was bypassable in practice — Claude
// reaches for MultiEdit whenever it has two or more changes in the same
// file, which is most non-trivial work.
func snippetsForTool(toolName string, in PreEditToolInput) ([]string, bool) {
	switch toolName {
	case "Edit":
		return []string{in.NewString}, true
	case "Write":
		return []string{in.Content}, true
	case "MultiEdit":
		out := make([]string, 0, len(in.Edits))
		for _, e := range in.Edits {
			out = append(out, e.NewString)
		}
		return out, true
	case "NotebookEdit":
		return []string{in.NewSource}, true
	default:
		return nil, false
	}
}

// validateToolInputSizes rejects payloads that exceed the per-snippet
// or per-element caps. Bughunt-4 caps F5/F6: the previous policy of
// silently zeroing oversize snippets / truncating over-count MultiEdits
// let a fabricated reference at a discarded position pass through the
// fabrication guard. Returning ErrDecode here maps to exit-code 2 so
// Claude sees a clear "edit rejected" signal.
func validateToolInputSizes(toolName string, in PreEditToolInput) error {
	switch toolName {
	case "Edit":
		if len(in.NewString) > MaxSnippetBytes {
			return fmt.Errorf("%w: Edit new_string exceeds %d bytes", ErrDecode, MaxSnippetBytes)
		}
	case "Write":
		if len(in.Content) > MaxSnippetBytes {
			return fmt.Errorf("%w: Write content exceeds %d bytes", ErrDecode, MaxSnippetBytes)
		}
	case "MultiEdit":
		if len(in.Edits) > MaxMultiEditElements {
			return fmt.Errorf("%w: MultiEdit edits count exceeds %d", ErrDecode, MaxMultiEditElements)
		}
		for i, e := range in.Edits {
			if len(e.NewString) > MaxSnippetBytes {
				return fmt.Errorf("%w: MultiEdit edits[%d].new_string exceeds %d bytes", ErrDecode, i, MaxSnippetBytes)
			}
		}
	case "NotebookEdit":
		if len(in.NewSource) > MaxSnippetBytes {
			return fmt.Errorf("%w: NotebookEdit new_source exceeds %d bytes", ErrDecode, MaxSnippetBytes)
		}
	}
	return nil
}

// parseSnippet tries a few wrappers so partial-edit snippets still parse.
// Order matters: a Write payload should parse as a whole file on the first
// pass; an Edit replacing a top-level declaration needs only the package
// header; an Edit replacing a function body needs both the header and a
// wrapping func. Returns nil when none of the attempts produce a clean AST —
// the caller then errs on the side of allowing the edit.
func parseSnippet(filePath, snippet string) *ast.File {
	fset := token.NewFileSet()
	attempts := []string{
		snippet,
		"package leonardshim\n\n" + snippet,
		"package leonardshim\n\nfunc _leonardShim() {\n" + snippet + "\n}\n",
	}
	for _, src := range attempts {
		if f, err := parser.ParseFile(fset, filePath, src, parser.AllErrors); err == nil {
			return f
		}
	}
	return nil
}

// collectImportsFromFile maps each import alias to its package path. The
// alias for an aliased import is the explicit name; for an unaliased one we
// fall back to the path's last segment (skipping a /vN major-version suffix).
// Blank and dot imports are skipped — neither contributes a usable alias.
func collectImportsFromFile(f *ast.File) map[string]string {
	out := make(map[string]string, len(f.Imports))
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		alias := importAlias(imp, path)
		if alias == "" || alias == "_" || alias == "." {
			continue
		}
		out[alias] = path
	}
	return out
}

func importAlias(imp *ast.ImportSpec, path string) string {
	if imp.Name != nil && imp.Name.Name != "" {
		return imp.Name.Name
	}
	return defaultPackageAlias(path)
}

// defaultPackageAlias guesses the package name for an unaliased import. This
// matches the directory's last segment for the vast majority of Go modules.
// Edge cases (package name differs from directory) cause at most a false
// negative here — never a wrongful block — which is acceptable v0 noise.
func defaultPackageAlias(path string) string {
	base := filepath.Base(path)
	if isMajorVersionSegment(base) {
		trimmed := strings.TrimSuffix(path, "/"+base)
		if trimmed != "" && trimmed != path {
			base = filepath.Base(trimmed)
		}
	}
	return base
}

// isMajorVersionSegment matches `vN` where N is a positive integer; those
// are the `/v2`-style suffixes Go module imports use for major versions.
func isMajorVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// readSiblingPackages walks moduleRoot for .go files and returns a map of
// package name → import path for every non-main, non-test package detected
// in the module. Used as a last-resort alias map (hooks F8): a snippet that
// references `pkg.X` for an in-module package gets caught by the
// fabrication guard even when neither the snippet nor the target file
// imports the package explicitly.
//
// Returns an empty map when moduleRoot or modulePath is empty (preserves
// pre-F8 behavior in tests / mis-configured projects) or when the walk hits
// an unreadable directory mid-stream. Per-file PackageClauseOnly parse is
// cheap — a single read up to the package keyword.
//
// First package name wins on collisions. The map is small (one entry per
// in-module package, not per file) so allocation cost is negligible even
// for large modules.
func readSiblingPackages(moduleRoot, modulePath string) map[string]string {
	out := map[string]string{}
	if moduleRoot == "" || modulePath == "" {
		return out
	}
	_ = filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != moduleRoot && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip symlinks — they'd let an attacker plant a `evil.go`
		// link pointing at /etc/passwd or similar; the parse below
		// would follow it. Security-1 F3.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly)
		if perr != nil || f.Name == nil {
			return nil
		}
		pkgName := f.Name.Name
		if pkgName == "" || pkgName == "main" {
			return nil
		}
		rel, rerr := filepath.Rel(moduleRoot, filepath.Dir(path))
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		importPath := modulePath
		if rel != "." {
			importPath = modulePath + "/" + rel
		}
		if _, exists := out[pkgName]; !exists {
			out[pkgName] = importPath
		}
		return nil
	})
	return out
}

// readFileImports parses just the import block of the target file (if it
// exists on disk) so an Edit-to-a-function-body can resolve aliases declared
// elsewhere in the same source file. Errors — including "file doesn't exist"
// for a Write creating a new path — yield an empty map.
func readFileImports(path string) map[string]string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return map[string]string{}
	}
	return collectImportsFromFile(f)
}

// findFabricatedReferences walks every SelectorExpr in the snippet whose
// receiver is an identifier matching a known import alias. For each such
// reference that points into a tracked package (under opts.ModulePath), the
// store is asked whether the symbol name exists; misses surface as
// fabricated references. Each `pkg.Name` pair is checked at most once.
func findFabricatedReferences(f *ast.File, imports map[string]string, opts PreEditOptions) ([]string, error) {
	seen := make(map[string]bool)
	var fabricated []string
	var storeErr error
	ast.Inspect(f, func(n ast.Node) bool {
		if storeErr != nil {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		path, found := imports[ident.Name]
		if !found {
			return true
		}
		if !isTrackedImport(path, opts.ModulePath) {
			return true
		}
		ref := ident.Name + "." + sel.Sel.Name
		if seen[ref] {
			return true
		}
		seen[ref] = true
		has, err := opts.Store.HasSymbol(sel.Sel.Name)
		if err != nil {
			storeErr = fmt.Errorf("hooks: lookup symbol %q: %w", sel.Sel.Name, err)
			return false
		}
		if !has {
			fabricated = append(fabricated, ref)
		}
		return true
	})
	if storeErr != nil {
		return nil, storeErr
	}
	sort.Strings(fabricated)
	return fabricated, nil
}

func isTrackedImport(path, modulePath string) bool {
	if modulePath == "" {
		return false
	}
	return path == modulePath || strings.HasPrefix(path, modulePath+"/")
}

func allowResponse() PreEditResponse {
	return PreEditResponse{Continue: true}
}

func blockResponse(fabricated []string) PreEditResponse {
	reason := fmt.Sprintf(
		"leonard pre-edit: blocked references to symbols not in the index: %s",
		strings.Join(fabricated, ", "),
	)
	return PreEditResponse{
		HookSpecificOutput: &PreToolUseSpecificOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       "deny",
			PermissionDecisionReason: reason,
		},
	}
}
