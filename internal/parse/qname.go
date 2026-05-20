package parse

import "strings"

// moduleQualifier returns the dotted prefix used in Python/TypeScript
// qualified_names so two files with the same base name (e.g. utils.py
// in different directories) don't shadow each other in the index. The
// file's path separators become dots and the extension is stripped:
//
//	"module.py"                  → "module"
//	"testdata/python/module.py"  → "testdata.python.module"
//	"src/components/Button.tsx"  → "src.components.Button"
//
// An empty path returns "" so a caller that constructed a parser
// without one keeps the pre-prefix bare-name behavior. Go is unaffected
// — its qualified_name convention is package-scoped, not file-scoped.
func moduleQualifier(path string) string {
	if path == "" {
		return ""
	}
	path = strings.ReplaceAll(path, "\\", "/")
	if dot := strings.LastIndex(path, "."); dot >= 0 {
		if !strings.Contains(path[dot:], "/") {
			path = path[:dot]
		}
	}
	return strings.ReplaceAll(path, "/", ".")
}

// joinQName is the small "prefix + name" composer the parsers use. An
// empty prefix returns name unchanged, so legacy callers that didn't
// thread a path through still produce bare-name qnames.
func joinQName(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
