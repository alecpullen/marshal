// Package tokens provides token counting for marshal's backend layer.
//
// For OpenAI model families it uses tiktoken-go; for all other providers it
// falls back to a character-based heuristic (~4 chars per token).
package tokens

import (
	"strings"
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

// Counter estimates the number of tokens in a text string.
type Counter interface {
	// Count returns the estimated token count for the given text.
	Count(text string) int
	// CountMessages sums the token cost of a slice of (role, content) pairs,
	// including the per-message overhead the API charges.
	CountMessages(messages []Message) int
}

// Message is a minimal (role, content) pair for token counting.
// It mirrors backend.Message without creating an import cycle.
type Message struct {
	Role    string
	Content string
}

// perMessageOverhead is the number of extra tokens the API charges per message
// for role + delimiter framing. Matches the value in OpenAI's cookbook.
const perMessageOverhead = 4

// --- tiktoken-based counter --------------------------------------------------

type tiktokenCounter struct {
	enc *tiktoken.Tiktoken
}

func (t *tiktokenCounter) Count(text string) int {
	return len(t.enc.Encode(text, nil, nil))
}

func (t *tiktokenCounter) CountMessages(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += t.Count(m.Role) + t.Count(m.Content) + perMessageOverhead
	}
	total += 3 // reply primer
	return total
}

// --- char-heuristic counter --------------------------------------------------

type charCounter struct{}

func (charCounter) Count(text string) int {
	// ~4 chars per token; round up.
	return (len(text) + 3) / 4
}

func (charCounter) CountMessages(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += charCounter{}.Count(m.Role) + charCounter{}.Count(m.Content) + perMessageOverhead
	}
	total += 3
	return total
}

// --- Cache -------------------------------------------------------------------

var (
	cacheMu sync.Mutex
	cache   = map[string]Counter{}
)

// ForModel returns a Counter appropriate for the given model string.
// Results are cached so the tiktoken encoder is loaded at most once per model.
func ForModel(model string) Counter {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	if c, ok := cache[model]; ok {
		return c
	}

	c := newForModel(model)
	cache[model] = c
	return c
}

func newForModel(model string) Counter {
	// Determine the tiktoken encoding name for known OpenAI model families.
	enc := tiktokenEncodingFor(model)
	if enc == "" {
		return charCounter{}
	}

	tke, err := tiktoken.GetEncoding(enc)
	if err != nil {
		// Download failed or unsupported; degrade gracefully.
		return charCounter{}
	}
	return &tiktokenCounter{enc: tke}
}

// tiktokenEncodingFor maps a model name to its tiktoken encoding name.
// Returns "" if the model is not in an OpenAI family.
func tiktokenEncodingFor(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "gpt-4o"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"):
		return "o200k_base"
	case strings.HasPrefix(m, "gpt-4"), strings.HasPrefix(m, "gpt-3.5"):
		return "cl100k_base"
	case strings.HasPrefix(m, "text-embedding-ada"), strings.HasPrefix(m, "text-davinci"):
		return "cl100k_base"
	default:
		return ""
	}
}

// CharHeuristic returns a Counter that uses only the char-based heuristic.
// Useful when tiktoken is unavailable or undesirable (e.g. in tests).
func CharHeuristic() Counter {
	return charCounter{}
}
