package code

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/hooks"
)

// Stop satisfies adapters.Adapter. Synthesizes a Claude Code Stop
// envelope, feeds it to hooks.HandleStop so the existing unverified-
// claim surfacing runs unchanged, and returns the resulting markdown
// as a StopResult.
//
// In degraded mode (no .leonard/leonard.db) Stop returns an empty
// result.
func (a *CodeAdapter) Stop(ctx context.Context, p adapters.StopPayload) (adapters.StopResult, error) {
	snap := a.snapshot()

	if !snap.hasLeonardDB {
		return adapters.StopResult{}, nil
	}

	envelope := hooks.StopPayload{
		SessionID:     p.SessionID,
		HookEventName: "Stop",
		CWD:           p.CWD,
	}
	var stdin bytes.Buffer
	if err := json.NewEncoder(&stdin).Encode(envelope); err != nil {
		return adapters.StopResult{}, fmt.Errorf("code adapter: encode stop envelope: %w", err)
	}

	opts := hooks.StopOptions{
		Claims: snap.storeHandle,
		Limit:  snap.cfg.Hooks.SurfaceUnverifiedClaimsAtStop,
	}

	var stdout bytes.Buffer
	if err := hooks.HandleStop(ctx, opts, &stdin, &stdout); err != nil {
		return adapters.StopResult{}, err
	}

	var resp hooks.StopResponse
	if err := json.NewDecoder(&stdout).Decode(&resp); err != nil {
		return adapters.StopResult{}, fmt.Errorf("code adapter: decode stop response: %w", err)
	}

	return adapters.StopResult{SystemMessage: resp.SystemMessage}, nil
}
