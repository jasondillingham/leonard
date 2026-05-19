//go:build !windows

package main

import "syscall"

// execMCP replaces the current process with leonard-mcp so stdin/stdout are
// inherited unchanged — important because MCP is a stdio protocol and any
// buffering pipe between Claude Code and the server would corrupt the JSON-RPC
// framing.
func execMCP(path string, argv []string) error {
	return syscall.Exec(path, argv, syscall.Environ())
}
