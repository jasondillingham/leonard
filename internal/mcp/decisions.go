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
	Topic          string   `json:"topic" jsonschema:"the decision topic (e.g. 'auth library', 'caching strategy')"`
	Choice         string   `json:"choice" jsonschema:"what was chosen (one short line)"`
	Reasoning      string   `json:"reasoning" jsonschema:"why it was chosen (a sentence or two)"`
	RelatedFiles   []string `json:"related_files,omitempty" jsonschema:"file paths this decision reasons about; get_stale_decisions flags the decision when any listed file disappears from the index"`
	RelatedSymbols []string `json:"related_symbols,omitempty" jsonschema:"symbol names or qualified names this decision reasons about; get_stale_decisions flags the decision when any listed symbol disappears from the index"`
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

// DecisionEntry is the wire-format decision returned by get_decisions and
// embedded in StaleDecisionEntry. Related arrays are omitted from the JSON
// when empty so callers that didn't supply refs still get the legacy shape.
type DecisionEntry struct {
	ID             int64    `json:"id"`
	Topic          string   `json:"topic"`
	Choice         string   `json:"choice"`
	Reasoning      string   `json:"reasoning"`
	RelatedFiles   []string `json:"related_files,omitempty"`
	RelatedSymbols []string `json:"related_symbols,omitempty"`
	RecordedAt     int64    `json:"recorded_at"`
}

// GetStaleDecisionsInput is the argument shape for get_stale_decisions.
type GetStaleDecisionsInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"max results (default 50, capped at 200)"`
}

// StaleDecisionEntry is the wire shape returned by get_stale_decisions: a
// decision plus the specific files / symbols referenced by it that no
// longer resolve against the live index.
type StaleDecisionEntry struct {
	Decision       DecisionEntry `json:"decision"`
	MissingFiles   []string      `json:"missing_files,omitempty"`
	MissingSymbols []string      `json:"missing_symbols,omitempty"`
}

// GetStaleDecisionsOutput wraps the entries array.
type GetStaleDecisionsOutput struct {
	Decisions []StaleDecisionEntry `json:"decisions"`
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

// Resource caps for decision text. Security-1 F4/F8: unbounded
// inputs let a single record_decision call grow the DB row to
// megabytes and balloon get_decisions response payloads. Sized
// generously above realistic real-world entries while preventing
// the obvious abuse cases.
const (
	maxDecisionTopicBytes     = 256
	maxDecisionChoiceBytes    = 4 << 10  // 4 KiB
	maxDecisionReasoningBytes = 32 << 10 // 32 KiB
)

func recordDecision(ctx context.Context, ds DecisionStore, in RecordDecisionInput) (RecordDecisionOutput, error) {
	if in.Topic == "" {
		return RecordDecisionOutput{}, errors.New("record_decision: topic is required")
	}
	if len(in.Topic) > maxDecisionTopicBytes {
		return RecordDecisionOutput{}, fmt.Errorf("record_decision: topic exceeds %d bytes", maxDecisionTopicBytes)
	}
	if len(in.Choice) > maxDecisionChoiceBytes {
		return RecordDecisionOutput{}, fmt.Errorf("record_decision: choice exceeds %d bytes", maxDecisionChoiceBytes)
	}
	if len(in.Reasoning) > maxDecisionReasoningBytes {
		return RecordDecisionOutput{}, fmt.Errorf("record_decision: reasoning exceeds %d bytes", maxDecisionReasoningBytes)
	}
	id, err := ds.RecordDecision(ctx, in.Topic, in.Choice, in.Reasoning, in.RelatedFiles, in.RelatedSymbols)
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
		out = append(out, decisionRecordToEntry(r))
	}
	return GetDecisionsOutput{Decisions: out}, nil
}

func decisionRecordToEntry(r DecisionRecord) DecisionEntry {
	return DecisionEntry{
		ID:             r.ID,
		Topic:          r.Topic,
		Choice:         r.Choice,
		Reasoning:      r.Reasoning,
		RelatedFiles:   r.RelatedFiles,
		RelatedSymbols: r.RelatedSymbols,
		RecordedAt:     r.RecordedAt,
	}
}

const (
	getStaleDecisionsDefaultLimit = 50
	getStaleDecisionsMaxLimit     = 200
)

func getStaleDecisions(ctx context.Context, ds DecisionStore, in GetStaleDecisionsInput) (GetStaleDecisionsOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = getStaleDecisionsDefaultLimit
	}
	if limit > getStaleDecisionsMaxLimit {
		limit = getStaleDecisionsMaxLimit
	}
	recs, err := ds.GetStaleDecisions(ctx, limit)
	if err != nil {
		return GetStaleDecisionsOutput{}, fmt.Errorf("get_stale_decisions: %w", err)
	}
	out := make([]StaleDecisionEntry, 0, len(recs))
	for _, r := range recs {
		out = append(out, StaleDecisionEntry{
			Decision:       decisionRecordToEntry(r.Decision),
			MissingFiles:   r.MissingFiles,
			MissingSymbols: r.MissingSymbols,
		})
	}
	return GetStaleDecisionsOutput{Decisions: out}, nil
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
		Description: "Record a project decision so future Claude Code sessions can see it. Takes a topic, the choice made, the reasoning, and optional related_files / related_symbols arrays — referenced files/symbols are cross-checked by get_stale_decisions so a decision whose target moves doesn't go silently stale. Returns the new decision id.",
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

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_stale_decisions",
		Description: "Return decisions whose related_files or related_symbols no longer resolve against the live index. Each entry includes the decision plus the specific missing refs so a session-start review can spot narratives the codebase has outgrown. Limit defaults to 50 and caps at 200.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetStaleDecisionsInput) (*mcp.CallToolResult, GetStaleDecisionsOutput, error) {
		out, err := getStaleDecisions(ctx, ds, in)
		if err != nil {
			return nil, GetStaleDecisionsOutput{}, err
		}
		return nil, out, nil
	})
}
