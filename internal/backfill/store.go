package backfill

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrAlreadyRunning = errors.New("backfill is already running")

type Store struct {
	dir string
	db  *sql.DB
}

type Status struct {
	Version        int       `json:"version"`
	State          string    `json:"state"`
	Mode           string    `json:"mode,omitempty"`
	RunID          string    `json:"run_id,omitempty"`
	PID            int       `json:"pid,omitempty"`
	Agent          string    `json:"agent"`
	Provider       string    `json:"provider"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	TotalFiles     int       `json:"total_files,omitempty"`
	ProcessedFiles int       `json:"processed_files,omitempty"`
	TotalBytes     int64     `json:"total_bytes,omitempty"`
	ProcessedBytes int64     `json:"processed_bytes,omitempty"`
	Discovered     int       `json:"discovered,omitempty"`
	Uploaded       int       `json:"uploaded,omitempty"`
	Skipped        int       `json:"skipped,omitempty"`
	Failed         int       `json:"failed,omitempty"`
	ItemsPerSecond float64   `json:"items_per_second,omitempty"`
	BytesPerSecond float64   `json:"bytes_per_second,omitempty"`
	ETASeconds     int64     `json:"eta_seconds,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type lockInfo struct {
	PID   int    `json:"pid"`
	RunID string `json:"run_id"`
}

func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("backfill state directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "backfill.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS imported_items (
  scope TEXT NOT NULL,
  item_id TEXT NOT NULL,
  provider_ref TEXT NOT NULL DEFAULT '',
  imported_at TEXT NOT NULL,
  PRIMARY KEY(scope, item_id)
);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{dir: dir, db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func Scope(configPath, agent, provider string) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{filepath.Clean(configPath), strings.ToLower(agent), provider}, "\x00")))
	return hex.EncodeToString(hash[:16])
}

func (s *Store) Acquire(scope, runID string) (func() error, error) {
	path := s.lockPath(scope)
	for attempts := 0; attempts < 2; attempts++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			info := lockInfo{PID: os.Getpid(), RunID: runID}
			encodeErr := json.NewEncoder(file).Encode(info)
			closeErr := file.Close()
			if encodeErr != nil || closeErr != nil {
				_ = os.Remove(path)
				return nil, errors.Join(encodeErr, closeErr)
			}
			return func() error { return s.release(scope, runID) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		current, readErr := readLock(path)
		if readErr == nil && current.PID > 0 && processAlive(current.PID) {
			return nil, fmt.Errorf("%w (pid %d)", ErrAlreadyRunning, current.PID)
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale backfill lock: %w", removeErr)
		}
	}
	return nil, ErrAlreadyRunning
}

func (s *Store) Succeeded(scope, itemID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM imported_items WHERE scope = ? AND item_id = ?`, scope, itemID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) MarkSucceeded(scope, itemID, providerRef string) error {
	_, err := s.db.Exec(`
INSERT INTO imported_items(scope, item_id, provider_ref, imported_at)
VALUES(?, ?, ?, ?)
ON CONFLICT(scope, item_id) DO UPDATE SET
  provider_ref = excluded.provider_ref,
  imported_at = excluded.imported_at`, scope, itemID, providerRef, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) WriteStatus(scope string, status Status) error {
	status.Version = 1
	status.UpdatedAt = time.Now().UTC()
	bytes, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	temporary := s.statusPath(scope) + ".tmp"
	if err := os.WriteFile(temporary, append(bytes, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, s.statusPath(scope))
}

func (s *Store) ReadStatus(scope string) (Status, error) {
	bytes, err := os.ReadFile(s.statusPath(scope))
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(bytes, &status); err != nil {
		return Status{}, err
	}
	if status.State == "running" && status.PID > 0 && !processAlive(status.PID) {
		status.State = "interrupted"
		status.FinishedAt = time.Now().UTC()
		status.Error = "background worker is no longer running"
		if err := s.WriteStatus(scope, status); err != nil {
			return Status{}, err
		}
	}
	return status, nil
}

func (s *Store) release(scope, runID string) error {
	path := s.lockPath(scope)
	current, err := readLock(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.RunID != runID {
		return nil
	}
	return os.Remove(path)
}

func (s *Store) lockPath(scope string) string {
	return filepath.Join(s.dir, "backfill-"+scope+".lock")
}

func (s *Store) statusPath(scope string) string {
	return filepath.Join(s.dir, "backfill-"+scope+".json")
}

func readLock(path string) (lockInfo, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return lockInfo{}, err
	}
	var info lockInfo
	if err := json.Unmarshal(bytes, &info); err != nil {
		return lockInfo{}, err
	}
	return info, nil
}
