// Package history implements chat-history summarisation for the TUI.
// This lives at the TUI chat level (distinct from M8's round-level compaction).
//
// Ported from aider/history.py::ChatSummary
package history

import (
	"fmt"
	"strings"
	"time"
)

// Entry is a single entry in the chat history.
type Entry struct {
	Role      string    // "user", "assistant", "system", "executor"
	Content   string
	Timestamp time.Time
	Tokens    int // estimated token count
}

// Summary is a condensed representation of a chat session.
type Summary struct {
	TotalEntries   int
	TotalTokens    int
	SummaryText    string
	KeyTopics      []string
	FilesMentioned []string
	LastSummarized time.Time
}

// Summarizer creates summaries of chat history.
type Summarizer struct {
	maxEntries int // number of entries before triggering summarization
	maxTokens  int // token budget before triggering summarization
}

// NewSummarizer creates a new chat history summarizer.
func NewSummarizer() *Summarizer {
	return &Summarizer{
		maxEntries: 50,  // Summarize after 50 entries
		maxTokens:  4000, // Or after ~4000 tokens
	}
}

// ShouldSummarize returns true if the history should be summarized.
func (s *Summarizer) ShouldSummarize(entries []Entry) bool {
	if len(entries) >= s.maxEntries {
		return true
	}

	totalTokens := 0
	for _, e := range entries {
		totalTokens += e.Tokens
	}
	return totalTokens >= s.maxTokens
}

// Summarize creates a summary of the chat history.
// This is a simple heuristic-based summarizer. For a more sophisticated
// approach, this could call the compactor model.
func (s *Summarizer) Summarize(entries []Entry) *Summary {
	if len(entries) == 0 {
		return &Summary{
			SummaryText: "No conversation history.",
		}
	}

	summary := &Summary{
		TotalEntries:   len(entries),
		LastSummarized: time.Now(),
	}

	var sb strings.Builder
	tokenCount := 0
	filesMentioned := make(map[string]bool)
	topics := make(map[string]int)

	// First pass: collect statistics and identify files/topics
	for _, e := range entries {
		tokenCount += e.Tokens

		// Extract file mentions
		for _, file := range extractFileMentions(e.Content) {
			filesMentioned[file] = true
		}

		// Count topic keywords (simple heuristic)
		for _, topic := range extractTopics(e.Content) {
			topics[topic]++
		}
	}

	summary.TotalTokens = tokenCount

	// Convert files map to slice
	for file := range filesMentioned {
		summary.FilesMentioned = append(summary.FilesMentioned, file)
	}

	// Get top topics
	type topicCount struct {
		topic string
		count int
	}
	var topicList []topicCount
	for t, c := range topics {
		topicList = append(topicList, topicCount{t, c})
	}
	// Sort by count (simple bubble sort for small lists)
	for i := 0; i < len(topicList); i++ {
		for j := i + 1; j < len(topicList); j++ {
			if topicList[j].count > topicList[i].count {
				topicList[i], topicList[j] = topicList[j], topicList[i]
			}
		}
	}

	// Take top 5 topics
	for i, tc := range topicList {
		if i >= 5 {
			break
		}
		summary.KeyTopics = append(summary.KeyTopics, tc.topic)
	}

	// Build summary text
	sb.WriteString(fmt.Sprintf("Chat session with %d messages (~%d tokens)\n",
		summary.TotalEntries, summary.TotalTokens))

	if len(summary.FilesMentioned) > 0 {
		sb.WriteString(fmt.Sprintf("\nFiles discussed: %s\n",
			strings.Join(summary.FilesMentioned, ", ")))
	}

	if len(summary.KeyTopics) > 0 {
		sb.WriteString(fmt.Sprintf("\nKey topics: %s\n",
			strings.Join(summary.KeyTopics, ", ")))
	}

	// Add recent activity summary
	sb.WriteString("\nRecent activity:\n")
	startIdx := len(entries) - 5
	if startIdx < 0 {
		startIdx = 0
	}
	for i := len(entries) - 1; i >= startIdx; i-- {
		e := entries[i]
		content := e.Content
		if len(content) > 60 {
			content = content[:57] + "..."
		}
		content = strings.ReplaceAll(content, "\n", " ")
		sb.WriteString(fmt.Sprintf("  [%s] %s\n", e.Role, content))
	}

	summary.SummaryText = sb.String()
	return summary
}

// extractFileMentions finds file paths mentioned in text.
// This is a simple heuristic - looks for common file patterns.
func extractFileMentions(text string) []string {
	var files []string
	words := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == ',' || r == ';' || r == '(' || r == ')'
	})

	for _, word := range words {
		// Look for file-like patterns
		if strings.Contains(word, ".") && !strings.HasPrefix(word, ".") {
			// Check for common source file extensions
			ext := strings.ToLower(filepathExt(word))
			switch ext {
			case ".go", ".py", ".js", ".ts", ".tsx", ".jsx",
				".rs", ".java", ".kt", ".c", ".cpp", ".h", ".hpp",
				".rb", ".php", ".swift", ".scala", ".md", ".txt",
				".json", ".yaml", ".yml", ".toml":
				files = append(files, word)
			}
		}
	}

	return files
}

// filepathExt returns the extension of a file path.
func filepathExt(path string) string {
	for i := len(path) - 1; i >= 0 && path[i] != '/'; i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}

// extractTopics extracts potential topic keywords from text.
// Simple heuristic: look for capitalized phrases and technical terms.
func extractTopics(text string) []string {
	var topics []string
	lower := strings.ToLower(text)

	// Technical terms and keywords
	keywords := []string{
		"refactor", "implement", "fix", "bug", "feature",
		"test", "testing", "function", "class", "struct",
		"interface", "api", "endpoint", "database", "query",
		"async", "sync", "concurrent", "parallel",
		"error", "exception", "handle", "catch",
		"optimize", "performance", "memory", "cpu",
		"deploy", "build", "ci", "cd", "git",
		"frontend", "backend", "server", "client",
		"config", "setting", "environment",
	}

	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			topics = append(topics, kw)
		}
	}

	return topics
}

// CompactHistory compacts the history by summarizing older entries.
// Returns a new slice of entries with older ones replaced by a summary entry.
func (s *Summarizer) CompactHistory(entries []Entry) []Entry {
	if !s.ShouldSummarize(entries) {
		return entries
	}

	summary := s.Summarize(entries)

	// Keep the most recent entries (last 10) and add a summary entry
	keepCount := 10
	if len(entries) < keepCount {
		keepCount = len(entries)
	}

	recent := entries[len(entries)-keepCount:]

	// Create a summary entry
	summaryEntry := Entry{
		Role:      "system",
		Content:   "[Earlier conversation summarized]\n" + summary.SummaryText,
		Timestamp: time.Now(),
		Tokens:    len(summary.SummaryText) / 4, // rough estimate
	}

	return append([]Entry{summaryEntry}, recent...)
}

// History manages a conversation history with optional summarization.
type History struct {
	entries    []Entry
	summarizer *Summarizer
	maxEntries int // hard limit on entries
}

// NewHistory creates a new conversation history manager.
func NewHistory() *History {
	return &History{
		entries:    make([]Entry, 0),
		summarizer: NewSummarizer(),
		maxEntries: 100,
	}
}

// Add adds an entry to the history.
func (h *History) Add(role, content string) {
	h.entries = append(h.entries, Entry{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
		Tokens:    len(content) / 4, // rough estimate: ~4 chars per token
	})

	// Compact if needed
	if len(h.entries) > h.maxEntries {
		h.entries = h.summarizer.CompactHistory(h.entries)
	}
}

// Entries returns all history entries.
func (h *History) Entries() []Entry {
	return h.entries
}

// Summary returns a summary of the history.
func (h *History) Summary() *Summary {
	return h.summarizer.Summarize(h.entries)
}

// Clear clears the history.
func (h *History) Clear() {
	h.entries = h.entries[:0]
}

// Count returns the number of entries.
func (h *History) Count() int {
	return len(h.entries)
}
