package sessionsequence

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("session sequence store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create session sequence directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open session sequence store: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	for _, statement := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS session_sequences (
			session_id TEXT PRIMARY KEY,
			last_sequence INTEGER NOT NULL
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize session sequence store: %w", err)
		}
	}
	return store, nil
}

func (s *Store) Next(sessionID string, floor int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("session sequence store is not open")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, errors.New("session ID is required")
	}
	if floor < 1 {
		floor = 1
	}
	var sequence int64
	err := s.db.QueryRow(`
INSERT INTO session_sequences(session_id, last_sequence)
VALUES (?, ?)
ON CONFLICT(session_id) DO UPDATE SET last_sequence =
	CASE
		WHEN excluded.last_sequence > session_sequences.last_sequence
			THEN excluded.last_sequence
		ELSE session_sequences.last_sequence + 1
	END
RETURNING last_sequence
`, sessionID, floor).Scan(&sequence)
	if err != nil {
		return 0, fmt.Errorf("reserve session sequence: %w", err)
	}
	return sequence, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
