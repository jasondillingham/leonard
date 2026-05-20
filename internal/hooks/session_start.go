package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jasondillingham/leonard/internal/store"
)

// SessionStartPayload mirrors the Claude Code SessionStart hook envelope.
// Source is the only field we currently branch on (see shouldInjectForSource).
// Decoding the rest is a well-formedness check so a malformed invocation
// surfaces as an error rather than silently emitting a no-op response.
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
//
// The payload.Source field gates injection: on "compact" Claude already has
// the prior decisions via the transcript summary, and on "clear" the user
// explicitly asked for a fresh slate. In both cases we skip the store read
// and emit a no-injection response. See shouldInjectForSource.
func HandleSessionStart(_ context.Context, opts SessionStartOptions, stdin io.Reader, stdout io.Writer) error {
	payload, err := decodeSessionStartPayload(stdin)
	if err != nil {
		return err
	}

	resp := SessionStartResponse{Continue: true}

	if !shouldInjectForSource(payload.Source) {
		if err := json.NewEncoder(stdout).Encode(resp); err != nil {
			return fmt.Errorf("hooks: encode SessionStart response: %w", err)
		}
		return nil
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

// shouldInjectForSource reports whether SessionStart should inject the prior-
// decisions block for the given source value. Claude Code documents four
// source values: "startup", "resume", "clear", "compact".
//
// Skip injection on:
//   - "compact" — the transcript summary already carries the prior context;
//     re-injecting the decisions block is duplicate context that wastes
//     tokens on every compaction.
//   - "clear"   — the user explicitly asked for a fresh slate.
//
// Inject otherwise (startup, resume, empty, or any future-unknown value).
// Defaulting unknown to inject preserves the original feature for callers
// that don't set source, and gives a future Claude Code source the
// startup-equivalent behavior.
func shouldInjectForSource(source string) bool {
	switch source {
	case "compact", "clear":
		return false
	default:
		return true
	}
}

func decodeSessionStartPayload(r io.Reader) (SessionStartPayload, error) {
	var p SessionStartPayload
	body, err := readPayloadBytes(r)
	if err != nil {
		return p, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return p, fmt.Errorf("%w: empty SessionStart payload on stdin", ErrDecode)
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("%w: decode SessionStart payload: %v", ErrDecode, err)
	}
	return p, nil
}

// sessionStartBulletMax caps each decision bullet's reasoning prefix
// at 240 runes (~2× the Stop hook's 120 — decisions are inherently
// chunkier than claim summaries, but unbounded inject was a real
// problem). Bughunt-4 mcp F3 measured 360 KiB of inject in the worst
// case under v0.12.0; with this cap the worst case is ~10 decisions
// × ~240 chars ≈ 2.4 KiB, well within reason for SessionStart context.
const sessionStartBulletMax = 240

// formatDecisions renders the bullet list. The output is plain Markdown with a
// single leading heading so Claude Code surfaces it cleanly as injected
// context. We don't filter superseded rows here — `store.GetDecisions` returns
// newest-first, so a freshly-recorded supersede sits on top of the row it
// replaces.
//
// Each bullet's reasoning prefix is capped at sessionStartBulletMax runes
// to bound the total inject size (bughunt-4 mcp F3).
func formatDecisions(decisions []store.Decision) string {
	var b strings.Builder
	b.WriteString("## Prior decisions (from Leonard)\n\n")
	for _, d := range decisions {
		topic := truncatePrefix(strings.TrimSpace(d.Topic), 80)
		choice := truncatePrefix(strings.TrimSpace(d.Choice), 200)
		reason := truncatePrefix(firstNonEmptyLine(d.Reasoning), sessionStartBulletMax)
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
