// Package codeindex implements a per-project code graph: symbols, calls,
// imports, and files. The index lives at <projectRoot>/.acorn/index.db
// and is the authoritative source for search_symbols / trace_calls /
// get_snippet / architecture / impact tools the agent calls over WS.
//
// The store is pure-Go (modernc.org/sqlite) so the acorn binary stays
// statically cross-compiled with CGO_ENABLED=0.
package codeindex

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SchemaVersion bumps whenever the on-disk shape changes OR when the
// extractor implementation changes in a way that would produce
// different symbols/calls for the same source. A mismatched db is
// dropped and rebuilt on the next index call rather than migrated —
// the index is always derivable from source.
//
// History:
//   v1 — initial schema, regex extractors for TS/JS/Python.
//   v2 — v0.6.0: tree-sitter for TS/JS/Python, new Rust extractor.
//        Bumped so existing v1 indexes (regex-era symbols) get
//        rebuilt on the next index pass without forcing the user
//        to remember `/index force`.
const SchemaVersion = 2

// Symbol is one identifier extracted from source.
type Symbol struct {
	QName     string // qualified name, e.g. "internal/tools.Executor.Execute"
	Name      string // bare name, e.g. "Execute"
	Kind      string // function | method | class | struct | interface | type | const | var | enum
	File      string // repo-relative posix path
	StartLine int
	EndLine   int
	Signature string // language-specific one-line signature, optional
	Language  string // go | ts | js | py | rs
	Container string // enclosing class/struct/module qname, "" if top-level
	Exported  bool
}

// Call is one resolved or partially-resolved function/method invocation.
// CalleeQName may be a bare name when cross-module resolution failed —
// trace_calls treats unresolved entries as best-effort hints.
type Call struct {
	CallerQName string
	CalleeQName string
	Line        int
}

// SearchQuery filters Search() results. Empty fields are wildcards.
type SearchQuery struct {
	NameLike   string // substring or LIKE pattern; case-insensitive
	Kind       string // exact match if set
	FileLike   string // LIKE pattern over file path
	Language   string // exact match if set
	OnlyExport bool
	Limit      int // 0 → 200 default
}

// Stats summarizes the current index for /architecture and the
// project_memory_summary bootstrap line.
type Stats struct {
	IndexHead    string
	Files        int
	Symbols      int
	ByLanguage   map[string]int
	UpdatedAt    time.Time
}

// Store wraps the sqlite handle. One Store per project root.
type Store struct {
	db   *sql.DB
	path string
	root string
}

// Open opens (or creates) <root>/.acorn/index.db. If the existing file
// has a stale schema version, it is dropped and recreated so the agent
// never sees half-migrated data.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("codeindex: root is required")
	}
	dir := filepath.Join(root, ".acorn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("codeindex: mkdir .acorn: %w", err)
	}
	path := filepath.Join(dir, "index.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("codeindex: open db: %w", err)
	}
	// Allow multiple connections so reads (FileMTime, GetSymbol,
	// CallersOf, etc.) don't deadlock against an open write tx —
	// SQLite WAL mode supports one writer + many concurrent readers.
	// MaxOpenConns=1 was the original setting and caused a hard
	// deadlock the first time a non-force /index ran: BeginIndex held
	// the only conn, FileMTime tried to grab another, blocked forever.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	s := &Store{db: db, path: path, root: root}
	if err := s.ensureSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Path returns the absolute path of the index database.
func (s *Store) Path() string { return s.path }

func (s *Store) ensureSchema() error {
	var version int
	err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version)
	if err != nil {
		return fmt.Errorf("codeindex: read user_version: %w", err)
	}
	if version == SchemaVersion {
		return nil
	}
	// Drop any partial state from older versions and recreate.
	if version != 0 {
		for _, t := range []string{"calls", "symbols", "imports", "files", "meta"} {
			_, _ = s.db.Exec("DROP TABLE IF EXISTS " + t)
		}
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS files (
			path           TEXT PRIMARY KEY,
			language       TEXT NOT NULL,
			mtime_unix     INTEGER NOT NULL,
			content_hash   TEXT,
			symbols_count  INTEGER NOT NULL DEFAULT 0,
			indexed_at     INTEGER NOT NULL,
			dirty          INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS symbols (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			qname        TEXT NOT NULL UNIQUE,
			name         TEXT NOT NULL,
			kind         TEXT NOT NULL,
			file         TEXT NOT NULL,
			start_line   INTEGER NOT NULL,
			end_line     INTEGER NOT NULL,
			signature    TEXT NOT NULL DEFAULT '',
			language     TEXT NOT NULL,
			container    TEXT NOT NULL DEFAULT '',
			exported     INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(file) REFERENCES files(path) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_symbols_name     ON symbols(name)`,
		`CREATE INDEX IF NOT EXISTS idx_symbols_file     ON symbols(file)`,
		`CREATE INDEX IF NOT EXISTS idx_symbols_kind     ON symbols(kind)`,
		`CREATE INDEX IF NOT EXISTS idx_symbols_lang     ON symbols(language)`,
		`CREATE TABLE IF NOT EXISTS calls (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			caller_qname  TEXT NOT NULL,
			callee_qname  TEXT NOT NULL,
			callee_name   TEXT NOT NULL,
			file          TEXT NOT NULL,
			line          INTEGER NOT NULL,
			FOREIGN KEY(file) REFERENCES files(path) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_calls_caller ON calls(caller_qname)`,
		`CREATE INDEX IF NOT EXISTS idx_calls_callee ON calls(callee_qname)`,
		`CREATE INDEX IF NOT EXISTS idx_calls_name   ON calls(callee_name)`,
		`CREATE TABLE IF NOT EXISTS imports (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			file         TEXT NOT NULL,
			target       TEXT NOT NULL,
			alias        TEXT NOT NULL DEFAULT '',
			line         INTEGER NOT NULL,
			FOREIGN KEY(file) REFERENCES files(path) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_imports_file   ON imports(file)`,
		`CREATE INDEX IF NOT EXISTS idx_imports_target ON imports(target)`,
		`CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion),
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("codeindex: begin schema tx: %w", err)
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			tx.Rollback()
			return fmt.Errorf("codeindex: schema stmt %q: %w", truncate(q, 60), err)
		}
	}
	return tx.Commit()
}

// IndexTx is a writable batch used by walkers/extractors. Per-file
// updates are atomic: DeleteFile clears prior symbols/calls/imports for
// that path so re-indexing a single file produces a clean state.
type IndexTx struct {
	store *Store
	tx    *sql.Tx
	now   int64
}

// BeginIndex opens a write transaction. Caller must Commit or Rollback.
func (s *Store) BeginIndex() (*IndexTx, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("codeindex: begin index tx: %w", err)
	}
	return &IndexTx{store: s, tx: tx, now: time.Now().Unix()}, nil
}

func (it *IndexTx) Commit() error   { return it.tx.Commit() }
func (it *IndexTx) Rollback() error { return it.tx.Rollback() }

// DeleteFile removes a file's row plus all symbols/calls/imports with
// that file, via the ON DELETE CASCADE on each child table. Called
// before re-extracting a file so old symbols don't linger.
func (it *IndexTx) DeleteFile(path string) error {
	_, err := it.tx.Exec(`DELETE FROM files WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("codeindex: delete file %s: %w", path, err)
	}
	return nil
}

// UpsertFile records (or refreshes) the file row. Symbols are inserted
// separately via UpsertSymbol after this row exists (FK requirement).
func (it *IndexTx) UpsertFile(path, language string, mtimeUnix int64, contentHash string, symbolsCount int) error {
	_, err := it.tx.Exec(`
		INSERT INTO files(path, language, mtime_unix, content_hash, symbols_count, indexed_at, dirty)
		VALUES(?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(path) DO UPDATE SET
			language = excluded.language,
			mtime_unix = excluded.mtime_unix,
			content_hash = excluded.content_hash,
			symbols_count = excluded.symbols_count,
			indexed_at = excluded.indexed_at,
			dirty = 0
	`, path, language, mtimeUnix, contentHash, symbolsCount, it.now)
	if err != nil {
		return fmt.Errorf("codeindex: upsert file %s: %w", path, err)
	}
	return nil
}

// UpsertSymbol writes a symbol row. qname is the unique key; conflicts
// (same qname appearing in two files — rare, e.g. build-tag duplicates)
// resolve to last-write-wins.
func (it *IndexTx) UpsertSymbol(s Symbol) error {
	if s.QName == "" || s.Name == "" || s.File == "" {
		return fmt.Errorf("codeindex: symbol missing required field: %+v", s)
	}
	exported := 0
	if s.Exported {
		exported = 1
	}
	_, err := it.tx.Exec(`
		INSERT INTO symbols(qname, name, kind, file, start_line, end_line, signature, language, container, exported)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(qname) DO UPDATE SET
			name = excluded.name,
			kind = excluded.kind,
			file = excluded.file,
			start_line = excluded.start_line,
			end_line = excluded.end_line,
			signature = excluded.signature,
			language = excluded.language,
			container = excluded.container,
			exported = excluded.exported
	`, s.QName, s.Name, s.Kind, s.File, s.StartLine, s.EndLine, s.Signature, s.Language, s.Container, exported)
	if err != nil {
		return fmt.Errorf("codeindex: upsert symbol %s: %w", s.QName, err)
	}
	return nil
}

// AddCall inserts a CALLS edge. callee_qname may be unresolved (== bare
// name) when cross-module lookup failed; AddCall stores both forms so
// trace_calls can match either way.
func (it *IndexTx) AddCall(c Call, calleeName, file string) error {
	_, err := it.tx.Exec(
		`INSERT INTO calls(caller_qname, callee_qname, callee_name, file, line) VALUES(?, ?, ?, ?, ?)`,
		c.CallerQName, c.CalleeQName, calleeName, file, c.Line,
	)
	if err != nil {
		return fmt.Errorf("codeindex: add call %s -> %s: %w", c.CallerQName, c.CalleeQName, err)
	}
	return nil
}

// AddImport inserts an import row.
func (it *IndexTx) AddImport(file, target, alias string, line int) error {
	_, err := it.tx.Exec(
		`INSERT INTO imports(file, target, alias, line) VALUES(?, ?, ?, ?)`,
		file, target, alias, line,
	)
	if err != nil {
		return fmt.Errorf("codeindex: add import %s -> %s: %w", file, target, err)
	}
	return nil
}

// Search returns symbols matching q, ordered by name. Limit defaults to
// 200 when q.Limit == 0.
func (s *Store) Search(q SearchQuery) ([]Symbol, error) {
	if q.Limit <= 0 {
		q.Limit = 200
	}
	clauses := []string{}
	args := []any{}
	if q.NameLike != "" {
		clauses = append(clauses, "name LIKE ? COLLATE NOCASE")
		args = append(args, "%"+q.NameLike+"%")
	}
	if q.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, q.Kind)
	}
	if q.FileLike != "" {
		clauses = append(clauses, "file LIKE ?")
		args = append(args, q.FileLike)
	}
	if q.Language != "" {
		clauses = append(clauses, "language = ?")
		args = append(args, q.Language)
	}
	if q.OnlyExport {
		clauses = append(clauses, "exported = 1")
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, q.Limit)
	rows, err := s.db.Query(`
		SELECT qname, name, kind, file, start_line, end_line, signature, language, container, exported
		FROM symbols `+where+`
		ORDER BY name COLLATE NOCASE, file
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("codeindex: search: %w", err)
	}
	defer rows.Close()
	var out []Symbol
	for rows.Next() {
		var sym Symbol
		var exp int
		if err := rows.Scan(&sym.QName, &sym.Name, &sym.Kind, &sym.File, &sym.StartLine, &sym.EndLine, &sym.Signature, &sym.Language, &sym.Container, &exp); err != nil {
			return nil, err
		}
		sym.Exported = exp != 0
		out = append(out, sym)
	}
	return out, rows.Err()
}

// GetSymbol fetches one symbol by qualified name. Returns (nil, nil)
// when the symbol is absent — callers should treat that as a cache miss
// and fall back to a name-LIKE search.
func (s *Store) GetSymbol(qname string) (*Symbol, error) {
	row := s.db.QueryRow(`
		SELECT qname, name, kind, file, start_line, end_line, signature, language, container, exported
		FROM symbols WHERE qname = ?
	`, qname)
	var sym Symbol
	var exp int
	err := row.Scan(&sym.QName, &sym.Name, &sym.Kind, &sym.File, &sym.StartLine, &sym.EndLine, &sym.Signature, &sym.Language, &sym.Container, &exp)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("codeindex: get symbol %s: %w", qname, err)
	}
	sym.Exported = exp != 0
	return &sym, nil
}

// CallersOf returns every (caller -> target) edge whose callee resolves
// to qname OR matches the bare name when qname is "". Used by
// trace_calls direction=callers. Empty qname is treated as "match by
// name only" — passing an empty string used to match all rows where
// callee_qname is the empty string (regex-extracted JS), which was a
// query-time bug we now guard against.
func (s *Store) CallersOf(qname, name string) ([]Call, error) {
	var (
		query string
		args  []any
	)
	switch {
	case qname != "" && name != "":
		query = `SELECT caller_qname, callee_qname, line FROM calls
		         WHERE callee_qname = ? OR callee_name = ?
		         ORDER BY caller_qname`
		args = []any{qname, name}
	case qname != "":
		query = `SELECT caller_qname, callee_qname, line FROM calls
		         WHERE callee_qname = ?
		         ORDER BY caller_qname`
		args = []any{qname}
	case name != "":
		query = `SELECT caller_qname, callee_qname, line FROM calls
		         WHERE callee_name = ?
		         ORDER BY caller_qname`
		args = []any{name}
	default:
		return nil, nil
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("codeindex: callers: %w", err)
	}
	defer rows.Close()
	var out []Call
	for rows.Next() {
		var c Call
		if err := rows.Scan(&c.CallerQName, &c.CalleeQName, &c.Line); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CalleesOf returns every (caller -> target) edge originating from qname.
func (s *Store) CalleesOf(qname string) ([]Call, error) {
	rows, err := s.db.Query(`
		SELECT caller_qname, callee_qname, line FROM calls
		WHERE caller_qname = ?
		ORDER BY callee_qname
	`, qname)
	if err != nil {
		return nil, fmt.Errorf("codeindex: callees: %w", err)
	}
	defer rows.Close()
	var out []Call
	for rows.Next() {
		var c Call
		if err := rows.Scan(&c.CallerQName, &c.CalleeQName, &c.Line); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkDirty flags a file so the next index pass reprocesses it. Called
// from internal/tools/fileops.go after every successful write_file or
// edit_file.
func (s *Store) MarkDirty(path string) error {
	_, err := s.db.Exec(`UPDATE files SET dirty = 1 WHERE path = ?`, path)
	return err
}

// DirtyFiles returns the set of paths flagged via MarkDirty.
func (s *Store) DirtyFiles() ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM files WHERE dirty = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FileMTime returns the last-known mtime for path or (0, false) if the
// file is unknown. Used by walkers to skip unchanged files on rescan.
func (s *Store) FileMTime(path string) (int64, bool, error) {
	row := s.db.QueryRow(`SELECT mtime_unix FROM files WHERE path = ?`, path)
	var mtime int64
	err := row.Scan(&mtime)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return mtime, true, nil
}

// FileFreshness returns (mtime, dirty, ok) for the file. Used by the
// indexer's mtime-skip path so files explicitly marked dirty by
// fileops post-write hooks are re-parsed regardless of mtime: a
// filesystem with second-resolution mtime can let a same-second
// edit slip past the mtime check, and the dirty flag is the
// authoritative "this changed since last index" signal.
func (s *Store) FileFreshness(path string) (mtime int64, dirty bool, ok bool, err error) {
	row := s.db.QueryRow(`SELECT mtime_unix, dirty FROM files WHERE path = ?`, path)
	var d int
	scanErr := row.Scan(&mtime, &d)
	if scanErr == sql.ErrNoRows {
		return 0, false, false, nil
	}
	if scanErr != nil {
		return 0, false, false, scanErr
	}
	return mtime, d != 0, true, nil
}

// SetMeta writes a key/value pair into the meta table. Used for
// IndexHead (current git sha) and other small bookkeeping.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// GetMeta reads a meta key, or "" if absent.
func (s *Store) GetMeta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// IndexHead returns the git sha recorded at the last successful index.
func (s *Store) IndexHead() (string, error) { return s.GetMeta("index_head") }

// SetIndexHead records the current git sha after a successful index.
func (s *Store) SetIndexHead(sha string) error { return s.SetMeta("index_head", sha) }

// Stats returns a summary used by the architecture tool and the
// project_memory_summary bootstrap line.
func (s *Store) Stats() (Stats, error) {
	st := Stats{ByLanguage: map[string]int{}, UpdatedAt: time.Now()}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&st.Files); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM symbols`).Scan(&st.Symbols); err != nil {
		return st, err
	}
	rows, err := s.db.Query(`SELECT language, COUNT(*) FROM files GROUP BY language`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var lang string
		var n int
		if err := rows.Scan(&lang, &n); err != nil {
			return st, err
		}
		st.ByLanguage[lang] = n
	}
	st.IndexHead, _ = s.IndexHead()
	return st, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
