package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jasondillingham/leonard/internal/adapters/groundtruth/sync"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth/sync/github"
)

// fakeGitHub spins up an httptest server that serves canned PR
// responses keyed by "owner/repo/number".
func fakeGitHub(t *testing.T, prs map[string]map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		// Strip "/repos/" prefix, then "owner/repo/pulls/N" → "owner/repo/N".
		path := strings.TrimPrefix(r.URL.Path, "/repos/")
		parts := strings.Split(path, "/")
		if len(parts) != 4 || parts[2] != "pulls" {
			http.NotFound(w, r)
			return
		}
		key := parts[0] + "/" + parts[1] + "/" + parts[3]
		pr, ok := prs[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(pr)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSync_HappyPath(t *testing.T) {
	srv := fakeGitHub(t, map[string]map[string]any{
		"acme/widget/42": {
			"state":     "closed",
			"merged":    true,
			"merged_at": "2026-04-15T10:00:00Z",
		},
	})

	facts := map[string]any{
		"oss_contributions": []any{
			map[string]any{
				"repo":          "acme/widget",
				"number":        42,
				"status":        "open",
				"last_verified": "2026-01-01",
			},
		},
	}

	out, err := github.Sync(sync.Input{Facts: facts}, github.Options{
		APIBase: srv.URL,
		Client:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	updated := out.UpdatedFacts["oss_contributions"].([]any)[0].(map[string]any)
	if updated["status"] != "merged" {
		t.Errorf("status: want merged, got %v", updated["status"])
	}
	if updated["merged_at"] != "2026-04-15T10:00:00Z" {
		t.Errorf("merged_at: got %v", updated["merged_at"])
	}
	today := time.Now().UTC().Format("2006-01-02")
	if updated["last_verified"] != today {
		t.Errorf("last_verified: want %q, got %v", today, updated["last_verified"])
	}

	// 3 changes: status, merged_at, last_verified.
	if len(out.Changes) < 1 {
		t.Errorf("expected at least one Change, got %+v", out.Changes)
	}
}

func TestSync_OpenPRStaysOpen(t *testing.T) {
	srv := fakeGitHub(t, map[string]map[string]any{
		"acme/widget/100": {
			"state":  "open",
			"merged": false,
		},
	})
	facts := map[string]any{
		"oss_contributions": []any{
			map[string]any{"repo": "acme/widget", "number": 100, "status": "open"},
		},
	}
	out, err := github.Sync(sync.Input{Facts: facts}, github.Options{
		APIBase: srv.URL,
		Client:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	updated := out.UpdatedFacts["oss_contributions"].([]any)[0].(map[string]any)
	if updated["status"] != "open" {
		t.Errorf("status: want open, got %v", updated["status"])
	}
}

func TestSync_ClosedNotMerged(t *testing.T) {
	srv := fakeGitHub(t, map[string]map[string]any{
		"acme/widget/55": {
			"state":  "closed",
			"merged": false,
		},
	})
	facts := map[string]any{
		"oss_contributions": []any{
			map[string]any{"repo": "acme/widget", "number": 55, "status": "open"},
		},
	}
	out, err := github.Sync(sync.Input{Facts: facts}, github.Options{
		APIBase: srv.URL,
		Client:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	updated := out.UpdatedFacts["oss_contributions"].([]any)[0].(map[string]any)
	if updated["status"] != "closed" {
		t.Errorf("status: want closed, got %v", updated["status"])
	}
}

func TestSync_MissingFieldsPassThrough(t *testing.T) {
	srv := fakeGitHub(t, nil)
	facts := map[string]any{
		"oss_contributions": []any{
			map[string]any{"note": "no repo or number; pass through"},
			map[string]any{"repo": "acme/widget"}, // missing number
		},
	}
	out, err := github.Sync(sync.Input{Facts: facts}, github.Options{
		APIBase: srv.URL,
		Client:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(out.Changes) != 0 {
		t.Errorf("unkeyed entries should produce no changes, got %+v", out.Changes)
	}
}

func TestSync_HTTPErrorPerEntryNotFatal(t *testing.T) {
	// One PR returns 404, another returns ok.
	srv := fakeGitHub(t, map[string]map[string]any{
		"acme/widget/2": {"state": "open", "merged": false},
	})
	facts := map[string]any{
		"oss_contributions": []any{
			map[string]any{"repo": "acme/widget", "number": 1, "status": "open"}, // 404
			map[string]any{"repo": "acme/widget", "number": 2, "status": "open"}, // ok
		},
	}
	out, err := github.Sync(sync.Input{Facts: facts}, github.Options{
		APIBase: srv.URL,
		Client:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// First entry's last_verified should NOT change (entry pass-thru on error).
	first := out.UpdatedFacts["oss_contributions"].([]any)[0].(map[string]any)
	if _, set := first["last_verified"]; set {
		t.Errorf("entry-with-error should not have stamped last_verified: %+v", first)
	}
	// One of the Changes should be a fetch error.
	var sawFetchErr bool
	for _, c := range out.Changes {
		if strings.Contains(c.Reason, "fetch error") {
			sawFetchErr = true
		}
	}
	if !sawFetchErr {
		t.Errorf("expected a fetch-error Change, got %+v", out.Changes)
	}
}

func TestSync_NoOssContributionsKey(t *testing.T) {
	srv := fakeGitHub(t, nil)
	facts := map[string]any{"tech_stack": map[string]any{"primary_language": "Go"}}
	out, err := github.Sync(sync.Input{Facts: facts}, github.Options{
		APIBase: srv.URL,
		Client:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(out.Changes) != 0 {
		t.Errorf("no oss_contributions → no changes, got %+v", out.Changes)
	}
}

func TestSync_RejectsNonListOssContributions(t *testing.T) {
	srv := fakeGitHub(t, nil)
	facts := map[string]any{"oss_contributions": "not a list"}
	_, err := github.Sync(sync.Input{Facts: facts}, github.Options{
		APIBase: srv.URL,
		Client:  srv.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "must be a list") {
		t.Errorf("want list-type error, got %v", err)
	}
}

func TestSync_AuthHeaderSet(t *testing.T) {
	var sawAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "open"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	facts := map[string]any{
		"oss_contributions": []any{
			map[string]any{"repo": "acme/widget", "number": 1},
		},
	}
	_, err := github.Sync(sync.Input{Facts: facts}, github.Options{
		AccessToken: "ghp_test123",
		APIBase:     srv.URL,
		Client:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if sawAuth != "Bearer ghp_test123" {
		t.Errorf("Authorization header: want %q, got %q", "Bearer ghp_test123", sawAuth)
	}
}
