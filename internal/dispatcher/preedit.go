package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/hooks"
)

// HandlePreEdit reads a Claude Code PreToolUse envelope from r, runs
// it through every loaded adapter, aggregates verdicts (deny-beats-
// pass, first-deny-wins reason), and writes the matching
// PreEditResponse to w.
//
// This is the #46 replacement for the cmd/leonard-hook pre-edit
// subcommand calling hooks.HandlePreEdit directly. The CodeAdapter
// still wraps hooks.HandlePreEdit internally (so the fabrication-
// guard logic is exercised verbatim); the dispatcher adds the
// ground-truth + self-logging adapters on top.
func (l *Loaded) HandlePreEdit(ctx context.Context, r io.Reader, w io.Writer) error {
	var env hooks.PreToolUsePayload
	if err := decodeEnvelope(r, &env); err != nil {
		return fmt.Errorf("dispatcher pre-edit: %w", err)
	}

	payload := adapters.PreEditPayload{
		SessionID: env.SessionID,
		Tool:      env.ToolName,
		FilePath:  preEditFilePath(env.ToolInput),
		Content:   preEditContent(env.ToolInput),
		Command:   env.ToolInput.Command,
		CWD:       env.CWD,
	}

	results := make([]adapters.PreEditResult, 0, len(l.Adapters))
	for _, a := range l.Adapters {
		res, err := a.PreEdit(ctx, payload)
		if err != nil {
			// hooks.ErrDecode is not an adapter malfunction — it is a
			// deliberate rejection of the payload itself (oversize
			// snippet, over-count MultiEdit, unparseable envelope).
			// internal/hooks maps it to exit 2 precisely so the edit is
			// blocked. Treating it like a soft failure inverted that:
			// an oversize Write to `.leonard/config.toml` returned
			// ErrDecode, fell through to `continue`, produced zero
			// results, and aggregated to Pass — allowing the edit the
			// guard exists to deny. Fail closed instead.
			if errors.Is(err, hooks.ErrDecode) {
				// The detail goes to stderr (operator-facing), not into
				// Reason. Reason is rendered to the model inside a
				// Leonard-branded block, and a decode error can quote a
				// fragment of the rejected payload back into it —
				// letting payload bytes masquerade as Leonard's own
				// ground-truth voice.
				fmt.Fprintf(stderrOf(a), "leonard dispatcher: %s rejected pre-edit payload: %v\n", a.Name(), err)
				results = append(results, adapters.PreEditResult{
					Decision:    adapters.Deny,
					Reason:      fmt.Sprintf("leonard pre-edit: the %s adapter rejected this payload as malformed or oversize. Check the hook's stderr for the specific limit.", a.Name()),
					AdapterName: a.Name(),
				})
				continue
			}
			// Other per-adapter failures don't deny the edit; log and
			// move on so a malformed truth file (for example)
			// doesn't block all edits.
			fmt.Fprintf(stderrOf(a), "leonard dispatcher: %s pre-edit error: %v\n", a.Name(), err)
			continue
		}
		if res.AdapterName == "" {
			res.AdapterName = a.Name()
		}
		results = append(results, res)
	}

	final := adapters.AggregatePreEdit(results)
	return writePreEditResponse(w, final)
}

// preEditFilePath flattens NotebookEdit's notebook_path into the same
// FilePath field as Edit/Write — matches what internal/hooks does
// internally when deciding which path to evaluate.
func preEditFilePath(in hooks.PreEditToolInput) string {
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.NotebookPath
}

// preEditContent joins MultiEdit's per-edit new_string snippets so
// adapters that scan the proposed write see the full delta — matches
// what the existing fabrication guard does for MultiEdit envelopes.
func preEditContent(in hooks.PreEditToolInput) string {
	if in.NewString != "" {
		return in.NewString
	}
	if in.Content != "" {
		return in.Content
	}
	if in.NewSource != "" {
		return in.NewSource
	}
	if len(in.Edits) == 0 {
		return ""
	}
	var b strings.Builder
	for i, e := range in.Edits {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(e.NewString)
	}
	return b.String()
}

// writePreEditResponse renders the aggregated decision into the
// PreEditResponse wire shape Claude Code expects.
func writePreEditResponse(w io.Writer, res adapters.PreEditResult) error {
	resp := hooks.PreEditResponse{}
	if res.Decision == adapters.Deny || res.Decision == adapters.Ask {
		// Ask collapses to deny in v0.6+ until a separate Ask flow
		// lands. The aggregator already promotes Ask to Deny but
		// we double-check here for direct callers.
		resp.HookSpecificOutput = &hooks.PreToolUseSpecificOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       "deny",
			PermissionDecisionReason: res.Reason,
		}
	} else {
		resp.Continue = true
	}
	return json.NewEncoder(w).Encode(resp)
}

// stderrOf returns the stderr writer for an adapter. v0.6 adapters
// store their own stderr handle; the dispatcher uses os.Stderr as
// a fallback. Currently the only path here is the fallback — the
// adapter system doesn't expose its stderr externally yet — but the
// indirection means we can route per-adapter logs later without
// changing call sites.
func stderrOf(_ adapters.Adapter) io.Writer {
	// TODO(#46-followup): return the per-adapter stderr the dispatcher
	// passed at Init time. For now leonard-hook's stderr is the
	// shared destination.
	return globalStderr
}

// globalStderr is set by cmd/leonard-hook at process startup to
// os.Stderr. Defaults to io.Discard so unit tests don't leak logs.
var globalStderr io.Writer = io.Discard

// SetGlobalStderr lets cmd/leonard-hook route adapter logs to its
// own stderr without each adapter needing a back-pointer.
func SetGlobalStderr(w io.Writer) { globalStderr = w }
