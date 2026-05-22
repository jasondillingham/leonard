package adapters_test

import (
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
)

func TestAggregatePreEdit_AllPass(t *testing.T) {
	in := []adapters.PreEditResult{
		{Decision: adapters.Pass},
		{Decision: adapters.Pass},
	}
	got := adapters.AggregatePreEdit(in)
	if got.Decision != adapters.Pass {
		t.Errorf("Decision: want Pass, got %v", got.Decision)
	}
}

func TestAggregatePreEdit_DenyBeatsPass(t *testing.T) {
	in := []adapters.PreEditResult{
		{Decision: adapters.Pass, AdapterName: "first"},
		{Decision: adapters.Deny, Reason: "forbidden claim", AdapterName: "ground-truth"},
		{Decision: adapters.Pass, AdapterName: "third"},
	}
	got := adapters.AggregatePreEdit(in)
	if got.Decision != adapters.Deny {
		t.Errorf("Decision: want Deny, got %v", got.Decision)
	}
	if got.Reason != "forbidden claim" {
		t.Errorf("Reason: want %q, got %q", "forbidden claim", got.Reason)
	}
	if got.AdapterName != "ground-truth" {
		t.Errorf("AdapterName: want %q, got %q", "ground-truth", got.AdapterName)
	}
}

func TestAggregatePreEdit_FirstDenyWins(t *testing.T) {
	in := []adapters.PreEditResult{
		{Decision: adapters.Deny, Reason: "first reason", AdapterName: "a"},
		{Decision: adapters.Deny, Reason: "second reason", AdapterName: "b"},
	}
	got := adapters.AggregatePreEdit(in)
	if got.Reason != "first reason" {
		t.Errorf("first-deny-wins: want %q, got %q", "first reason", got.Reason)
	}
	if got.AdapterName != "a" {
		t.Errorf("first-deny adapter: want %q, got %q", "a", got.AdapterName)
	}
}

func TestAggregatePreEdit_AskCollapsesToDeny(t *testing.T) {
	in := []adapters.PreEditResult{
		{Decision: adapters.Ask, Reason: "ask reason", AdapterName: "a"},
	}
	got := adapters.AggregatePreEdit(in)
	if got.Decision != adapters.Deny {
		t.Errorf("Ask collapse: want Deny, got %v", got.Decision)
	}
	if got.Reason != "ask reason" {
		t.Errorf("Ask reason passthrough: want %q, got %q", "ask reason", got.Reason)
	}
}

func TestAggregatePreEdit_Empty(t *testing.T) {
	got := adapters.AggregatePreEdit(nil)
	if got.Decision != adapters.Pass {
		t.Errorf("empty: want Pass, got %v", got.Decision)
	}
}

func TestAggregatePostEdit_ConcatenatesContext(t *testing.T) {
	in := []adapters.PostEditResult{
		{AdditionalContext: "block one"},
		{AdditionalContext: "block two"},
	}
	got := adapters.AggregatePostEdit(in)
	if !strings.Contains(got.AdditionalContext, "block one") {
		t.Errorf("missing first block: %q", got.AdditionalContext)
	}
	if !strings.Contains(got.AdditionalContext, "block two") {
		t.Errorf("missing second block: %q", got.AdditionalContext)
	}
}

func TestAggregatePostEdit_DropsEmpties(t *testing.T) {
	in := []adapters.PostEditResult{
		{AdditionalContext: ""},
		{AdditionalContext: "real block"},
		{AdditionalContext: ""},
	}
	got := adapters.AggregatePostEdit(in)
	if got.AdditionalContext != "real block" {
		t.Errorf("empties not dropped: %q", got.AdditionalContext)
	}
}

func TestAggregatePostEdit_AccumulatesClaims(t *testing.T) {
	in := []adapters.PostEditResult{
		{Claims: []adapters.ClaimRecord{{Claim: "first", Verified: true}}},
		{Claims: []adapters.ClaimRecord{{Claim: "second", Verified: false}}},
		{},
	}
	got := adapters.AggregatePostEdit(in)
	if len(got.Claims) != 2 {
		t.Fatalf("claim count: want 2, got %d", len(got.Claims))
	}
	if got.Claims[0].Claim != "first" || got.Claims[1].Claim != "second" {
		t.Errorf("claims out of order: %+v", got.Claims)
	}
}

func TestAggregatePostEdit_JoinsSystemMessages(t *testing.T) {
	in := []adapters.PostEditResult{
		{SystemMessage: "vet ok"},
		{},
		{SystemMessage: "audit logged"},
	}
	got := adapters.AggregatePostEdit(in)
	want := "vet ok\naudit logged"
	if got.SystemMessage != want {
		t.Errorf("SystemMessage: want %q, got %q", want, got.SystemMessage)
	}
}

func TestAggregateSessionStart_JoinsWithSeparator(t *testing.T) {
	in := []adapters.SessionStartResult{
		{AdditionalContext: "## Recent decisions\n- foo"},
		{AdditionalContext: "## Open facts\n- bar"},
	}
	got := adapters.AggregateSessionStart(in)
	if !strings.Contains(got.AdditionalContext, "## Recent decisions") {
		t.Errorf("missing first block")
	}
	if !strings.Contains(got.AdditionalContext, "## Open facts") {
		t.Errorf("missing second block")
	}
	if !strings.Contains(got.AdditionalContext, "---") {
		t.Errorf("missing separator: %q", got.AdditionalContext)
	}
}

func TestAggregateSessionStart_DropsEmpties(t *testing.T) {
	in := []adapters.SessionStartResult{
		{AdditionalContext: ""},
		{AdditionalContext: "real block"},
		{AdditionalContext: ""},
	}
	got := adapters.AggregateSessionStart(in)
	if got.AdditionalContext != "real block" {
		t.Errorf("empties not dropped: %q", got.AdditionalContext)
	}
	if strings.Contains(got.AdditionalContext, "---") {
		t.Errorf("separator emitted with no neighbor: %q", got.AdditionalContext)
	}
}

func TestAggregateStop_JoinsLines(t *testing.T) {
	in := []adapters.StopResult{
		{SystemMessage: "3 unverified claims"},
		{SystemMessage: "1 forbidden hit recorded"},
	}
	got := adapters.AggregateStop(in)
	want := "3 unverified claims\n1 forbidden hit recorded"
	if got.SystemMessage != want {
		t.Errorf("Stop join: want %q, got %q", want, got.SystemMessage)
	}
}

func TestAggregateStop_DropsEmpties(t *testing.T) {
	in := []adapters.StopResult{
		{},
		{SystemMessage: "only one"},
		{},
	}
	got := adapters.AggregateStop(in)
	if got.SystemMessage != "only one" {
		t.Errorf("Stop empties: want %q, got %q", "only one", got.SystemMessage)
	}
}
