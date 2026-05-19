// Package mcp implements Leonard's MCP tool surface: verify_symbol,
// find_symbol, list_files, and (later) decision/claim tools. Handlers are
// thin shims over a SymbolStore — the real implementation lives in
// internal/store; tests use the in-memory store in this package.
package mcp
