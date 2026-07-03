package knowledge

import (
	"fmt"
	"sort"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

const extractionPromptTemplate = `You are Marshal's knowledge agent. Your job is to review what happened in a coding session and extract durable, evidence-backed information worth remembering about this project.

Session transcript:
%s

Tool activity:
%s

Files touched this session:
%s

Respond with exactly one JSON object and nothing else, in this shape:
{"session_summary": "short paragraph summarizing what happened this session", "memories": [{"kind": "fact", "content": "..."}, {"kind": "architecture", "content": "..."}, {"kind": "decision", "content": "..."}], "file_summaries": {"path/to/file.go": "one-line summary of what this file does"}}

Rules:
- Only extract memories you have direct evidence for from the transcript or tool activity above. Do not invent facts.
- Prefer few, high-value memories over many trivial ones. It is fine to return an empty memories list.
- Only include file_summaries entries for files listed under "Files touched this session" above.
- kind must be one of "fact", "architecture", "decision".`

// BuildExtractionPrompt builds the single user-turn prompt sent to the
// knowledge model. messages is the session transcript, auditLog is the
// session's tool-call history, and touchedFiles maps each session-touched
// file path (from AuditEvent.FilesChanged) to its current content.
func BuildExtractionPrompt(messages []session.Message, auditLog []registry.AuditEvent, touchedFiles map[string]string) schema.ChatMessage {
	var transcript strings.Builder
	if len(messages) == 0 {
		transcript.WriteString("(no messages)")
	}
	for _, m := range messages {
		fmt.Fprintf(&transcript, "%s: %s\n", m.Role, m.Content)
	}

	var activity strings.Builder
	if len(auditLog) == 0 {
		activity.WriteString("(no tool activity)")
	}
	for _, event := range auditLog {
		fmt.Fprintf(&activity, "%s -> %s\n", event.ToolName, event.ResultSummary)
	}

	var files strings.Builder
	paths := make([]string, 0, len(touchedFiles))
	for path := range touchedFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		files.WriteString("(no files touched)")
	}
	for _, path := range paths {
		fmt.Fprintf(&files, "--- %s ---\n%s\n", path, touchedFiles[path])
	}

	return schema.ChatMessage{
		Role:    schema.RoleUser,
		Content: fmt.Sprintf(extractionPromptTemplate, transcript.String(), activity.String(), files.String()),
	}
}
