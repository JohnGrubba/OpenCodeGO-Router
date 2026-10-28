package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

type Key struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Note          string   `json:"note"`
	Prefix        string   `json:"prefix"`
	Hash          string   `json:"-"`
	Enabled       bool     `json:"enabled"`
	AllowedModels []string `json:"allowed_models"`
	ExpiresAt     int64    `json:"expires_at"`
	CreatedAt     int64    `json:"created_at"`
	LastUsedAt    int64    `json:"last_used_at"`
	Limits        []Limit  `json:"limits"`
}

type Limit struct {
	ID            int64   `json:"id"`
	KeyID         string  `json:"-"`
	Metric        string  `json:"metric"`
	WindowSeconds int64   `json:"window_seconds"`
	Max           float64 `json:"max"`
}

type Session struct {
	Hash      string
	IP        string
	UserAgent string
	CreatedAt int64
	ExpiresAt int64
	LastSeen  int64
}

type Request struct {
	ID         int64   `json:"id"`
	KeyID      string  `json:"key_id"`
	KeyName    string  `json:"key_name"`
	Model      string  `json:"model"`
	Endpoint   string  `json:"endpoint"`
	APIFamily  string  `json:"api_family"`
	SessionID  string  `json:"session_id"`
	IP         string  `json:"ip"`
	UserAgent  string  `json:"user_agent"`
	Error      string  `json:"error"`
	Status     int     `json:"status"`
	Stream     bool    `json:"stream"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	Reasoning  int64   `json:"reasoning_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	Cost       float64 `json:"cost"`
	Allowance  float64 `json:"allowance"`
	ListCost   float64 `json:"list_cost"`
	Estimated  bool    `json:"estimated"`
	DurationMS int64   `json:"duration_ms"`
	CreatedAt  int64   `json:"created_at"`
}

func (r Request) Tokens() int64 {
	return r.Input + r.Output + r.CacheRead + r.CacheWrite
}

type WindowUsage struct {
	Cost     float64 `json:"cost"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
}

type KeyUsage struct {
	H5  WindowUsage `json:"h5"`
	D1  WindowUsage `json:"d1"`
	D7  WindowUsage `json:"d7"`
	D30 WindowUsage `json:"d30"`
}

type Totals struct {
	Cost          float64 `json:"cost"`
	Requests      int64   `json:"requests"`
	Input         int64   `json:"input_tokens"`
	Output        int64   `json:"output_tokens"`
	CacheRead     int64   `json:"cache_read_tokens"`
	CacheWrite    int64   `json:"cache_write_tokens"`
	Errors        int64   `json:"errors"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
}

func (t Totals) Tokens() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

type SeriesPoint struct {
	T        int64   `json:"t"`
	Cost     float64 `json:"cost"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
}

type ModelUsage struct {
	Model    string  `json:"model"`
	Cost     float64 `json:"cost"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
}

type KeyUsageRow struct {
	KeyID    string  `json:"key_id"`
	KeyName  string  `json:"key_name"`
	Cost     float64 `json:"cost"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	LastAt   int64   `json:"last_at"`
}

type Overview struct {
	From   int64         `json:"from"`
	To     int64         `json:"to"`
	Totals Totals        `json:"totals"`
	Series []SeriesPoint `json:"series"`
	Models []ModelUsage  `json:"models"`
	Keys   []KeyUsageRow `json:"keys"`
}

type RequestFilter struct {
	KeyID    string
	Model    string
	Status   string // success|error|""
	BeforeID int64
	From     int64
	To       int64
	Limit    int
}

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS admin (
	id            INTEGER PRIMARY KEY CHECK (id = 1),
	password_hash TEXT NOT NULL,
	created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	ip         TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS api_keys (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	note           TEXT NOT NULL DEFAULT '',
	prefix         TEXT NOT NULL,
	hash           TEXT NOT NULL UNIQUE,
	enabled        INTEGER NOT NULL DEFAULT 1,
	allowed_models TEXT NOT NULL DEFAULT '["*"]',
	expires_at     INTEGER NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL,
	last_used_at   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS key_limits (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	key_id         TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
	metric         TEXT NOT NULL,
	window_seconds INTEGER NOT NULL,
	max_value      REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS requests (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	key_id        TEXT NOT NULL,
	key_name      TEXT NOT NULL DEFAULT '',
	model         TEXT NOT NULL DEFAULT '',
	endpoint      TEXT NOT NULL DEFAULT '',
	api_family    TEXT NOT NULL DEFAULT '',
	session_id    TEXT NOT NULL DEFAULT '',
	ip            TEXT NOT NULL DEFAULT '',
	user_agent    TEXT NOT NULL DEFAULT '',
	error         TEXT NOT NULL DEFAULT '',
	status        INTEGER NOT NULL DEFAULT 0,
	stream        INTEGER NOT NULL DEFAULT 0,
	input_tokens  INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens INTEGER NOT NULL DEFAULT 0,
	cache_write_tokens INTEGER NOT NULL DEFAULT 0,
	cost          REAL NOT NULL DEFAULT 0,
	allowance     REAL NOT NULL DEFAULT 0,
	list_cost     REAL NOT NULL DEFAULT 0,
	estimated     INTEGER NOT NULL DEFAULT 0,
	duration_ms   INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_requests_created ON requests(created_at);
CREATE INDEX IF NOT EXISTS idx_requests_key_created ON requests(key_id, created_at);
CREATE INDEX IF NOT EXISTS idx_requests_model_created ON requests(model, created_at);
CREATE INDEX IF NOT EXISTS idx_requests_key_model_created ON requests(key_id, model, created_at);
CREATE INDEX IF NOT EXISTS idx_requests_created_id ON requests(created_at, id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
`

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=temp_store(MEMORY)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(time.Hour)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrateCostBasis(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate cost basis: %w", err)
	}
	// Best effort: tighten permissions on the database and WAL files.
	_ = os.Chmod(path, 0o600)
	return s, nil
}

// costBasisMigration is a one-time backfill for databases written before the
// router switched from Go's discounted allowance to the list price shown in
// the OpenCode console. Older rows recorded allowance in cost, but already
// stored list_cost alongside it, so the original value can be restored.
const costBasisMigration = "migration_cost_basis_list_v1"

func (s *Store) migrateCostBasis() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO settings(key,value) VALUES(?, '1') ON CONFLICT(key) DO NOTHING`, costBasisMigration)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil // already applied
	}
	if _, err := tx.Exec(`UPDATE requests SET cost = list_cost, estimated = 0
		WHERE list_cost > 0 AND allowance > 0 AND cost <> list_cost`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Health(ctx context.Context) error {
	var n int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&n)
}

// ---- settings ----

func (s *Store) Setting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- admin ----

func (s *Store) AdminHash(ctx context.Context) (string, bool, error) {
	var h string
	err := s.db.QueryRowContext(ctx, "SELECT password_hash FROM admin WHERE id=1").Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return h, err == nil, err
}

func (s *Store) EnsureAdmin(ctx context.Context, hash string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, `INSERT INTO admin(id,password_hash,created_at) VALUES(1,?,?)
		ON CONFLICT(id) DO NOTHING`, hash, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) SetAdminHash(ctx context.Context, hash string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin(id,password_hash,created_at) VALUES(1,?,?)
		ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash`, hash, time.Now().Unix())
	return err
}

// ---- sessions ----

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,ip,user_agent,created_at,expires_at,last_seen)
		VALUES(?,?,?,?,?,?)`, sess.Hash, sess.IP, sess.UserAgent, sess.CreatedAt, sess.ExpiresAt, sess.CreatedAt)
	return err
}

func (s *Store) GetSession(ctx context.Context, hash string, now int64) (Session, error) {
	var sess Session
	err := s.db.QueryRowContext(ctx, `SELECT token_hash,ip,user_agent,created_at,expires_at,last_seen
		FROM sessions WHERE token_hash=? AND expires_at>?`, hash, now).
		Scan(&sess.Hash, &sess.IP, &sess.UserAgent, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sess, err
}

func (s *Store) TouchSession(ctx context.Context, hash string, now int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, "UPDATE sessions SET last_seen=? WHERE token_hash=?", now, hash)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", hash)
	return err
}

func (s *Store) DeleteAllSessions(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions")
	return err
}

func (s *Store) DeleteOtherSessions(ctx context.Context, keepHash string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash<>?", keepHash)
	return err
}

// ClearExpiredSessions deletes sessions whose expiry has already passed.
func (s *Store) ClearExpiredSessions(ctx context.Context, now int64) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<?", now)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---- keys ----

func scanAllowed(s string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

func encodeAllowed(list []string) string {
	if list == nil {
		list = []string{}
	}
	b, _ := json.Marshal(list)
	return string(b)
}

func (s *Store) CreateKey(ctx context.Context, k Key) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys(id,name,note,prefix,hash,enabled,allowed_models,expires_at,created_at,last_used_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, k.ID, k.Name, k.Note, k.Prefix, k.Hash, boolInt(k.Enabled), encodeAllowed(k.AllowedModels), k.ExpiresAt, k.CreatedAt, k.LastUsedAt)
	if err != nil {
		return err
	}
	for _, l := range k.Limits {
		if _, err := tx.ExecContext(ctx, `INSERT INTO key_limits(key_id,metric,window_seconds,max_value) VALUES(?,?,?,?)`,
			k.ID, l.Metric, l.WindowSeconds, l.Max); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpdateKey(ctx context.Context, k Key) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE api_keys SET name=?,note=?,enabled=?,allowed_models=?,expires_at=? WHERE id=?`,
		k.Name, k.Note, boolInt(k.Enabled), encodeAllowed(k.AllowedModels), k.ExpiresAt, k.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM key_limits WHERE key_id=?", k.ID); err != nil {
		return err
	}
	for _, l := range k.Limits {
		if _, err := tx.ExecContext(ctx, `INSERT INTO key_limits(key_id,metric,window_seconds,max_value) VALUES(?,?,?,?)`,
			k.ID, l.Metric, l.WindowSeconds, l.Max); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RotateKey(ctx context.Context, id, prefix, hash string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, "UPDATE api_keys SET prefix=?,hash=? WHERE id=?", prefix, hash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetKeyEnabled(ctx context.Context, id string, enabled bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, "UPDATE api_keys SET enabled=? WHERE id=?", boolInt(enabled), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteKey(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) loadLimits(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) ([]Limit, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,key_id,metric,window_seconds,max_value FROM key_limits WHERE key_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Limit
	for rows.Next() {
		var l Limit
		if err := rows.Scan(&l.ID, &l.KeyID, &l.Metric, &l.WindowSeconds, &l.Max); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) GetKey(ctx context.Context, id string) (Key, error) {
	var k Key
	var enabled int
	var allowed string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,note,prefix,hash,enabled,allowed_models,expires_at,created_at,last_used_at
		FROM api_keys WHERE id=?`, id).
		Scan(&k.ID, &k.Name, &k.Note, &k.Prefix, &k.Hash, &enabled, &allowed, &k.ExpiresAt, &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, ErrNotFound
	}
	if err != nil {
		return Key{}, err
	}
	k.Enabled = enabled == 1
	k.AllowedModels = scanAllowed(allowed)
	k.Limits, err = s.loadLimits(ctx, s.db, id)
	return k, err
}

func (s *Store) GetKeyByHash(ctx context.Context, hash string) (Key, error) {
	var k Key
	var enabled int
	var allowed string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,note,prefix,hash,enabled,allowed_models,expires_at,created_at,last_used_at
		FROM api_keys WHERE hash=?`, hash).
		Scan(&k.ID, &k.Name, &k.Note, &k.Prefix, &k.Hash, &enabled, &allowed, &k.ExpiresAt, &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, ErrNotFound
	}
	if err != nil {
		return Key{}, err
	}
	k.Enabled = enabled == 1
	k.AllowedModels = scanAllowed(allowed)
	k.Limits, err = s.loadLimits(ctx, s.db, k.ID)
	return k, err
}

func (s *Store) ListKeys(ctx context.Context) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,note,prefix,hash,enabled,allowed_models,expires_at,created_at,last_used_at
		FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []Key
	for rows.Next() {
		var k Key
		var enabled int
		var allowed string
		if err := rows.Scan(&k.ID, &k.Name, &k.Note, &k.Prefix, &k.Hash, &enabled, &allowed, &k.ExpiresAt, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		k.Enabled = enabled == 1
		k.AllowedModels = scanAllowed(allowed)
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range keys {
		keys[i].Limits, err = s.loadLimits(ctx, s.db, keys[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func (s *Store) TouchKey(ctx context.Context, id string, at int64) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.db.ExecContext(ctx, "UPDATE api_keys SET last_used_at=? WHERE id=?", at, id)
}

// ---- requests ----

func (s *Store) InsertRequest(ctx context.Context, r Request) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO requests
		(key_id,key_name,model,endpoint,api_family,session_id,ip,user_agent,error,status,stream,
		 input_tokens,output_tokens,reasoning_tokens,cache_read_tokens,cache_write_tokens,
		 cost,allowance,list_cost,estimated,duration_ms,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.KeyID, r.KeyName, r.Model, r.Endpoint, r.APIFamily, r.SessionID, r.IP, r.UserAgent, r.Error,
		r.Status, boolInt(r.Stream), r.Input, r.Output, r.Reasoning, r.CacheRead, r.CacheWrite,
		r.Cost, r.Allowance, r.ListCost, boolInt(r.Estimated), r.DurationMS, r.CreatedAt)
	return err
}

// ClearRequests deletes all request history. Derived usage views become empty;
// the caller is responsible for resetting any in-memory limit tracker.
func (s *Store) ClearRequests(ctx context.Context) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, "DELETE FROM requests")
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) KeyUsageAll(ctx context.Context, now int64) (map[string]KeyUsage, error) {
	cut := map[string]int64{
		"h5":  now - 5*3600,
		"d1":  now - 86400,
		"d7":  now - 7*86400,
		"d30": now - 30*86400,
	}
	args := []any{
		cut["h5"], cut["h5"], cut["h5"],
		cut["d1"], cut["d1"], cut["d1"],
		cut["d7"], cut["d7"], cut["d7"],
		cut["d30"], cut["d30"], cut["d30"],
		cut["d30"],
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key_id,
		COALESCE(SUM(CASE WHEN created_at>=? THEN cost ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN input_tokens+output_tokens+cache_read_tokens+cache_write_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN cost ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN input_tokens+output_tokens+cache_read_tokens+cache_write_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN cost ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN input_tokens+output_tokens+cache_read_tokens+cache_write_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN cost ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN input_tokens+output_tokens+cache_read_tokens+cache_write_tokens ELSE 0 END),0)
		FROM requests WHERE created_at>=? GROUP BY key_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]KeyUsage)
	for rows.Next() {
		var id string
		var u KeyUsage
		if err := rows.Scan(&id,
			&u.H5.Cost, &u.H5.Requests, &u.H5.Tokens,
			&u.D1.Cost, &u.D1.Requests, &u.D1.Tokens,
			&u.D7.Cost, &u.D7.Requests, &u.D7.Tokens,
			&u.D30.Cost, &u.D30.Requests, &u.D30.Tokens); err != nil {
			return nil, err
		}
		out[id] = u
	}
	return out, rows.Err()
}

func (s *Store) Overview(ctx context.Context, from, to, bucket int64) (Overview, error) {
	ov := Overview{From: from, To: to, Series: []SeriesPoint{}, Models: []ModelUsage{}, Keys: []KeyUsageRow{}}
	if bucket < 1 {
		bucket = 3600
	}
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(cost),0), COUNT(*),
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
		COALESCE(SUM(CASE WHEN status>=400 THEN 1 ELSE 0 END),0),
		COALESCE(AVG(duration_ms),0)
		FROM requests WHERE created_at>=? AND created_at<?`, from, to).
		Scan(&ov.Totals.Cost, &ov.Totals.Requests, &ov.Totals.Input, &ov.Totals.Output,
			&ov.Totals.CacheRead, &ov.Totals.CacheWrite, &ov.Totals.Errors, &ov.Totals.AvgDurationMS)
	if err != nil {
		return ov, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT (created_at/?)*? AS b,
		COALESCE(SUM(cost),0), COUNT(*),
		COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens),0)
		FROM requests WHERE created_at>=? AND created_at<? GROUP BY b ORDER BY b`, bucket, bucket, from, to)
	if err != nil {
		return ov, err
	}
	for rows.Next() {
		var p SeriesPoint
		if err := rows.Scan(&p.T, &p.Cost, &p.Requests, &p.Tokens); err != nil {
			rows.Close()
			return ov, err
		}
		ov.Series = append(ov.Series, p)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT model,
		COALESCE(SUM(cost),0), COUNT(*),
		COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens),0)
		FROM requests WHERE created_at>=? AND created_at<? GROUP BY model ORDER BY 2 DESC`, from, to)
	if err != nil {
		return ov, err
	}
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Cost, &m.Requests, &m.Tokens); err != nil {
			rows.Close()
			return ov, err
		}
		ov.Models = append(ov.Models, m)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT r.key_id,
		COALESCE(MAX(k.name), MAX(r.key_name), 'Deleted key'),
		COALESCE(SUM(r.cost),0), COUNT(*),
		COALESCE(SUM(r.input_tokens+r.output_tokens+r.cache_read_tokens+r.cache_write_tokens),0),
		COALESCE(MAX(r.created_at),0)
		FROM requests r LEFT JOIN api_keys k ON k.id=r.key_id
		WHERE r.created_at>=? AND r.created_at<? GROUP BY r.key_id ORDER BY 3 DESC`, from, to)
	if err != nil {
		return ov, err
	}
	for rows.Next() {
		var k KeyUsageRow
		if err := rows.Scan(&k.KeyID, &k.KeyName, &k.Cost, &k.Requests, &k.Tokens, &k.LastAt); err != nil {
			rows.Close()
			return ov, err
		}
		ov.Keys = append(ov.Keys, k)
	}
	rows.Close()
	return ov, nil
}

func (s *Store) PlanUsage(ctx context.Context, now int64) (map[string]KeyUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model,
		COALESCE(SUM(CASE WHEN created_at>=? THEN cost ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN cost ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN created_at>=? THEN cost ELSE 0 END),0)
		FROM requests WHERE created_at>=? GROUP BY model`,
		now-5*3600, now-7*86400, now-30*86400, now-30*86400)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]KeyUsage{}
	for rows.Next() {
		var model string
		var u KeyUsage
		if err := rows.Scan(&model, &u.H5.Cost, &u.D7.Cost, &u.D30.Cost); err != nil {
			return nil, err
		}
		out[model] = u
	}
	return out, rows.Err()
}

func (s *Store) Requests(ctx context.Context, f RequestFilter) ([]Request, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	var where []string
	var args []any
	if f.KeyID != "" {
		where = append(where, "key_id=?")
		args = append(args, f.KeyID)
	}
	if f.Model != "" {
		where = append(where, "model=?")
		args = append(args, f.Model)
	}
	switch f.Status {
	case "success":
		where = append(where, "status>=200 AND status<400")
	case "error":
		where = append(where, "status>=400")
	}
	if f.From > 0 {
		where = append(where, "created_at>=?")
		args = append(args, f.From)
	}
	if f.To > 0 {
		where = append(where, "created_at<?")
		args = append(args, f.To)
	}
	if f.BeforeID > 0 {
		where = append(where, "id<?")
		args = append(args, f.BeforeID)
	}
	q := `SELECT id,key_id,key_name,model,endpoint,api_family,session_id,ip,user_agent,error,status,stream,
		input_tokens,output_tokens,reasoning_tokens,cache_read_tokens,cache_write_tokens,
		cost,allowance,list_cost,estimated,duration_ms,created_at FROM requests`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		var r Request
		var stream, estimated int
		if err := rows.Scan(&r.ID, &r.KeyID, &r.KeyName, &r.Model, &r.Endpoint, &r.APIFamily, &r.SessionID,
			&r.IP, &r.UserAgent, &r.Error, &r.Status, &stream,
			&r.Input, &r.Output, &r.Reasoning, &r.CacheRead, &r.CacheWrite,
			&r.Cost, &r.Allowance, &r.ListCost, &estimated, &r.DurationMS, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Stream = stream == 1
		r.Estimated = estimated == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecentTrackerEvents returns raw usage rows for the in-memory limit tracker.
func (s *Store) RecentTrackerEvents(ctx context.Context, since int64) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key_id,created_at,cost,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens
		FROM requests WHERE created_at>=? ORDER BY created_at`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var r Request
		if err := rows.Scan(&r.KeyID, &r.CreatedAt, &r.Cost, &r.Input, &r.Output, &r.CacheRead, &r.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) MaxLimitWindow(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT MAX(window_seconds) FROM key_limits").Scan(&n)
	if err != nil {
		return 0, err
	}
	if !n.Valid {
		return 0, nil
	}
	return n.Int64, nil
}

func (s *Store) Cleanup(ctx context.Context, now int64, retentionDays int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cut := now - int64(retentionDays)*86400
	if _, err := s.db.ExecContext(ctx, "DELETE FROM requests WHERE created_at<?", cut); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<?", now)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// KeySeries returns bucketed usage for one key.
func (s *Store) KeySeries(ctx context.Context, keyID string, from, to, bucket int64) ([]SeriesPoint, error) {
	if bucket < 1 {
		bucket = 3600
	}
	rows, err := s.db.QueryContext(ctx, `SELECT (created_at/?)*? AS b,
		COALESCE(SUM(cost),0), COUNT(*),
		COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens),0)
		FROM requests WHERE key_id=? AND created_at>=? AND created_at<? GROUP BY b ORDER BY b`,
		bucket, bucket, keyID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SeriesPoint{}
	for rows.Next() {
		var p SeriesPoint
		if err := rows.Scan(&p.T, &p.Cost, &p.Requests, &p.Tokens); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// KeyTotals aggregates one key in a time range.
func (s *Store) KeyTotals(ctx context.Context, keyID string, from, to int64) (Totals, error) {
	var t Totals
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(cost),0), COUNT(*),
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
		COALESCE(SUM(CASE WHEN status>=400 THEN 1 ELSE 0 END),0),
		COALESCE(AVG(duration_ms),0)
		FROM requests WHERE key_id=? AND created_at>=? AND created_at<?`, keyID, from, to).
		Scan(&t.Cost, &t.Requests, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &t.Errors, &t.AvgDurationMS)
	return t, err
}

// ExportRequests returns request rows for CSV export (bounded for safety).
func (s *Store) ExportRequests(ctx context.Context, from, to int64) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,key_id,key_name,model,endpoint,api_family,session_id,ip,user_agent,error,status,stream,
		input_tokens,output_tokens,reasoning_tokens,cache_read_tokens,cache_write_tokens,
		cost,allowance,list_cost,estimated,duration_ms,created_at FROM requests
		WHERE created_at>=? AND created_at<? ORDER BY id DESC LIMIT 100000`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		var r Request
		var stream, estimated int
		if err := rows.Scan(&r.ID, &r.KeyID, &r.KeyName, &r.Model, &r.Endpoint, &r.APIFamily, &r.SessionID,
			&r.IP, &r.UserAgent, &r.Error, &r.Status, &stream,
			&r.Input, &r.Output, &r.Reasoning, &r.CacheRead, &r.CacheWrite,
			&r.Cost, &r.Allowance, &r.ListCost, &estimated, &r.DurationMS, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Stream = stream == 1
		r.Estimated = estimated == 1
		out = append(out, r)
	}
	return out, rows.Err()
}
