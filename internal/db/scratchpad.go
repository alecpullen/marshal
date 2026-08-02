package db

import (
	"fmt"
	"time"
)

// ScratchpadEntry is a single key-value entry in the session-scoped
// scratchpad store.
type ScratchpadEntry struct {
	Key       string `json:"key"`
	Content   string `json:"content"`
	Format    string `json:"format"`    // "text" | "json" | "table"
	Updated   int64  `json:"updated"`   // unix timestamp in milliseconds
	SizeBytes int    `json:"size_bytes"`
}

// SaveScratchpadEntry upserts a single scratchpad entry.
func (db *DB) SaveScratchpadEntry(sessionID string, entry ScratchpadEntry) error {
	if entry.Key == "" {
		return fmt.Errorf("scratchpad entry key must not be empty")
	}
	_, err := db.sqlDB.Exec(`
		INSERT INTO scratchpad_entries (session_id, entry_key, content, format, updated, size_bytes)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, entry_key) DO UPDATE SET
			content=excluded.content,
			format=excluded.format,
			updated=excluded.updated,
			size_bytes=excluded.size_bytes
	`, sessionID, entry.Key, entry.Content, entry.Format, entry.Updated, entry.SizeBytes)
	if err != nil {
		return fmt.Errorf("save scratchpad entry: %w", err)
	}
	return nil
}

// DeleteScratchpadEntry removes a single entry. Idempotent.
func (db *DB) DeleteScratchpadEntry(sessionID, key string) error {
	_, err := db.sqlDB.Exec(`
		DELETE FROM scratchpad_entries WHERE session_id=? AND entry_key=?
	`, sessionID, key)
	if err != nil {
		return fmt.Errorf("delete scratchpad entry: %w", err)
	}
	return nil
}

// LoadScratchpad restores all entries for a session, ordered by updated desc.
func (db *DB) LoadScratchpad(sessionID string) ([]ScratchpadEntry, error) {
	rows, err := db.sqlDB.Query(`
		SELECT entry_key, content, format, updated, size_bytes
		FROM scratchpad_entries
		WHERE session_id=?
		ORDER BY updated DESC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load scratchpad: %w", err)
	}
	defer rows.Close()

	var entries []ScratchpadEntry
	for rows.Next() {
		var e ScratchpadEntry
		if err := rows.Scan(&e.Key, &e.Content, &e.Format, &e.Updated, &e.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan scratchpad entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load scratchpad rows: %w", err)
	}
	return entries, nil
}

// NewScratchpadEntry stamps Updated and SizeBytes from the current time/content.
func NewScratchpadEntry(key, content, format string) ScratchpadEntry {
	return ScratchpadEntry{
		Key:       key,
		Content:   content,
		Format:    format,
		Updated:   time.Now().UnixMilli(),
		SizeBytes: len(content),
	}
}
