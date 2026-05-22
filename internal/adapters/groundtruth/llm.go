package groundtruth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// LLMClient classifies whether a sentence is making a verifiable
// claim. v1.0's hybrid detection (#36) uses this as a fallback for
// sentences the heuristic detector didn't classify.
//
// Implementations:
//   - OllamaClient — HTTP client for a local Ollama-style endpoint
//   - mockLLMClient (test-only) — canned responses
//
// ClassifyClaim returns (true, nil) when the sentence makes a
// verifiable claim worth surfacing as Unverified; (false, nil)
// when the sentence is non-claim prose; (false, err) on transport
// or parse errors (caller treats the sentence as non-claim and
// logs the error).
type LLMClient interface {
	ClassifyClaim(ctx context.Context, sentence string) (bool, error)
}

// DetectMode controls hybrid behavior. "heuristic" (default) is
// the v0.6 behavior — regex patterns only. "hybrid" adds the LLM
// fallback for sentences the patterns missed.
type DetectMode string

const (
	ModeHeuristic DetectMode = "heuristic"
	ModeHybrid    DetectMode = "hybrid"
)

// DetectOpts carries optional configuration for DetectHybrid. A
// zero value falls back to ModeHeuristic with no LLM call.
type DetectOpts struct {
	Mode DetectMode
	LLM  LLMClient
}

// DetectHybrid extends Detect with optional LLM fallback for
// sentences the heuristic missed. When opts.Mode == ModeHybrid
// and opts.LLM is non-nil:
//
//   1. Run the heuristic detector exactly as Detect does.
//   2. Split the input into sentences.
//   3. For each sentence that didn't produce a heuristic claim
//      AND isn't covered by a forbidden hit, ask the LLM
//      whether it's a verifiable claim.
//   4. LLM-confirmed claims are emitted as VerdictUnverified
//      with category="llm" so callers can distinguish.
//
// Mode == "" or ModeHeuristic short-circuits to Detect; nil LLM
// in hybrid mode also short-circuits (clean failure on a missing
// dependency).
func DetectHybrid(ctx context.Context, text string, facts *Facts, rules Rules, opts DetectOpts) DetectionResult {
	res := Detect(text, facts, rules)
	if opts.Mode != ModeHybrid || opts.LLM == nil {
		return res
	}

	// Don't waste tokens on sentences already covered by a claim.
	covered := claimSpans(res.Claims)
	for _, s := range splitSentences(text) {
		if overlapsAnySpan(s.start, s.end, covered) {
			continue
		}
		sentence := strings.TrimSpace(text[s.start:s.end])
		if sentence == "" {
			continue
		}
		isClaim, err := opts.LLM.ClassifyClaim(ctx, sentence)
		if err != nil {
			// Log to nowhere — callers don't have a logger here
			// and this is best-effort fallback. The heuristic
			// path already produced its result.
			continue
		}
		if !isClaim {
			continue
		}
		res.Claims = append(res.Claims, Claim{
			Text:      sentence,
			Category:  "llm",
			Verdict:   VerdictUnverified,
			StartByte: s.start,
			EndByte:   s.end,
			Note:      "LLM-classified as a verifiable claim; no heuristic match",
		})
		res.Summary.Total++
		res.Summary.Unverified++
	}
	sortClaimsBySource(res.Claims)
	return res
}

// claimSpans extracts the byte spans of existing claims so the
// LLM fallback can skip already-covered sentences.
func claimSpans(claims []Claim) []span {
	out := make([]span, len(claims))
	for i, c := range claims {
		out[i] = span{start: c.StartByte, end: c.EndByte}
	}
	return out
}

func overlapsAnySpan(start, end int, spans []span) bool {
	for _, s := range spans {
		if start < s.end && end > s.start {
			return true
		}
	}
	return false
}

// splitSentences breaks text into sentence spans using terminator
// punctuation (. ! ?). Naive — doesn't handle "Dr.", "etc." etc.
// — but good enough for the hybrid fallback's signal.
func splitSentences(text string) []span {
	var out []span
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '.' || text[i] == '!' || text[i] == '?' || text[i] == '\n' {
			if i > start {
				out = append(out, span{start: start, end: i})
			}
			start = i + 1
		}
	}
	if start < len(text) {
		out = append(out, span{start: start, end: len(text)})
	}
	return out
}

// OllamaClient is the production LLMClient. Talks to an Ollama-
// style endpoint via the /api/generate HTTP route.
type OllamaClient struct {
	// Endpoint base URL. Default "http://localhost:11434" matches
	// Ollama's default port.
	Endpoint string

	// Model is the local model name. Operators pick this in
	// config.toml. Small models (qwen2.5:0.5b, llama3.2:1b) are
	// the v1.0 recommendation.
	Model string

	// Client is the HTTP client. Default uses a 30s timeout.
	Client *http.Client
}

// ollamaRequest mirrors Ollama's /api/generate request shape.
type ollamaRequest struct {
	Model       string  `json:"model"`
	Prompt      string  `json:"prompt"`
	Stream      bool    `json:"stream"`
	Temperature float64 `json:"temperature"`
	NumPredict  int     `json:"num_predict"`
}

// ollamaResponse mirrors Ollama's /api/generate response shape.
type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// claimClassifierPrompt is the system + user prompt for the
// LLM fallback. Asks for a single-word yes/no answer to keep
// the response cost minimal and parsing trivial.
const claimClassifierPrompt = `Is the following sentence making a verifiable factual claim (about a product feature, a person's experience, a quantity, a date, or a capability)? Answer with only "yes" or "no".

Sentence: %q

Answer:`

// ClassifyClaim implements LLMClient. Returns true when the LLM's
// response starts with "yes" (case-insensitive); false otherwise.
// Network or parse errors propagate to the caller.
func (c *OllamaClient) ClassifyClaim(ctx context.Context, sentence string) (bool, error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	if c.Model == "" {
		return false, errors.New("ollama: Model is required")
	}
	httpClient := c.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	body, err := json.Marshal(ollamaRequest{
		Model:       c.Model,
		Prompt:      fmt.Sprintf(claimClassifierPrompt, sentence),
		Stream:      false,
		Temperature: 0,
		NumPredict:  6,
	})
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}

	var or ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&or); err != nil {
		return false, fmt.Errorf("ollama: decode: %w", err)
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(or.Response)), "yes"), nil
}
