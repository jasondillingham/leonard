package code

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/config"
	"github.com/jasondillingham/leonard/internal/hooks"
	"github.com/jasondillingham/leonard/internal/store"
)

// defaultVerifyTimeout matches cmd/leonard-hook/post_edit.go. Bumped
// past RunGoVet's 30s default because `cargo check`, `pnpm tsc`, and
// similar verifiers can take longer on cold builds.
const defaultVerifyTimeout = 60 * time.Second

// PostEdit satisfies adapters.Adapter. Like PreEdit, this is a thin
// wrapper that synthesizes a Claude Code PostToolUse envelope, feeds
// it to the existing hooks.HandlePostEdit (so the index refresh +
// verifier + claim-ledger logic runs unchanged), and translates the
// hook's response into a PostEditResult.
//
// In degraded mode (no .leonard/leonard.db) PostEdit emits a no-op
// result with a one-line operator hint, matching the existing
// cmd/leonard-hook behavior on a fresh checkout.
func (a *CodeAdapter) PostEdit(ctx context.Context, p adapters.PostEditPayload) (adapters.PostEditResult, error) {
	snap := a.snapshot()

	if !snap.hasLeonardDB {
		fmt.Fprintln(snap.stderr, "leonard: post-edit skipped — run `leonard init` first")
		return adapters.PostEditResult{}, nil
	}

	envelope := hooks.PostToolUsePayload{
		SessionID:     p.SessionID,
		HookEventName: "PostToolUse",
		ToolName:      p.Tool,
		ToolInput:     hooks.ToolInput{FilePath: p.FilePath},
		CWD:           p.CWD,
	}
	var stdin bytes.Buffer
	if err := json.NewEncoder(&stdin).Encode(envelope); err != nil {
		return adapters.PostEditResult{}, fmt.Errorf("code adapter: encode post-edit envelope: %w", err)
	}

	opts := hooks.PostEditOptions{
		Indexer:     snap.indexer(),
		Claims:      snap.claimRecorder(),
		ProjectRoot: snap.projectRoot,
	}

	// Apply [post_edit.verify] overrides exactly like
	// cmd/leonard-hook/post_edit.go does. The trust gate runs here too.
	if verify := snap.cfg.PostEdit.Verify; strings.TrimSpace(verify.Command) != "" {
		trusted, trustErr := config.VerifyCommandTrusted(snap.projectRoot, verify.Command)
		if trustErr != nil {
			fmt.Fprintf(snap.stderr, "leonard: trust check failed, falling back to defaults: %v\n", trustErr)
		} else if !trusted {
			fmt.Fprintln(snap.stderr, "leonard: [post_edit.verify].command is configured but UNTRUSTED — run `leonard config trust` to authorize. Falling back to the default `go vet` verifier for this run.")
		} else {
			opts.Vet = hooks.MakeShellRunner(verify.Command, verify.WorkingDir)
			opts.VetVerb = hooks.VerifyVerb(verify.Command)
			opts.AlwaysVet = true
			opts.VetTimeout = parseVerifyTimeout(verify.Timeout, snap.stderr)
		}
	}

	var stdout bytes.Buffer
	if err := hooks.HandlePostEdit(ctx, opts, &stdin, &stdout); err != nil {
		return adapters.PostEditResult{}, err
	}

	var resp hooks.HookResponse
	if err := json.NewDecoder(&stdout).Decode(&resp); err != nil {
		return adapters.PostEditResult{}, fmt.Errorf("code adapter: decode post-edit response: %w", err)
	}

	out := adapters.PostEditResult{
		SystemMessage: resp.SystemMessage,
	}
	if resp.HookSpecificOutput != nil {
		out.AdditionalContext = resp.HookSpecificOutput.AdditionalContext
	}
	return out, nil
}

// parseVerifyTimeout matches cmd/leonard-hook/post_edit.go. Negative
// or zero durations fall back to the default — bughunt-5 verifier F4
// closed a "negative timeout immediately fires context deadline
// exceeded" footgun. stderr is the destination for the operator hint
// emitted on syntax / sign errors; nil is acceptable for tests that
// don't care about the hint text.
func parseVerifyTimeout(raw string, stderr io.Writer) time.Duration {
	if raw == "" {
		return defaultVerifyTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "leonard: invalid [post_edit.verify].timeout %q, using %s: %v\n", raw, defaultVerifyTimeout, err)
		}
		return defaultVerifyTimeout
	}
	if d <= 0 {
		if stderr != nil {
			fmt.Fprintf(stderr, "leonard: [post_edit.verify].timeout %q must be positive, using %s\n", raw, defaultVerifyTimeout)
		}
		return defaultVerifyTimeout
	}
	return d
}

// claimRecorder returns the hooks.ClaimRecorder adapter. Replicated
// from cmd/leonard-hook/backend_real.go so the package is self-
// contained.
func (s snapshot) claimRecorder() hooks.ClaimRecorder {
	if s.storeHandle == nil {
		return nil
	}
	return storeClaimsAdapter{s: s.storeHandle}
}

// storeClaimsAdapter mirrors the type in cmd/leonard-hook/backend_real.go.
// The store owns the clock; we leave RecordedAt zero so the store
// stamps it.
type storeClaimsAdapter struct{ s *store.Store }

func (a storeClaimsAdapter) RecordClaim(rec hooks.ClaimRecord) (int64, error) {
	return a.s.RecordClaim(store.Claim{
		SessionID:       rec.SessionID,
		Claim:           rec.Claim,
		Evidence:        rec.Evidence,
		Verified:        rec.Verified,
		FilePath:        rec.FilePath,
		Tool:            rec.Tool,
		IndexOK:         rec.IndexOK,
		VetOK:           rec.VetOK,
		VetErrorSummary: rec.VetErrorSummary,
	})
}

func (a storeClaimsAdapter) SupersedeClaimsForFile(filePath string, supersedingClaimID int64) (int, error) {
	return a.s.SupersedeClaimsForFile(filePath, supersedingClaimID)
}

func (a storeClaimsAdapter) SupersedeOutstandingFailures(supersedingClaimID int64) (int, error) {
	return a.s.SupersedeOutstandingFailures(supersedingClaimID)
}
