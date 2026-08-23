package knowledge

import (
	"context"
	"fmt"
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
	DB                  *db.DB
	ProjectID           int64
	SessionID           string
	State               *session.State
	RouteResolver       RouteResolver
	WorkingDir          string
	MaxTouchedFileBytes int
	Now                 func() time.Time
	Logger              *slog.Logger
}

// ExtractInput carries an explicit snapshot for one knowledge pass. Unlike
// EndSessionInput it does not take the live State: periodic callers snapshot
// messages/audit at trigger time so the pass runs against a stable view.
type ExtractInput struct {
	DB                  *db.DB
	ProjectID           int64
	SessionID           string
	RouteResolver       RouteResolver
	WorkingDir          string
	MaxTouchedFileBytes int
	Messages            []session.Message
	AuditLog            []registry.AuditEvent
	Now                 func() time.Time
	Logger              *slog.Logger
}

// Extract runs one knowledge pass — durable memories plus per-file
// summaries — over the given snapshot. Best-effort like EndSession: every
// failure is logged and swallowed; the return is nil on failure. The
// returned Extraction carries the session summary for the caller that
// persists it (EndSession); periodic callers ignore it.
func Extract(ctx context.Context, in ExtractInput) *Extraction {
	if !hasUserMessage(in.Messages) {
		return nil
	}
	now := in.Now
	if now == nil {
		now = time.Now
	}
	route, p, err := in.RouteResolver.Resolve("knowledge")
	if err != nil {
		in.Logger.Error("knowledge: resolve route failed", "error", err, "session_id", in.SessionID)
		return nil
	}
	maxBytes := in.MaxTouchedFileBytes
	if maxBytes <= 0 {
		maxBytes = 65536
	}
	touchedFiles := readTouchedFiles(in.WorkingDir, in.AuditLog, maxBytes)

	prompt := BuildExtractionPrompt(in.Messages, in.AuditLog, touchedFiles)
	raw, err := provider.ChatText(ctx, p, schema.ChatRequest{
		Model:    route.Preset.Model,
		Messages: []schema.ChatMessage{prompt},
	})
	if err != nil {
		in.Logger.Error("knowledge: chat call failed", "error", err, "session_id", in.SessionID)
		return nil
	}
	extraction, err := ParseExtraction(raw)
	if err != nil {
		in.Logger.Error("knowledge: parse extraction failed", "error", err, "session_id", in.SessionID)
		return nil
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
	return &extraction
}

// EndSession runs the final knowledge extraction pass and persists the
// session summary. It is best-effort: every internal failure is logged and
// swallowed, never returned to the caller, so a failed knowledge pass never
// affects Marshal's process exit. It is a no-op if the session has no user
// messages.
func EndSession(ctx context.Context, in EndSessionInput) {
	messages := in.State.Messages()
	if !hasUserMessage(messages) {
		return
	}
	now := in.Now
	if now == nil {
		now = time.Now
	}
	extraction := Extract(ctx, ExtractInput{
		DB:                  in.DB,
		ProjectID:           in.ProjectID,
		SessionID:           in.SessionID,
		RouteResolver:       in.RouteResolver,
		WorkingDir:          in.WorkingDir,
		MaxTouchedFileBytes: in.MaxTouchedFileBytes,
		Messages:            messages,
		AuditLog:            in.State.AuditLog(),
		Now:                 now,
		Logger:              in.Logger,
	})
	if extraction == nil {
		return
	}
	if err := in.DB.EndSession(in.SessionID, now(), extraction.SessionSummary); err != nil {
		in.Logger.Error("knowledge: save session summary failed", "error", err, "session_id", in.SessionID)
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
// must not fail the whole extraction over one unreadable file. Files larger
// than maxBytes are truncated to that size with a marker appended.
func readTouchedFiles(workingDir string, auditLog []registry.AuditEvent, maxBytes int) map[string]string {
	seen := map[string]bool{}
	files := map[string]string{}
	for _, event := range auditLog {
		for _, path := range event.FilesChanged {
			if seen[path] {
				continue
			}
			seen[path] = true
			fullPath := filepath.Join(workingDir, path)
			info, err := os.Stat(fullPath)
			if err != nil {
				continue
			}
			if info.Size() > int64(maxBytes) {
				f, err := os.Open(fullPath)
				if err != nil {
					continue
				}
				buf := make([]byte, maxBytes)
				n, _ := f.Read(buf)
				f.Close()
				content := string(buf[:n])
				content += fmt.Sprintf("\n[... file truncated at %d bytes ...]", maxBytes)
				files[path] = content
				continue
			}
			content, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			files[path] = string(content)
		}
	}
	return files
}
