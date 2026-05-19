package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jasondillingham/leonard/internal/store"
)

// SessionStartPayload mirrors the Claude Code SessionStart hook envelope.
// We don't actually consume any of these fields today — decoding is purely a
// well-formedness check so a malformed invocation surfaces as an error rather
// than silently emitting a no-op response.
type SessionStartPayload struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Source        string `json:"source"`
	CWD           string `json:"cwd"`
}

// DecisionReader is the minimum surface internal/store.Store must satisfy for
// the SessionStart hook. Keeping the contract local (and shared with the
// in-process decisions MCP work) means we can unit-test without a real DB.
type DecisionReader interface {
	GetDecisions(topic string, since int64, limit int) ([]store.Decision, error)
}

// SessionStartResponse follows Claude Code's documented hook response shape
// for SessionStart events: the injected Markdown lives in
// hookSpecificOutput.additionalContext, not in the generic systemMessage
// field used by PostToolUse.
type SessionStartResponse struct {
	Continue           bool                        `json:"continue"`
	SuppressOutput     bool                        `json:"suppressOutput,omitempty"`
	HookSpecificOutput *SessionStartSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// SessionStartSpecificOutput carries the injection payload. AdditionalContext
// is prepended to Claude's system context for the new session.
type SessionStartSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// SessionStartOptions wires the SessionStart handler to its collaborators.
// A nil Decisions reader is treated as "store not yet created" and produces a
// no-op response — the brief calls this out so a fresh project that hasn't
// run `leonard init` doesn't fail its first session.
type SessionStartOptions struct {
	Decisions DecisionReader
	Limit     int
}

// DefaultDecisionsLimit is the fallback used when SessionStartOptions.Limit
// is zero or negative. Mirrors the default written by `leonard init` to
// .leonard/config.toml so the hook's behavior matches the documented config.
const DefaultDecisionsLimit = 10

// HandleSessionStart reads a SessionStart JSON envelope from stdin, fetches
// the most-recent decisions from the store, formats them as a short Markdown
// block, and writes a SessionStart hook response that injects that block as
// additional system context.
//
// When opts.Decisions is nil (no store yet) or returns an empty slice, the
// handler emits a minimal response with Continue=true and no injection.
func HandleSessionStart(_ context.Context, opts SessionStartOptions, stdin io.Reader, stdout io.Writer) error {
	if _, err := decodeSessionStartPayload(stdin); err != nil {
		return err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultDecisionsLimit
	}

	var decisions []store.Decision
	if opts.Decisions != nil {
		got, err := opts.Decisions.GetDecisions("", 0, limit)
		if err != nil {
			return fmt.Errorf("hooks: read decisions: %w", err)
		}
		decisions = got
	}

	resp := SessionStartResponse{Continue: true}
	if len(decisions) > 0 {
		resp.HookSpecificOutput = &SessionStartSpecificOutput{
			HookEventName:     "SessionStart",
			AdditionalContext: formatDecisions(decisions),
		}
	}
	if err := json.NewEncoder(stdout).Encode(resp); err != nil {
		return fmt.Errorf("hooks: encode SessionStart response: %w", err)
	}
	return nil
}

func decodeSessionStartPayload(r io.Reader) (SessionStartPayload, error) {
	var p SessionStartPayload
	body, err := io.ReadAll(r)
	if err != nil {
		return p, fmt.Errorf("hooks: read stdin: %w", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return p, errors.New("hooks: empty SessionStart payload on stdin")
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("hooks: decode SessionStart payload: %w", err)
	}
	return p, nil
}

// formatDecisions renders the bullet list. The output is plain Markdown with a
// single leading heading so Claude Code surfaces it cleanly as injected
// context. We don't filter superseded rows here — `store.GetDecisions` returns
// newest-first, so a freshly-recorded supersede sits on top of the row it
// replaces.
func formatDecisions(decisions []store.Decision) string {
	var b strings.Builder
	b.WriteString("## Prior decisions (from Leonard)\n\n")
	for _, d := range decisions {
		topic := strings.TrimSpace(d.Topic)
		choice := strings.TrimSpace(d.Choice)
		reason := firstNonEmptyLine(d.Reasoning)
		fmt.Fprintf(&b, "- **%s** → %s", topic, choice)
		if reason != "" {
			fmt.Fprintf(&b, " — %s", reason)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if l != "" {
			return l
		}
	}
	return ""
}
