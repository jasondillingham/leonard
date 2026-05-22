package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

// normalizeClaimPath canonicalizes a file_path destined for the
// claims table the same way the indexer's storeKey does for the
// files table: forward-slash separators + NFC Unicode normalization.
// Bughunt-6 store-eval F6: without this, a claim recorded with a
// path Claude Code emitted in NFD form fails to match the file
// row stored in NFC, breaking SupersedeClaimsForFile.
func normalizeClaimPath(p string) string {
	if p == "" {
		return ""
	}
	return norm.NFC.String(filepath.ToSlash(p))
}

// TruthChange is the optional provenance carried by decision-log
// entries that record a change to project ground-truth (per the
// self-logging amendment to docs/ROADMAP-v1-ground-truth.md). When
// present, the post-edit auto-draft logger (#22) populates this from
// session context; operators can also fill it manually via the v0.9
// truth-history CLI.
//
// All fields are optional except Scope — which must be "domain" or
// "toolkit" to be meaningful. The store accepts other values without
// validation so future scopes can land without a schema migration.
type TruthChange struct {
	// Scope is "domain" (edits inside .leonard/ground-truth/) or
	// "toolkit" (edits to Leonard's own source). Empty string is
	// preserved as-is for round-trip compatibility.
	Scope string `json:"scope,omitempty"`

	// Files lists the truth-source files this entry covers,
	// relative to project root. Used by `leonard truth-history`
	// (#28) to look up the entries that touched a given file.
	Files []string `json:"files,omitempty"`

	// DiffRef identifies the change. Conventions:
	//   "git:<short-sha>"        — git-tracked repo
	//   "hash:<pre>-><post>"     — content hash, non-git
	DiffRef string `json:"diff_ref,omitempty"`

	// MotivatedBy is the free-text rationale drafted from session
	// context (or written by hand). Surfaced verbatim by
	// truth-history / truth-story.
	MotivatedBy string `json:"motivated_by,omitempty"`

	// Supersedes is the prior decision-log entry this one replaces.
	// nil for fresh entries. Distinct from the existing Decision.
	// SupersededBy chain — TruthChange.Supersedes captures the
	// per-truth-file chain, which can differ from the decision-log
	// topic chain.
	Supersedes *int64 `json:"supersedes,omitempty"`

	// Trivial marks an entry that bypassed require-tier enforcement
	// (typo fix, whitespace, etc.). Tracked separately so the
	// truth-history renderer can collapse trivial entries by
	// default. Honored by #26.
	Trivial bool `json:"trivial,omitempty"`

	// TrivialReason is the short justification an operator gave
	// when using `--trivial`. Empty when Trivial is false.
	TrivialReason string `json:"trivial_reason,omitempty"`
}

// schemaVersion is the current schema version applied by migrate. Bump this
// whenever a new migration is appended to migrations below.
const schemaVersion = 8

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

	// TruthChange is the optional provenance block introduced by
	// the v0.6 self-logging amendment (#21). nil for legacy
	// entries and for any decision that isn't a truth change.
	TruthChange *TruthChange
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
	// Bughunt-8 F6 (HIGH) + bughunt-9 F3: wal_autocheckpoint(1000)
	// fires per-connection. Each hook-process opens a fresh
	// connection, so the frame counter resets and the WAL grows
	// unbounded across short-lived processes. Forcing a TRUNCATE
	// checkpoint at Close (not PASSIVE — TRUNCATE actually shrinks
	// the WAL file to zero bytes after checkpoint) keeps WAL
	// bounded regardless of process lifetime. v0.51 used PASSIVE
	// which left the WAL file at its high-water-mark size.
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
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
	// Bughunt-6 perf F6 (PROMOTED to HIGH at bughunt-7): the v0.15
	// per-DeleteFiles checkpoint only fired after big bulk deletes,
	// so long-running processes (leonard-mcp, the post-edit hook
	// driver) accumulated unbounded WAL. The audit measured 4.15 MiB
	// of WAL after 700 sequential post-edit hooks on a single file.
	// `wal_autocheckpoint = 1000` triggers an automatic PASSIVE
	// checkpoint after every 1000 frames written — bounded growth
	// without hurting hot-path latency.
	q.Add("_pragma", "wal_autocheckpoint(1000)")
	return "file:" + path + "?" + q.Encode()
}

// migrations is appended-to as the schema evolves. Index N applies migration
// to version N+1 (so migrations[0] takes a fresh DB to v1).
var migrations = []func(*sql.Tx) error{
	migrateV1,
	migrateV2,
	migrateV3,
	migrateV4,
	migrateV5,
	migrateV6,
	migrateV7,
	migrateV8,
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

// migrateV5 adds an index on the self-referential symbols.parent_id
// column. Without it, the FK CASCADE that fires when a parent symbol
// is deleted does a full-table scan of children — which dominated
// the v0.7.0 BenchmarkDeleteFiles_1k cost (~5.8s/op was attributed
// to WAL fsync in that commit message; the real bottleneck was this
// missing index). Bughunt-3 skip-dirs F1 measured ~180× speedup
// with the index in place: same benchmark drops to ~38ms/op.
func migrateV5(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE INDEX idx_symbols_parent ON symbols(parent_id)`); err != nil {
		return fmt.Errorf("exec idx_symbols_parent: %w", err)
	}
	return nil
}

// migrateV6 adds three indexes the bughunt-4 store-perf lane found
// missing via EXPLAIN QUERY PLAN:
//
//   - idx_files_indexed_at: ListFilesIndexedSince (powers recent_changes)
//     full-scanned the files table sorting by indexed_at DESC.
//   - idx_claims_verified: GetUnverifiedClaims full-scanned the claims
//     table when no session_id was supplied (the common case).
//   - idx_decisions_recorded_at: GetDecisions ORDER BY recorded_at DESC
//     could scan when no topic filter was present.
func migrateV6(tx *sql.Tx) error {
	stmts := []string{
		`CREATE INDEX idx_files_indexed_at ON files(indexed_at)`,
		`CREATE INDEX idx_claims_verified ON claims(verified)`,
		`CREATE INDEX idx_decisions_recorded_at ON decisions(recorded_at)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// migrateV7 is a one-time cleanup of two categories of historical
// claim rows that v0.38.0's semantics no longer treat as claims:
//
//   1. Path-escape rejection rows recorded by the pre-v0.38
//      handleEscapedPath path. Those weren't unverified Claude
//      claims — they were tool-layer rejections that already
//      surfaced via additionalContext at decision time. Recording
//      them as ledger entries polluted Stop's "unverified claims"
//      summary forever (the Stop hook would surface every old
//      `/tmp/bughunt-*/` scratch-write rejection on every session
//      end).
//
//   2. Missing-file rejection rows from handleMissingFile that
//      similarly aren't actionable at Stop time.
//
// Both classes are identified by claim-text patterns the relevant
// handlers used. Idempotent — re-running on a clean DB deletes
// zero rows.
func migrateV7(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		DELETE FROM claims
		WHERE claim LIKE '%path escapes project root%'
		   OR claim LIKE '%index=skipped (file not found)%'
	`); err != nil {
		return fmt.Errorf("exec ledger cleanup: %w", err)
	}
	return nil
}

// migrateV8 adds an optional truth_change JSON column to the
// decisions table for the v0.6 self-logging amendment. Per #21, the
// column carries scope / files / diff_ref / motivated_by /
// supersedes / trivial / trivial_reason for decision-log entries
// that record a change to project ground-truth (.leonard/ground-
// truth/* or Leonard's own source).
//
// The column is nullable so existing decision-log entries continue
// to load without modification. Future entries can opt in by setting
// Decision.TruthChange before calling RecordDecision.
func migrateV8(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE decisions ADD COLUMN truth_change TEXT`); err != nil {
		return fmt.Errorf("alter decisions add truth_change: %w", err)
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

// MaxSymbolQueryRows is the store-layer cap on rows returned by
// FindSymbolsByName / FindSymbolsByQuery. Security review #3 F3
// (HIGH): pre-v0.50 FindSymbolsByName had NO LIMIT, so a name with
// 500k matches materialized every row in Go memory (302 MB RSS in
// the audit) before the MCP-layer 500-cap truncated. Capping at the
// SQL boundary closes the heap-pressure path; the MCP-layer cap is
// still 500 for the wire response.
const MaxSymbolQueryRows = 1000

// FindSymbolsByName returns up to MaxSymbolQueryRows symbols whose
// name column matches exactly, ordered by file path then start line.
func (s *Store) FindSymbolsByName(name string) ([]Symbol, error) {
	rows, err := s.db.Query(`SELECT id, file_path, name, qualified_name, kind,
		signature, start_line, end_line, exported, parent_id
		FROM symbols WHERE name = ?
		ORDER BY file_path, start_line, id
		LIMIT ?`, name, MaxSymbolQueryRows)
	if err != nil {
		return nil, fmt.Errorf("store: FindSymbolsByName: %w", err)
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// FindSymbolsByQuery does a case-sensitive substring search across
// name and qualified_name, capped at limit. limit <= 0 defaults to
// 50; limit > MaxSymbolQueryRows clamps down (security review #3 F3:
// the user-supplied limit was previously passed verbatim, so a
// `find_symbol(limit=10000000)` call could materialize every match).
func (s *Store) FindSymbolsByQuery(q string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > MaxSymbolQueryRows {
		limit = MaxSymbolQueryRows
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
	// Sec-4 F3: bound the row count at the SQL boundary. On a
	// monorepo with 1M+ files the unbounded SELECT returned every
	// row before the MCP-layer cap clamped, consuming hundreds of
	// MB of Go heap. The MCP-layer cap is 1000; pushing it here
	// stops the heap-pressure path. MaxSymbolQueryRows is reused
	// since file counts and symbol counts have the same blast
	// radius in real consumers.
	q += " ORDER BY path LIMIT ?"
	args = append(args, MaxSymbolQueryRows)
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

// DeleteFile removes a single file row and (via FK CASCADE) its symbols.
// Thin wrapper around DeleteFiles — see that method for the contract.
//
// Kept as a separate function to avoid touching every existing caller; new
// code should prefer DeleteFiles when removing more than one row at a time
// (the indexer prune sweep was the v0.7 motivation — see DeleteFiles).
func (s *Store) DeleteFile(path string) error {
	if path == "" {
		return errors.New("store: DeleteFile: empty path")
	}
	_, err := s.DeleteFiles([]string{path})
	return err
}

// DeleteFiles removes a batch of file rows in a single transaction and
// (via FK CASCADE) all their symbols. Returns the count of file rows
// actually deleted — a path that isn't in the store is a silent no-op
// (matches DeleteFile's contract).
//
// Why batched: pruneStaleFiles can produce thousands of rows on a stale
// index (a polluted v0.6.1 reindex picked up ~12k site-packages rows).
// One-DELETE-per-row averaged ~1.3s per row in that case (each statement
// implicitly opens a transaction; the FK cascade then sweeps O(symbols/
// file)). Batched into a single tx with chunked IN clauses, the same
// work runs in milliseconds.
//
// Chunk size is 500 — well under SQLite's default 999-parameter cap.
// An empty slice returns (0, nil) without opening a transaction.
func (s *Store) DeleteFiles(paths []string) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: DeleteFiles: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const chunkSize = 500
	total := 0
	for start := 0; start < len(paths); start += chunkSize {
		end := start + chunkSize
		if end > len(paths) {
			end = len(paths)
		}
		chunk := paths[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, p := range chunk {
			args[i] = p
		}
		// FK CASCADE on the files row sweeps the matching symbols.
		// We don't pre-delete symbols explicitly: a benchmark of
		// the both-DELETEs-vs-cascade-only variants showed no
		// difference at this batch size, so the simpler shape wins.
		res, err := tx.Exec("DELETE FROM files WHERE path IN ("+placeholders+")", args...)
		if err != nil {
			return total, fmt.Errorf("store: DeleteFiles: exec chunk %d-%d: %w", start, end, err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	if err := tx.Commit(); err != nil {
		return total, fmt.Errorf("store: DeleteFiles: commit: %w", err)
	}
	// Bughunt-4 store-perf F7: WAL grows unbounded during bulk
	// wipes. SQLite normally checkpoints automatically every ~1000
	// frames, but a single multi-megabyte transaction can blow past
	// that and leave a huge WAL file behind. After a bulk delete
	// (>= 100 rows), force a PASSIVE checkpoint so the WAL can
	// truncate. Errors here are non-fatal — the data is committed,
	// checkpointing is just housekeeping.
	if total >= 100 {
		_, _ = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
	}
	return total, nil
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
	tc, err := jsonTruthChange(d.TruthChange)
	if err != nil {
		return 0, fmt.Errorf("store: RecordDecision marshal truth_change: %w", err)
	}
	res, err := s.db.Exec(`INSERT INTO decisions(
		topic, choice, reasoning, recorded_at, superseded_by, related_files, related_symbols, truth_change
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		d.Topic, d.Choice, d.Reasoning, d.RecordedAt, nullableInt64(d.SupersededBy),
		relFiles, relSyms, tc)
	if err != nil {
		return 0, fmt.Errorf("store: RecordDecision: %w", err)
	}
	return res.LastInsertId()
}

// jsonTruthChange serializes tc as JSON, or returns nil so the
// caller persists SQL NULL. Mirrors jsonStringArray's nil-on-empty
// contract so the column is NULL for decisions that aren't truth
// changes.
func jsonTruthChange(tc *TruthChange) (any, error) {
	if tc == nil {
		return nil, nil
	}
	b, err := json.Marshal(tc)
	if err != nil {
		return nil, err
	}
	return string(b), nil
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
		related_files, related_symbols, truth_change FROM decisions`
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
		superseded_by, related_files, related_symbols, truth_change FROM decisions
		WHERE related_files IS NOT NULL OR related_symbols IS NOT NULL
		ORDER BY recorded_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: GetStaleDecisions: %w", err)
	}
	defer rows.Close()

	// Bughunt-4 store-perf F4: previously this loop issued one
	// `SELECT 1 FROM files WHERE path=?` per related-file ref and
	// one `SELECT 1 FROM symbols ...` per related-symbol ref. With
	// 200 decisions × ~10 refs = 2000+ round-trips per call.
	//
	// New shape: collect every unique ref across all decisions
	// upfront, run one IN-clause query per category, then membership-
	// check during iteration. O(refs_total) work instead of
	// O(refs_total × decisions).
	var decisions []Decision
	uniqueFiles := map[string]bool{}
	uniqueSyms := map[string]bool{}
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("store: GetStaleDecisions scan: %w", err)
		}
		for _, f := range d.RelatedFiles {
			if f != "" {
				uniqueFiles[f] = true
			}
		}
		for _, n := range d.RelatedSymbols {
			if n != "" {
				uniqueSyms[n] = true
			}
		}
		decisions = append(decisions, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: GetStaleDecisions iter: %w", err)
	}

	existingFiles, err := s.existingFiles(keysOf(uniqueFiles))
	if err != nil {
		return nil, err
	}
	existingSyms, err := s.existingSymbols(keysOf(uniqueSyms))
	if err != nil {
		return nil, err
	}

	var out []StaleDecision
	for _, d := range decisions {
		var missingFiles []string
		for _, f := range d.RelatedFiles {
			if f == "" {
				continue
			}
			if !existingFiles[f] {
				missingFiles = append(missingFiles, f)
			}
		}
		var missingSyms []string
		for _, n := range d.RelatedSymbols {
			if n == "" {
				continue
			}
			if !existingSyms[n] {
				missingSyms = append(missingSyms, n)
			}
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
	return out, nil
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// existingFiles returns the set of paths that exist as rows in the
// files table. Used by GetStaleDecisions to bulk-check related-file
// references. An empty input returns an empty (non-nil) map.
func (s *Store) existingFiles(paths []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(paths) == 0 {
		return out, nil
	}
	const chunkSize = 500
	for start := 0; start < len(paths); start += chunkSize {
		end := start + chunkSize
		if end > len(paths) {
			end = len(paths)
		}
		chunk := paths[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, p := range chunk {
			args[i] = p
		}
		rows, err := s.db.Query("SELECT path FROM files WHERE path IN ("+placeholders+")", args...)
		if err != nil {
			return nil, fmt.Errorf("store: existingFiles: %w", err)
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: existingFiles scan: %w", err)
			}
			out[p] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: existingFiles iter: %w", err)
		}
		rows.Close()
	}
	return out, nil
}

// existingSymbols returns the set of names that exist as either
// symbols.name OR symbols.qualified_name. Lookups are unioned because
// related_symbols refs can be either form. Uses chunked IN clauses;
// matched lookups are added under BOTH name + qualified_name so the
// caller's membership check works regardless of which form the
// related-symbols ref used.
func (s *Store) existingSymbols(names []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(names) == 0 {
		return out, nil
	}
	const chunkSize = 500
	for start := 0; start < len(names); start += chunkSize {
		end := start + chunkSize
		if end > len(names) {
			end = len(names)
		}
		chunk := names[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, 0, 2*len(chunk))
		for _, n := range chunk {
			args = append(args, n)
		}
		// Build a second arg list for the qualified_name half of the OR.
		args2 := make([]any, len(chunk))
		for i, n := range chunk {
			args2[i] = n
		}
		args = append(args, args2...)
		q := "SELECT name, qualified_name FROM symbols WHERE name IN (" + placeholders + ") OR qualified_name IN (" + placeholders + ")"
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, fmt.Errorf("store: existingSymbols: %w", err)
		}
		for rows.Next() {
			var n, qn string
			if err := rows.Scan(&n, &qn); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: existingSymbols scan: %w", err)
			}
			out[n] = true
			out[qn] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: existingSymbols iter: %w", err)
		}
		rows.Close()
	}
	return out, nil
}

// scanDecision factors out the common row scan used by GetDecisions and
// GetStaleDecisions. The row's column list must match exactly:
//   id, topic, choice, reasoning, recorded_at, superseded_by,
//   related_files, related_symbols, truth_change
func scanDecision(rows *sql.Rows) (Decision, error) {
	var (
		d       Decision
		sup     sql.NullInt64
		relF    sql.NullString
		relS    sql.NullString
		tcRaw   sql.NullString
	)
	if err := rows.Scan(&d.ID, &d.Topic, &d.Choice, &d.Reasoning,
		&d.RecordedAt, &sup, &relF, &relS, &tcRaw); err != nil {
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
	if tcRaw.Valid && tcRaw.String != "" {
		var tc TruthChange
		if err := json.Unmarshal([]byte(tcRaw.String), &tc); err != nil {
			return Decision{}, fmt.Errorf("decode truth_change: %w", err)
		}
		d.TruthChange = &tc
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

// GetTruthHistory returns decisions whose TruthChange.Files
// includes filePath. Used by `leonard truth-history` and the
// get_truth_history MCP tool (#28) to answer "why is this rule the
// way it is?"
//
// Returned oldest-first so the slice reads as a story: how the
// rule got to its current shape. limit caps the slice; 0 means
// "use the default" (200).
//
// Implementation: fetch all decisions with a non-null truth_change
// column and filter in Go. Decision tables are typically small
// enough (operators ship hundreds, not millions) that a full scan
// is cheaper than the SQLite JSON-search machinery.
func (s *Store) GetTruthHistory(filePath string, limit int) ([]Decision, error) {
	if filePath == "" {
		return nil, errors.New("store: GetTruthHistory: empty filePath")
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id, topic, choice, reasoning, recorded_at,
		superseded_by, related_files, related_symbols, truth_change FROM decisions
		WHERE truth_change IS NOT NULL
		ORDER BY recorded_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: GetTruthHistory: %w", err)
	}
	defer rows.Close()

	var out []Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("store: GetTruthHistory scan: %w", err)
		}
		if d.TruthChange == nil {
			continue
		}
		if truthChangeMentionsFile(d.TruthChange, filePath) {
			out = append(out, d)
			if len(out) >= limit {
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: GetTruthHistory iter: %w", err)
	}
	return out, nil
}

// truthChangeMentionsFile reports whether the entry's Files slice
// includes filePath. Exact match; future versions may relax to glob
// or path-prefix when the operator workflow demands it.
func truthChangeMentionsFile(tc *TruthChange, filePath string) bool {
	for _, f := range tc.Files {
		if f == filePath {
			return true
		}
	}
	return false
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
	c.FilePath = normalizeClaimPath(c.FilePath)
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
	filePath = normalizeClaimPath(filePath)
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

// SupersedeOutstandingFailures marks every unverified, not-yet-
// superseded claim with vet_ok=0 (an explicit verifier failure) as
// resolved by supersedingClaimID. Used by the post-edit hook when
// a fresh vet=ok run lands and the project as a whole is now
// clean — earlier failure claims, even for files the current edit
// didn't touch, are no longer accurate.
//
// This catches the multi-file fix-cascade case
// SupersedeClaimsForFile misses: a vet failure in file A is
// commonly fixed by edits to dependent files B and C, never
// touching A again. The file-specific supersede never fires for A;
// the failure claim hangs in the ledger forever.
//
// Bughunt-5 verifier F1: the v0.38 implementation used
// `claim LIKE '%=failed%'`, which matched any claim text
// containing the substring (e.g. a user `record_claim` with text
// "user_input=failed to load gracefully" got silently superseded).
// The fix switches to the existing `vet_ok` integer column added
// in migrateV3, which is set only by the post-edit hook based on
// the verifier's actual exit status. Uses idx_claims_vet_ok.
func (s *Store) SupersedeOutstandingFailures(supersedingClaimID int64) (int, error) {
	res, err := s.db.Exec(`UPDATE claims
		SET superseded_by_claim_id = ?
		WHERE verified = 0
		  AND superseded_by_claim_id IS NULL
		  AND vet_ok = 0
		  AND id != ?`,
		supersedingClaimID, supersedingClaimID)
	if err != nil {
		return 0, fmt.Errorf("store: SupersedeOutstandingFailures: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: SupersedeOutstandingFailures rows: %w", err)
	}
	return int(n), nil
}

// ResolveClaim marks a single claim as manually resolved by the
// operator. The implementation marks verified=true and appends a
// "manually resolved" note to the evidence field so the audit
// trail records why this row stopped surfacing in Stop. Used by
// `leonard claims resolve <id>` — a manual escape hatch for stale
// claims that don't fit the auto-supersede pattern (e.g. a
// failure claim the operator inspected and decided is no longer
// actionable).
//
// Returns ErrNoRows when no claim with the given ID exists.
func (s *Store) ResolveClaim(claimID int64, note string) error {
	// Bughunt-5 verifier F6: v0.13's MaxClaimEvidenceBytes cap
	// bound RecordClaim's evidence at 256 KiB, but ResolveClaim
	// appended notes without checking. A pathological `--note`
	// flag could grow a row past the cap. Truncate before append.
	const noteCap = 4 << 10 // 4 KiB note; the existing evidence column already obeys the 256 KiB total cap
	trimmed := strings.TrimSpace(note)
	if len(trimmed) > noteCap {
		trimmed = trimmed[:noteCap] + " …(truncated)"
	}
	suffix := "\n\nmanually resolved by operator"
	if trimmed != "" {
		suffix = "\n\nmanually resolved by operator: " + trimmed
	}
	res, err := s.db.Exec(`UPDATE claims
		SET verified = 1,
		    evidence = COALESCE(evidence, '') || ?
		WHERE id = ?`, suffix, claimID)
	if err != nil {
		return fmt.Errorf("store: ResolveClaim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: ResolveClaim rows: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
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
	// v0.44 (bughunt-5 perf F2): cap the materialized row count at
	// the SQL layer. On a 500k-claim ledger the previous unbounded
	// query took 1.15s to materialize every row before the
	// caller's Go-side slice truncated to 50/200. The MCP-layer
	// cap is at most 200; CLI doesn't paginate; the Stop hook
	// uses at most ~20. 1000 is well above every real consumer.
	const queryRowCap = 1000
	q := selectCols + " FROM claims WHERE " + strings.Join(clauses, " AND ") +
		" ORDER BY recorded_at DESC, id DESC LIMIT ?"
	args = append(args, queryRowCap)
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
