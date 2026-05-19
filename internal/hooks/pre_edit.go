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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

// PreEditToolInput captures the subset of Edit/Write tool_input we read.
// Unknown fields (old_string, replace_all, etc.) deserialize harmlessly and
// we drop them.
type PreEditToolInput struct {
	FilePath  string `json:"file_path"`
	NewString string `json:"new_string"`
	Content   string `json:"content"`
}

// PreEditResponse is the JSON document the pre-edit hook emits on stdout.
// Allow leaves Decision/Reason empty and Continue=true. Block sets
// Decision="block", a Reason that Claude Code surfaces to the model, and
// Continue=false so the tool call is rejected.
type PreEditResponse struct {
	Continue       bool   `json:"continue"`
	SuppressOutput bool   `json:"suppressOutput,omitempty"`
	Decision       string `json:"decision,omitempty"`
	Reason         string `json:"reason,omitempty"`
	StopReason     string `json:"stopReason,omitempty"`
}

// PreEditOptions wires the handler to its collaborators. ModulePath is the
// current go.mod module (e.g., "github.com/jasondillingham/leonard"); imports
// that fall outside that prefix are treated as external and skipped. An empty
// ModulePath disables blocking entirely — a useful safety net when go.mod
// can't be located.
type PreEditOptions struct {
	Store      SymbolStore
	ModulePath string
}

// HandlePreEdit reads a PreToolUse JSON payload from stdin, decides whether
// the proposed Edit/Write introduces a reference to a tracked symbol that
// doesn't exist in the index, and writes a hook response to stdout. Tool
// calls other than Edit/Write, non-Go files, and snippets whose references
// resolve only to stdlib or external packages all pass through.
func HandlePreEdit(ctx context.Context, opts PreEditOptions, stdin io.Reader, stdout io.Writer) error {
	if opts.Store == nil {
		return errors.New("hooks: SymbolStore is required")
	}
	payload, err := decodePreToolUsePayload(stdin)
	if err != nil {
		return err
	}
	decision, err := decidePreEdit(opts, payload)
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
	body, err := io.ReadAll(r)
	if err != nil {
		return p, fmt.Errorf("hooks: read stdin: %w", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return p, errors.New("hooks: empty PreToolUse payload on stdin")
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("hooks: decode PreToolUse payload: %w", err)
	}
	return p, nil
}

func decidePreEdit(opts PreEditOptions, p PreToolUsePayload) (PreEditResponse, error) {
	snippet, targeted := snippetForTool(p.ToolName, p.ToolInput)
	if !targeted {
		return allowResponse(), nil
	}
	filePath := strings.TrimSpace(p.ToolInput.FilePath)
	if filePath == "" || !strings.HasSuffix(filePath, ".go") {
		return allowResponse(), nil
	}
	if strings.TrimSpace(snippet) == "" {
		return allowResponse(), nil
	}
	snippetFile := parseSnippet(filePath, snippet)
	if snippetFile == nil {
		// Unparseable snippets — let downstream tooling (go vet, go build)
		// catch them; the pre-edit hook deliberately doesn't fabricate
		// syntax errors of its own.
		return allowResponse(), nil
	}
	imports := collectImportsFromFile(snippetFile)
	for alias, path := range readFileImports(filePath) {
		if _, exists := imports[alias]; !exists {
			imports[alias] = path
		}
	}
	fabricated, err := findFabricatedReferences(snippetFile, imports, opts)
	if err != nil {
		return PreEditResponse{}, err
	}
	if len(fabricated) == 0 {
		return allowResponse(), nil
	}
	return blockResponse(fabricated), nil
}

// snippetForTool returns the proposed source the handler has to scan: the
// Edit payload's new_string or the Write payload's content. Tools we don't
// recognize (Bash, Read, MultiEdit, …) get targeted=false and pass through.
func snippetForTool(toolName string, in PreEditToolInput) (string, bool) {
	switch toolName {
	case "Edit":
		return in.NewString, true
	case "Write":
		return in.Content, true
	default:
		return "", false
	}
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
		Continue:   false,
		Decision:   "block",
		Reason:     reason,
		StopReason: reason,
	}
}
