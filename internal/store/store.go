// Package store persists bnm's monitoring history (samples, baselines, speed
// results, events) and key/value settings in a single SQLite file, using the
// pure-Go modernc.org/sqlite driver so bnmd stays CGO-free.
//
// The DB is opened in WAL mode with a busy timeout; writes are serialised
// through one mutex so the daemon never sees SQLITE_BUSY. Migrations are a
// numbered list applied inside one transaction and recorded in the
// migrations table.
//
// Tests run against ":memory:" and a temp file; no network, no root.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dopeCape/better-nm/internal/core"
)

// DefaultRetention is how long samples and events are kept by Prune.
const DefaultRetention = 30 * 24 * time.Hour

// DB is the SQLite-backed core.Store.
type DB struct {
	db        *sql.DB
	retention time.Duration
	memory    bool

	mu sync.Mutex // serialises writers
}

var _ core.Store = (*DB)(nil)

// Option tunes Open.
type Option func(*DB)

// WithRetention sets how much sample/event history Prune keeps.
func WithRetention(d time.Duration) Option {
	return func(s *DB) {
		if d > 0 {
			s.retention = d
		}
	}
}

// Open opens (creating if needed) the database at path and applies migrations.
// path == ":memory:" gives a private in-memory database, for tests.
func Open(path string, opts ...Option) (*DB, error) {
	if path == "" {
		return nil, errors.New("store: open: empty path")
	}
	s := &DB{retention: DefaultRetention, memory: path == ":memory:"}
	for _, o := range opts {
		o(s)
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if s.memory {
		// Every pooled connection would otherwise get its own empty database.
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(4)
	}
	db.SetConnMaxLifetime(0)
	s.db = db

	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", path, err)
	}
	return s, nil
}

func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	if path == ":memory:" {
		return "file::memory:?" + q.Encode()
	}
	return "file:" + path + "?" + q.Encode()
}

// Close closes the underlying database.
func (s *DB) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}

// Retention returns the configured retention window.
func (s *DB) Retention() time.Duration { return s.retention }

// ---- migrations -----------------------------------------------------------

var migrations = []string{
	// 1: initial schema
	`CREATE TABLE samples (
		id          INTEGER PRIMARY KEY,
		time        INTEGER NOT NULL,
		network_key TEXT    NOT NULL,
		anchor      TEXT    NOT NULL,
		anchor_addr TEXT    NOT NULL DEFAULT '',
		rtt_ms      REAL    NOT NULL,
		loss        REAL    NOT NULL,
		dns_ms      REAL    NOT NULL DEFAULT -1,
		method      TEXT    NOT NULL DEFAULT ''
	);
	CREATE INDEX samples_key_anchor_time ON samples(network_key, anchor, time);
	CREATE INDEX samples_time ON samples(time);

	CREATE TABLE baselines (
		network_key     TEXT NOT NULL,
		anchor          TEXT NOT NULL,
		state           TEXT NOT NULL,
		sample_count    INTEGER NOT NULL DEFAULT 0,
		baseline_rtt_ms REAL NOT NULL DEFAULT -1,
		baseline_loss   REAL NOT NULL DEFAULT 0,
		current_rtt_ms  REAL NOT NULL DEFAULT -1,
		current_loss    REAL NOT NULL DEFAULT 0,
		current_dns_ms  REAL NOT NULL DEFAULT -1,
		since           INTEGER NOT NULL DEFAULT 0,
		updated_at      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (network_key, anchor)
	);

	CREATE TABLE speed_results (
		id            INTEGER PRIMARY KEY,
		time          INTEGER NOT NULL,
		network_key   TEXT NOT NULL DEFAULT '',
		provider      TEXT NOT NULL,
		server        TEXT NOT NULL DEFAULT '',
		download_mbps REAL NOT NULL DEFAULT 0,
		upload_mbps   REAL NOT NULL DEFAULT 0,
		latency_ms    REAL NOT NULL DEFAULT 0,
		jitter_ms     REAL NOT NULL DEFAULT 0,
		bytes_moved   INTEGER NOT NULL DEFAULT 0,
		duration_ns   INTEGER NOT NULL DEFAULT 0,
		quick         INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX speed_results_key_time ON speed_results(network_key, time);

	CREATE TABLE events (
		id          INTEGER PRIMARY KEY,
		time        INTEGER NOT NULL,
		type        TEXT NOT NULL,
		network_key TEXT NOT NULL DEFAULT '',
		title       TEXT NOT NULL DEFAULT '',
		body        TEXT NOT NULL DEFAULT '',
		urgency     TEXT NOT NULL DEFAULT '',
		data        TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX events_time ON events(time);

	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
}

func (s *DB) migrate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM migrations`).Scan(&current); err != nil {
		return err
	}
	if current > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this build supports (%d)", current, len(migrations))
	}
	for i := current; i < len(migrations); i++ {
		version := i + 1
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO migrations(version, applied_at) VALUES (?, ?)`,
			version, time.Now().UnixNano()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: record: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: commit: %w", version, err)
		}
	}
	return nil
}

// SchemaVersion returns the number of migrations applied.
func (s *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: schema version: %w", err)
	}
	return v, nil
}

// ---- time helpers ---------------------------------------------------------

func toNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// ---- samples --------------------------------------------------------------

// AddSample stores one probe sample.
func (s *DB) AddSample(ctx context.Context, sm core.Sample) error {
	if sm.NetworkKey == "" || sm.Anchor == "" {
		return errors.New("store: add sample: network key and anchor are required")
	}
	if sm.Time.IsZero() {
		sm.Time = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO samples
		(time, network_key, anchor, anchor_addr, rtt_ms, loss, dns_ms, method)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		toNanos(sm.Time), sm.NetworkKey, sm.Anchor, sm.AnchorAddr, sm.RTTms, sm.Loss, sm.DNSms, sm.Method)
	if err != nil {
		return fmt.Errorf("store: add sample %s/%s: %w", sm.NetworkKey, sm.Anchor, err)
	}
	return nil
}

// Samples returns samples for networkKey/anchor (anchor "" = every anchor),
// oldest first, at most limit (0 = all). With a limit the newest rows are kept.
func (s *DB) Samples(ctx context.Context, networkKey, anchor string, limit int) ([]core.Sample, error) {
	var (
		where = `WHERE network_key = ?`
		args  = []any{networkKey}
	)
	if anchor != "" {
		where += ` AND anchor = ?`
		args = append(args, anchor)
	}
	q := `SELECT time, network_key, anchor, anchor_addr, rtt_ms, loss, dns_ms, method FROM samples ` + where
	if limit > 0 {
		q += ` ORDER BY time DESC, id DESC LIMIT ?`
		args = append(args, limit)
	} else {
		q += ` ORDER BY time ASC, id ASC`
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: samples %s/%s: %w", networkKey, anchor, err)
	}
	defer rows.Close()
	var out []core.Sample
	for rows.Next() {
		var sm core.Sample
		var t int64
		if err := rows.Scan(&t, &sm.NetworkKey, &sm.Anchor, &sm.AnchorAddr, &sm.RTTms, &sm.Loss, &sm.DNSms, &sm.Method); err != nil {
			return nil, fmt.Errorf("store: samples scan: %w", err)
		}
		sm.Time = fromNanos(t)
		out = append(out, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: samples: %w", err)
	}
	if limit > 0 {
		reverse(out)
	}
	return out, nil
}

// SampleCount returns the number of stored samples (all keys).
func (s *DB) SampleCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM samples`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: sample count: %w", err)
	}
	return n, nil
}

// ---- baselines ------------------------------------------------------------

// Baselines returns the stored baselines for networkKey, gateway first then by anchor.
func (s *DB) Baselines(ctx context.Context, networkKey string) ([]core.Baseline, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT network_key, anchor, state, sample_count,
		baseline_rtt_ms, baseline_loss, current_rtt_ms, current_loss, current_dns_ms, since, updated_at
		FROM baselines WHERE network_key = ?
		ORDER BY CASE WHEN anchor = 'gateway' THEN 0 ELSE 1 END, anchor`, networkKey)
	if err != nil {
		return nil, fmt.Errorf("store: baselines %s: %w", networkKey, err)
	}
	defer rows.Close()
	var out []core.Baseline
	for rows.Next() {
		var b core.Baseline
		var since, updated int64
		if err := rows.Scan(&b.NetworkKey, &b.Anchor, &b.State, &b.SampleCount,
			&b.BaselineRTT, &b.BaselineLoss, &b.CurrentRTT, &b.CurrentLoss, &b.CurrentDNS, &since, &updated); err != nil {
			return nil, fmt.Errorf("store: baselines scan: %w", err)
		}
		b.Since = fromNanos(since)
		b.UpdatedAt = fromNanos(updated)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: baselines: %w", err)
	}
	return out, nil
}

// PutBaseline inserts or replaces the baseline for (network key, anchor).
func (s *DB) PutBaseline(ctx context.Context, b core.Baseline) error {
	if b.NetworkKey == "" || b.Anchor == "" {
		return errors.New("store: put baseline: network key and anchor are required")
	}
	if b.UpdatedAt.IsZero() {
		b.UpdatedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO baselines
		(network_key, anchor, state, sample_count, baseline_rtt_ms, baseline_loss,
		 current_rtt_ms, current_loss, current_dns_ms, since, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(network_key, anchor) DO UPDATE SET
		 state = excluded.state, sample_count = excluded.sample_count,
		 baseline_rtt_ms = excluded.baseline_rtt_ms, baseline_loss = excluded.baseline_loss,
		 current_rtt_ms = excluded.current_rtt_ms, current_loss = excluded.current_loss,
		 current_dns_ms = excluded.current_dns_ms, since = excluded.since, updated_at = excluded.updated_at`,
		b.NetworkKey, b.Anchor, string(b.State), b.SampleCount, b.BaselineRTT, b.BaselineLoss,
		b.CurrentRTT, b.CurrentLoss, b.CurrentDNS, toNanos(b.Since), toNanos(b.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: put baseline %s/%s: %w", b.NetworkKey, b.Anchor, err)
	}
	return nil
}

// DeleteBaselines drops every baseline for networkKey (the samples stay).
func (s *DB) DeleteBaselines(ctx context.Context, networkKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM baselines WHERE network_key = ?`, networkKey); err != nil {
		return fmt.Errorf("store: delete baselines %s: %w", networkKey, err)
	}
	return nil
}

// ---- speed results --------------------------------------------------------

// AddSpeedResult stores one on-demand speed test.
func (s *DB) AddSpeedResult(ctx context.Context, r core.SpeedResult) error {
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO speed_results
		(time, network_key, provider, server, download_mbps, upload_mbps, latency_ms, jitter_ms,
		 bytes_moved, duration_ns, quick)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		toNanos(r.Time), r.NetworkKey, r.Provider, r.Server, r.DownloadMbps, r.UploadMbps,
		r.LatencyMs, r.JitterMs, r.BytesMoved, int64(r.Duration), boolInt(r.Quick))
	if err != nil {
		return fmt.Errorf("store: add speed result: %w", err)
	}
	return nil
}

// SpeedResults returns speed tests for networkKey ("" = all), oldest first, at most limit (0 = all).
func (s *DB) SpeedResults(ctx context.Context, networkKey string, limit int) ([]core.SpeedResult, error) {
	q := `SELECT time, network_key, provider, server, download_mbps, upload_mbps, latency_ms, jitter_ms,
		bytes_moved, duration_ns, quick FROM speed_results`
	var args []any
	if networkKey != "" {
		q += ` WHERE network_key = ?`
		args = append(args, networkKey)
	}
	if limit > 0 {
		q += ` ORDER BY time DESC, id DESC LIMIT ?`
		args = append(args, limit)
	} else {
		q += ` ORDER BY time ASC, id ASC`
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: speed results: %w", err)
	}
	defer rows.Close()
	var out []core.SpeedResult
	for rows.Next() {
		var r core.SpeedResult
		var t, dur int64
		var quick int
		if err := rows.Scan(&t, &r.NetworkKey, &r.Provider, &r.Server, &r.DownloadMbps, &r.UploadMbps,
			&r.LatencyMs, &r.JitterMs, &r.BytesMoved, &dur, &quick); err != nil {
			return nil, fmt.Errorf("store: speed results scan: %w", err)
		}
		r.Time = fromNanos(t)
		r.Duration = time.Duration(dur)
		r.Quick = quick != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: speed results: %w", err)
	}
	if limit > 0 {
		reverse(out)
	}
	return out, nil
}

// ---- events ---------------------------------------------------------------

// AddEvent stores one emitted event.
func (s *DB) AddEvent(ctx context.Context, e core.Event) error {
	if e.Type == "" {
		return errors.New("store: add event: type is required")
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	data := ""
	if len(e.Data) > 0 {
		b, err := json.Marshal(e.Data)
		if err != nil {
			return fmt.Errorf("store: add event: encode data: %w", err)
		}
		data = string(b)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO events
		(time, type, network_key, title, body, urgency, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		toNanos(e.Time), string(e.Type), e.NetworkKey, e.Title, e.Body, e.Urgency, data)
	if err != nil {
		return fmt.Errorf("store: add event %s: %w", e.Type, err)
	}
	return nil
}

// Events returns stored events, oldest first, at most limit (0 = all); with a limit the newest are kept.
func (s *DB) Events(ctx context.Context, limit int) ([]core.Event, error) {
	q := `SELECT time, type, network_key, title, body, urgency, data FROM events`
	var args []any
	if limit > 0 {
		q += ` ORDER BY time DESC, id DESC LIMIT ?`
		args = append(args, limit)
	} else {
		q += ` ORDER BY time ASC, id ASC`
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: events: %w", err)
	}
	defer rows.Close()
	var out []core.Event
	for rows.Next() {
		var e core.Event
		var t int64
		var data string
		if err := rows.Scan(&t, &e.Type, &e.NetworkKey, &e.Title, &e.Body, &e.Urgency, &data); err != nil {
			return nil, fmt.Errorf("store: events scan: %w", err)
		}
		e.Time = fromNanos(t)
		if data != "" {
			if err := json.Unmarshal([]byte(data), &e.Data); err != nil {
				return nil, fmt.Errorf("store: events: decode data: %w", err)
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: events: %w", err)
	}
	if limit > 0 {
		reverse(out)
	}
	return out, nil
}

// ---- settings -------------------------------------------------------------

// Get reads a setting; ok is false when the key is absent.
func (s *DB) Get(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: get %q: %w", key, err)
	}
	return value, true, nil
}

// Set writes a setting, replacing any previous value.
func (s *DB) Set(ctx context.Context, key, value string) error {
	if key == "" {
		return errors.New("store: set: empty key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: set %q: %w", key, err)
	}
	return nil
}

// Delete removes a setting; missing keys are not an error.
func (s *DB) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete %q: %w", key, err)
	}
	return nil
}

// ---- maintenance ----------------------------------------------------------

// Prune drops samples and events older than the retention window.
func (s *DB) Prune(ctx context.Context) error {
	cutoff := time.Now().Add(-s.retention).UnixNano()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM samples WHERE time < ?`, cutoff); err != nil {
		return fmt.Errorf("store: prune samples: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE time < ?`, cutoff); err != nil {
		return fmt.Errorf("store: prune events: %w", err)
	}
	return nil
}

// ---- helpers --------------------------------------------------------------

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func reverse[T any](xs []T) {
	for i, j := 0, len(xs)-1; i < j; i, j = i+1, j-1 {
		xs[i], xs[j] = xs[j], xs[i]
	}
}
