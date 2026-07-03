package knowledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrNoExtractionFound = errors.New("knowledge: no JSON extraction object found in model output")

// MemoryNote is knowledge's own view of an extracted memory candidate. It
// is a separate, identically-shaped type from contextpack.MemoryNote (not
// shared) so that internal/agent and internal/knowledge never need to
// import each other — see the Milestone N design doc.
type MemoryNote struct {
	Kind    string
	Content string
}

// Extraction is the parsed form of the knowledge pass's JSON response.
type Extraction struct {
	SessionSummary string
	Memories       []MemoryNote
	FileSummaries  map[string]string
}

type extractionEnvelope struct {
	SessionSummary string            `json:"session_summary"`
	Memories       []memoryPayload   `json:"memories"`
	FileSummaries  map[string]string `json:"file_summaries"`
}

type memoryPayload struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// ParseExtraction extracts and validates the single JSON object the
// knowledge prompt instructs the model to return. It tolerates a leading/
// trailing ```json fence, since local models frequently wrap JSON in
// markdown even when told not to (same tolerance as agent.ParseAction).
func ParseExtraction(raw string) (Extraction, error) {
	jsonText, err := extractJSONObject(raw)
	if err != nil {
		return Extraction{}, err
	}

	var envelope extractionEnvelope
	if err := json.Unmarshal([]byte(jsonText), &envelope); err != nil {
		return Extraction{}, fmt.Errorf("knowledge: malformed extraction JSON: %w", err)
	}

	memories := make([]MemoryNote, 0, len(envelope.Memories))
	for _, m := range envelope.Memories {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		kind := strings.TrimSpace(m.Kind)
		if kind == "" {
			kind = "fact"
		}
		memories = append(memories, MemoryNote{Kind: kind, Content: content})
	}

	return Extraction{
		SessionSummary: strings.TrimSpace(envelope.SessionSummary),
		Memories:       memories,
		FileSummaries:  envelope.FileSummaries,
	}, nil
}

func extractJSONObject(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start == -1 || end == -1 || end < start {
		return "", ErrNoExtractionFound
	}
	return trimmed[start : end+1], nil
}
