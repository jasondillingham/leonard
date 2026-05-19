package mcp

import (
	"path/filepath"
	"strings"
)

// languageFromPath maps a file extension to a canonical language label.
// Centralized here so verify_symbol's language filter stays consistent
// with whatever the indexer eventually writes into store.File.Language.
// Unknown extensions return "" — callers treat that as "no filter match".
func languageFromPath(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	default:
		return ""
	}
}
