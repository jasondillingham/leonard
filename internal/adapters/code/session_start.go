package code

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/hooks"
)

// SessionStart satisfies adapters.Adapter. Synthesizes a Claude Code
// SessionStart envelope, feeds it to hooks.HandleSessionStart so the
// existing decisions-injection logic runs unchanged, and returns the
// injected markdown as a SessionStartResult.
//
// In degraded mode (no .leonard/leonard.db) SessionStart returns an
// empty result — the existing handler also short-circuits to no
// injection when the DecisionReader is nil, but we skip the envelope
// dance entirely for clarity.
func (a *CodeAdapter) SessionStart(ctx context.Context, p adapters.SessionStartPayload) (adapters.SessionStartResult, error) {
	snap := a.snapshot()

	if !snap.hasLeonardDB {
		return adapters.SessionStartResult{}, nil
	}

	envelope := hooks.SessionStartPayload{
		SessionID:     p.SessionID,
		HookEventName: "SessionStart",
		Source:        p.Source,
		CWD:           p.CWD,
	}
	var stdin bytes.Buffer
	if err := json.NewEncoder(&stdin).Encode(envelope); err != nil {
		return adapters.SessionStartResult{}, fmt.Errorf("code adapter: encode session-start envelope: %w", err)
	}

	opts := hooks.SessionStartOptions{
		Decisions: snap.storeHandle,
		Limit:     snap.cfg.Hooks.InjectDecisionsAtSessionStart,
	}

	var stdout bytes.Buffer
	if err := hooks.HandleSessionStart(ctx, opts, &stdin, &stdout); err != nil {
		return adapters.SessionStartResult{}, err
	}

	var resp hooks.SessionStartResponse
	if err := json.NewDecoder(&stdout).Decode(&resp); err != nil {
		return adapters.SessionStartResult{}, fmt.Errorf("code adapter: decode session-start response: %w", err)
	}

	out := adapters.SessionStartResult{}
	if resp.HookSpecificOutput != nil {
		out.AdditionalContext = resp.HookSpecificOutput.AdditionalContext
	}
	return out, nil
}
