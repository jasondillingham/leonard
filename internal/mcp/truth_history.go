package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jasondillingham/leonard/internal/telemetry"
)

// GetTruthHistoryInput is the wire shape for get_truth_history.
type GetTruthHistoryInput struct {
	// FilePath is the project-relative path whose decision-log
	// entries should be returned. Required.
	FilePath string `json:"file_path"`

	// Limit caps the result slice. Defaults to 200 when <= 0.
	Limit int `json:"limit,omitempty"`
}

// GetTruthHistoryOutput wraps the decisions array.
type GetTruthHistoryOutput struct {
	FilePath  string                   `json:"file_path"`
	Decisions []TruthHistoryEntryRecord `json:"decisions"`
}

// TruthHistoryEntryRecord is the per-entry wire shape. Fields chosen
// to be useful for "why is this rule the way it is?" — surfacing
// the rationale (Reasoning / TruthChange.MotivatedBy) and provenance
// (Topic, Files, DiffRef) prominently.
type TruthHistoryEntryRecord struct {
	ID          int64              `json:"id"`
	Topic       string             `json:"topic"`
	Choice      string             `json:"choice,omitempty"`
	Reasoning   string             `json:"reasoning,omitempty"`
	RecordedAt  int64              `json:"recorded_at"`
	TruthChange *TruthChangeRecord `json:"truth_change,omitempty"`
}

func getTruthHistory(ctx context.Context, ts TruthHistoryStore, in GetTruthHistoryInput) (GetTruthHistoryOutput, error) {
	ctx, end := telemetry.Span(ctx, "leonard.mcp.get_truth_history")
	defer end()

	if in.FilePath == "" {
		return GetTruthHistoryOutput{}, errors.New("get_truth_history: file_path is required")
	}

	recs, err := ts.GetTruthHistory(ctx, in.FilePath, in.Limit)
	if err != nil {
		return GetTruthHistoryOutput{}, fmt.Errorf("get_truth_history: %w", err)
	}
	out := GetTruthHistoryOutput{
		FilePath:  in.FilePath,
		Decisions: make([]TruthHistoryEntryRecord, 0, len(recs)),
	}
	for _, r := range recs {
		out.Decisions = append(out.Decisions, TruthHistoryEntryRecord{
			ID:          r.ID,
			Topic:       r.Topic,
			Choice:      r.Choice,
			Reasoning:   r.Reasoning,
			RecordedAt:  r.RecordedAt,
			TruthChange: r.TruthChange,
		})
	}
	return out, nil
}

// registerTruthHistoryTool wires get_truth_history onto srv. Called
// from register() when the store satisfies TruthHistoryStore.
func registerTruthHistoryTool(srv *mcp.Server, ts TruthHistoryStore) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_truth_history",
		Description: "Return decision-log entries whose truth_change.files includes the supplied file_path. Returns entries oldest-first so the slice reads as the rule's evolution narrative. Used to answer 'why is this rule the way it is?'",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetTruthHistoryInput) (*mcp.CallToolResult, GetTruthHistoryOutput, error) {
		out, err := getTruthHistory(ctx, ts, in)
		if err != nil {
			return nil, GetTruthHistoryOutput{}, err
		}
		return nil, out, nil
	})
}
