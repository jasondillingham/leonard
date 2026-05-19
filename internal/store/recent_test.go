package store

import (
	"reflect"
	"testing"
)

func TestListFilesIndexedSince(t *testing.T) {
	s, _ := newTestStore(t)

	// Seed four files with deterministic indexed_at timestamps so the test
	// can assert ordering and the since cutoff without time.Now() flake.
	files := []File{
		{Path: "a.go", Hash: "h", Language: "go", SizeBytes: 10, IndexedAt: 1000},
		{Path: "b.go", Hash: "h", Language: "go", SizeBytes: 20, IndexedAt: 2000},
		{Path: "c.py", Hash: "h", Language: "python", SizeBytes: 30, IndexedAt: 3000},
		{Path: "d.go", Hash: "h", Language: "go", SizeBytes: 40, IndexedAt: 4000},
	}
	for _, f := range files {
		if err := s.UpsertFile(f); err != nil {
			t.Fatalf("UpsertFile %q: %v", f.Path, err)
		}
	}

	cases := []struct {
		name      string
		since     int64
		limit     int
		wantPaths []string
	}{
		{"all newest first", 0, 0, []string{"d.go", "c.py", "b.go", "a.go"}},
		{"since cutoff inclusive", 2000, 0, []string{"d.go", "c.py", "b.go"}},
		{"since excludes earlier", 2500, 0, []string{"d.go", "c.py"}},
		{"limit caps results", 0, 2, []string{"d.go", "c.py"}},
		{"limit + since", 1500, 1, []string{"d.go"}},
		{"future cutoff returns empty", 9999, 0, nil},
		{"default limit applied when zero", 0, 0, []string{"d.go", "c.py", "b.go", "a.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListFilesIndexedSince(tc.since, tc.limit)
			if err != nil {
				t.Fatalf("ListFilesIndexedSince: %v", err)
			}
			var paths []string
			for _, f := range got {
				paths = append(paths, f.Path)
			}
			if !reflect.DeepEqual(paths, tc.wantPaths) {
				t.Fatalf("paths=%v want %v", paths, tc.wantPaths)
			}
		})
	}
}

func TestListFilesIndexedSinceReflectsUpserts(t *testing.T) {
	s, _ := newTestStore(t)

	if err := s.UpsertFile(File{Path: "a.go", Hash: "h1", Language: "go", SizeBytes: 1, IndexedAt: 1000}); err != nil {
		t.Fatalf("UpsertFile a: %v", err)
	}
	if err := s.UpsertFile(File{Path: "b.go", Hash: "h1", Language: "go", SizeBytes: 1, IndexedAt: 1000}); err != nil {
		t.Fatalf("UpsertFile b: %v", err)
	}
	if err := s.UpsertFile(File{Path: "c.go", Hash: "h1", Language: "go", SizeBytes: 1, IndexedAt: 1000}); err != nil {
		t.Fatalf("UpsertFile c: %v", err)
	}

	// Mutate two files — they should bubble up first in indexed_at order.
	if err := s.UpsertFile(File{Path: "b.go", Hash: "h2", Language: "go", SizeBytes: 2, IndexedAt: 2000}); err != nil {
		t.Fatalf("re-UpsertFile b: %v", err)
	}
	if err := s.UpsertFile(File{Path: "c.go", Hash: "h2", Language: "go", SizeBytes: 2, IndexedAt: 3000}); err != nil {
		t.Fatalf("re-UpsertFile c: %v", err)
	}

	got, err := s.ListFilesIndexedSince(1500, 0)
	if err != nil {
		t.Fatalf("ListFilesIndexedSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(got), got)
	}
	if got[0].Path != "c.go" || got[1].Path != "b.go" {
		t.Fatalf("expected c.go then b.go, got %+v", got)
	}
	if got[0].IndexedAt != 3000 || got[1].IndexedAt != 2000 {
		t.Fatalf("indexed_at not preserved: %+v", got)
	}
}

func TestListFilesIndexedSinceEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	got, err := s.ListFilesIndexedSince(0, 0)
	if err != nil {
		t.Fatalf("ListFilesIndexedSince: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %+v", got)
	}
}
