package filetrack

import (
	"database/sql"
	"fmt"
	"time"
)

type Service struct {
	db        *sql.DB
	sessionID string
}

func New(db *sql.DB, sessionID string) *Service {
	return &Service{db: db, sessionID: sessionID}
}

func (s *Service) RecordRead(path string, at time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO file_reads (session_id, path, read_at) VALUES (?, ?, ?)
		 ON CONFLICT(session_id, path) DO UPDATE SET read_at=excluded.read_at`,
		s.sessionID, path, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record read: %w", err)
	}
	return nil
}

func (s *Service) LastReadTime(path string) (time.Time, bool, error) {
	var readAt string
	err := s.db.QueryRow(
		`SELECT read_at FROM file_reads WHERE session_id = ? AND path = ?`,
		s.sessionID, path).Scan(&readAt)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("query last read: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, readAt)
	return t.UTC(), true, nil
}

func (s *Service) RecordWrite(path string, at time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO file_writes (session_id, path, written_at) VALUES (?, ?, ?)
		 ON CONFLICT(session_id, path) DO UPDATE SET written_at=excluded.written_at`,
		s.sessionID, path, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record write: %w", err)
	}
	return nil
}

func (s *Service) ListReadFiles() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT path FROM file_reads WHERE session_id = ? ORDER BY path`, s.sessionID)
	if err != nil {
		return nil, fmt.Errorf("list reads: %w", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

func (s *Service) ListWrittenFiles() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT path FROM file_writes WHERE session_id = ? ORDER BY path`, s.sessionID)
	if err != nil {
		return nil, fmt.Errorf("list writes: %w", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
