// Package store wraps the SQLite + FTS5 ledger backing store.
//
// Layout: a single "items" table (typed columns we query on) joined
// to a contentless FTS5 virtual table for full-text search. The FTS5
// table is contentless so we don't double-store the body — items
// holds the canonical content, FTS5 keeps only the inverted index
// keyed by rowid. Sync between the two is via triggers.
//
// All public functions are safe to call concurrently. SQLite WAL mode
// is enabled at open time; multi-reader / single-writer semantics
// keep us correct under the research orchestrator's parallel ingest.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver — no CGO, keeps static-binary story.

	"github.com/jedi4ever/socialfetch/internal/ledger/item"
	"github.com/jedi4ever/socialfetch/internal/ledger/urlutil"
)

// Store is the SQLite-backed ledger.
type Store struct {
	db *sql.DB
}

// Open returns a Store backed by path, creating the schema if needed.
// path is typically ~/.local/share/socialfetch-ledger/ledger.db; in
// tests, pass ":memory:" for an ephemeral DB.
func Open(path string) (*Store, error) {
	// _journal=WAL: multi-reader / single-writer concurrency.
	// _busy_timeout=5000: wait up to 5s for the write lock instead of
	// failing immediately under contention.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open ledger db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate creates the schema if it doesn't exist. Idempotent — safe
// to call on every Open. We use IF NOT EXISTS rather than a versioned
// migration table because there's only one schema today; introduce
// versioning when we make our first breaking change.
func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS items (
			key            TEXT PRIMARY KEY,    -- source::canonical_id|url
			source         TEXT NOT NULL,
			url            TEXT NOT NULL,        -- canonical / post-redirect
			request_url    TEXT,                 -- original input when ≠ url
			canonical_id   TEXT,
			title          TEXT,
			author         TEXT,
			summary        TEXT,
			content        TEXT,
			score          INTEGER,
			tags           TEXT,                 -- comma-joined
			published_at   INTEGER,              -- unix seconds
			fetched_at     INTEGER NOT NULL,
			first_seen_at  INTEGER NOT NULL,
			last_seen_at   INTEGER NOT NULL,
			content_hash   TEXT NOT NULL,
			extra          TEXT,                 -- raw json
			mirror_status  TEXT DEFAULT 'pending',
			mirror_path    TEXT,
			mirror_synced_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_items_source ON items(source)`,
		`CREATE INDEX IF NOT EXISTS idx_items_last_seen ON items(last_seen_at)`,
		`CREATE INDEX IF NOT EXISTS idx_items_pending ON items(mirror_status) WHERE mirror_status='pending'`,
		// request_url tracks the URL the user originally asked for
		// when it differs from the canonical URL the fetcher
		// produced (typically a t.co / bit.ly / 301 redirect).
		// Added after the v0 schema landed, so this is a non-
		// breaking ADD COLUMN: the table-creation step CREATE TABLE
		// IF NOT EXISTS won't re-add it on existing dbs, but the
		// idempotent ALTER below will populate older databases on
		// first open. ALTER TABLE … ADD COLUMN succeeds-then-errors
		// on re-run, hence the IGNORE-style sentinel below.
		`CREATE INDEX IF NOT EXISTS idx_items_request_url ON items(request_url) WHERE request_url IS NOT NULL`,
		// Contentless FTS5 — we keep the body in items.content, FTS5
		// only stores the inverted index. Cheap, and reorganizing the
		// FTS index is just DROP + repopulate.
		`CREATE VIRTUAL TABLE IF NOT EXISTS items_fts USING fts5(
			title, summary, content, author, tags,
			content='items', content_rowid='rowid',
			tokenize = 'unicode61 remove_diacritics 2'
		)`,
		// Triggers keep FTS5 in sync with items. INSERT/UPDATE/DELETE
		// each have their own trigger; we use the standard FTS5
		// content-table sync pattern.
		`CREATE TRIGGER IF NOT EXISTS items_ai AFTER INSERT ON items BEGIN
			INSERT INTO items_fts(rowid, title, summary, content, author, tags)
				VALUES (new.rowid, new.title, new.summary, new.content, new.author, new.tags);
		END`,
		`CREATE TRIGGER IF NOT EXISTS items_ad AFTER DELETE ON items BEGIN
			INSERT INTO items_fts(items_fts, rowid, title, summary, content, author, tags)
				VALUES ('delete', old.rowid, old.title, old.summary, old.content, old.author, old.tags);
		END`,
		`CREATE TRIGGER IF NOT EXISTS items_au AFTER UPDATE ON items BEGIN
			INSERT INTO items_fts(items_fts, rowid, title, summary, content, author, tags)
				VALUES ('delete', old.rowid, old.title, old.summary, old.content, old.author, old.tags);
			INSERT INTO items_fts(rowid, title, summary, content, author, tags)
				VALUES (new.rowid, new.title, new.summary, new.content, new.author, new.tags);
		END`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w (stmt: %s)", err, firstLine(q))
		}
	}
	// Best-effort additive migration for databases created before
	// the request_url column existed. SQLite has no IF NOT EXISTS
	// for ALTER TABLE ADD COLUMN, so we run it and swallow the
	// duplicate-column error. New databases (where CREATE TABLE
	// already includes the column) hit the duplicate-column path
	// too — same outcome.
	if _, err := s.db.Exec(`ALTER TABLE items ADD COLUMN request_url TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate: add request_url: %w", err)
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

// IngestResult tells the caller what happened to one item — used by
// stats output and by the filter command's "skip-seen" logic.
type IngestResult int

const (
	// IngestNew means the item didn't exist before this call.
	IngestNew IngestResult = iota
	// IngestUpdated means the item existed but content_hash changed.
	IngestUpdated
	// IngestUnchanged means the item existed with the same hash; only
	// last_seen_at was bumped.
	IngestUnchanged
)

// Ingest upserts one Item. Returns the resulting state (new/updated/
// unchanged) so callers can drive mirror writes only when needed.
// Idempotent: calling it twice with the same Item is cheap and only
// touches last_seen_at on the second call.
func (s *Store) Ingest(it item.Item) (IngestResult, error) {
	// Normalize the URL on the way in so trivial variants (case,
	// fragment, trailing slash) collapse to a single canonical
	// row. Querystring is preserved — HN's ?id=N, GitHub's
	// ?tab=…, and UTM trackers all stay intact (per
	// urlutil.Normalize's contract). Without this, two ingests
	// of the same URL with different surface form would create
	// two rows, defeating the dedup purpose.
	it.URL = urlutil.Normalize(it.URL)
	now := time.Now().Unix()
	hash := it.Hash()
	key := it.Key()
	extraJSON, _ := json.Marshal(it.Extra)
	pubAt := int64(0)
	if it.Published != nil {
		pubAt = it.Published.Unix()
	}

	// Lookup current state in a single query so we know whether to
	// emit Updated vs Unchanged — and to preserve first_seen_at on
	// updates.
	var (
		existing      bool
		existingHash  string
		existingFirst int64
	)
	row := s.db.QueryRow(`SELECT content_hash, first_seen_at FROM items WHERE key = ?`, key)
	if err := row.Scan(&existingHash, &existingFirst); err == nil {
		existing = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("ingest lookup: %w", err)
	}

	firstSeen := now
	if existing {
		firstSeen = existingFirst
	}

	// Normalize request_url too if set, then drop it when it
	// equals the canonical URL — saves a row of redundant data
	// for the no-redirect case.
	requestURL := urlutil.Normalize(it.RequestURL)
	if requestURL == it.URL {
		requestURL = ""
	}

	_, err := s.db.Exec(`
		INSERT INTO items (
			key, source, url, request_url, canonical_id, title, author, summary, content,
			score, tags, published_at, fetched_at, first_seen_at, last_seen_at,
			content_hash, extra, mirror_status
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'pending')
		ON CONFLICT(key) DO UPDATE SET
			url           = excluded.url,
			request_url   = excluded.request_url,
			title         = excluded.title,
			author        = excluded.author,
			summary       = excluded.summary,
			content       = excluded.content,
			score         = excluded.score,
			tags          = excluded.tags,
			published_at  = excluded.published_at,
			fetched_at    = excluded.fetched_at,
			last_seen_at  = excluded.last_seen_at,
			content_hash  = excluded.content_hash,
			extra         = excluded.extra,
			mirror_status = CASE WHEN content_hash = excluded.content_hash
			                     THEN mirror_status ELSE 'pending' END
	`,
		key, it.Source, it.URL, nullIfEmpty(requestURL), it.CanonicalID, it.Title, it.Author,
		it.Summary, it.Content, it.Score, strings.Join(it.Tags, ","),
		pubAt, it.FetchedAt.Unix(), firstSeen, now, hash, string(extraJSON),
	)
	if err != nil {
		return 0, fmt.Errorf("ingest upsert: %w", err)
	}

	switch {
	case !existing:
		return IngestNew, nil
	case existingHash != hash:
		return IngestUpdated, nil
	default:
		return IngestUnchanged, nil
	}
}

// Has returns whether an Item with the given Key already exists. Used
// by the filter --skip-seen command to drop already-known items from
// a JSONL stream before the agent ever sees them.
func (s *Store) Has(key string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT 1 FROM items WHERE key = ? LIMIT 1`, key).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// HasURL checks whether any row's `url` OR `request_url` column
// matches the supplied string. Both columns participate so a
// `seen` lookup against the user-typed shortener URL
// (`https://t.co/abc`) finds the row stored under its canonical
// post-redirect URL (`https://example.com/article`).
//
// Performance: `url` has no index (primary key is `key`), so this
// is a table scan; `request_url` has a partial index from the
// migration above. For a single-user ledger of <100K items the
// scan is <100ms. If it becomes a bottleneck, add
// `CREATE INDEX idx_items_url ON items(url)` in a migration.
//
// The supplied URL is matched literally — callers should call
// urlutil.Normalize first (or use the seen subcommand which
// already does that). Round-tripping the normalization on the
// way in (in Ingest) keeps the stored values canonical so a
// literal match here works for trivial surface variants.
func (s *Store) HasURL(url string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT 1 FROM items WHERE url = ? OR request_url = ? LIMIT 1`,
		url, url,
	).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// nullIfEmpty returns the SQL NULL when s is empty so the
// request_url column stays NULL (rather than empty string) for
// items where there was no redirect. Keeps the partial index
// `idx_items_request_url ... WHERE request_url IS NOT NULL`
// honest — empty strings would still get indexed.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Get returns one Item by Key. Returns (nil, nil) when not found —
// callers branch on the nil pointer rather than a sentinel error
// because "not found" is normal flow control here.
func (s *Store) Get(key string) (*item.Item, error) {
	row := s.db.QueryRow(`
		SELECT source, url, canonical_id, title, author, summary, content,
		       score, tags, published_at, fetched_at, extra
		FROM items WHERE key = ?`, key)
	return scanItem(row)
}

// Search runs an FTS5 query and returns matching items, BM25-ranked.
// q is passed straight to FTS5 — callers can use FTS5 syntax
// (phrase quoting, NEAR/, prefix*, AND/OR/NOT). Empty q returns nil.
func (s *Store) Search(q string, limit int) ([]item.Item, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.db.Query(`
		SELECT i.source, i.url, i.canonical_id, i.title, i.author, i.summary,
		       i.content, i.score, i.tags, i.published_at, i.fetched_at, i.extra
		FROM items_fts f
		JOIN items i ON i.rowid = f.rowid
		WHERE items_fts MATCH ?
		ORDER BY bm25(items_fts)
		LIMIT ?`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// ListOpts narrows what List returns. Zero-value Opts means "all
// items, newest first".
type ListOpts struct {
	Source string    // exact-match source filter, or "" for any
	Since  time.Time // only items with last_seen_at >= Since
	Limit  int       // 0 means default 50
}

// List returns recent items matching opts, ordered by last_seen_at desc.
func (s *Store) List(opts ListOpts) ([]item.Item, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	q := `SELECT source, url, canonical_id, title, author, summary, content,
	             score, tags, published_at, fetched_at, extra
	      FROM items WHERE 1=1`
	args := []any{}
	if opts.Source != "" {
		q += ` AND source = ?`
		args = append(args, opts.Source)
	}
	if !opts.Since.IsZero() {
		q += ` AND last_seen_at >= ?`
		args = append(args, opts.Since.Unix())
	}
	q += ` ORDER BY last_seen_at DESC LIMIT ?`
	args = append(args, opts.Limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

// Forget deletes one item by Key. No-op if the item doesn't exist.
// Returns true when a row was actually deleted, so callers can
// distinguish "we cleaned up the mirror" from "nothing to clean".
func (s *Store) Forget(key string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM items WHERE key = ?`, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Stats summarizes ledger contents — surfaced by `socialfetch-ledger
// stats` so users can spot runaway growth before it eats the disk.
type Stats struct {
	Total       int
	BySource    map[string]int
	Pending     int
	Failed      int
	OldestSeen  time.Time
	NewestSeen  time.Time
	BytesOnDisk int64
}

// Stats counts items and groups them by source. The disk-size
// estimate uses SQLite's page count × page size; the figure excludes
// the WAL/-shm side files but is close enough for "is this a problem
// yet?" decisions.
func (s *Store) Stats() (Stats, error) {
	st := Stats{BySource: map[string]int{}}

	rows, err := s.db.Query(`SELECT source, COUNT(*) FROM items GROUP BY source`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var src string
		var n int
		if err := rows.Scan(&src, &n); err != nil {
			return st, err
		}
		st.BySource[src] = n
		st.Total += n
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE mirror_status='pending'`).Scan(&st.Pending); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE mirror_status='failed'`).Scan(&st.Failed); err != nil {
		return st, err
	}
	if st.Total > 0 {
		var oldest, newest int64
		_ = s.db.QueryRow(`SELECT MIN(first_seen_at), MAX(last_seen_at) FROM items`).Scan(&oldest, &newest)
		st.OldestSeen = time.Unix(oldest, 0)
		st.NewestSeen = time.Unix(newest, 0)
	}
	var pageCount, pageSize int64
	_ = s.db.QueryRow(`PRAGMA page_count`).Scan(&pageCount)
	_ = s.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize)
	st.BytesOnDisk = pageCount * pageSize
	return st, nil
}

// MarkMirrored records that the item has been written to the file
// tree. Called by the mirror layer after a successful write.
func (s *Store) MarkMirrored(key, mirrorPath string) error {
	_, err := s.db.Exec(`UPDATE items SET mirror_status='mirrored', mirror_path=?, mirror_synced_at=? WHERE key=?`,
		mirrorPath, time.Now().Unix(), key)
	return err
}

// PendingMirror returns items whose mirror_status='pending' — used by
// startup recovery and by `mirror sync` to find work to do.
func (s *Store) PendingMirror() ([]item.Item, error) {
	rows, err := s.db.Query(`
		SELECT source, url, canonical_id, title, author, summary, content,
		       score, tags, published_at, fetched_at, extra
		FROM items WHERE mirror_status='pending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

// ----- scanning helpers -----

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(row rowScanner) (*item.Item, error) {
	var (
		it       item.Item
		tags     string
		extraStr string
		pubAt    sql.NullInt64
		fetched  int64
	)
	err := row.Scan(&it.Source, &it.URL, &it.CanonicalID, &it.Title, &it.Author,
		&it.Summary, &it.Content, &it.Score, &tags, &pubAt, &fetched, &extraStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if tags != "" {
		it.Tags = strings.Split(tags, ",")
	}
	if pubAt.Valid && pubAt.Int64 > 0 {
		t := time.Unix(pubAt.Int64, 0).UTC()
		it.Published = &t
	}
	it.FetchedAt = time.Unix(fetched, 0).UTC()
	if extraStr != "" && extraStr != "null" {
		_ = json.Unmarshal([]byte(extraStr), &it.Extra)
	}
	return &it, nil
}

func scanItems(rows *sql.Rows) ([]item.Item, error) {
	var out []item.Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		if it != nil {
			out = append(out, *it)
		}
	}
	return out, rows.Err()
}
