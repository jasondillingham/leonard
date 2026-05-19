package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Wire-format payloads for the recent_changes MCP tool. Field names and
// JSON tags match the schema declared in phase-3-brief.md.

// RecentChangesInput is the argument shape for recent_changes.
type RecentChangesInput struct {
	Since int64 `json:"since,omitempty" jsonschema:"optional unix-seconds lower bound on indexed_at"`
	Limit int   `json:"limit,omitempty" jsonschema:"max results (default 50, capped at 500)"`
}

// ChangeEntry is the wire-format file returned by recent_changes.
type ChangeEntry struct {
	Path      string `json:"path"`
	Language  string `json:"language"`
	SizeBytes int64  `json:"size_bytes"`
	IndexedAt int64  `json:"indexed_at"`
}

// RecentChangesOutput wraps the changes array. MCP requires structured
// tool output to be an object (see FindSymbolOutput note).
type RecentChangesOutput struct {
	Changes []ChangeEntry `json:"changes"`
}

const (
	recentChangesDefaultLimit = 50
	recentChangesMaxLimit     = 500
)

func recentChanges(ctx context.Context, cs ChangesStore, in RecentChangesInput) (RecentChangesOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = recentChangesDefaultLimit
	}
	if limit > recentChangesMaxLimit {
		limit = recentChangesMaxLimit
	}
	recs, err := cs.ListFilesIndexedSince(ctx, in.Since, limit)
	if err != nil {
		return RecentChangesOutput{}, fmt.Errorf("recent_changes: %w", err)
	}
	out := make([]ChangeEntry, 0, len(recs))
	for _, r := range recs {
		out = append(out, ChangeEntry{
			Path:      r.Path,
			Language:  r.Language,
			SizeBytes: r.SizeBytes,
			IndexedAt: r.IndexedAt,
		})
	}
	return RecentChangesOutput{Changes: out}, nil
}

// registerChangesTool wires the recent_changes tool onto srv. Called from
// register() only when the underlying store satisfies ChangesStore.
func registerChangesTool(srv *mcp.Server, cs ChangesStore) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "recent_changes",
		Description: "Return files indexed since an optional unix-seconds cutoff, newest first. Use to find what's moved in the project since the last look. Limit defaults to 50 and caps at 500.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RecentChangesInput) (*mcp.CallToolResult, RecentChangesOutput, error) {
		out, err := recentChanges(ctx, cs, in)
		if err != nil {
			return nil, RecentChangesOutput{}, err
		}
		return nil, out, nil
	})
}
