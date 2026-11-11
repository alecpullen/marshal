package filetrack

import (
	"database/sql"
	"fmt"
	"time"
)

type Actor struct {
	ID    string
	Label string
}

type ReadState struct {
	Revision int64
	At       time.Time
	OK       bool
}

type WriteState struct {
	Revision int64
	At       time.Time
	Actor    Actor
	OK       bool
}

type Service struct {
	db        *sql.DB
	sessionID string
}

func New(db *sql.DB, sessionID string) *Service {
	return &Service{db: db, sessionID: sessionID}
}

func (s *Service) RecordRead(path string, at time.Time, revision int64) error {
	_, err := s.db.Exec(
		`INSERT INTO file_reads (session_id, path, read_at, revision) VALUES (?, ?, ?, ?)
		 ON CONFLICT(session_id, path) DO UPDATE SET
			revision = MAX(file_reads.revision, excluded.revision),
			read_at = CASE
				WHEN excluded.revision >= file_reads.revision
					THEN excluded.read_at
				ELSE file_reads.read_at
			END`,
		s.sessionID, path, at.UTC().Format(time.RFC3339Nano), revision)
	if err != nil {
		return fmt.Errorf("record read: %w", err)
	}
	return nil
}

func (s *Service) ReadState(path string) (ReadState, error) {
	var readAt string
	var revision int64
	err := s.db.QueryRow(
		`SELECT read_at, revision FROM file_reads WHERE session_id = ? AND path = ?`,
		s.sessionID, path).Scan(&readAt, &revision)
	if err == sql.ErrNoRows {
		return ReadState{}, nil
	}
	if err != nil {
		return ReadState{}, fmt.Errorf("query read state: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, readAt)
	if err != nil {
		return ReadState{}, fmt.Errorf("parse last read time %q: %w", readAt, err)
	}
	return ReadState{Revision: revision, At: t.UTC(), OK: true}, nil
}

func (s *Service) RecordWrite(path string, at time.Time, actor Actor) error {
	_, err := s.db.Exec(
		`INSERT INTO file_writes (session_id, path, written_at, written_by, written_id, revision)
		 VALUES (?, ?, ?, ?, ?, 1)
		 ON CONFLICT(session_id, path) DO UPDATE SET
			written_at=excluded.written_at,
			written_by=excluded.written_by,
			written_id=excluded.written_id,
			revision=file_writes.revision + 1`,
		s.sessionID, path, at.UTC().Format(time.RFC3339Nano), actor.Label, actor.ID)
	if err != nil {
		return fmt.Errorf("record write: %w", err)
	}
	return nil
}

func (s *Service) WriteState(path string) (WriteState, error) {
	var writtenAt, writtenBy, writtenID string
	var revision int64
	err := s.db.QueryRow(
		`SELECT written_at, written_by, written_id, revision FROM file_writes WHERE session_id = ? AND path = ?`,
		s.sessionID, path).Scan(&writtenAt, &writtenBy, &writtenID, &revision)
	if err == sql.ErrNoRows {
		return WriteState{}, nil
	}
	if err != nil {
		return WriteState{}, fmt.Errorf("query write state: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, writtenAt)
	if err != nil {
		return WriteState{}, fmt.Errorf("parse last write time %q: %w", writtenAt, err)
	}
	return WriteState{
		Revision: revision,
		At:       t.UTC(),
		Actor:    Actor{ID: writtenID, Label: writtenBy},
		OK:       true,
	}, nil
}
