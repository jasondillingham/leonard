package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jasondillingham/leonard/internal/index"
)

// MakeShellRunner returns a VetRunner that executes command through
// `sh -c`. The shell affords standard quoting and composition
// (`&&`, `|`, env vars) without requiring callers to pre-tokenize
// their command. workingDir overrides the directory the command runs
// in; when empty, the command inherits the projectRoot passed to the
// returned VetRunner (the same directory the default RunGoVet uses).
//
// Security review #2 F2 (v0.46): workingDir is path-trust-validated
// against projectRoot via index.ResolveSafe. The pre-v0.46 runner
// took the config value verbatim — `working_dir = "/etc"` would
// run the verifier in /etc with access to whatever files live
// there. Trust boundary: even though `.leonard/config.toml` is
// (now, post-v0.46 F1) operator-authored, an unbounded working_dir
// is still a confused-deputy footgun for an operator typo.
// Working dirs that escape the project root fall back to
// projectRoot with a captured-output note so the operator sees
// what happened on the next post-edit.
//
// Platforms without `/bin/sh` (e.g. Windows native) will fail at
// exec time with a clear error; WSL and macOS/Linux work as expected.
func MakeShellRunner(command, workingDir string) VetRunner {
	return func(ctx context.Context, projectRoot string) (string, error) {
		dir := projectRoot
		var rejection string
		if trimmed := strings.TrimSpace(workingDir); trimmed != "" {
			if safe, ok := index.ResolveSafe(projectRoot, trimmed); ok {
				dir = safe
			} else {
				rejection = fmt.Sprintf("leonard: [post_edit.verify].working_dir %q resolves outside the project root; falling back to project root\n", trimmed)
			}
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = dir
		var buf bytes.Buffer
		if rejection != "" {
			buf.WriteString(rejection)
		}
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
