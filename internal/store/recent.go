package store

import "fmt"

// ListFilesIndexedSince returns files with indexed_at >= since, newest first,
// capped at limit. limit <= 0 falls back to the default of 50; the MCP layer
// applies the hard cap before calling. since <= 0 returns every indexed file.
func (s *Store) ListFilesIndexedSince(since int64, limit int) ([]File, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT path, language, size_bytes, indexed_at
		FROM files
		WHERE indexed_at >= ?
		ORDER BY indexed_at DESC, path
		LIMIT ?`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("store: ListFilesIndexedSince: %w", err)
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.Language, &f.SizeBytes, &f.IndexedAt); err != nil {
			return nil, fmt.Errorf("store: ListFilesIndexedSince scan: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListFilesIndexedSince iter: %w", err)
	}
	return out, nil
}
