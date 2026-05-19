package store

import (
	"database/sql"
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
const schemaVersion = 1

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
// that replaces this one (if any).
type Decision struct {
	ID           int64
	Topic        string
	Choice       string
	Reasoning    string
	RecordedAt   int64
	SupersededBy *int64
}

// Claim is a verifiable assertion made during a Claude Code session.
type Claim struct {
	ID         int64
	SessionID  string
	Claim      string
	Evidence   string
	Verified   bool
	RecordedAt int64
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

// RecordDecision inserts a decision and returns its row ID. If RecordedAt is
// zero, the current unix time is used.
func (s *Store) RecordDecision(d Decision) (int64, error) {
	if d.Topic == "" {
		return 0, errors.New("store: RecordDecision: empty topic")
	}
	if d.RecordedAt == 0 {
		d.RecordedAt = time.Now().Unix()
	}
	res, err := s.db.Exec(`INSERT INTO decisions(topic, choice, reasoning, recorded_at, superseded_by)
		VALUES(?, ?, ?, ?, ?)`,
		d.Topic, d.Choice, d.Reasoning, d.RecordedAt, nullableInt64(d.SupersededBy))
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
	q := `SELECT id, topic, choice, reasoning, recorded_at, superseded_by FROM decisions`
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
		var (
			d   Decision
			sup sql.NullInt64
		)
		if err := rows.Scan(&d.ID, &d.Topic, &d.Choice, &d.Reasoning, &d.RecordedAt, &sup); err != nil {
			return nil, fmt.Errorf("store: GetDecisions scan: %w", err)
		}
		if sup.Valid {
			v := sup.Int64
			d.SupersededBy = &v
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: GetDecisions iter: %w", err)
	}
	return out, nil
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
// the current unix time is used.
func (s *Store) RecordClaim(c Claim) (int64, error) {
	if c.SessionID == "" {
		return 0, errors.New("store: RecordClaim: empty session id")
	}
	if c.RecordedAt == 0 {
		c.RecordedAt = time.Now().Unix()
	}
	res, err := s.db.Exec(`INSERT INTO claims(session_id, claim, evidence, verified, recorded_at)
		VALUES(?, ?, ?, ?, ?)`,
		c.SessionID, c.Claim, c.Evidence, boolToInt(c.Verified), c.RecordedAt)
	if err != nil {
		return 0, fmt.Errorf("store: RecordClaim: %w", err)
	}
	return res.LastInsertId()
}

// GetUnverifiedClaims returns claims with verified=0, newest first. An empty
// sessionID returns unverified claims across all sessions.
func (s *Store) GetUnverifiedClaims(sessionID string) ([]Claim, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if sessionID == "" {
		rows, err = s.db.Query(`SELECT id, session_id, claim, evidence, verified, recorded_at
			FROM claims WHERE verified = 0
			ORDER BY recorded_at DESC, id DESC`)
	} else {
		rows, err = s.db.Query(`SELECT id, session_id, claim, evidence, verified, recorded_at
			FROM claims WHERE verified = 0 AND session_id = ?
			ORDER BY recorded_at DESC, id DESC`, sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetUnverifiedClaims: %w", err)
	}
	defer rows.Close()

	var out []Claim
	for rows.Next() {
		var (
			c        Claim
			verified int
		)
		if err := rows.Scan(&c.ID, &c.SessionID, &c.Claim, &c.Evidence, &verified, &c.RecordedAt); err != nil {
			return nil, fmt.Errorf("store: GetUnverifiedClaims scan: %w", err)
		}
		c.Verified = verified != 0
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

// escapeLike escapes the SQL LIKE wildcards %, _, and the backslash escape
// character itself so the surrounding query can wrap the result with % and
// match it as a literal substring.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
