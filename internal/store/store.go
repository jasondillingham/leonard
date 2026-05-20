package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// schemaVersion is the current schema version applied by migrate. Bump this
// whenever a new migration is appended to migrations below.
const schemaVersion = 4

// File describes an indexed source file.
type File struct {
	Path      string
	Hash      string
	Language  string
	SizeBytes int64
	IndexedAt int64 // unix seconds; populated by UpsertFile if zero
}

// Symbol is a top-level identifier extracted from a source file.
type Symbol struct {
	ID            int64
	FilePath      string
	Name          string
	QualifiedName string
	Kind          string // function|method|type|const|var|interface
	Signature     string
	StartLine     int
	EndLine       int
	Exported      bool
	ParentID      *int64
}

// Decision is a recorded project choice. SupersededBy points at the decision
// that replaces this one (if any). RelatedFiles / RelatedSymbols are
// optional pointers into the index used by GetStaleDecisions: a decision
// referencing a symbol the index no longer knows about is flagged for
// review rather than going silently stale.
type Decision struct {
	ID             int64
	Topic          string
	Choice         string
	Reasoning      string
	RecordedAt     int64
	SupersededBy   *int64
	RelatedFiles   []string
	RelatedSymbols []string
}

// StaleDecision is the response shape for GetStaleDecisions: the decision
// itself plus the specific refs that no longer resolve against the index.
// At least one of MissingFiles or MissingSymbols is non-empty by
// construction — clean decisions are filtered out.
type StaleDecision struct {
	Decision       Decision
	MissingFiles   []string
	MissingSymbols []string
}

// Claim is a verifiable assertion made during a Claude Code session.
// SupersededByClaimID points at the later claim that resolved this one (a
// vet=ok run on the same FilePath, recorded by the post-edit hook). Nil
// while the claim is still the latest signal for its file.
//
// Tool, IndexOK, VetOK, and VetErrorSummary are the structured fields the
// post-edit hook derives. *bool fields are nil for legacy rows recorded
// before migration v3 and for rows the hook didn't populate (e.g.
// record_claim calls from a model where there's no index/vet step).
type Claim struct {
	ID                  int64
	SessionID           string
	Claim               string
	Evidence            string
	Verified            bool
	RecordedAt          int64
	FilePath            string
	SupersededByClaimID *int64
	Tool                string
	IndexOK             *bool
	VetOK               *bool
	VetErrorSummary     string
}

// Store is the SQLite-backed Leonard data layer. The zero value is not usable;
// callers must obtain a *Store via Open.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path, enables WAL mode plus
// foreign-key enforcement, and applies any pending schema migrations. Calling
// Open repeatedly on the same path is a no-op once migrations are at the
// current version.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	// Verify the connection actually works before returning.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database handle. Safe to call multiple times;
// subsequent calls return whatever the driver reports.
func (s *Store) Close() error {
	return s.db.Close()
}

// buildDSN turns a filesystem path into a modernc.org/sqlite DSN with the
// pragmas we always want: WAL journal mode, foreign-key enforcement, and a
// 5-second busy timeout so brief writer contention doesn't error out.
func buildDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(wal)")
	q.Add("_pragma", "foreign_keys(on)")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + path + "?" + q.Encode()
}

// migrations is appended-to as the schema evolves. Index N applies migration
// to version N+1 (so migrations[0] takes a fresh DB to v1).
var migrations = []func(*sql.Tx) error{
	migrateV1,
	migrateV2,
	migrateV3,
	migrateV4,
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create meta table: %w", err)
	}

	var current int
	var raw string
	switch err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
		current = 0
	case err != nil:
		return fmt.Errorf("store: read schema_version: %w", err)
	default:
		current, err = strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("store: parse schema_version %q: %w", raw, err)
		}
	}

	if current > schemaVersion {
		return fmt.Errorf("store: db schema v%d newer than supported v%d", current, schemaVersion)
	}
	if current == schemaVersion {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin migration tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for v := current; v < schemaVersion; v++ {
		if err := migrations[v](tx); err != nil {
			return fmt.Errorf("store: migrate to v%d: %w", v+1, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('schema_version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		strconv.Itoa(schemaVersion)); err != nil {
		return fmt.Errorf("store: record schema_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration: %w", err)
	}
	return nil
}

func migrateV1(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE files (
			path        TEXT PRIMARY KEY,
			hash        TEXT NOT NULL,
			language    TEXT NOT NULL,
			size_bytes  INTEGER NOT NULL,
			indexed_at  INTEGER NOT NULL
		)`,
		`CREATE TABLE symbols (
			id               INTEGER PRIMARY KEY,
			file_path        TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
			name             TEXT NOT NULL,
			qualified_name   TEXT NOT NULL,
			kind             TEXT NOT NULL,
			signature        TEXT,
			start_line       INTEGER NOT NULL,
			end_line         INTEGER NOT NULL,
			exported         INTEGER NOT NULL,
			parent_id        INTEGER REFERENCES symbols(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX idx_symbols_name ON symbols(name)`,
		`CREATE INDEX idx_symbols_qname ON symbols(qualified_name)`,
		`CREATE INDEX idx_symbols_file ON symbols(file_path)`,
		`CREATE TABLE decisions (
			id            INTEGER PRIMARY KEY,
			topic         TEXT NOT NULL,
			choice        TEXT NOT NULL,
			reasoning     TEXT NOT NULL,
			recorded_at   INTEGER NOT NULL,
			superseded_by INTEGER REFERENCES decisions(id)
		)`,
		`CREATE INDEX idx_decisions_topic ON decisions(topic)`,
		`CREATE TABLE claims (
			id           INTEGER PRIMARY KEY,
			session_id   TEXT NOT NULL,
			claim        TEXT NOT NULL,
			evidence     TEXT NOT NULL,
			verified     INTEGER NOT NULL,
			recorded_at  INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_claims_session ON claims(session_id)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// migrateV2 adds claim metadata that lets the post-edit hook supersede prior
// failure records when a later vet=ok run lands on the same file. Existing
// rows get NULL file_path (so they're never matched as supersession targets)
// and NULL superseded_by_claim_id (so GetUnverifiedClaims still returns
// them).
func migrateV2(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE claims ADD COLUMN file_path TEXT`,
		`ALTER TABLE claims ADD COLUMN superseded_by_claim_id INTEGER REFERENCES claims(id)`,
		`CREATE INDEX idx_claims_file_path ON claims(file_path)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// migrateV3 promotes the structured fields the post-edit hook already
// derives (tool, index_ok, vet_ok, vet_error_summary) into columns so they
// can be queried directly instead of LIKE-grepping the claim/evidence
// blobs. Pre-migration rows leave all four NULL — readers must treat NULL
// as "unknown" and fall back to the verified column for older history.
func migrateV3(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE claims ADD COLUMN tool TEXT`,
		`ALTER TABLE claims ADD COLUMN index_ok INTEGER`,
		`ALTER TABLE claims ADD COLUMN vet_ok INTEGER`,
		`ALTER TABLE claims ADD COLUMN vet_error_summary TEXT`,
		`CREATE INDEX idx_claims_vet_ok ON claims(vet_ok)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// migrateV4 adds JSON-array columns that let decisions point at the files
// and symbols they reason about. GetStaleDecisions cross-checks these refs
// against the live index so a decision whose target symbol has been
// renamed or removed surfaces for review instead of going silently stale.
// Both columns are nullable — legacy decisions and refs-less new ones keep
// NULL and round-trip with empty slices.
func migrateV4(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE decisions ADD COLUMN related_files TEXT`,
		`ALTER TABLE decisions ADD COLUMN related_symbols TEXT`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// UpsertFile inserts a file row, or replaces the existing row at the same
// path. If f.IndexedAt is zero, the current unix time is used.
func (s *Store) UpsertFile(f File) error {
	if f.Path == "" {
		return errors.New("store: UpsertFile: empty path")
	}
	if f.IndexedAt == 0 {
		f.IndexedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(`INSERT INTO files(path, hash, language, size_bytes, indexed_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			hash       = excluded.hash,
			language   = excluded.language,
			size_bytes = excluded.size_bytes,
			indexed_at = excluded.indexed_at`,
		f.Path, f.Hash, f.Language, f.SizeBytes, f.IndexedAt,
	)
	if err != nil {
		return fmt.Errorf("store: UpsertFile %q: %w", f.Path, err)
	}
	return nil
}

// GetFile returns the file row at path. The boolean is false when no row
// exists (in which case the error is nil).
func (s *Store) GetFile(path string) (File, bool, error) {
	var f File
	err := s.db.QueryRow(`SELECT path, hash, language, size_bytes, indexed_at
		FROM files WHERE path = ?`, path).
		Scan(&f.Path, &f.Hash, &f.Language, &f.SizeBytes, &f.IndexedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, fmt.Errorf("store: GetFile %q: %w", path, err)
	}
	return f, true, nil
}

// ReplaceSymbols deletes every symbol previously recorded for filePath and
// inserts syms in slice order. Within a batch, a non-zero Symbol.ID acts as a
// caller-chosen tag: subsequent symbols may set ParentID to that tag and the
// store rewrites it to the real auto-generated row ID before inserting.
// ParentID values that don't match any tag are passed through as literal IDs.
func (s *Store) ReplaceSymbols(filePath string, syms []Symbol) error {
	if filePath == "" {
		return errors.New("store: ReplaceSymbols: empty file path")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: ReplaceSymbols begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM symbols WHERE file_path = ?`, filePath); err != nil {
		return fmt.Errorf("store: ReplaceSymbols clear: %w", err)
	}

	stmt, err := tx.Prepare(`INSERT INTO symbols(
		file_path, name, qualified_name, kind, signature,
		start_line, end_line, exported, parent_id
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: ReplaceSymbols prepare: %w", err)
	}
	defer stmt.Close()

	tagToID := make(map[int64]int64, len(syms))
	for i, sym := range syms {
		var parent any
		if sym.ParentID != nil {
			if real, ok := tagToID[*sym.ParentID]; ok {
				parent = real
			} else {
				parent = *sym.ParentID
			}
		}
		res, err := stmt.Exec(
			filePath, sym.Name, sym.QualifiedName, sym.Kind, sym.Signature,
			sym.StartLine, sym.EndLine, boolToInt(sym.Exported), parent,
		)
		if err != nil {
			return fmt.Errorf("store: ReplaceSymbols insert idx %d (%q): %w", i, sym.QualifiedName, err)
		}
		realID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: ReplaceSymbols last id idx %d: %w", i, err)
		}
		if sym.ID != 0 {
			tagToID[sym.ID] = realID
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: ReplaceSymbols commit: %w", err)
	}
	return nil
}

// FindSymbolsByName returns every symbol whose name column matches exactly,
// ordered by file path then start line.
func (s *Store) FindSymbolsByName(name string) ([]Symbol, error) {
	rows, err := s.db.Query(`SELECT id, file_path, name, qualified_name, kind,
		signature, start_line, end_line, exported, parent_id
		FROM symbols WHERE name = ?
		ORDER BY file_path, start_line, id`, name)
	if err != nil {
		return nil, fmt.Errorf("store: FindSymbolsByName: %w", err)
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// FindSymbolsByQuery does a case-sensitive substring search across name and
// qualified_name, capped at limit (defaults to 50 when limit <= 0).
func (s *Store) FindSymbolsByQuery(q string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 50
	}
	pattern := "%" + escapeLike(q) + "%"
	rows, err := s.db.Query(`SELECT id, file_path, name, qualified_name, kind,
		signature, start_line, end_line, exported, parent_id
		FROM symbols
		WHERE name LIKE ? ESCAPE '\' OR qualified_name LIKE ? ESCAPE '\'
		ORDER BY name, file_path, start_line, id
		LIMIT ?`, pattern, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("store: FindSymbolsByQuery: %w", err)
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListFiles returns files matching the optional GLOB pattern and/or language.
// Empty filters are ignored. Results are ordered by path.
func (s *Store) ListFiles(pattern, lang string) ([]File, error) {
	var (
		clauses []string
		args    []any
	)
	if pattern != "" {
		clauses = append(clauses, "path GLOB ?")
		args = append(args, pattern)
	}
	if lang != "" {
		clauses = append(clauses, "language = ?")
		args = append(args, lang)
	}
	q := `SELECT path, hash, language, size_bytes, indexed_at FROM files`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY path"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: ListFiles: %w", err)
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.Hash, &f.Language, &f.SizeBytes, &f.IndexedAt); err != nil {
			return nil, fmt.Errorf("store: ListFiles scan: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListFiles iter: %w", err)
	}
	return out, nil
}

// DeleteFile removes a file row and (via FK CASCADE) its symbols. Used by
// the indexer's stale-row pruner — when a path vanishes from disk between
// IndexAll runs, the prior file/symbol rows must come out of the store or
// verify_symbol keeps returning matches that no longer exist.
//
// An empty path is rejected to avoid accidentally clearing the whole table
// via a typo. Deleting a path that isn't in the store is a silent no-op.
func (s *Store) DeleteFile(path string) error {
	if path == "" {
		return errors.New("store: DeleteFile: empty path")
	}
	if _, err := s.db.Exec(`DELETE FROM files WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: DeleteFile: %w", err)
	}
	return nil
}

// SymbolCountsByFile returns file_path → symbol-row count for every file
// that has at least one symbol. Files with zero symbols are intentionally
// absent so callers can spot them by diffing against the files table —
// `leonard doctor` uses this to surface likely parse failures.
func (s *Store) SymbolCountsByFile() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT file_path, COUNT(*) FROM symbols GROUP BY file_path`)
	if err != nil {
		return nil, fmt.Errorf("store: SymbolCountsByFile: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var path string
		var n int
		if err := rows.Scan(&path, &n); err != nil {
			return nil, fmt.Errorf("store: SymbolCountsByFile scan: %w", err)
		}
		out[path] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: SymbolCountsByFile iter: %w", err)
	}
	return out, nil
}

// RecordDecision inserts a decision and returns its row ID. If RecordedAt is
// zero, the current unix time is used. RelatedFiles / RelatedSymbols are
// serialized as JSON arrays (or left NULL when empty) so GetStaleDecisions
// can later cross-check them against the live index.
func (s *Store) RecordDecision(d Decision) (int64, error) {
	if d.Topic == "" {
		return 0, errors.New("store: RecordDecision: empty topic")
	}
	if d.RecordedAt == 0 {
		d.RecordedAt = time.Now().Unix()
	}
	relFiles, err := jsonStringArray(d.RelatedFiles)
	if err != nil {
		return 0, fmt.Errorf("store: RecordDecision marshal related_files: %w", err)
	}
	relSyms, err := jsonStringArray(d.RelatedSymbols)
	if err != nil {
		return 0, fmt.Errorf("store: RecordDecision marshal related_symbols: %w", err)
	}
	res, err := s.db.Exec(`INSERT INTO decisions(
		topic, choice, reasoning, recorded_at, superseded_by, related_files, related_symbols
	) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		d.Topic, d.Choice, d.Reasoning, d.RecordedAt, nullableInt64(d.SupersededBy),
		relFiles, relSyms)
	if err != nil {
		return 0, fmt.Errorf("store: RecordDecision: %w", err)
	}
	return res.LastInsertId()
}

// GetDecisions returns decisions matching the optional topic and since
// (recorded_at >=) filters, newest first. limit <= 0 uses a default of 50.
func (s *Store) GetDecisions(topic string, since int64, limit int) ([]Decision, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		clauses []string
		args    []any
	)
	if topic != "" {
		clauses = append(clauses, "topic = ?")
		args = append(args, topic)
	}
	if since > 0 {
		clauses = append(clauses, "recorded_at >= ?")
		args = append(args, since)
	}
	q := `SELECT id, topic, choice, reasoning, recorded_at, superseded_by,
		related_files, related_symbols FROM decisions`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY recorded_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: GetDecisions: %w", err)
	}
	defer rows.Close()

	var out []Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("store: GetDecisions scan: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: GetDecisions iter: %w", err)
	}
	return out, nil
}

// GetStaleDecisions walks every decision (newest first), cross-checks its
// related_files / related_symbols against the live index, and returns the
// ones with at least one missing ref. A file ref is missing when no row in
// the files table matches the path; a symbol ref is missing when no row in
// the symbols table matches either the name column or qualified_name. A
// limit of zero returns up to 200 decisions; the caller is expected to
// keep total decision counts modest.
func (s *Store) GetStaleDecisions(limit int) ([]StaleDecision, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id, topic, choice, reasoning, recorded_at,
		superseded_by, related_files, related_symbols FROM decisions
		WHERE related_files IS NOT NULL OR related_symbols IS NOT NULL
		ORDER BY recorded_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: GetStaleDecisions: %w", err)
	}
	defer rows.Close()

	var out []StaleDecision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("store: GetStaleDecisions scan: %w", err)
		}
		missingFiles, err := s.missingFiles(d.RelatedFiles)
		if err != nil {
			return nil, err
		}
		missingSyms, err := s.missingSymbols(d.RelatedSymbols)
		if err != nil {
			return nil, err
		}
		if len(missingFiles) == 0 && len(missingSyms) == 0 {
			continue
		}
		out = append(out, StaleDecision{
			Decision:       d,
			MissingFiles:   missingFiles,
			MissingSymbols: missingSyms,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: GetStaleDecisions iter: %w", err)
	}
	return out, nil
}

func (s *Store) missingFiles(paths []string) ([]string, error) {
	var missing []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		var found int
		if err := s.db.QueryRow(`SELECT 1 FROM files WHERE path = ? LIMIT 1`, p).Scan(&found); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				missing = append(missing, p)
				continue
			}
			return nil, fmt.Errorf("store: missingFiles probe %q: %w", p, err)
		}
	}
	return missing, nil
}

func (s *Store) missingSymbols(names []string) ([]string, error) {
	var missing []string
	for _, n := range names {
		if n == "" {
			continue
		}
		var found int
		err := s.db.QueryRow(`SELECT 1 FROM symbols
			WHERE name = ? OR qualified_name = ? LIMIT 1`, n, n).Scan(&found)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				missing = append(missing, n)
				continue
			}
			return nil, fmt.Errorf("store: missingSymbols probe %q: %w", n, err)
		}
	}
	return missing, nil
}

// scanDecision factors out the common row scan used by GetDecisions and
// GetStaleDecisions. The row's column list must match exactly:
//   id, topic, choice, reasoning, recorded_at, superseded_by,
//   related_files, related_symbols
func scanDecision(rows *sql.Rows) (Decision, error) {
	var (
		d       Decision
		sup     sql.NullInt64
		relF    sql.NullString
		relS    sql.NullString
	)
	if err := rows.Scan(&d.ID, &d.Topic, &d.Choice, &d.Reasoning,
		&d.RecordedAt, &sup, &relF, &relS); err != nil {
		return Decision{}, err
	}
	if sup.Valid {
		v := sup.Int64
		d.SupersededBy = &v
	}
	if relF.Valid && relF.String != "" {
		if err := json.Unmarshal([]byte(relF.String), &d.RelatedFiles); err != nil {
			return Decision{}, fmt.Errorf("decode related_files: %w", err)
		}
	}
	if relS.Valid && relS.String != "" {
		if err := json.Unmarshal([]byte(relS.String), &d.RelatedSymbols); err != nil {
			return Decision{}, fmt.Errorf("decode related_symbols: %w", err)
		}
	}
	return d, nil
}

// jsonStringArray serializes xs as a JSON array, or returns nil so the
// caller persists SQL NULL when there's nothing to record. Empty slices
// and nil are equivalent — both store NULL.
func jsonStringArray(xs []string) (any, error) {
	if len(xs) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// SupersedeDecision records a replacement for an existing decision under the
// same topic, points the old row's superseded_by at the new row, and returns
// the new row ID. Errors if id doesn't exist or has already been superseded.
func (s *Store) SupersedeDecision(id int64, choice, reasoning string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: SupersedeDecision begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		topic string
		sup   sql.NullInt64
	)
	switch err := tx.QueryRow(`SELECT topic, superseded_by FROM decisions WHERE id = ?`, id).
		Scan(&topic, &sup); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("store: SupersedeDecision: decision %d not found", id)
	case err != nil:
		return 0, fmt.Errorf("store: SupersedeDecision lookup: %w", err)
	}
	if sup.Valid {
		return 0, fmt.Errorf("store: SupersedeDecision: decision %d already superseded by %d", id, sup.Int64)
	}

	now := time.Now().Unix()
	res, err := tx.Exec(`INSERT INTO decisions(topic, choice, reasoning, recorded_at, superseded_by)
		VALUES(?, ?, ?, ?, NULL)`,
		topic, choice, reasoning, now)
	if err != nil {
		return 0, fmt.Errorf("store: SupersedeDecision insert: %w", err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: SupersedeDecision id: %w", err)
	}
	if _, err := tx.Exec(`UPDATE decisions SET superseded_by = ? WHERE id = ?`, newID, id); err != nil {
		return 0, fmt.Errorf("store: SupersedeDecision link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: SupersedeDecision commit: %w", err)
	}
	return newID, nil
}

// RecordClaim inserts a claim row and returns its ID. If RecordedAt is zero,
// the current unix time is used. SessionID is opaque to the store — an empty
// value means "unscoped" (no session associated yet) and is accepted as-is.
// FilePath, when non-empty, lets SupersedeClaimsForFile match this row later.
func (s *Store) RecordClaim(c Claim) (int64, error) {
	if c.RecordedAt == 0 {
		c.RecordedAt = time.Now().Unix()
	}
	res, err := s.db.Exec(`INSERT INTO claims(
		session_id, claim, evidence, verified, recorded_at,
		file_path, tool, index_ok, vet_ok, vet_error_summary
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.SessionID, c.Claim, c.Evidence, boolToInt(c.Verified), c.RecordedAt,
		nullableString(c.FilePath), nullableString(c.Tool),
		nullableBool(c.IndexOK), nullableBool(c.VetOK), nullableString(c.VetErrorSummary))
	if err != nil {
		return 0, fmt.Errorf("store: RecordClaim: %w", err)
	}
	return res.LastInsertId()
}

// SupersedeClaimsForFile marks every prior unverified, not-yet-superseded
// claim for filePath as resolved by supersedingClaimID, returning the count
// updated. Used by the post-edit hook when a vet=ok run lands on a file that
// previously had vet=fail records, so stop-time get_unverified_claims doesn't
// resurface failures the next edit already fixed. Empty filePath returns
// (0, nil) — supersession only applies when both old and new rows agree on a
// concrete file.
func (s *Store) SupersedeClaimsForFile(filePath string, supersedingClaimID int64) (int, error) {
	if filePath == "" {
		return 0, nil
	}
	res, err := s.db.Exec(`UPDATE claims
		SET superseded_by_claim_id = ?
		WHERE file_path = ?
		  AND verified = 0
		  AND superseded_by_claim_id IS NULL
		  AND id != ?`,
		supersedingClaimID, filePath, supersedingClaimID)
	if err != nil {
		return 0, fmt.Errorf("store: SupersedeClaimsForFile: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: SupersedeClaimsForFile rows: %w", err)
	}
	return int(n), nil
}

// GetUnverifiedClaims returns claims with verified=0 and no supersession
// link, newest first. An empty sessionID returns unverified claims across all
// sessions. Superseded rows are hidden so stop-time output stays focused on
// failures the next edit hasn't already resolved; use GetUnverifiedClaimsAll
// to include the full history.
func (s *Store) GetUnverifiedClaims(sessionID string) ([]Claim, error) {
	return s.queryUnverifiedClaims(sessionID, false)
}

// GetUnverifiedClaimsAll is GetUnverifiedClaims without the supersession
// filter. Useful for forensics ("which fixed-and-forgotten failures were
// recorded?") and for the MCP tool's include-history opt-in.
func (s *Store) GetUnverifiedClaimsAll(sessionID string) ([]Claim, error) {
	return s.queryUnverifiedClaims(sessionID, true)
}

func (s *Store) queryUnverifiedClaims(sessionID string, includeSuperseded bool) ([]Claim, error) {
	const selectCols = `SELECT id, session_id, claim, evidence, verified, recorded_at,
		file_path, superseded_by_claim_id, tool, index_ok, vet_ok, vet_error_summary`
	var (
		clauses = []string{"verified = 0"}
		args    []any
	)
	if !includeSuperseded {
		clauses = append(clauses, "superseded_by_claim_id IS NULL")
	}
	if sessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, sessionID)
	}
	q := selectCols + " FROM claims WHERE " + strings.Join(clauses, " AND ") +
		" ORDER BY recorded_at DESC, id DESC"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: GetUnverifiedClaims: %w", err)
	}
	defer rows.Close()

	var out []Claim
	for rows.Next() {
		var (
			c           Claim
			verified    int
			filePath    sql.NullString
			superseded  sql.NullInt64
			tool        sql.NullString
			indexOK     sql.NullInt64
			vetOK       sql.NullInt64
			vetErrSum   sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.SessionID, &c.Claim, &c.Evidence,
			&verified, &c.RecordedAt, &filePath, &superseded,
			&tool, &indexOK, &vetOK, &vetErrSum); err != nil {
			return nil, fmt.Errorf("store: GetUnverifiedClaims scan: %w", err)
		}
		c.Verified = verified != 0
		if filePath.Valid {
			c.FilePath = filePath.String
		}
		if superseded.Valid {
			v := superseded.Int64
			c.SupersededByClaimID = &v
		}
		if tool.Valid {
			c.Tool = tool.String
		}
		if indexOK.Valid {
			b := indexOK.Int64 != 0
			c.IndexOK = &b
		}
		if vetOK.Valid {
			b := vetOK.Int64 != 0
			c.VetOK = &b
		}
		if vetErrSum.Valid {
			c.VetErrorSummary = vetErrSum.String
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: GetUnverifiedClaims iter: %w", err)
	}
	return out, nil
}

func scanSymbols(rows *sql.Rows) ([]Symbol, error) {
	var out []Symbol
	for rows.Next() {
		var (
			s        Symbol
			sig      sql.NullString
			parent   sql.NullInt64
			exported int
		)
		if err := rows.Scan(&s.ID, &s.FilePath, &s.Name, &s.QualifiedName, &s.Kind,
			&sig, &s.StartLine, &s.EndLine, &exported, &parent); err != nil {
			return nil, fmt.Errorf("store: scan symbol: %w", err)
		}
		if sig.Valid {
			s.Signature = sig.String
		}
		s.Exported = exported != 0
		if parent.Valid {
			v := parent.Int64
			s.ParentID = &v
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate symbols: %w", err)
	}
	return out, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableBool(p *bool) any {
	if p == nil {
		return nil
	}
	if *p {
		return 1
	}
	return 0
}

// escapeLike escapes the SQL LIKE wildcards %, _, and the backslash escape
// character itself so the surrounding query can wrap the result with % and
// match it as a literal substring.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
