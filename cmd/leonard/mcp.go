package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

// mcpBinaryName is the name of the leonard-mcp executable we shell out to.
// Cobra `mcp` is a convenience pass-through so users can register a single
// command in their MCP config (`leonard mcp`) and get the stdio server.
const mcpBinaryName = "leonard-mcp"

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Exec leonard-mcp (stdio MCP server).",
		Long:  "Convenience pass-through that exec's leonard-mcp, looking it up first beside the current binary and then on $PATH.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := locateMCP()
			if err != nil {
				return err
			}
			full := append([]string{path}, args...)
			if err := execMCP(path, full); err != nil {
				return fmt.Errorf("exec %s: %w", path, err)
			}
			return nil
		},
	}
}

// locateMCP prefers a leonard-mcp binary sitting next to the running leonard
// binary; falls back to PATH. Returns a clear error if neither resolves.
func locateMCP() (string, error) {
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), mcpBinaryName)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath(mcpBinaryName); err == nil {
		return path, nil
	}
	return "", errors.New("leonard-mcp not found beside leonard or on PATH")
}
