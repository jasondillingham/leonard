package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/hooks"
)

// HandleSessionStart reads a SessionStart envelope, dispatches to
// every loaded adapter, aggregates their AdditionalContext blocks
// with a "---" separator (matching adapters.AggregateSessionStart),
// and writes a SessionStartResponse.
//
// "compact" and "clear" sources short-circuit to a {"continue": true}
// response without invoking adapters — matches the existing
// internal/hooks.shouldInjectForSource gate.
func (l *Loaded) HandleSessionStart(ctx context.Context, r io.Reader, w io.Writer) error {
	var env hooks.SessionStartPayload
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		return fmt.Errorf("dispatcher session-start: decode envelope: %w", err)
	}

	if env.Source == "compact" || env.Source == "clear" {
		return json.NewEncoder(w).Encode(hooks.SessionStartResponse{Continue: true})
	}

	payload := adapters.SessionStartPayload{
		SessionID: env.SessionID,
		Source:    env.Source,
		CWD:       env.CWD,
	}

	results := make([]adapters.SessionStartResult, 0, len(l.Adapters))
	for _, a := range l.Adapters {
		res, err := a.SessionStart(ctx, payload)
		if err != nil {
			fmt.Fprintf(globalStderr, "leonard dispatcher: %s session-start error: %v\n", a.Name(), err)
			continue
		}
		results = append(results, res)
	}

	final := adapters.AggregateSessionStart(results)
	resp := hooks.SessionStartResponse{Continue: true}
	if final.AdditionalContext != "" {
		resp.HookSpecificOutput = &hooks.SessionStartSpecificOutput{
			HookEventName:     "SessionStart",
			AdditionalContext: final.AdditionalContext,
		}
	}
	return json.NewEncoder(w).Encode(resp)
}

// HandleStop reads a Stop envelope, dispatches to every loaded
// adapter, concatenates their SystemMessage lines (matching
// adapters.AggregateStop), and writes a StopResponse.
func (l *Loaded) HandleStop(ctx context.Context, r io.Reader, w io.Writer) error {
	var env hooks.StopPayload
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		return fmt.Errorf("dispatcher stop: decode envelope: %w", err)
	}

	payload := adapters.StopPayload{
		SessionID: env.SessionID,
		CWD:       env.CWD,
	}

	results := make([]adapters.StopResult, 0, len(l.Adapters))
	for _, a := range l.Adapters {
		res, err := a.Stop(ctx, payload)
		if err != nil {
			fmt.Fprintf(globalStderr, "leonard dispatcher: %s stop error: %v\n", a.Name(), err)
			continue
		}
		results = append(results, res)
	}

	final := adapters.AggregateStop(results)
	resp := hooks.StopResponse{Continue: true}
	if final.SystemMessage != "" {
		resp.SystemMessage = final.SystemMessage
	}
	return json.NewEncoder(w).Encode(resp)
}
