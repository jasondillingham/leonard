package mcp

import (
	"path/filepath"
	"strings"
)

// languageFromPath maps a file extension (or basename, for the
// extensionless cases the indexer registers via langExtractorsByName)
// to a canonical language label. Centralized here so verify_symbol's
// and find_symbol's language filter stays consistent with what the
// indexer actually labels files as.
//
// Bughunt-6 mcp F1: the v0.1 implementation only knew Go / Python /
// TypeScript / JavaScript — every other extension returned "" and
// filtered to zero matches. This table now covers all 39 registered
// languages.
//
// Unknown extensions return "" — callers treat that as "no filter
// match". Lowercase comparison matches the indexer's dispatchByExt
// normalization.
func languageFromPath(p string) string {
	base := strings.ToLower(filepath.Base(p))
	// Basename-based dispatch (matches langExtractorsByName).
	switch base {
	case "makefile", "gnumakefile":
		return "make"
	case "cmakelists.txt":
		return "cmake"
	case "justfile":
		return "just"
	case "build", "build.bazel", "workspace", "workspace.bazel":
		return "starlark"
	case "package.json", "cargo.toml", "go.mod", "pom.xml":
		return "manifest"
	}
	// Extension-based dispatch (matches langExtractors).
	switch strings.ToLower(filepath.Ext(p)) {
	// Production-dogfooded parsers
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".rs":
		return "rust"
	// Tree-sitter languages
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".cs":
		return "csharp"
	case ".swift":
		return "swift"
	case ".kt", ".kts":
		return "kotlin"
	case ".scala":
		return "scala"
	case ".dart":
		return "dart"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh":
		return "cpp"
	case ".php":
		return "php"
	case ".lua":
		return "lua"
	case ".sh", ".bash":
		return "bash"
	case ".zig":
		return "zig"
	case ".nix":
		return "nix"
	case ".ex", ".exs":
		return "elixir"
	case ".sol":
		return "solidity"
	case ".erl", ".hrl":
		return "erlang"
	case ".r":
		return "r"
	case ".bzl", ".bazel", ".star":
		return "starlark"
	case ".mk":
		return "make"
	case ".cmake":
		return "cmake"
	case ".tf", ".tfvars", ".hcl":
		return "hcl"
	case ".graphql", ".gql":
		return "graphql"
	case ".proto":
		return "proto"
	case ".wit":
		return "wit"
	case ".sql":
		return "sql"
	case ".glsl", ".vert", ".frag", ".geom", ".comp", ".tesc", ".tese":
		return "glsl"
	case ".hlsl", ".fx", ".fxh":
		return "hlsl"
	// SFC preprocessors
	case ".vue":
		return "vue"
	case ".svelte":
		return "svelte"
	case ".astro":
		return "astro"
	// Structured-file inspectors
	case ".ipynb":
		return "jupyter"
	// OpenAPI uses basename-based dispatch; the extension can be
	// .yaml/.yml/.json. We can't reliably map those generically.
	default:
		return ""
	}
}
