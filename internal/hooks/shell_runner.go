package hooks

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

// MakeShellRunner returns a VetRunner that executes command through
// `sh -c`. The shell affords standard quoting and composition
// (`&&`, `|`, env vars) without requiring callers to pre-tokenize
// their command. workingDir overrides the directory the command runs
// in; when empty, the command inherits the projectRoot passed to the
// returned VetRunner (the same directory the default RunGoVet uses).
//
// The command string is sourced from project-local
// `.leonard/config.toml` ([post_edit.verify].command), which is
// already a trust boundary the user has authored — there is no
// additional untrusted input flowing through here. The shell runs
// in a context with a timeout enforced upstream by runVet.
//
// Platforms without `/bin/sh` (e.g. Windows native) will fail at
// exec time with a clear error; WSL and macOS/Linux work as expected.
func MakeShellRunner(command, workingDir string) VetRunner {
	return func(ctx context.Context, projectRoot string) (string, error) {
		dir := strings.TrimSpace(workingDir)
		if dir == "" {
			dir = projectRoot
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = dir
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		return strings.TrimSpace(buf.String()), err
	}
}

// VerifyVerb extracts a short label from a shell command for use in
// claim summaries and evidence blocks. Returns the first whitespace-
// separated token ("cargo check --workspace" → "cargo check"), keeping
// enough context to disambiguate `cargo check` from `cargo test` while
// dropping noisy flags. Falls back to "verify" for empty input.
func VerifyVerb(command string) string {
	fields := strings.Fields(command)
	switch len(fields) {
	case 0:
		return "verify"
	case 1:
		return fields[0]
	default:
		// Keep the program + its first arg ("cargo check", "pnpm tsc",
		// "ruff check"). Anything past that is usually flags.
		second := fields[1]
		if strings.HasPrefix(second, "-") {
			return fields[0]
		}
		return fields[0] + " " + second
	}
}
