package code

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/hooks"
	"github.com/jasondillingham/leonard/internal/store"
)

// PreEdit satisfies adapters.Adapter. It synthesizes a Claude Code
// PreToolUse JSON envelope from the flattened PreEditPayload, feeds it
// to the existing hooks.HandlePreEdit (so the fabrication-guard logic
// is reused verbatim), and translates the hook's JSON response back
// into a PreEditResult. A "deny" verdict becomes adapters.Deny with
// the rule citation; everything else passes.
func (a *CodeAdapter) PreEdit(ctx context.Context, p adapters.PreEditPayload) (adapters.PreEditResult, error) {
	snap := a.snapshot()

	envelope := hooks.PreToolUsePayload{
		SessionID:     p.SessionID,
		HookEventName: "PreToolUse",
		ToolName:      p.Tool,
		ToolInput: hooks.PreEditToolInput{
			FilePath:  p.FilePath,
			NewString: p.Content,
			Content:   p.Content,
			NewSource: p.Content,
			Command:   p.Command,
		},
		CWD: p.CWD,
	}

	var stdin bytes.Buffer
	if err := json.NewEncoder(&stdin).Encode(envelope); err != nil {
		return adapters.PreEditResult{}, fmt.Errorf("code adapter: encode pre-edit envelope: %w", err)
	}

	opts := hooks.PreEditOptions{
		Store:      snap.symbolStore(),
		ModulePath: snap.modulePath,
		ModuleRoot: snap.projectRoot,
	}

	var stdout bytes.Buffer
	if err := hooks.HandlePreEdit(ctx, opts, &stdin, &stdout); err != nil {
		return adapters.PreEditResult{}, err
	}

	var resp hooks.PreEditResponse
	if err := json.NewDecoder(&stdout).Decode(&resp); err != nil {
		return adapters.PreEditResult{}, fmt.Errorf("code adapter: decode pre-edit response: %w", err)
	}

	if resp.HookSpecificOutput != nil &&
		resp.HookSpecificOutput.PermissionDecision == "deny" {
		return adapters.PreEditResult{
			Decision:    adapters.Deny,
			Reason:      resp.HookSpecificOutput.PermissionDecisionReason,
			AdapterName: Name,
		}, nil
	}
	return adapters.PreEditResult{Decision: adapters.Pass}, nil
}

// symbolStore returns the SymbolStore the pre-edit handler needs. In
// degraded mode (no .leonard/leonard.db) it returns a permissive store
// that reports every symbol as present — the same fallback the existing
// cmd/leonard-hook uses on a fresh checkout.
func (s snapshot) symbolStore() hooks.SymbolStore {
	if s.storeHandle == nil {
		return permissiveStore{}
	}
	return preEditSymbolAdapter{s: s.storeHandle}
}

// preEditSymbolAdapter adapts hooks.SymbolStore onto *store.Store.
// Replicated from cmd/leonard-hook/pre_edit.go so the adapter package
// is self-contained. When the cmd/leonard-hook rewiring lands the
// version there can be deleted.
type preEditSymbolAdapter struct{ s *store.Store }

func (a preEditSymbolAdapter) HasSymbol(name string) (bool, error) {
	syms, err := a.s.FindSymbolsByName(name)
	if err != nil {
		return false, err
	}
	return len(syms) > 0, nil
}

// permissiveStore mirrors cmd/leonard-hook/pre_edit.go's no-DB
// fallback. Reporting every symbol as present means the pre-edit hook
// never blocks before the index has anything to compare against.
type permissiveStore struct{}

func (permissiveStore) HasSymbol(string) (bool, error) { return true, nil }
