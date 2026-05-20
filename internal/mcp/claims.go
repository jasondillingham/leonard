package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jasondillingham/leonard/internal/store"
)

// Wire-format payloads for the claim MCP tools. Field names and JSON tags
// match the schemas declared in phase-3-brief.md / DESIGN.md §4.4.
//
// session_id is opaque to Leonard — it travels as a tag, not a foreign key.
// An empty value means "unscoped" (not associated with any session yet)
// and is stored verbatim; get_unverified_claims with no session filter
// surfaces unscoped rows alongside session-tagged ones.

// RecordClaimInput is the argument shape for record_claim.
type RecordClaimInput struct {
	Claim     string `json:"claim" jsonschema:"the assertion being recorded (e.g. 'go vet clean', 'all tests pass')"`
	Evidence  string `json:"evidence" jsonschema:"supporting evidence — command output, exit codes, file paths; can be longer than the claim itself"`
	Verified  bool   `json:"verified" jsonschema:"true if the claim has already been confirmed; false to flag it for follow-up at session end"`
	SessionID string `json:"session_id,omitempty" jsonschema:"opaque Claude Code session identifier; empty means unscoped (no session attribution yet)"`
}

// RecordClaimOutput returns the newly assigned claim id.
type RecordClaimOutput struct {
	ClaimID int64 `json:"claim_id"`
}

// GetUnverifiedClaimsInput is the argument shape for get_unverified_claims.
type GetUnverifiedClaimsInput struct {
	SessionID         string `json:"session_id,omitempty" jsonschema:"optional session filter; empty returns unverified claims across all sessions"`
	IncludeSuperseded bool   `json:"include_superseded,omitempty" jsonschema:"when true, also return prior failure claims that a later vet=ok run on the same file already resolved (default: false — superseded rows hidden so stop-time output stays focused)"`
	Limit             int    `json:"limit,omitempty" jsonschema:"max rows to return (default 50, capped at 200). 0 = use default."`
}

const (
	getUnverifiedDefaultLimit = 50
	getUnverifiedMaxLimit     = 200
)

// ClaimEntry is the wire-format claim returned by get_unverified_claims.
// Evidence is intentionally omitted from this read shape because it can be
// large — a future per-id read tool can surface it on demand. FilePath,
// Tool, IndexOK, VetOK, and VetErrorSummary are populated by the post-edit
// hook (schema v3+); claims recorded via the record_claim MCP tool from a
// model leave them empty.
type ClaimEntry struct {
	ID              int64  `json:"id"`
	SessionID       string `json:"session_id"`
	Claim           string `json:"claim"`
	FilePath        string `json:"file_path,omitempty"`
	Tool            string `json:"tool,omitempty"`
	IndexOK         *bool  `json:"index_ok,omitempty"`
	VetOK           *bool  `json:"vet_ok,omitempty"`
	VetErrorSummary string `json:"vet_error_summary,omitempty"`
	RecordedAt      int64  `json:"recorded_at"`
}

// GetUnverifiedClaimsOutput wraps the claims array. MCP requires structured
// tool output to be an object, so arrays get a single-field envelope (see
// FindSymbolOutput / GetDecisionsOutput).
type GetUnverifiedClaimsOutput struct {
	Claims []ClaimEntry `json:"claims"`
}

// Cap values live in internal/store/limits.go so the CLI and MCP
// layers share one source of truth (bughunt-4 caps F3).
const (
	maxClaimSummaryBytes  = store.MaxClaimSummaryBytes
	maxClaimEvidenceBytes = store.MaxClaimEvidenceBytes
)

func recordClaim(ctx context.Context, cs ClaimStore, in RecordClaimInput) (RecordClaimOutput, error) {
	if in.Claim == "" {
		return RecordClaimOutput{}, errors.New("record_claim: claim is required")
	}
	if len(in.Claim) > maxClaimSummaryBytes {
		return RecordClaimOutput{}, fmt.Errorf("record_claim: claim exceeds %d bytes", maxClaimSummaryBytes)
	}
	if len(in.Evidence) > maxClaimEvidenceBytes {
		return RecordClaimOutput{}, fmt.Errorf("record_claim: evidence exceeds %d bytes", maxClaimEvidenceBytes)
	}
	id, err := cs.RecordClaim(ctx, in.SessionID, in.Claim, in.Evidence, in.Verified)
	if err != nil {
		return RecordClaimOutput{}, fmt.Errorf("record_claim: %w", err)
	}
	return RecordClaimOutput{ClaimID: id}, nil
}

// maxClaimsResponseBytes mirrors maxDecisionsResponseBytes — caps
// aggregate response size at 1 MiB so a runaway claims table can't
// produce a multi-megabyte tool result. Bughunt-4 mcp F2.
const maxClaimsResponseBytes = 1 << 20

func getUnverifiedClaims(ctx context.Context, cs ClaimStore, in GetUnverifiedClaimsInput) (GetUnverifiedClaimsOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = getUnverifiedDefaultLimit
	}
	if limit > getUnverifiedMaxLimit {
		limit = getUnverifiedMaxLimit
	}
	recs, err := cs.GetUnverifiedClaims(ctx, in.SessionID, in.IncludeSuperseded)
	if err != nil {
		return GetUnverifiedClaimsOutput{}, fmt.Errorf("get_unverified_claims: %w", err)
	}
	if len(recs) > limit {
		recs = recs[:limit]
	}
	out := make([]ClaimEntry, 0, len(recs))
	bytesEmitted := 0
	for _, r := range recs {
		e := ClaimEntry{
			ID:              r.ID,
			SessionID:       r.SessionID,
			Claim:           r.Claim,
			FilePath:        r.FilePath,
			Tool:            r.Tool,
			IndexOK:         r.IndexOK,
			VetOK:           r.VetOK,
			VetErrorSummary: r.VetErrorSummary,
			RecordedAt:      r.RecordedAt,
		}
		rowBytes := len(e.SessionID) + len(e.Claim) + len(e.FilePath) + len(e.Tool) + len(e.VetErrorSummary)
		if len(out) > 0 && bytesEmitted+rowBytes > maxClaimsResponseBytes {
			break
		}
		out = append(out, e)
		bytesEmitted += rowBytes
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
		Description: "Return claims still flagged as unverified, newest first. Optional session_id filter scopes the query to a single session; empty returns claims across all sessions. By default, prior failure claims that a later vet=ok run on the same file already resolved are hidden — pass include_superseded=true to see the full history. Evidence is omitted from the response to keep the payload small.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetUnverifiedClaimsInput) (*mcp.CallToolResult, GetUnverifiedClaimsOutput, error) {
		out, err := getUnverifiedClaims(ctx, cs, in)
		if err != nil {
			return nil, GetUnverifiedClaimsOutput{}, err
		}
		return nil, out, nil
	})
}
