// internal/conversation/intent.go
// Rule-based intent classification for user messages.
// This is an MVP - can be replaced with model-based classification later.

package conversation

import (
	"strings"
)

// ClassifyIntent determines the user's intent from their message and conversation state
func ClassifyIntent(msg string, conv *Conversation) Intent {
	msgLower := strings.ToLower(strings.TrimSpace(msg))

	// Cancel detection
	if isCancel(msgLower) {
		return IntentCancel
	}

	// Confirmation/decline detection (when in clarifying state or when plan proposed)
	if conv.State == StateClarifying || len(conv.PendingTasks) > 0 {
		if isConfirmation(msgLower) {
			return IntentConfirm
		}
		if isDecline(msgLower) {
			return IntentDecline
		}
	}

	// Context provision detection (when Marshal asked questions)
	if conv.IsClarifying() && conv.LastMessage() != nil && conv.LastMessage().Role == "marshal" {
		// User is answering Marshal's question
		return IntentProvideContext
	}

	// Work request detection
	if isWorkRequest(msgLower) {
		return IntentRequestWork
	}

	// Default: casual chat
	return IntentChat
}

// isCancel detects cancellation requests
func isCancel(msg string) bool {
	cancelPhrases := []string{
		"cancel", "stop", "abort", "never mind", "nevermind",
		"forget it", "don't do that", "dont do that",
		"quit", "exit", "end",
	}
	for _, phrase := range cancelPhrases {
		if msg == phrase || strings.HasPrefix(msg, phrase+" ") {
			return true
		}
	}
	return false
}

// isConfirmation detects confirmation responses
func isConfirmation(msg string) bool {
	confirmations := []string{
		"yes", "y", "yeah", "yep", "sure", "ok", "okay",
		"go ahead", "do it", "proceed", "confirm",
		"sounds good", "looks good", "that works",
		"execute", "run it", "start",
	}
	for _, phrase := range confirmations {
		if msg == phrase || strings.HasPrefix(msg, phrase+" ") || strings.HasSuffix(msg, " "+phrase) {
			return true
		}
	}
	return false
}

// isDecline detects decline/rejection responses
func isDecline(msg string) bool {
	declines := []string{
		"no", "n", "nah", "nope",
		"don't", "dont", "do not",
		"cancel", "abort", "stop",
		"bad idea", "that won't work", "that wont work",
		"reject", "decline",
	}
	for _, phrase := range declines {
		if msg == phrase || strings.HasPrefix(msg, phrase+" ") {
			return true
		}
	}
	return false
}

// isWorkRequest detects if user is asking for work to be done
func isWorkRequest(msg string) bool {
	// Direct action verbs
	actionVerbs := []string{
		"fix", "add", "implement", "create", "build", "write",
		"refactor", "update", "modify", "change", "remove", "delete",
		"optimize", "improve", "clean up", "cleanup",
		"debug", "solve", "resolve",
		"generate", "produce",
		"move", "rename", "extract", "split", "merge",
		"test", "verify", "check",
		"configure", "set up", "setup",
		"analyze", "analyse", "explore", "understand", "investigate", "review",
		"find", "locate", "discover", "identify",
	}

	for _, verb := range actionVerbs {
		if strings.HasPrefix(msg, verb+" ") || strings.HasPrefix(msg, verb+"s ") {
			return true
		}
	}

	// Intent phrases
	intentPhrases := []string{
		"i want", "i need", "please", "can you", "could you",
		"would you", "help me", "make it", "let's", "lets",
		"we should", "it would be nice", "i'd like", "id like",
	}

	for _, phrase := range intentPhrases {
		if strings.HasPrefix(msg, phrase+" ") {
			return true
		}
	}

	// Task indicators
	taskIndicators := []string{
		"bug", "issue", "problem", "error", "broken",
		"feature", "enhancement", "request",
	}

	for _, indicator := range taskIndicators {
		if strings.Contains(msg, indicator) {
			return true
		}
	}

	return false
}

// IsTaskCompleteQuery checks if user is asking about task status
func IsTaskCompleteQuery(msg string) bool {
	queries := []string{
		"done", "ready", "finished", "complete",
		"status", "progress", "update",
		"how is", "what's the",
	}
	msgLower := strings.ToLower(msg)
	for _, q := range queries {
		if strings.Contains(msgLower, q) {
			return true
		}
	}
	return false
}

// ExtractFilesFromMessage attempts to find file paths in a message
func ExtractFilesFromMessage(msg string) []string {
	var files []string
	words := strings.Fields(msg)

	for _, word := range words {
		// Simple heuristic: files often have extensions or path separators
		if strings.Contains(word, ".go") ||
			strings.Contains(word, ".js") ||
			strings.Contains(word, ".ts") ||
			strings.Contains(word, ".py") ||
			strings.Contains(word, ".md") ||
			strings.Contains(word, ".toml") ||
			strings.Contains(word, "/") ||
			strings.Contains(word, "\\") {
			// Clean up punctuation
			cleaned := strings.Trim(word, ".,;:!?\"'()[]")
			if cleaned != "" && len(cleaned) > 1 {
				files = append(files, cleaned)
			}
		}
	}

	return files
}
