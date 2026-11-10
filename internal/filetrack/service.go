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
	if err != nil {
		// Previously discarded, which reported a zero timestamp as a valid
		// read time. Callers use this to gate the read-before-write staleness
		// check, so a corrupt row silently became "you last read this file at
		// the zero time" instead of an error they could act on.
		return time.Time{}, false, fmt.Errorf("parse last read time %q: %w", readAt, err)
	}
	return t.UTC(), true, nil
}

// RecordWrite records that this session wrote path at at. writer names the
// agent that committed the write ("main agent", "a subagent", …); it is what
// lets a later stale-file error tell a reader which writer invalidated its
// view, so the orchestrator can serialise instead of guessing.
func (s *Service) RecordWrite(path string, at time.Time, writer string) error {
	_, err := s.db.Exec(
		`INSERT INTO file_writes (session_id, path, written_at, written_by) VALUES (?, ?, ?, ?)
		 ON CONFLICT(session_id, path) DO UPDATE SET
			written_at=excluded.written_at, written_by=excluded.written_by`,
		s.sessionID, path, at.UTC().Format(time.RFC3339Nano), writer)
	if err != nil {
		return fmt.Errorf("record write: %w", err)
	}
	return nil
}

// LastWriter reports which agent last wrote path through this session, and
// when. ok is false for a path the session has never written — including one
// changed by something outside the session (a checkout, another process), which
// is exactly the case where naming a session writer would be a lie.
func (s *Service) LastWriter(path string) (writer string, at time.Time, ok bool, err error) {
	var writtenAt, writtenBy string
	scanErr := s.db.QueryRow(
		`SELECT written_at, written_by FROM file_writes WHERE session_id = ? AND path = ?`,
		s.sessionID, path).Scan(&writtenAt, &writtenBy)
	if scanErr == sql.ErrNoRows {
		return "", time.Time{}, false, nil
	}
	if scanErr != nil {
		return "", time.Time{}, false, fmt.Errorf("query last write: %w", scanErr)
	}
	t, parseErr := time.Parse(time.RFC3339Nano, writtenAt)
	if parseErr != nil {
		return "", time.Time{}, true, fmt.Errorf("parse last write time %q: %w", writtenAt, parseErr)
	}
	return writtenBy, t.UTC(), true, nil
}
