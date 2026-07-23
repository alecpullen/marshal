package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Generation represents a single generation within a session.
type Generation struct {
	ID           string
	SessionID    string
	Seq          int
	StartedAt    time.Time
	EndedAt      *time.Time
	SeedDigest   string
	DigestSource string
	EndReason    string
}

// ArchivedTurn represents a single turn archived for a generation.
type ArchivedTurn struct {
	ID        int64
	TurnSeq   int
	Role      string
	Content   string
	ToolCalls string
	CreatedAt time.Time
}

// BeginGeneration inserts a live generation row.
func (db *DB) BeginGeneration(g Generation) error {
	_, err := db.sqlDB.Exec(
		`INSERT INTO session_generations
		 (generation_id, session_id, seq, started_at, seed_digest, digest_source)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		g.ID, g.SessionID, g.Seq, g.StartedAt.UTC().Format(time.RFC3339),
		nullString(g.SeedDigest), nullString(g.DigestSource),
	)
	if err != nil {
		return fmt.Errorf("begin generation: %w", err)
	}
	return nil
}

// EndGeneration closes a generation with end_reason. Idempotent: re-closing
// an already-closed generation is a no-op (WHERE ended_at IS NULL guard).
func (db *DB) EndGeneration(generationID string, endedAt time.Time, endReason string) error {
	_, err := db.sqlDB.Exec(
		`UPDATE session_generations SET ended_at = ?, end_reason = ?
		 WHERE generation_id = ? AND ended_at IS NULL`,
		endedAt.UTC().Format(time.RFC3339), endReason, generationID,
	)
	if err != nil {
		return fmt.Errorf("end generation: %w", err)
	}
	return nil
}

// ArchiveTurns stores turns for a generation. Small content (<= blobThreshold
// bytes) is stored inline in the content column. Large content is stored as a
// content-addressed blob via PutBlob, and the content_blob_hash column is set
// instead.
func (db *DB) ArchiveTurns(generationID string, turns []ArchivedTurn, blobThreshold int, at time.Time) error {
	for _, t := range turns {
		var contentArg sql.NullString
		var blobHashArg sql.NullString

		if len(t.Content) > blobThreshold {
			hash, err := db.PutBlob(t.Content, at)
			if err != nil {
				return fmt.Errorf("archive turn %d: put blob: %w", t.TurnSeq, err)
			}
			blobHashArg = sql.NullString{String: hash, Valid: true}
		} else {
			contentArg = sql.NullString{String: t.Content, Valid: true}
		}

		_, err := db.sqlDB.Exec(
			`INSERT INTO generation_turns
			 (generation_id, turn_seq, role, content, content_blob_hash, tool_calls, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			generationID, t.TurnSeq, t.Role,
			contentArg, blobHashArg,
			nullString(t.ToolCalls),
			at.UTC().Format(time.RFC3339),
		)
		if err != nil {
			return fmt.Errorf("archive turn %d: %w", t.TurnSeq, err)
		}
	}
	return nil
}

// TurnsForGeneration returns archived turns for a generation, resolving blob
// refs transparently.
func (db *DB) TurnsForGeneration(generationID string) ([]ArchivedTurn, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, turn_seq, role, content, content_blob_hash, tool_calls, created_at
		 FROM generation_turns
		 WHERE generation_id = ?
		 ORDER BY turn_seq ASC`,
		generationID,
	)
	if err != nil {
		return nil, fmt.Errorf("turns for generation: %w", err)
	}
	defer rows.Close()

	// Read all rows first, then close rows before resolving blobs, because
	// the single-connection DB cannot issue another query while iterating.
	type rawTurn struct {
		ID        int64
		TurnSeq   int
		Role      string
		Content   sql.NullString
		BlobHash  sql.NullString
		ToolCalls sql.NullString
		Created   string
	}
	var raw []rawTurn
	for rows.Next() {
		var r rawTurn
		if err := rows.Scan(&r.ID, &r.TurnSeq, &r.Role, &r.Content, &r.BlobHash, &r.ToolCalls, &r.Created); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turns: %w", err)
	}
	// rows is now closed; safe to issue further queries.

	out := make([]ArchivedTurn, len(raw))
	for i, r := range raw {
		t := ArchivedTurn{
			ID:      r.ID,
			TurnSeq: r.TurnSeq,
			Role:    r.Role,
		}
		if r.BlobHash.Valid {
			resolved, err := db.GetBlob(r.BlobHash.String)
			if err != nil {
				return nil, fmt.Errorf("resolve blob %s: %w", r.BlobHash.String, err)
			}
			t.Content = resolved
		} else if r.Content.Valid {
			t.Content = r.Content.String
		}
		if r.ToolCalls.Valid {
			t.ToolCalls = r.ToolCalls.String
		}
		parsed, err := time.Parse(time.RFC3339, r.Created)
		if err != nil {
			return nil, fmt.Errorf("parse turn created_at: %w", err)
		}
		t.CreatedAt = parsed.UTC()
		out[i] = t
	}
	return out, nil
}

// GenerationsForSession returns generations for a session, oldest first.
func (db *DB) GenerationsForSession(sessionID string) ([]Generation, error) {
	rows, err := db.sqlDB.Query(
		`SELECT generation_id, session_id, seq, started_at, ended_at,
		        seed_digest, digest_source, end_reason
		 FROM session_generations
		 WHERE session_id = ?
		 ORDER BY seq ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("generations for session: %w", err)
	}
	defer rows.Close()

	var out []Generation
	for rows.Next() {
		var g Generation
		var started, ended sql.NullString
		var seedDigest, digestSource, endReason sql.NullString
		if err := rows.Scan(&g.ID, &g.SessionID, &g.Seq, &started, &ended,
			&seedDigest, &digestSource, &endReason); err != nil {
			return nil, fmt.Errorf("scan generation: %w", err)
		}
		if !started.Valid {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, started.String)
		if err != nil {
			return nil, fmt.Errorf("parse started_at: %w", err)
		}
		g.StartedAt = parsed.UTC()
		if ended.Valid {
			parsedEnded, err := time.Parse(time.RFC3339, ended.String)
			if err != nil {
				return nil, fmt.Errorf("parse ended_at: %w", err)
			}
			pe := parsedEnded.UTC()
			g.EndedAt = &pe
		}
		if seedDigest.Valid {
			g.SeedDigest = seedDigest.String
		}
		if digestSource.Valid {
			g.DigestSource = digestSource.String
		}
		if endReason.Valid {
			g.EndReason = endReason.String
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate generations: %w", err)
	}
	return out, nil
}

// ReconcileOpenGenerations closes any generations left open (e.g. by a crashed
// process) by setting ended_at and end_reason = 'error'. Idempotent: a second
// call is a no-op because no open generations remain.
func (db *DB) ReconcileOpenGenerations(at time.Time) (int, error) {
	res, err := db.sqlDB.Exec(
		`UPDATE session_generations SET ended_at = ?, end_reason = 'error' WHERE ended_at IS NULL`,
		at.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("reconcile open generations: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reconcile open generations rows: %w", err)
	}
	return int(n), nil
}

// GenerationTurnCount returns the number of archived turns for a generation.
func (db *DB) GenerationTurnCount(generationID string) (int, error) {
	var count int
	err := db.sqlDB.QueryRow(
		`SELECT COUNT(*) FROM generation_turns WHERE generation_id = ?`,
		generationID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("generation turn count: %w", err)
	}
	return count, nil
}

// nullString returns a valid sql.NullString for non-empty strings, or a
// NullString with Valid=false for empty strings.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}
