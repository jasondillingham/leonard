package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Wire-format payloads for the decision MCP tools. Field names and JSON
// tags match the schemas declared in phase-2-brief.md / DESIGN.md §4.3.

// RecordDecisionInput is the argument shape for record_decision.
type RecordDecisionInput struct {
	Topic     string `json:"topic" jsonschema:"the decision topic (e.g. 'auth library', 'caching strategy')"`
	Choice    string `json:"choice" jsonschema:"what was chosen (one short line)"`
	Reasoning string `json:"reasoning" jsonschema:"why it was chosen (a sentence or two)"`
}

// RecordDecisionOutput returns the newly assigned decision id.
type RecordDecisionOutput struct {
	DecisionID int64 `json:"decision_id"`
}

// GetDecisionsInput is the argument shape for get_decisions. The topic
// filter is exact-match in v0 — substring search is a phase-3 candidate
// (track in DESIGN.md §6 if it earns its keep).
type GetDecisionsInput struct {
	Topic string `json:"topic,omitempty" jsonschema:"optional exact-match topic filter (substring search is a phase-3 candidate)"`
	Since int64  `json:"since,omitempty" jsonschema:"optional unix-seconds lower bound on recorded_at"`
	Limit int    `json:"limit,omitempty" jsonschema:"max results (default 20, capped at 200)"`
}

// DecisionEntry is the wire-format decision returned by get_decisions.
type DecisionEntry struct {
	ID         int64  `json:"id"`
	Topic      string `json:"topic"`
	Choice     string `json:"choice"`
	Reasoning  string `json:"reasoning"`
	RecordedAt int64  `json:"recorded_at"`
}

// GetDecisionsOutput wraps the decisions array. MCP requires structured
// tool output to be an object, so arrays get a single-field envelope (see
// FindSymbolOutput).
type GetDecisionsOutput struct {
	Decisions []DecisionEntry `json:"decisions"`
}

// SupersedeDecisionInput is the argument shape for supersede_decision.
type SupersedeDecisionInput struct {
	DecisionID   int64  `json:"decision_id" jsonschema:"id of the decision being replaced"`
	NewChoice    string `json:"new_choice" jsonschema:"the replacement choice"`
	NewReasoning string `json:"new_reasoning" jsonschema:"reason for the change"`
}

// SupersedeDecisionOutput returns the id of the new (replacement) row.
type SupersedeDecisionOutput struct {
	NewDecisionID int64 `json:"new_decision_id"`
}

const (
	getDecisionsDefaultLimit = 20
	getDecisionsMaxLimit     = 200
)

func recordDecision(ctx context.Context, ds DecisionStore, in RecordDecisionInput) (RecordDecisionOutput, error) {
	if in.Topic == "" {
		return RecordDecisionOutput{}, errors.New("record_decision: topic is required")
	}
	id, err := ds.RecordDecision(ctx, in.Topic, in.Choice, in.Reasoning)
	if err != nil {
		return RecordDecisionOutput{}, fmt.Errorf("record_decision: %w", err)
	}
	return RecordDecisionOutput{DecisionID: id}, nil
}

func getDecisions(ctx context.Context, ds DecisionStore, in GetDecisionsInput) (GetDecisionsOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = getDecisionsDefaultLimit
	}
	if limit > getDecisionsMaxLimit {
		limit = getDecisionsMaxLimit
	}
	recs, err := ds.GetDecisions(ctx, in.Topic, in.Since, limit)
	if err != nil {
		return GetDecisionsOutput{}, fmt.Errorf("get_decisions: %w", err)
	}
	out := make([]DecisionEntry, 0, len(recs))
	for _, r := range recs {
		out = append(out, DecisionEntry{
			ID:         r.ID,
			Topic:      r.Topic,
			Choice:     r.Choice,
			Reasoning:  r.Reasoning,
			RecordedAt: r.RecordedAt,
		})
	}
	return GetDecisionsOutput{Decisions: out}, nil
}

func supersedeDecision(ctx context.Context, ds DecisionStore, in SupersedeDecisionInput) (SupersedeDecisionOutput, error) {
	if in.DecisionID <= 0 {
		return SupersedeDecisionOutput{}, errors.New("supersede_decision: decision_id is required")
	}
	newID, err := ds.SupersedeDecision(ctx, in.DecisionID, in.NewChoice, in.NewReasoning)
	if err != nil {
		return SupersedeDecisionOutput{}, fmt.Errorf("supersede_decision: %w", err)
	}
	return SupersedeDecisionOutput{NewDecisionID: newID}, nil
}

// registerDecisionTools wires the three decision tools onto srv. Called
// from register() only when the underlying store satisfies DecisionStore.
func registerDecisionTools(srv *mcp.Server, ds DecisionStore) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "record_decision",
		Description: "Record a project decision so future Claude Code sessions can see it. Takes a topic, the choice made, and the reasoning. Returns the new decision id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RecordDecisionInput) (*mcp.CallToolResult, RecordDecisionOutput, error) {
		out, err := recordDecision(ctx, ds, in)
		if err != nil {
			return nil, RecordDecisionOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_decisions",
		Description: "Return recorded project decisions newest-first. Optional exact-match topic filter and since (unix seconds) lower bound. Limit defaults to 20 and caps at 200.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetDecisionsInput) (*mcp.CallToolResult, GetDecisionsOutput, error) {
		out, err := getDecisions(ctx, ds, in)
		if err != nil {
			return nil, GetDecisionsOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "supersede_decision",
		Description: "Replace an existing decision under the same topic with a new choice + reasoning. Links the old row to the new one and returns the new decision id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SupersedeDecisionInput) (*mcp.CallToolResult, SupersedeDecisionOutput, error) {
		out, err := supersedeDecision(ctx, ds, in)
		if err != nil {
			return nil, SupersedeDecisionOutput{}, err
		}
		return nil, out, nil
	})
}
