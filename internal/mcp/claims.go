package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Wire-format payloads for the claim MCP tools. Field names and JSON tags
// match the schemas declared in phase-3-brief.md / DESIGN.md §4.4.
//
// session_id is opaque to Leonard — Claude Code surfaces it via the Stop
// hook payload and the MCP layer plumbs it through faithfully. The MCP
// handler does not validate the session id; the real store rejects empty
// values, which surface as an MCP error result.

// RecordClaimInput is the argument shape for record_claim.
type RecordClaimInput struct {
	Claim     string `json:"claim" jsonschema:"the assertion being recorded (e.g. 'go vet clean', 'all tests pass')"`
	Evidence  string `json:"evidence" jsonschema:"supporting evidence — command output, exit codes, file paths; can be longer than the claim itself"`
	Verified  bool   `json:"verified" jsonschema:"true if the claim has already been confirmed; false to flag it for follow-up at session end"`
	SessionID string `json:"session_id,omitempty" jsonschema:"opaque Claude Code session identifier; the stop hook fills it in"`
}

// RecordClaimOutput returns the newly assigned claim id.
type RecordClaimOutput struct {
	ClaimID int64 `json:"claim_id"`
}

// GetUnverifiedClaimsInput is the argument shape for get_unverified_claims.
type GetUnverifiedClaimsInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"optional session filter; empty returns unverified claims across all sessions"`
}

// ClaimEntry is the wire-format claim returned by get_unverified_claims.
// Evidence is intentionally omitted from this read shape because it can be
// large — a future per-id read tool can surface it on demand.
type ClaimEntry struct {
	ID         int64  `json:"id"`
	SessionID  string `json:"session_id"`
	Claim      string `json:"claim"`
	RecordedAt int64  `json:"recorded_at"`
}

// GetUnverifiedClaimsOutput wraps the claims array. MCP requires structured
// tool output to be an object, so arrays get a single-field envelope (see
// FindSymbolOutput / GetDecisionsOutput).
type GetUnverifiedClaimsOutput struct {
	Claims []ClaimEntry `json:"claims"`
}

func recordClaim(ctx context.Context, cs ClaimStore, in RecordClaimInput) (RecordClaimOutput, error) {
	if in.Claim == "" {
		return RecordClaimOutput{}, errors.New("record_claim: claim is required")
	}
	id, err := cs.RecordClaim(ctx, in.SessionID, in.Claim, in.Evidence, in.Verified)
	if err != nil {
		return RecordClaimOutput{}, fmt.Errorf("record_claim: %w", err)
	}
	return RecordClaimOutput{ClaimID: id}, nil
}

func getUnverifiedClaims(ctx context.Context, cs ClaimStore, in GetUnverifiedClaimsInput) (GetUnverifiedClaimsOutput, error) {
	recs, err := cs.GetUnverifiedClaims(ctx, in.SessionID)
	if err != nil {
		return GetUnverifiedClaimsOutput{}, fmt.Errorf("get_unverified_claims: %w", err)
	}
	out := make([]ClaimEntry, 0, len(recs))
	for _, r := range recs {
		out = append(out, ClaimEntry{
			ID:         r.ID,
			SessionID:  r.SessionID,
			Claim:      r.Claim,
			RecordedAt: r.RecordedAt,
		})
	}
	return GetUnverifiedClaimsOutput{Claims: out}, nil
}

// registerClaimTools wires the two claim tools onto srv. Called from
// register() only when the underlying store satisfies ClaimStore.
func registerClaimTools(srv *mcp.Server, cs ClaimStore) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "record_claim",
		Description: "Record an assertion made during this Claude Code session along with supporting evidence. Set verified=true if it's already been confirmed; verified=false flags it for follow-up at session end via the Stop hook. Returns the new claim id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RecordClaimInput) (*mcp.CallToolResult, RecordClaimOutput, error) {
		out, err := recordClaim(ctx, cs, in)
		if err != nil {
			return nil, RecordClaimOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_unverified_claims",
		Description: "Return claims still flagged as unverified, newest first. Optional session_id filter scopes the query to a single session; empty returns claims across all sessions. Evidence is omitted from the response to keep the payload small.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetUnverifiedClaimsInput) (*mcp.CallToolResult, GetUnverifiedClaimsOutput, error) {
		out, err := getUnverifiedClaims(ctx, cs, in)
		if err != nil {
			return nil, GetUnverifiedClaimsOutput{}, err
		}
		return nil, out, nil
	})
}
