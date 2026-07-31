package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ScratchpadEntry is a single key-value entry in the session-scoped
// scratchpad store. Content is arbitrary text the agent uses to park
// structured intermediate state (audit results, file lists, decision
// logs, etc.) that should survive context compaction but is not needed
// in full within the context budget.
type ScratchpadEntry struct {
	Key     string `json:"key"`
	Content string `json:"content"`
	Format  string `json:"format"` // "text" | "json" | "table"
	Updated int64  `json:"updated"` // unix timestamp
}

// SaveScratchpad persists the full scratchpad for a session to the
// session_state table under the key "scratchpad". The entire entry set
// is replaced on each call (write-amend is simpler than per-key diff).
func (db *DB) SaveScratchpad(sessionID string, entries []ScratchpadEntry) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal scratchpad: %w", err)
	}
	_, err = db.sqlDB.Exec(
		`INSERT INTO session_state (session_id, key, value) VALUES (?, 'scratchpad', ?)
		 ON CONFLICT(session_id, key) DO UPDATE SET value=excluded.value`,
		sessionID, string(data),
	)
	if err != nil {
		return fmt.Errorf("save scratchpad: %w", err)
	}
	return nil
}

// LoadScratchpad restores the scratchpad entries for a session from the
// session_state table. Returns an empty slice (not an error) when no
// scratchpad row exists yet.
func (db *DB) LoadScratchpad(sessionID string) ([]ScratchpadEntry, error) {
	var data string
	err := db.sqlDB.QueryRow(
		`SELECT value FROM session_state WHERE session_id=? AND key='scratchpad'`,
		sessionID,
	).Scan(&data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load scratchpad: %w", err)
	}
	var entries []ScratchpadEntry
	if err := json.Unmarshal([]byte(data), &entries); err != nil {
		return nil, fmt.Errorf("unmarshal scratchpad: %w", err)
	}
	return entries, nil
}

// NewScratchpadEntry is a convenience constructor that stamps Updated with
// the current time.
func NewScratchpadEntry(key, content, format string) ScratchpadEntry {
	return ScratchpadEntry{
		Key:     key,
		Content: content,
		Format:  format,
		Updated: time.Now().Unix(),
	}
}