// Package hooks holds the Claude Code hook handlers — one function per
// subcommand of leonard-hook (post-edit, pre-edit, session-start, stop).
// Each reads the hook JSON payload from stdin and writes a hook response to
// stdout.
package hooks
