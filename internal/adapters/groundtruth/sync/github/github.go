// Package github is the built-in GitHub sync plugin (#32). It
// refreshes facts.yaml.oss_contributions entries against the
// GitHub REST API.
//
// Expected facts.yaml shape:
//
//	oss_contributions:
//	  - repo: jasondillingham/leonard
//	    number: 42
//	    status: open
//	    last_verified: 2026-04-15
//	    merged_at: ""
//
// Per entry, the plugin queries /repos/{repo}/pulls/{number} and
// updates status / merged_at / last_verified. Unknown entries
// (missing repo or number) are passed through unchanged.
package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jasondillingham/leonard/internal/adapters/groundtruth/sync"
)

// Options controls the plugin's behavior. AccessToken is used as a
// Bearer credential. APIBase defaults to https://api.github.com but
// is overridable for httptest fixtures.
type Options struct {
	AccessToken string
	APIBase     string

	// Client allows the test suite to inject a custom HTTP client
	// (e.g., one pointed at httptest.NewServer). Production uses
	// http.DefaultClient with a 10s timeout.
	Client *http.Client
}

// Sync refreshes the oss_contributions slice in input.Facts and
// returns the updated facts + a slice of changes. Non-fatal:
// per-entry HTTP errors are recorded in Changes (with a Reason
// containing the error) but don't abort the whole sync.
func Sync(input sync.Input, opts Options) (sync.Output, error) {
	if opts.APIBase == "" {
		opts.APIBase = "https://api.github.com"
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 10 * time.Second}
	}

	rawList, ok := input.Facts["oss_contributions"]
	if !ok {
		return sync.Output{
			UpdatedFacts: input.Facts,
			Changes:      nil,
		}, nil
	}

	list, ok := rawList.([]any)
	if !ok {
		return sync.Output{}, fmt.Errorf("github sync: facts.oss_contributions must be a list, got %T", rawList)
	}

	var changes []sync.Change
	updated := make([]any, 0, len(list))

	for i, e := range list {
		entry, ok := e.(map[string]any)
		if !ok {
			// Pass through non-map entries.
			updated = append(updated, e)
			continue
		}
		repo, _ := entry["repo"].(string)
		number := asInt(entry["number"])
		if repo == "" || number == 0 {
			updated = append(updated, entry)
			continue
		}

		pr, err := fetchPR(opts, repo, number)
		if err != nil {
			changes = append(changes, sync.Change{
				Path:   fmt.Sprintf("oss_contributions.%d", i),
				Reason: fmt.Sprintf("fetch error: %v", err),
			})
			updated = append(updated, entry)
			continue
		}

		// Build the updated entry (preserve unrelated keys).
		newEntry := copyMap(entry)
		todayUTC := time.Now().UTC().Format("2006-01-02")

		recordIfChanged(&changes, entry, newEntry, "status", pr.state(), fmt.Sprintf("oss_contributions.%d.status", i))
		if pr.MergedAt != "" {
			recordIfChanged(&changes, entry, newEntry, "merged_at", pr.MergedAt, fmt.Sprintf("oss_contributions.%d.merged_at", i))
		}
		// Always stamp last_verified.
		if oldLV, _ := entry["last_verified"].(string); oldLV != todayUTC {
			changes = append(changes, sync.Change{
				Path:   fmt.Sprintf("oss_contributions.%d.last_verified", i),
				Old:    oldLV,
				New:    todayUTC,
				Reason: "stamped on github sync",
			})
		}
		newEntry["last_verified"] = todayUTC

		updated = append(updated, newEntry)
	}

	out := copyMap(input.Facts)
	out["oss_contributions"] = updated
	return sync.Output{
		UpdatedFacts: out,
		Changes:      changes,
	}, nil
}

// pullRequest is the subset of GitHub's PR response we read.
type pullRequest struct {
	State    string `json:"state"`    // "open" or "closed"
	Merged   bool   `json:"merged"`
	MergedAt string `json:"merged_at"`
}

// state returns "merged" for merged PRs, "closed" for un-merged
// closed, "open" for open. This is the value we stamp into the
// facts entry's status field — "merged" is more useful than just
// "closed" for OSS-contribution tracking.
func (p pullRequest) state() string {
	if p.Merged {
		return "merged"
	}
	return p.State
}

func fetchPR(opts Options, repo string, number int) (pullRequest, error) {
	url := fmt.Sprintf("%s/repos/%s/pulls/%d", opts.APIBase, repo, number)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return pullRequest{}, err
	}
	if opts.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+opts.AccessToken)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := opts.Client.Do(req)
	if err != nil {
		return pullRequest{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden && strings.Contains(resp.Header.Get("X-Ratelimit-Remaining"), "0") {
		return pullRequest{}, errors.New("github API rate-limited; retry later or set GH_TOKEN")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return pullRequest{}, fmt.Errorf("github API %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var pr pullRequest
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return pullRequest{}, fmt.Errorf("decode: %w", err)
	}
	return pr, nil
}

// recordIfChanged compares old/new on a single field, appends a
// Change when they differ, and updates the new-entry map. Used to
// keep the per-field change logging consistent.
func recordIfChanged(changes *[]sync.Change, oldEntry, newEntry map[string]any, key string, newVal any, path string) {
	oldVal := oldEntry[key]
	if eq(oldVal, newVal) {
		return
	}
	*changes = append(*changes, sync.Change{
		Path: path,
		Old:  oldVal,
		New:  newVal,
	})
	newEntry[key] = newVal
}

func eq(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// asInt is best-effort coercion from yaml's any to int. yaml.v3
// emits int for small whole numbers and int64 / float64 for larger
// ones; we accept the common shapes.
func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
