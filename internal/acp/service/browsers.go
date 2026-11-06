package service

import (
	"context"

	"marshal/internal/acpwire"
)

// errNoDatabase is what the browser calls answer when the session has no
// project database.
var errNoDatabase = &acpwire.Error{Code: acpwire.CodeNoDatabase, Message: "inproc: the session has no database"}

// ListSessions lists the stored sessions whose working directory is cwd, most
// recent first, at most limit. It fails with CodeNoDatabase when the agent has
// no database.
func (h *Host) ListSessions(ctx context.Context, cwd string, limit int) ([]acpwire.SessionEntry, error) {
	state := h.currentState()
	if state == nil {
		return nil, errNoDatabase
	}
	database := state.DB()
	if database == nil {
		return nil, errNoDatabase
	}
	rows, _, err := database.ListSessions(ctx, cwd, "", limit)
	if err != nil {
		return nil, err
	}
	out := make([]acpwire.SessionEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, acpwire.SessionEntry{
			SessionID:    row.SessionID,
			Title:        row.Title,
			UpdatedAt:    acpwire.Millis(row.UpdatedAt),
			MessageCount: row.MessageCount,
		})
	}
	return out, nil
}

// Memories lists the project's own memories, oldest first. It fails with
// CodeNoDatabase when the session has no database.
func (s *Session) Memories(ctx context.Context) ([]acpwire.MemoryEntry, error) {
	database := s.state.DB()
	if database == nil {
		return nil, errNoDatabase
	}
	rows, err := database.GetMemories(s.projectID)
	if err != nil {
		return nil, err
	}
	out := make([]acpwire.MemoryEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, acpwire.MemoryEntry{
			ID:         row.ID,
			Kind:       row.Kind,
			Content:    row.Content,
			Confidence: row.Confidence,
			Scope:      row.Scope,
			CreatedAt:  acpwire.Millis(row.CreatedAt),
		})
	}
	return out, nil
}

// DeleteMemory deletes one memory. It fails with CodeNoDatabase when the
// session has no database.
func (s *Session) DeleteMemory(ctx context.Context, id int64) error {
	database := s.state.DB()
	if database == nil {
		return errNoDatabase
	}
	return database.DeleteMemory(id)
}
