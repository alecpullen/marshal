package service

import (
	"context"
	"strings"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// RecentPrompts returns the project's input history, newest first, at most
// limit entries. A session without a database or a project returns none.
func (s *Session) RecentPrompts(ctx context.Context, limit int) ([]string, error) {
	database := s.state.DB()
	if database == nil || s.projectID == 0 {
		return nil, nil
	}
	return database.RecentPrompts(s.projectID, limit)
}

// SavePrompt adds text to the project's input history. A session without a
// database or a project saves nothing and reports no error.
func (s *Session) SavePrompt(ctx context.Context, text string) error {
	database := s.state.DB()
	if database == nil || s.projectID == 0 {
		return nil
	}
	return database.SavePrompt(s.projectID, text, s.state.Now())
}

// Messages returns the session's transcript messages that query selects, the
// newest Last of them when Last is set. It never fails, so a closed session
// keeps answering from its final transcript.
func (s *Session) Messages(ctx context.Context, query acpwire.MessageQuery) (acpwire.MessagePage, error) {
	var kept []acpwire.HistoryMessage
	for _, m := range s.state.Messages() {
		role := string(m.Role)
		if query.Role != "" && query.Role != role {
			continue
		}
		if query.Final && (!m.Final || strings.TrimSpace(m.Content) == "") {
			continue
		}
		kept = append(kept, acpwire.HistoryMessage{Role: role, Content: m.Content, Final: m.Final})
	}

	total := len(kept)
	if query.Last > 0 && query.Last < total {
		kept = kept[total-query.Last:]
	}
	return acpwire.MessagePage{Messages: kept, Total: total}, nil
}

// lastAssistantText is the content of the state's last assistant message, or
// "" when the transcript holds none.
func (s *Session) lastAssistantText() string {
	messages := s.state.Messages()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == session.RoleAssistant {
			return messages[i].Content
		}
	}
	return ""
}
