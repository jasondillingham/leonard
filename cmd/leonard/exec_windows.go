//go:build windows

package main

import (
	"os"
	"os/exec"
)

// execMCP on Windows falls back to a child process because syscall.Exec is
// not available. We forward stdin/stdout/stderr verbatim and exit with the
// child's status so MCP framing is preserved.
func execMCP(path string, argv []string) error {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
