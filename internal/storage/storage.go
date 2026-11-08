// Package storage persists normalized usage records in SQLite.
//
// Only token counts and metadata are stored: no prompts, completions or
// source code ever reach the database.
package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ht2803-max/tokenmanager/internal/model"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS records (
	key            TEXT PRIMARY KEY,
	agent          TEXT NOT NULL,
	provider       TEXT NOT NULL,
	model          TEXT NOT NULL,
	session_id     TEXT NOT NULL,
	project        TEXT NOT NULL,
	ts             INTEGER NOT NULL,
	input          INTEGER NOT NULL,
	output         INTEGER NOT NULL,
	cache_read     INTEGER NOT NULL,
	cache_write_5m INTEGER NOT NULL,
	cache_write_1h INTEGER NOT NULL,
	reasoning      INTEGER NOT NULL,
	context_window INTEGER NOT NULL,
	speed          TEXT NOT NULL,
	accuracy       TEXT NOT NULL,
	reported_cost  REAL NOT NULL,
	source_file    TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS records_ts ON records(ts);
CREATE INDEX IF NOT EXISTS records_session ON records(session_id);
CREATE TABLE IF NOT EXISTS files (
	path   TEXT PRIMARY KEY,
	agent  TEXT NOT NULL,
	size   INTEGER NOT NULL,
	mtime  INTEGER NOT NULL,
	offset INTEGER NOT NULL,
	state  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS kv (
	k TEXT PRIMARY KEY,
	v TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tool_calls (
	id            TEXT PRIMARY KEY,
	record_key    TEXT NOT NULL,
	session_id    TEXT NOT NULL,
	ts            INTEGER NOT NULL,
	agent         TEXT NOT NULL,
	tool          TEXT NOT NULL,
	mcp_server    TEXT NOT NULL,
	input_bytes   INTEGER NOT NULL,
	result_bytes  INTEGER NOT NULL,
	result_tokens INTEGER NOT NULL,
	has_result    INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS tool_calls_ts ON tool_calls(ts);
`

// SchemaVersion is stored in kv as "schema_version". Version 2 added
// tool_calls; databases created earlier have no tool rows for files already
// read past their offsets (the collector backfills them once).
const SchemaVersion = 2

// DB wraps the SQLite handle.
type DB struct{ db *sql.DB }

// Open opens (and migrates) the database at path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	d := &DB{db: db}
	if d.Get("schema_version") != fmt.Sprint(SchemaVersion) {
		if err := d.Set("schema_version", fmt.Sprint(SchemaVersion)); err != nil {
			db.Close()
			return nil, err
		}
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// Insert stores records, ignoring keys that already exist. It returns the
// records that were new.
func (d *DB) Insert(recs []model.Record) ([]model.Record, error) {
	if len(recs) == 0 {
		return nil, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO records VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	var added []model.Record
	for _, r := range recs {
		res, err := stmt.Exec(r.Key, r.Agent, r.Provider, r.Model, r.SessionID, r.Project,
			r.Timestamp.UnixMilli(), r.Input, r.Output, r.CacheRead, r.CacheWrite5m, r.CacheWrite1h,
			r.Reasoning, r.ContextWindow, r.Speed, string(r.Accuracy), r.ReportedCost, r.SourceFile)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added = append(added, r)
		}
	}
	return added, tx.Commit()
}

// UpsertTools merges partial tool rows by call id. A call, its result and a
// late link to its request may arrive in different reads or files (resumed
// sessions copy history), so every column keeps the first non-empty value
// and sizes keep the maximum: re-reading anything is idempotent.
func (d *DB) UpsertTools(calls []model.ToolCall) error {
	if len(calls) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO tool_calls VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			record_key    = CASE WHEN record_key = '' THEN excluded.record_key ELSE record_key END,
			session_id    = CASE WHEN session_id = '' THEN excluded.session_id ELSE session_id END,
			ts            = CASE WHEN excluded.ts <> 0 AND (ts = 0 OR (tool = '' AND excluded.tool <> '')) THEN excluded.ts ELSE ts END,
			agent         = CASE WHEN agent = '' THEN excluded.agent ELSE agent END,
			mcp_server    = CASE WHEN tool = '' THEN excluded.mcp_server ELSE mcp_server END,
			tool          = CASE WHEN tool = '' THEN excluded.tool ELSE tool END,
			input_bytes   = MAX(input_bytes, excluded.input_bytes),
			result_bytes  = MAX(result_bytes, excluded.result_bytes),
			result_tokens = MAX(result_tokens, excluded.result_tokens),
			has_result    = MAX(has_result, excluded.has_result)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range calls {
		var ts int64
		if !c.Timestamp.IsZero() {
			ts = c.Timestamp.UnixMilli()
		}
		if _, err := stmt.Exec(c.ID, c.RecordKey, c.SessionID, ts, c.Agent, c.Tool, c.MCPServer,
			c.InputBytes, c.ResultBytes, c.ResultTokens, c.HasResult); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// QueryTools returns tool calls in the filter's time range. Agent filters
// apply to the call; project and model filters to the request that emitted
// it, so calls whose request is unknown are dropped when those are set.
func (d *DB) QueryTools(f Filter) ([]model.ToolCall, error) {
	where := []string{"t.tool <> ''"}
	var args []any
	if !f.From.IsZero() {
		where, args = append(where, "t.ts >= ?"), append(args, f.From.UnixMilli())
	}
	if !f.To.IsZero() {
		where, args = append(where, "t.ts < ?"), append(args, f.To.UnixMilli())
	}
	if f.Agent != "" {
		where, args = append(where, "t.agent = ?"), append(args, f.Agent)
	}
	if f.Project != "" {
		where, args = append(where, "r.project LIKE ?"), append(args, "%"+f.Project+"%")
	}
	if f.Model != "" {
		where, args = append(where, "r.model LIKE ?"), append(args, "%"+f.Model+"%")
	}
	if f.Session != "" {
		where, args = append(where, "t.session_id = ?"), append(args, f.Session)
	}
	q := `SELECT t.id,t.record_key,t.session_id,t.ts,t.agent,t.tool,t.mcp_server,t.input_bytes,t.result_bytes,t.result_tokens,t.has_result
		FROM tool_calls t LEFT JOIN records r ON r.key = t.record_key WHERE ` + strings.Join(where, " AND ") + ` ORDER BY t.ts`
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ToolCall
	for rows.Next() {
		var c model.ToolCall
		var ts int64
		if err := rows.Scan(&c.ID, &c.RecordKey, &c.SessionID, &ts, &c.Agent, &c.Tool, &c.MCPServer,
			&c.InputBytes, &c.ResultBytes, &c.ResultTokens, &c.HasResult); err != nil {
			return nil, err
		}
		c.Timestamp = time.UnixMilli(ts)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Filter narrows record queries. Zero values match everything.
type Filter struct {
	From, To time.Time
	Agent    string
	Project  string // substring match
	Model    string // substring match
	Session  string
}

// where builds the SQL WHERE clause (with leading " WHERE ") and its args.
func (f Filter) where() (string, []any) {
	var where []string
	var args []any
	if !f.From.IsZero() {
		where, args = append(where, "ts >= ?"), append(args, f.From.UnixMilli())
	}
	if !f.To.IsZero() {
		where, args = append(where, "ts < ?"), append(args, f.To.UnixMilli())
	}
	if f.Agent != "" {
		where, args = append(where, "agent = ?"), append(args, f.Agent)
	}
	if f.Project != "" {
		where, args = append(where, "project LIKE ?"), append(args, "%"+f.Project+"%")
	}
	if f.Model != "" {
		where, args = append(where, "model LIKE ?"), append(args, "%"+f.Model+"%")
	}
	if f.Session != "" {
		where, args = append(where, "session_id = ?"), append(args, f.Session)
	}
	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// BucketMillis is the width of a TokenBuckets bucket (15 minutes), fine
// enough that every UTC offset (including +5:30 and +5:45) falls on a
// bucket edge, so callers can sum buckets into local days exactly.
const BucketMillis = 15 * 60 * 1000

// TokenBuckets sums Record.Total() per BucketMillis bucket. Keys are bucket
// indexes since the Unix epoch (start = key*BucketMillis).
func (d *DB) TokenBuckets(f Filter) (map[int64]int64, error) {
	w, args := f.where()
	q := fmt.Sprintf("SELECT ts/%d, SUM(input+output+cache_read+cache_write_5m+cache_write_1h) FROM records%s GROUP BY 1", BucketMillis, w)
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var b, n int64
		if err := rows.Scan(&b, &n); err != nil {
			return nil, err
		}
		out[b] = n
	}
	return out, rows.Err()
}

// Query returns records matching the filter ordered by time.
func (d *DB) Query(f Filter) ([]model.Record, error) {
	w, args := f.where()
	q := "SELECT key,agent,provider,model,session_id,project,ts,input,output,cache_read,cache_write_5m,cache_write_1h,reasoning,context_window,speed,accuracy,reported_cost,source_file FROM records" + w + " ORDER BY ts"
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Record
	for rows.Next() {
		var r model.Record
		var ts int64
		var acc string
		if err := rows.Scan(&r.Key, &r.Agent, &r.Provider, &r.Model, &r.SessionID, &r.Project, &ts,
			&r.Input, &r.Output, &r.CacheRead, &r.CacheWrite5m, &r.CacheWrite1h, &r.Reasoning,
			&r.ContextWindow, &r.Speed, &acc, &r.ReportedCost, &r.SourceFile); err != nil {
			return nil, err
		}
		r.Timestamp = time.UnixMilli(ts)
		r.Accuracy = model.Accuracy(acc)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FileState is the incremental read position of one source file.
type FileState struct {
	Path   string
	Agent  string
	Size   int64
	MTime  int64
	Offset int64
	State  string // JSON-encoded parser state
}

// GetFile returns the saved state for path (zero value if unseen).
func (d *DB) GetFile(path string) (FileState, error) {
	fs := FileState{Path: path}
	err := d.db.QueryRow(`SELECT agent,size,mtime,offset,state FROM files WHERE path=?`, path).
		Scan(&fs.Agent, &fs.Size, &fs.MTime, &fs.Offset, &fs.State)
	if err == sql.ErrNoRows {
		return fs, nil
	}
	return fs, err
}

// PutFile saves the state for a file.
func (d *DB) PutFile(fs FileState) error {
	_, err := d.db.Exec(`INSERT INTO files(path,agent,size,mtime,offset,state) VALUES(?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET agent=excluded.agent,size=excluded.size,mtime=excluded.mtime,offset=excluded.offset,state=excluded.state`,
		fs.Path, fs.Agent, fs.Size, fs.MTime, fs.Offset, fs.State)
	return err
}

// Get reads a kv value.
func (d *DB) Get(k string) string {
	var v string
	_ = d.db.QueryRow(`SELECT v FROM kv WHERE k=?`, k).Scan(&v)
	return v
}

// Set writes a kv value.
func (d *DB) Set(k, v string) error {
	_, err := d.db.Exec(`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// RateLimits returns the latest stored snapshot for a provider.
func (d *DB) RateLimits(provider string) (model.RateLimits, bool) {
	var rl model.RateLimits
	v := d.Get("ratelimits:" + provider)
	if v == "" || json.Unmarshal([]byte(v), &rl) != nil {
		return rl, false
	}
	return rl, true
}

// SaveRateLimits stores a snapshot unless a newer one is already stored
// (files are not ingested in chronological order).
func (d *DB) SaveRateLimits(rl model.RateLimits) error {
	if old, ok := d.RateLimits(rl.Provider); ok && !rl.Observed.After(old.Observed) {
		return nil
	}
	b, err := json.Marshal(rl)
	if err != nil {
		return err
	}
	return d.Set("ratelimits:"+rl.Provider, string(b))
}

// Prune deletes records older than the cutoff and returns how many.
func (d *DB) Prune(before time.Time) (int64, error) {
	res, err := d.db.Exec(`DELETE FROM records WHERE ts < ?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	if _, err := d.db.Exec(`DELETE FROM tool_calls WHERE ts < ?`, before.UnixMilli()); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Stats summarizes database contents.
type Stats struct {
	Records int64
	Files   int64
	First   time.Time
	Last    time.Time
}

// Stats returns record and file counts.
func (d *DB) Stats() (Stats, error) {
	var s Stats
	var first, last sql.NullInt64
	err := d.db.QueryRow(`SELECT COUNT(*), MIN(ts), MAX(ts) FROM records`).Scan(&s.Records, &first, &last)
	if err != nil {
		return s, err
	}
	if first.Valid {
		s.First, s.Last = time.UnixMilli(first.Int64), time.UnixMilli(last.Int64)
	}
	err = d.db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&s.Files)
	return s, err
}

// Reset deletes all records and file offsets (used by "scan --rebuild").
func (d *DB) Reset() error {
	_, err := d.db.Exec(`DELETE FROM records; DELETE FROM files; DELETE FROM tool_calls;`)
	return err
}
