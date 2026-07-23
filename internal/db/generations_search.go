package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const defaultSearchLimit = 20

// SearchHit is a single result from SearchArchivedTurns.
type SearchHit struct {
	Turn          ArchivedTurn
	GenerationID  string
	GenerationSeq int
	SessionID     string
}

// SearchArchivedTurns searches archived turn content via the FTS5 index.
// Empty or whitespace-only queries return nil. sessionID and generationID
// are optional scope filters. limit defaults to defaultSearchLimit when <= 0.
func (db *DB) SearchArchivedTurns(sessionID, query, generationID string, limit int) ([]SearchHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	sqlStmt := `SELECT t.id, t.turn_seq, t.role, t.content, t.content_blob_hash, t.tool_calls, t.created_at,
	               g.generation_id, g.seq, g.session_id
	        FROM generation_turns_fts f
	        JOIN generation_turns t ON t.id = f.rowid
	        JOIN session_generations g ON g.generation_id = t.generation_id
	        WHERE generation_turns_fts MATCH ?`
	args := []any{ftsPhrase(query)}
	if sessionID != "" {
		sqlStmt += ` AND g.session_id = ?`
		args = append(args, sessionID)
	}
	if generationID != "" {
		sqlStmt += ` AND g.generation_id = ?`
		args = append(args, generationID)
	}
	sqlStmt += ` ORDER BY bm25(generation_turns_fts) LIMIT ?`
	args = append(args, limit)

	rows, err := db.sqlDB.Query(sqlStmt, args...)
	if err != nil {
		return nil, fmt.Errorf("search archived turns: %w", err)
	}
	defer rows.Close()

	type rawHit struct {
		ID            int64
		TurnSeq       int
		Role          string
		Content       sql.NullString
		BlobHash      sql.NullString
		ToolCalls     sql.NullString
		Created       string
		GenerationID  string
		GenerationSeq int
		SessionID     string
	}
	var raw []rawHit
	for rows.Next() {
		var r rawHit
		if err := rows.Scan(&r.ID, &r.TurnSeq, &r.Role, &r.Content, &r.BlobHash,
			&r.ToolCalls, &r.Created, &r.GenerationID, &r.GenerationSeq, &r.SessionID); err != nil {
			return nil, fmt.Errorf("scan search hit: %w", err)
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search hits: %w", err)
	}
	// rows is now closed; safe to issue further queries.

	out := make([]SearchHit, len(raw))
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
			return nil, fmt.Errorf("parse search hit created_at: %w", err)
		}
		t.CreatedAt = parsed.UTC()
		out[i] = SearchHit{
			Turn:          t,
			GenerationID:  r.GenerationID,
			GenerationSeq: r.GenerationSeq,
			SessionID:     r.SessionID,
		}
	}
	return out, nil
}

// ftsPhrase wraps a query in double quotes so FTS5 treats it as a literal
// phrase, preventing operator characters from being interpreted.
func ftsPhrase(query string) string {
	escaped := strings.ReplaceAll(query, `"`, `""`)
	return `"` + escaped + `"`
}
