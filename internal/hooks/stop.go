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

// StopPayload mirrors the Claude Code Stop hook envelope. SessionID is the
// only field the handler actually reads — Claude Code carries the active
// session identifier in the documented `session_id` field, and we plumb it
// straight into store.GetUnverifiedClaims so the surfaced claims are scoped
// to the session that's about to end. The other fields are decoded purely
// as a well-formedness check so a malformed invocation surfaces as an error
// rather than silently no-op.
type StopPayload struct {
	SessionID      string `json:"session_id"`
	HookEventName  string `json:"hook_event_name"`
	StopHookActive bool   `json:"stop_hook_active"`
	CWD            string `json:"cwd"`
}

// ClaimsReader is the minimum surface internal/store.Store must satisfy for
// the Stop hook. Keeping the contract local means the handler can be
// unit-tested without standing up a real SQLite store.
type ClaimsReader interface {
	GetUnverifiedClaims(sessionID string) ([]store.Claim, error)
}

// StopResponse is the JSON document the Stop hook emits. Claude Code's
// schema validator only accepts hookSpecificOutput for PreToolUse,
// UserPromptSubmit, PostToolUse, and PostToolBatch — emitting it from
// Stop dumps a schema-mismatch error into the user's session every time
// the hook fires. Stop's surfacing channels are systemMessage (visible
// to the user, not the model) or decision="block" + reason (visible to
// the model, but blocks the session from ending).
//
// We use systemMessage: the original intent of the surfacing is
// advisory and per-edit failure context already reaches the model via
// PostToolUse's hookSpecificOutput.additionalContext (the F4 fix), so
// duplicating that surface at Stop time would be redundant and would
// turn every routine session-end into an interrupt.
type StopResponse struct {
	Continue       bool   `json:"continue"`
	SuppressOutput bool   `json:"suppressOutput,omitempty"`
	SystemMessage  string `json:"systemMessage,omitempty"`
}

// StopOptions wires the Stop handler to its collaborators. A nil Claims
// reader is treated as "store not yet created" and produces a no-op
// response — the brief mandates this so a fresh checkout that hasn't run
// `leonard init` doesn't fail its first session stop.
type StopOptions struct {
	Claims ClaimsReader
	Limit  int
}

// DefaultStopClaimLimit is the fallback used when StopOptions.Limit is zero
// or negative. Matches the default written by `leonard init` for the
// surface_unverified_claims_at_stop config key.
const DefaultStopClaimLimit = 20

// stopClaimPrefixMax bounds the bullet-line prefix taken from a claim's
// text so a single oversized claim can't blow out the surfaced Markdown.
const stopClaimPrefixMax = 120

// HandleStop reads a Stop JSON envelope from stdin, fetches unverified
// claims for the payload's session from the store, formats up to limit of
// them as a short Markdown block, and writes a Stop hook response that
// surfaces that block as additional system context.
//
// When opts.Claims is nil (no store yet) or returns an empty slice, the
// handler emits a minimal response with Continue=true and no surfacing.
// Continue is always true — phase 3 keeps the hook advisory.
func HandleStop(_ context.Context, opts StopOptions, stdin io.Reader, stdout io.Writer) error {
	payload, err := decodeStopPayload(stdin)
	if err != nil {
		return err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultStopClaimLimit
	}

	var claims []store.Claim
	if opts.Claims != nil {
		got, err := opts.Claims.GetUnverifiedClaims(payload.SessionID)
		if err != nil {
			return fmt.Errorf("hooks: read unverified claims: %w", err)
		}
		claims = got
	}

	resp := StopResponse{Continue: true}
	if len(claims) > 0 {
		if len(claims) > limit {
			claims = claims[:limit]
		}
		resp.SystemMessage = formatUnverifiedClaims(claims)
	}
	if err := json.NewEncoder(stdout).Encode(resp); err != nil {
		return fmt.Errorf("hooks: encode Stop response: %w", err)
	}
	return nil
}

func decodeStopPayload(r io.Reader) (StopPayload, error) {
	var p StopPayload
	body, err := readPayloadBytes(r)
	if err != nil {
		return p, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return p, fmt.Errorf("%w: empty Stop payload on stdin", ErrDecode)
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("%w: decode Stop payload: %v", ErrDecode, err)
	}
	return p, nil
}

// formatUnverifiedClaims renders the bullet list. Each bullet starts from
// the claim's first non-empty line and is truncated at stopClaimPrefixMax
// runes so a long claim doesn't dominate the surfaced block.
func formatUnverifiedClaims(claims []store.Claim) string {
	var b strings.Builder
	b.WriteString("## Unverified claims (from Leonard)\n\n")
	for _, c := range claims {
		b.WriteString("- ")
		b.WriteString(truncatePrefix(firstNonEmptyLine(c.Claim), stopClaimPrefixMax))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncatePrefix returns s if it's at or under max runes, otherwise the
// first max-1 runes followed by "…". Operates on runes, not bytes, so
// multi-byte text is sliced safely.
func truncatePrefix(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
