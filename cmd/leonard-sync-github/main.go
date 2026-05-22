// Command leonard-sync-github is the built-in github sync plugin
// (#32). It implements the JSON-stdin/JSON-stdout sync plugin
// protocol defined in internal/adapters/groundtruth/sync.
//
// Configure in .leonard/config.toml:
//
//	[sync.github]
//	command = "/path/to/leonard-sync-github"
//
//	[sync.github.config]
//	# (no per-plugin config needed; auth via GH_TOKEN env var)
//
// Auth: reads GH_TOKEN from the environment. If unset, runs as an
// anonymous client (rate-limited to 60 req/hour per GitHub policy).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	syncpkg "github.com/jasondillingham/leonard/internal/adapters/groundtruth/sync"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth/sync/github"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "leonard-sync-github:", err)
		os.Exit(1)
	}
}

func run() error {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var input syncpkg.Input
	if err := json.Unmarshal(body, &input); err != nil {
		return fmt.Errorf("parse input: %w", err)
	}
	out, err := github.Sync(input, github.Options{
		AccessToken: os.Getenv("GH_TOKEN"),
	})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	return nil
}
