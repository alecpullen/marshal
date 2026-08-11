package db

import (
	"database/sql"
	"fmt"
	"time"
)

// MailRow is one inter-session message. ToSession == "" means broadcast:
// it is unread for every session except the sender until MarkMailRead.
//
// Note on broadcasts: a single read_at marks a broadcast read for everyone,
// so the first session to read consumes it for all. This is acceptable for
// the minimal design (the sender's use case is "hand context to the next
// session"). A per-recipient receipts table is the follow-up if
// multi-recipient delivery matters.
type MailRow struct {
	ID          int64
	FromSession string
	ToSession   string
	Body        string
	CreatedAt   time.Time
}

// SendMail writes an inter-session message. toSession == "" sends a
// broadcast.
func (db *DB) SendMail(projectID int64, fromSession, toSession, body string, at time.Time) error {
	_, err := db.sqlDB.Exec(
		`INSERT INTO session_mail (project_id, from_session, to_session, body, created_at)
		 VALUES (?, ?, NULLIF(?, ''), ?, ?)`,
		projectID, fromSession, toSession, body, at.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}

// UnreadMail returns rows addressed to sessionID plus broadcasts not sent
// by sessionID, where read_at IS NULL, oldest first.
func (db *DB) UnreadMail(sessionID string) ([]MailRow, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, from_session, to_session, body, created_at
		   FROM session_mail
		  WHERE read_at IS NULL
		    AND (to_session = ? OR (to_session IS NULL AND from_session != ?))
		  ORDER BY created_at ASC, id ASC`,
		sessionID, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("list unread mail: %w", err)
	}
	defer rows.Close()

	var out []MailRow
	for rows.Next() {
		var r MailRow
		var to sql.NullString
		var created string
		if err := rows.Scan(&r.ID, &r.FromSession, &to, &r.Body, &created); err != nil {
			return nil, fmt.Errorf("scan mail row: %w", err)
		}
		if to.Valid {
			r.ToSession = to.String
		}
		parsed, perr := time.Parse(time.RFC3339, created)
		if perr != nil {
			return nil, fmt.Errorf("parse created_at %q: %w", created, perr)
		}
		r.CreatedAt = parsed.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mail rows: %w", err)
	}
	return out, nil
}

// MarkMailRead records the given mail IDs as read at the supplied time.
func (db *DB) MarkMailRead(ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin mark read: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`UPDATE session_mail SET read_at = ? WHERE id = ? AND read_at IS NULL`)
	if err != nil {
		return fmt.Errorf("prepare mark read: %w", err)
	}
	defer stmt.Close()

	atStr := at.Format(time.RFC3339)
	for _, id := range ids {
		if _, err := stmt.Exec(atStr, id); err != nil {
			return fmt.Errorf("mark read id %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark read: %w", err)
	}
	return nil
}
