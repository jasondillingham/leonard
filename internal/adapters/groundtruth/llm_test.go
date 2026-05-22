package groundtruth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// mockLLM is a canned LLMClient. Responses keyed by sentence
// (after TrimSpace).
type mockLLM struct {
	responses map[string]bool
	err       error
	calls     []string
}

func (m *mockLLM) ClassifyClaim(_ context.Context, sentence string) (bool, error) {
	m.calls = append(m.calls, sentence)
	if m.err != nil {
		return false, m.err
	}
	return m.responses[strings.TrimSpace(sentence)], nil
}

func TestDetectHybrid_HeuristicModePassesThrough(t *testing.T) {
	// In heuristic mode the LLM should never be called even when
	// supplied — the function short-circuits to Detect.
	llm := &mockLLM{}
	res := groundtruth.DetectHybrid(context.Background(),
		"We use TypeScript on the frontend.", nil, nil,
		groundtruth.DetectOpts{Mode: groundtruth.ModeHeuristic, LLM: llm})
	if len(llm.calls) != 0 {
		t.Errorf("heuristic mode should not invoke LLM, got %d calls", len(llm.calls))
	}
	_ = res
}

func TestDetectHybrid_NilLLMShortCircuits(t *testing.T) {
	// Hybrid mode with no LLM is a clean no-op (no panic).
	res := groundtruth.DetectHybrid(context.Background(),
		"Some sentence here.", nil, nil,
		groundtruth.DetectOpts{Mode: groundtruth.ModeHybrid, LLM: nil})
	_ = res
}

func TestDetectHybrid_LLMAddsUnverifiedClaim(t *testing.T) {
	// Heuristic won't catch "Our customers love us" — too vague.
	// Hybrid LLM marks it as a verifiable claim.
	llm := &mockLLM{
		responses: map[string]bool{
			"Our customers love us": true,
		},
	}
	res := groundtruth.DetectHybrid(context.Background(),
		"Our customers love us.", nil, nil,
		groundtruth.DetectOpts{Mode: groundtruth.ModeHybrid, LLM: llm})

	if len(llm.calls) == 0 {
		t.Error("LLM should have been called for an uncovered sentence")
	}

	var llmClaim *groundtruth.Claim
	for i := range res.Claims {
		if res.Claims[i].Category == "llm" {
			llmClaim = &res.Claims[i]
			break
		}
	}
	if llmClaim == nil {
		t.Fatalf("expected LLM claim, got %+v", res.Claims)
	}
	if llmClaim.Verdict != groundtruth.VerdictUnverified {
		t.Errorf("LLM claim verdict: want Unverified, got %v", llmClaim.Verdict)
	}
	if res.Summary.Unverified < 1 {
		t.Errorf("Summary.Unverified should include LLM claim: %+v", res.Summary)
	}
}

func TestDetectHybrid_LLMSkipsAlreadyCoveredSentences(t *testing.T) {
	// "I shipped Go in production." is caught by the heuristic
	// detector. Hybrid LLM shouldn't be asked about it.
	llm := &mockLLM{responses: map[string]bool{}}
	facts := &groundtruth.Facts{Root: map[string]any{"tech_stack": map[string]any{"primary_language": "Go"}}}
	res := groundtruth.DetectHybrid(context.Background(),
		"I shipped Go in production. Our customers love us.",
		facts, nil,
		groundtruth.DetectOpts{Mode: groundtruth.ModeHybrid, LLM: llm})

	for _, c := range llm.calls {
		if strings.Contains(c, "shipped Go") {
			t.Errorf("LLM was asked about covered sentence: %q", c)
		}
	}
	_ = res
}

func TestDetectHybrid_LLMErrorIsNonFatal(t *testing.T) {
	llm := &mockLLM{err: errors.New("LLM unreachable")}
	res := groundtruth.DetectHybrid(context.Background(),
		"Some sentence.", nil, nil,
		groundtruth.DetectOpts{Mode: groundtruth.ModeHybrid, LLM: llm})
	// Should not panic; no LLM-derived claims added.
	for _, c := range res.Claims {
		if c.Category == "llm" {
			t.Errorf("LLM error path should NOT emit a claim, got %+v", c)
		}
	}
}

func TestDetectHybrid_LLMNoSayPasses(t *testing.T) {
	// LLM says "no" → no claim emitted.
	llm := &mockLLM{responses: map[string]bool{"The sky is blue": false}}
	res := groundtruth.DetectHybrid(context.Background(),
		"The sky is blue.", nil, nil,
		groundtruth.DetectOpts{Mode: groundtruth.ModeHybrid, LLM: llm})
	if len(res.Claims) != 0 {
		t.Errorf("LLM=false should produce no claims, got %+v", res.Claims)
	}
}

// --- OllamaClient transport tests ---

func TestOllamaClient_ParsesYesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": "yes",
			"done":     true,
		})
	}))
	defer srv.Close()

	c := &groundtruth.OllamaClient{
		Endpoint: srv.URL,
		Model:    "test-model",
		Client:   srv.Client(),
	}
	ok, err := c.ClassifyClaim(context.Background(), "test sentence")
	if err != nil {
		t.Fatalf("ClassifyClaim: %v", err)
	}
	if !ok {
		t.Error("response 'yes' should return true")
	}
}

func TestOllamaClient_ParsesNoResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "no", "done": true})
	}))
	defer srv.Close()

	c := &groundtruth.OllamaClient{Endpoint: srv.URL, Model: "m", Client: srv.Client()}
	ok, err := c.ClassifyClaim(context.Background(), "x")
	if err != nil {
		t.Fatalf("ClassifyClaim: %v", err)
	}
	if ok {
		t.Error("response 'no' should return false")
	}
}

func TestOllamaClient_RequiresModel(t *testing.T) {
	c := &groundtruth.OllamaClient{Endpoint: "http://localhost"}
	_, err := c.ClassifyClaim(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "Model is required") {
		t.Errorf("want Model-required error, got %v", err)
	}
}

func TestOllamaClient_HTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := &groundtruth.OllamaClient{Endpoint: srv.URL, Model: "m", Client: srv.Client()}
	_, err := c.ClassifyClaim(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("want HTTP 500 error, got %v", err)
	}
}

func TestOllamaClient_PostBodyShape(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "yes"})
	}))
	defer srv.Close()
	c := &groundtruth.OllamaClient{Endpoint: srv.URL, Model: "qwen2.5:0.5b", Client: srv.Client()}
	if _, err := c.ClassifyClaim(context.Background(), "test"); err != nil {
		t.Fatalf("ClassifyClaim: %v", err)
	}
	if gotBody["model"] != "qwen2.5:0.5b" {
		t.Errorf("model not sent: %v", gotBody)
	}
	if gotBody["stream"] != false {
		t.Errorf("stream should be false: %v", gotBody)
	}
}
