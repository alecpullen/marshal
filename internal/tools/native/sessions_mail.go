package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// sessionsListTool returns a read-only tool that lists recent sessions for
// the current project, including their live status and titles.
func (t *toolSet) sessionsListTool() registry.Tool {
	return registry.Tool{
		Name:        "sessions.list",
		Description: "List recent sessions in this project. Returns session ID, title, whether the session is live (not ended), and message count. Newest first.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","description":"Maximum rows to return (default 20, max 50)"}},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			cwd := t.activeRoot()
			limit := 20
			if len(call.Args) > 0 {
				var args struct {
					Limit int `json:"limit"`
				}
				if err := json.Unmarshal(call.Args, &args); err == nil && args.Limit > 0 {
					limit = args.Limit
					if limit > 50 {
						limit = 50
					}
				}
			}
			entries, _, err := t.db.ListSessions(ctx, cwd, "", limit)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sessions.list: %w", err)
			}
			if len(entries) == 0 {
				return registry.ToolResult{Content: "No sessions found."}, nil
			}
			var b strings.Builder
			for _, e := range entries {
				live := "live"
				if e.EndedAt != nil {
					live = "ended"
				}
				title := e.Title
				if title == "" {
					title = "(untitled)"
				}
				fmt.Fprintf(&b, "- %s | %s | %s | messages=%d\n", e.SessionID, title, live, e.MessageCount)
			}
			return registry.ToolResult{Content: strings.TrimRight(b.String(), "\n")}, nil
		},
	}
}

// sessionSendTool returns a tool that sends a message to another session
// (or broadcasts to all sessions if no recipient is given).
func (t *toolSet) sessionSendTool() registry.Tool {
	return registry.Tool{
		Name:        "session.send",
		Description: "Send a message to another session by session ID or title, or broadcast to all sessions if no recipient is given. The message will be delivered as a system message at the start of the recipient's next turn.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"to":{"type":"string","description":"Target session ID or title (omit to broadcast)"},"body":{"type":"string","description":"Message body"}},"required":["body"],"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			if t.sessionState == nil {
				return registry.ToolResult{}, fmt.Errorf("session.send: no current session")
			}
			var args struct {
				To   string `json:"to"`
				Body string `json:"body"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("session.send: invalid args: %w", err)
			}
			args.Body = strings.TrimSpace(args.Body)
			if args.Body == "" {
				return registry.ToolResult{}, fmt.Errorf("session.send: body is required")
			}

			fromSession := t.sessionState.SessionID()
			toSession := ""
			if strings.TrimSpace(args.To) != "" {
				resolved, err := t.resolveSessionRecipient(ctx, strings.TrimSpace(args.To))
				if err != nil {
					return registry.ToolResult{}, err
				}
				toSession = resolved
			}

			if err := t.db.SendMail(t.projectID, fromSession, toSession, args.Body, time.Now()); err != nil {
				return registry.ToolResult{}, fmt.Errorf("session.send: %w", err)
			}
			if toSession == "" {
				return registry.ToolResult{Content: "Broadcast message sent to all sessions."}, nil
			}
			return registry.ToolResult{Content: fmt.Sprintf("Message sent to session %s.", toSession)}, nil
		},
	}
}

// resolveSessionRecipient resolves a session ID or title to a unique
// session ID. It returns an error on ambiguity or unknown target.
func (t *toolSet) resolveSessionRecipient(ctx context.Context, to string) (string, error) {
	cwd := t.activeRoot()
	entries, _, err := t.db.ListSessions(ctx, cwd, "", 50)
	if err != nil {
		return "", fmt.Errorf("session.send: list sessions: %w", err)
	}

	var matches []db.SessionEntry
	for _, e := range entries {
		if e.SessionID == to {
			return e.SessionID, nil
		}
		if strings.EqualFold(e.Title, to) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("session.send: no session matches %q", to)
	case 1:
		return matches[0].SessionID, nil
	default:
		var ids []string
		for _, m := range matches {
			ids = append(ids, m.SessionID)
		}
		return "", fmt.Errorf("session.send: multiple sessions match title %q: %s", to, strings.Join(ids, ", "))
	}
}
