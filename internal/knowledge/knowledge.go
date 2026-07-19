package knowledge

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

// RouteResolver is declared locally rather than imported from
// internal/agent, even though it is structurally identical to
// agent.RouteResolver: internal/agent's MemoryProvider needs
// contextpack.MemoryNote, and internal/knowledge needs a route resolver
// here, so a type shared in either direction would create an import cycle
// between the two packages. Go's structural interfaces mean the same
// *routedProviderResolver constructed in internal/app/app.go satisfies
// both without either package importing the other.
type RouteResolver interface {
	Resolve(class string) (routing.Route, provider.Provider, error)
}

type EndSessionInput struct {
	DB            *db.DB
	ProjectID     int64
	SessionID     string
	State         *session.State
	RouteResolver RouteResolver
	WorkingDir    string
	Now           func() time.Time
	Logger        *slog.Logger
}

// EndSession summarizes the session, extracts durable memories, and
// summarizes session-touched files. It is best-effort: every internal
// failure is logged and swallowed, never returned to the caller, so a
// failed knowledge pass never affects Marshal's process exit. It is a
// no-op if the session has no user messages.
func EndSession(ctx context.Context, in EndSessionInput) {
	messages := in.State.Messages()
	if !hasUserMessage(messages) {
		return
	}

	now := in.Now
	if now == nil {
		now = time.Now
	}

	route, p, err := in.RouteResolver.Resolve("knowledge")
	if err != nil {
		in.Logger.Error("knowledge: resolve route failed", "error", err, "session_id", in.SessionID)
		return
	}

	auditLog := in.State.AuditLog()
	touchedFiles := readTouchedFiles(in.WorkingDir, auditLog)

	prompt := BuildExtractionPrompt(messages, auditLog, touchedFiles)
	raw, err := provider.ChatText(ctx, p, schema.ChatRequest{
		Model:    route.Preset.Model,
		Messages: []schema.ChatMessage{prompt},
	})
	if err != nil {
		in.Logger.Error("knowledge: chat call failed", "error", err, "session_id", in.SessionID)
		return
	}

	extraction, err := ParseExtraction(raw)
	if err != nil {
		in.Logger.Error("knowledge: parse extraction failed", "error", err, "session_id", in.SessionID)
		return
	}

	if err := in.DB.EndSession(in.SessionID, now(), extraction.SessionSummary); err != nil {
		in.Logger.Error("knowledge: save session summary failed", "error", err, "session_id", in.SessionID)
	}

	for _, memory := range extraction.Memories {
		if err := in.DB.SaveMemory(in.ProjectID, memory.Kind, memory.Content, in.SessionID, now()); err != nil {
			in.Logger.Error("knowledge: save memory failed", "error", err, "session_id", in.SessionID)
		}
	}

	for path, summary := range extraction.FileSummaries {
		if _, touched := touchedFiles[path]; !touched {
			continue
		}
		if err := in.DB.UpdateFileSummary(in.ProjectID, path, summary); err != nil {
			in.Logger.Error("knowledge: update file summary failed", "error", err, "session_id", in.SessionID, "path", path)
		}
	}
}

func hasUserMessage(messages []session.Message) bool {
	for _, m := range messages {
		if m.Role == session.RoleUser {
			return true
		}
	}
	return false
}

// readTouchedFiles reads the current content of every distinct path in
// auditLog's FilesChanged. Files that no longer exist or cannot be read are
// silently skipped: the knowledge pass runs best-effort at process exit and
// must not fail the whole extraction over one unreadable file.
func readTouchedFiles(workingDir string, auditLog []registry.AuditEvent) map[string]string {
	seen := map[string]bool{}
	files := map[string]string{}
	for _, event := range auditLog {
		for _, path := range event.FilesChanged {
			if seen[path] {
				continue
			}
			seen[path] = true
			content, err := os.ReadFile(filepath.Join(workingDir, path))
			if err != nil {
				continue
			}
			files[path] = string(content)
		}
	}
	return files
}
