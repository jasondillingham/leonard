package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Plugin describes one sync plugin: name (operator-friendly) and
// the executable path. Config is the per-plugin config passed
// verbatim to the plugin on stdin.
type Plugin struct {
	Name    string
	Command string
	Config  map[string]any
}

// Input is the JSON envelope the runner pipes to the plugin's stdin.
type Input struct {
	Facts  map[string]any `json:"facts"`
	Config map[string]any `json:"config"`
}

// Output is the JSON envelope the runner expects on the plugin's
// stdout. UpdatedFacts replaces the supplied Facts subset;
// Changes is the list of per-field deltas for audit/log rendering.
type Output struct {
	UpdatedFacts map[string]any `json:"updated_facts"`
	Changes      []Change       `json:"changes"`
}

// Change is a single per-field update reported by the plugin.
// Path is a dotted facts.yaml path (e.g., "oss_contributions.0.status").
// Old / New are the values before and after the update. Reason is
// the plugin's human-readable explanation.
type Change struct {
	Path   string `json:"path"`
	Old    any    `json:"old,omitempty"`
	New    any    `json:"new,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Result wraps Output with the runner-side metadata callers need:
// stderr from the plugin (surfaced verbatim on operator failure
// messages) and the plugin's wall-clock runtime.
type Result struct {
	Output
	Stderr  string
	Elapsed time.Duration
}

// defaultTimeout caps a single plugin invocation. Generous because
// network-bound sync plugins (GitHub API, etc.) hit rate limits;
// hard ceiling so a hung plugin doesn't block `leonard sync` forever.
const defaultTimeout = 5 * time.Minute

// maxPluginOutputBytes caps stdout / stderr per invocation
// (bughunt-11 F4). A misbehaving plugin that writes gigabytes to
// stdout would otherwise OOM the leonard CLI process. 16 MiB is
// generous so legitimate plugins (large facts.yaml updates,
// verbose progress on stderr) aren't truncated.
const maxPluginOutputBytes = 16 << 20 // 16 MiB

// boundedBuffer is an io.Writer that drops writes past a cap and
// flags Truncated. Used to wrap both stdout and stderr so a
// misbehaving plugin can't exhaust the CLI process's memory.
type boundedBuffer struct {
	buf       bytes.Buffer
	cap       int
	Truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.cap - b.buf.Len()
	if remaining <= 0 {
		b.Truncated = true
		return len(p), nil
	}
	if len(p) <= remaining {
		return b.buf.Write(p)
	}
	if _, err := b.buf.Write(p[:remaining]); err != nil {
		return 0, err
	}
	b.Truncated = true
	return len(p), nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }
func (b *boundedBuffer) Bytes() []byte  { return b.buf.Bytes() }

var _ io.Writer = (*boundedBuffer)(nil)

// Run invokes the plugin synchronously. Returns the parsed Output
// plus runner metadata. Returns an error on:
//   - Plugin.Command empty
//   - exec.Command failed to start
//   - timeout (uses defaultTimeout when ctx has no deadline)
//   - plugin exited non-zero (Stderr in the Result carries the
//     plugin's error message)
//   - stdout wasn't a valid Output JSON envelope
func Run(ctx context.Context, p Plugin, facts map[string]any) (Result, error) {
	if p.Command == "" {
		return Result{}, errors.New("sync: Plugin.Command is required")
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	payload, err := json.Marshal(Input{
		Facts:  facts,
		Config: p.Config,
	})
	if err != nil {
		return Result{}, fmt.Errorf("sync: marshal input: %w", err)
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, p.Command)
	cmd.Stdin = bytes.NewReader(payload)
	// bughunt-11 F4: bounded buffers so a runaway plugin can't
	// exhaust memory.
	stdout := &boundedBuffer{cap: maxPluginOutputBytes}
	stderr := &boundedBuffer{cap: maxPluginOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return Result{
			Stderr:  stderr.String(),
			Elapsed: time.Since(start),
		}, fmt.Errorf("sync: plugin %q failed: %w", p.Name, err)
	}

	if stdout.Truncated {
		return Result{
			Stderr:  stderr.String(),
			Elapsed: time.Since(start),
		}, fmt.Errorf("sync: plugin %q stdout exceeded %d-byte cap; refusing to parse partial output", p.Name, maxPluginOutputBytes)
	}

	var out Output
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return Result{
			Stderr:  stderr.String(),
			Elapsed: time.Since(start),
		}, fmt.Errorf("sync: plugin %q stdout is not valid JSON: %w", p.Name, err)
	}

	return Result{
		Output:  out,
		Stderr:  stderr.String(),
		Elapsed: time.Since(start),
	}, nil
}
