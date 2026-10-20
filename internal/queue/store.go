// Package queue persists scan results and the work queue. It is deliberately
// small: one SQLite file, no external service, and a single writer.
package queue

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

type State string

const (
	Pending  State = "pending"
	Running  State = "running"
	Done     State = "done"
	Failed   State = "failed"
	Skipped  State = "skipped"
	Withheld State = "withheld"
)

func (j Job) StateName() string { return string(j.State) }

type Job struct {
	ID         int64
	Path       string
	Library    string
	State      State
	Action     string
	Codec      string
	Reason     string
	SizeBefore int64
	SizeAfter  int64
	Attempts   int
	Error      string
	Backup     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

type File struct {
	Path      string
	Library   string
	Size      int64
	MTime     time.Time
	Action    string
	Codes     string
	Reason    string
	Codec     string
	CheckedAt time.Time
}

type Counts struct {
	Pending  int
	Running  int
	Done     int
	Failed   int
	Skipped  int
	Withheld int
	Saved    int64
}

type Summary struct {
	Files   int
	Actions map[string]int
	Bytes   int64
}

type Store struct {
	db *sql.DB
}

// Open creates the database and its parent directory if needed.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One process owns the file. A single connection avoids SQLITE_BUSY between
	// the scanner, the worker and the status page.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS files (
			path TEXT PRIMARY KEY,
			library TEXT NOT NULL,
			size INTEGER NOT NULL,
			mtime INTEGER NOT NULL,
			action TEXT NOT NULL,
			codes TEXT NOT NULL,
			reason TEXT NOT NULL,
			codec TEXT NOT NULL,
			checked_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			path TEXT NOT NULL,
			library TEXT NOT NULL,
			state TEXT NOT NULL,
			action TEXT NOT NULL,
			codec TEXT NOT NULL,
			reason TEXT NOT NULL,
			size_before INTEGER NOT NULL DEFAULT 0,
			size_after INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			backup TEXT NOT NULL DEFAULT '',
			next_attempt_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			started_at INTEGER NOT NULL DEFAULT 0,
			finished_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_path ON jobs(path) WHERE state IN ('pending','running')`,
		`CREATE INDEX IF NOT EXISTS jobs_state ON jobs(state, id)`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func (s *Store) File(path string) (File, bool, error) {
	row := s.db.QueryRow(`SELECT path, library, size, mtime, action, codes, reason, codec, checked_at FROM files WHERE path = ?`, path)
	var f File
	var mtime, checked int64
	err := row.Scan(&f.Path, &f.Library, &f.Size, &mtime, &f.Action, &f.Codes, &f.Reason, &f.Codec, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, err
	}
	f.MTime = time.Unix(mtime, 0)
	f.CheckedAt = time.Unix(checked, 0)
	return f, true, nil
}

func (s *Store) PutFile(f File) error {
	_, err := s.db.Exec(`INSERT INTO files (path, library, size, mtime, action, codes, reason, codec, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET library=excluded.library, size=excluded.size, mtime=excluded.mtime,
			action=excluded.action, codes=excluded.codes, reason=excluded.reason, codec=excluded.codec,
			checked_at=excluded.checked_at`,
		f.Path, f.Library, f.Size, f.MTime.Unix(), f.Action, f.Codes, f.Reason, f.Codec, f.CheckedAt.Unix())
	if err != nil {
		return fmt.Errorf("record file: %w", err)
	}
	return nil
}

// Enqueue returns false when an active job for the path already exists.
func (s *Store) Enqueue(j Job) (bool, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO jobs (path, library, state, action, codec, reason, size_before, created_at, updated_at)
		SELECT ?, ?, 'pending', ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE path = ? AND state IN ('pending','running'))`,
		j.Path, j.Library, j.Action, j.Codec, j.Reason, j.SizeBefore, now, now, j.Path)
	if err != nil {
		return false, fmt.Errorf("enqueue: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) Claim(now time.Time) (*Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var j Job
	var created, updated, started, finished int64
	err = tx.QueryRow(`SELECT id, path, library, state, action, codec, reason, size_before, size_after,
		attempts, error, backup, created_at, updated_at, started_at, finished_at
		FROM jobs WHERE state = 'pending' AND next_attempt_at <= ? ORDER BY id LIMIT 1`, now.Unix()).
		Scan(&j.ID, &j.Path, &j.Library, &j.State, &j.Action, &j.Codec, &j.Reason, &j.SizeBefore, &j.SizeAfter,
			&j.Attempts, &j.Error, &j.Backup, &created, &updated, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Attempts++
	if _, err := tx.Exec(`UPDATE jobs SET state='running', attempts=?, started_at=?, updated_at=? WHERE id=?`,
		j.Attempts, now.Unix(), now.Unix(), j.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	j.State = Running
	j.StartedAt = now
	j.UpdatedAt = now
	j.CreatedAt = time.Unix(created, 0)
	j.FinishedAt = time.Unix(finished, 0)
	return &j, nil
}

func (s *Store) Complete(id int64, state State, sizeAfter int64, backup string) error {
	now := time.Now()
	_, err := s.db.Exec(`UPDATE jobs SET state=?, size_after=?, backup=?, error='', updated_at=?, finished_at=? WHERE id=?`,
		state, sizeAfter, backup, now.Unix(), now.Unix(), id)
	return err
}

// Fail requeues a job until maxAttempts, then marks it failed.
func (s *Store) Fail(id int64, attempts, maxAttempts int, message string, retryDelay time.Duration) error {
	now := time.Now()
	state := Failed
	next := int64(0)
	if attempts < maxAttempts {
		state = Pending
		next = now.Add(retryDelay).Unix()
	}
	_, err := s.db.Exec(`UPDATE jobs SET state=?, error=?, next_attempt_at=?, updated_at=? WHERE id=?`,
		state, message, next, now.Unix(), id)
	return err
}

func (s *Store) Counts() (Counts, error) {
	var c Counts
	rows, err := s.db.Query(`SELECT state, COUNT(*) FROM jobs GROUP BY state`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return c, err
		}
		switch State(state) {
		case Pending:
			c.Pending = n
		case Running:
			c.Running = n
		case Done:
			c.Done = n
		case Failed:
			c.Failed = n
		case Skipped:
			c.Skipped = n
		case Withheld:
			c.Withheld = n
		}
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	// The store has a single connection; release it before the next query.
	if err := rows.Close(); err != nil {
		return c, err
	}
	row := s.db.QueryRow(`SELECT COALESCE(SUM(size_before - size_after), 0) FROM jobs WHERE state = 'done'`)
	if err := row.Scan(&c.Saved); err != nil {
		return c, err
	}
	return c, nil
}

func (s *Store) FilesSummary() (map[string]Summary, error) {
	rows, err := s.db.Query(`SELECT library, action, COUNT(*), COALESCE(SUM(size),0) FROM files GROUP BY library, action`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Summary{}
	for rows.Next() {
		var library, action string
		var n int
		var bytes int64
		if err := rows.Scan(&library, &action, &n, &bytes); err != nil {
			return nil, err
		}
		sum := out[library]
		if sum.Actions == nil {
			sum.Actions = map[string]int{}
		}
		sum.Files += n
		sum.Bytes += bytes
		sum.Actions[action] += n
		out[library] = sum
	}
	return out, rows.Err()
}

func (s *Store) Recent(limit int) ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, path, library, state, action, codec, reason, size_before, size_after,
		attempts, error, backup, created_at, updated_at, started_at, finished_at
		FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Store) Current() (*Job, error) {
	row := s.db.QueryRow(`SELECT id, path, library, state, action, codec, reason, size_before, size_after,
		attempts, error, backup, created_at, updated_at, started_at, finished_at
		FROM jobs WHERE state='running' ORDER BY id LIMIT 1`)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

type scanner interface {
	Scan(...any) error
}

func scanJob(row scanner) (Job, error) {
	var j Job
	var created, updated, started, finished int64
	err := row.Scan(&j.ID, &j.Path, &j.Library, &j.State, &j.Action, &j.Codec, &j.Reason, &j.SizeBefore, &j.SizeAfter,
		&j.Attempts, &j.Error, &j.Backup, &created, &updated, &started, &finished)
	if err != nil {
		return j, err
	}
	j.CreatedAt = time.Unix(created, 0)
	j.UpdatedAt = time.Unix(updated, 0)
	if started > 0 {
		j.StartedAt = time.Unix(started, 0)
	}
	if finished > 0 {
		j.FinishedAt = time.Unix(finished, 0)
	}
	return j, nil
}

func (s *Store) Paused() (bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key='paused'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.EqualFold(value, "true"), nil
}

func (s *Store) SetPaused(paused bool) error {
	value := "false"
	if paused {
		value = "true"
	}
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES ('paused', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, value)
	return err
}

// ResetRunning recovers jobs left running by an unclean shutdown.
func (s *Store) ResetRunning() error {
	_, err := s.db.Exec(`UPDATE jobs SET state='pending', updated_at=? WHERE state='running'`, time.Now().Unix())
	return err
}
