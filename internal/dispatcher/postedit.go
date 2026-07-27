package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/hooks"
)

// HandlePostEdit reads a Claude Code PostToolUse envelope from r,
// dispatches to every loaded adapter, aggregates results, and writes
// a HookResponse to w.
//
// PostEdit is advisory (cannot deny); per-adapter errors are logged
// to stderr but don't fail the response. AdditionalContext and
// SystemMessage fields are concatenated across adapters (matches
// adapters.AggregatePostEdit semantics).
func (l *Loaded) HandlePostEdit(ctx context.Context, r io.Reader, w io.Writer) error {
	var env hooks.PostToolUsePayload
	if err := decodeEnvelope(r, &env); err != nil {
		return fmt.Errorf("dispatcher post-edit: %w", err)
	}

	payload := adapters.PostEditPayload{
		SessionID: env.SessionID,
		Tool:      env.ToolName,
		FilePath:  env.ToolInput.FilePath,
		CWD:       env.CWD,
	}

	results := make([]adapters.PostEditResult, 0, len(l.Adapters))
	for _, a := range l.Adapters {
		res, err := a.PostEdit(ctx, payload)
		if err != nil {
			fmt.Fprintf(globalStderr, "leonard dispatcher: %s post-edit error: %v\n", a.Name(), err)
			continue
		}
		for i := range res.Claims {
			if res.Claims[i].AdapterName == "" {
				res.Claims[i].AdapterName = a.Name()
			}
		}
		results = append(results, res)
	}

	final := adapters.AggregatePostEdit(results)
	resp := hooks.HookResponse{Continue: true}
	if final.SystemMessage != "" {
		resp.SystemMessage = final.SystemMessage
	}
	if final.AdditionalContext != "" {
		resp.HookSpecificOutput = &hooks.PostToolUseSpecificOutput{
			HookEventName:     "PostToolUse",
			AdditionalContext: final.AdditionalContext,
		}
	}
	// Suppress per-edit status lines on clean runs unless --verbose was
	// passed. Failures (non-empty additionalContext) always surface so
	// the operator sees vet failures and other actionable findings.
	if !l.Verbose && final.AdditionalContext == "" {
		resp.SuppressOutput = true
	}
	return json.NewEncoder(w).Encode(resp)
}
