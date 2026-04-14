package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alecpullen/marshal/internal/commands"
)

// completionType indicates what kind of completion we're showing
type completionType int

const (
	completionNone     completionType = iota
	completionCommand                 // Completing command names (e.g., /shi -> /ship)
	completionFilePath                // Completing file paths (e.g., /add cmd/ma -> /add cmd/marshal)
	completionTaskID                  // Completing task IDs (e.g., /revert abc -> /revert abc123)
)

// completionState tracks the current completion state
type completionState struct {
	type_       completionType
	suggestions []string
	selectedIdx int
	prefix      string // The text that triggered the completion
	command     string // The command being completed (e.g., "add" for /add)
}

// allCommands is the list of all available slash commands
var allCommands = []string{
	"/add",
	"/clear",
	"/commit",
	"/copy",
	"/copy-context",
	"/diff",
	"/discard",
	"/drop",
	"/edit",
	"/editor",
	"/git",
	"/help",
	"/history",
	"/init",
	"/lint",
	"/load",
	"/ls",
	"/map",
	"/map-refresh",
	"/model",
	"/multiline-mode",
	"/paste",
	"/permission",
	"/quit",
	"/read-only",
	"/reasoning-effort",
	"/report",
	"/reset",
	"/revert",
	"/run",
	"/save",
	"/session",
	"/settings",
	"/ship",
	"/skills",
	"/task",
	"/test",
	"/think-tokens",
	"/tokens",
	"/undo",
	"/unwatch",
	"/voice",
	"/watch",
	"/web",
}

// commandsWithFileArgs are commands that expect file paths as arguments
var commandsWithFileArgs = map[string]bool{
	commands.CmdAdd:      true,
	commands.CmdDrop:     true,
	commands.CmdReadOnly: true,
	commands.CmdEdit:     true,
}

// commandsWithTaskArgs are commands that expect task IDs as arguments
var commandsWithTaskArgs = map[string]bool{
	commands.CmdRevert: true,
}

// updateCompletions updates the completion state based on the current input
func (m *model) updateCompletions() {
	input := m.input.Value()

	// Reset completions if not typing a command
	if !strings.HasPrefix(input, "/") {
		m.clearCompletions()
		return
	}

	// Check if we're typing arguments (after a space)
	if strings.Contains(input, " ") {
		m.updateArgCompletions(input)
		return
	}

	// We're completing a command name
	m.updateCommandCompletions(input)
}

// updateCommandCompletions updates suggestions for command names
func (m *model) updateCommandCompletions(input string) {
	input = strings.ToLower(input)
	var matches []string

	for _, cmd := range allCommands {
		if strings.HasPrefix(cmd, input) {
			matches = append(matches, cmd)
		}
	}

	if len(matches) == 0 {
		m.clearCompletions()
		return
	}

	sort.Strings(matches)

	m.completionState = &completionState{
		type_:       completionCommand,
		suggestions: matches,
		selectedIdx: 0,
		prefix:      input,
	}
}

// updateArgCompletions updates suggestions for command arguments
func (m *model) updateArgCompletions(input string) {
	parts := strings.SplitN(input, " ", 2)
	if len(parts) < 1 {
		m.clearCompletions()
		return
	}

	cmd := strings.TrimPrefix(parts[0], "/")
	argPrefix := ""
	if len(parts) > 1 {
		argPrefix = parts[1]
	}

	// Check if this command takes file arguments
	if commandsWithFileArgs[cmd] {
		m.updateFileCompletions(cmd, argPrefix)
		return
	}

	// Check if this command takes task ID arguments
	if commandsWithTaskArgs[cmd] {
		m.updateTaskIDCompletions(cmd, argPrefix)
		return
	}

	m.clearCompletions()
}

// updateFileCompletions suggests files from the repo
func (m *model) updateFileCompletions(cmd, prefix string) {
	if m.repoRoot == "" {
		m.clearCompletions()
		return
	}

	var matches []string

	// If prefix starts with "/" or is empty, search from root
	searchDir := m.repoRoot
	searchPrefix := prefix

	// Handle relative paths
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		// Check if prefix contains directory components
		dir := filepath.Dir(prefix)
		base := filepath.Base(prefix)
		if dir != "." && dir != "/" {
			searchDir = filepath.Join(m.repoRoot, dir)
			searchPrefix = base
		}
	}

	// List files in the search directory
	entries, err := os.ReadDir(searchDir)
	if err != nil {
		m.clearCompletions()
		return
	}

	for _, entry := range entries {
		name := entry.Name()
		// Skip hidden files and common ignore patterns
		if strings.HasPrefix(name, ".") {
			continue
		}
		if shouldIgnoreFile(name) {
			continue
		}

		// Match against prefix
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(searchPrefix)) {
			// Reconstruct the full path
			fullPath := name
			if dir := filepath.Dir(prefix); dir != "." && !strings.HasPrefix(prefix, "/") {
				if dir != "/" {
					fullPath = filepath.Join(dir, name)
				}
			}
			if entry.IsDir() {
				fullPath += "/"
			}
			matches = append(matches, fullPath)
		}
	}

	if len(matches) == 0 {
		m.clearCompletions()
		return
	}

	sort.Strings(matches)

	m.completionState = &completionState{
		type_:       completionFilePath,
		suggestions: matches,
		selectedIdx: 0,
		prefix:      prefix,
		command:     cmd,
	}
}

// updateTaskIDCompletions suggests task IDs from the session
func (m *model) updateTaskIDCompletions(cmd, prefix string) {
	if m.store == nil || m.sessionID == "" {
		m.clearCompletions()
		return
	}

	tasks, err := m.store.TasksForSession(m.sessionID)
	if err != nil {
		m.clearCompletions()
		return
	}

	var matches []string
	for _, task := range tasks {
		if strings.HasPrefix(strings.ToLower(task.ID), strings.ToLower(prefix)) {
			// Include status in the display
			display := task.ID
			if task.Summary != nil && *task.Summary != "" {
				summary := *task.Summary
				if len(summary) > 40 {
					summary = summary[:37] + "..."
				}
				display = task.ID + " (" + string(task.Status) + ": " + summary + ")"
			} else {
				display = task.ID + " (" + string(task.Status) + ")"
			}
			matches = append(matches, display)
		}
	}

	if len(matches) == 0 {
		m.clearCompletions()
		return
	}

	m.completionState = &completionState{
		type_:       completionTaskID,
		suggestions: matches,
		selectedIdx: 0,
		prefix:      prefix,
		command:     cmd,
	}
}

// shouldIgnoreFile returns true for files that should be excluded from completions
func shouldIgnoreFile(name string) bool {
	ignorePatterns := []string{
		"node_modules",
		"vendor",
		"__pycache__",
		".git",
		".marshal",
		"bin",
		"dist",
		"build",
		".DS_Store",
	}
	lower := strings.ToLower(name)
	for _, pattern := range ignorePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// clearCompletions clears the completion state
func (m *model) clearCompletions() {
	m.completionState = nil
}

// hasCompletions returns true if there are active completions
func (m *model) hasCompletions() bool {
	return m.completionState != nil && len(m.completionState.suggestions) > 0
}

// selectNextCompletion moves to the next suggestion
func (m *model) selectNextCompletion() {
	if !m.hasCompletions() {
		return
	}
	m.completionState.selectedIdx++
	if m.completionState.selectedIdx >= len(m.completionState.suggestions) {
		m.completionState.selectedIdx = 0
	}
}

// selectPrevCompletion moves to the previous suggestion
func (m *model) selectPrevCompletion() {
	if !m.hasCompletions() {
		return
	}
	m.completionState.selectedIdx--
	if m.completionState.selectedIdx < 0 {
		m.completionState.selectedIdx = len(m.completionState.suggestions) - 1
	}
}

// acceptCompletion inserts the currently selected completion into the input
func (m *model) acceptCompletion() bool {
	if !m.hasCompletions() {
		return false
	}

	suggestion := m.completionState.suggestions[m.completionState.selectedIdx]

	switch m.completionState.type_ {
	case completionCommand:
		// For commands, add a space after the command
		m.input.SetValue(suggestion + " ")
		m.input.SetCursor(len(suggestion) + 1)

	case completionFilePath:
		// For file paths, keep the command and add the selected file
		cmd := m.completionState.command
		m.input.SetValue("/" + cmd + " " + suggestion)
		m.input.SetCursor(len(cmd) + 1 + len(suggestion))

	case completionTaskID:
		// For task IDs, extract just the ID part (before any parentheses)
		cmd := m.completionState.command
		taskID := suggestion
		if idx := strings.Index(suggestion, " ("); idx != -1 {
			taskID = suggestion[:idx]
		}
		m.input.SetValue("/" + cmd + " " + taskID)
		m.input.SetCursor(len(cmd) + 1 + len(taskID))
	}

	m.clearCompletions()
	return true
}

// getCurrentCompletion returns the currently selected completion
func (m *model) getCurrentCompletion() string {
	if !m.hasCompletions() {
		return ""
	}
	return m.completionState.suggestions[m.completionState.selectedIdx]
}

// getCompletionPreview returns a preview of all suggestions (for display)
func (m *model) getCompletionPreview() string {
	if !m.hasCompletions() {
		return ""
	}

	var sb strings.Builder
	maxDisplay := 8 // Maximum number of suggestions to show

	for i, suggestion := range m.completionState.suggestions {
		if i >= maxDisplay {
			remaining := len(m.completionState.suggestions) - maxDisplay
			sb.WriteString(fmt.Sprintf("... and %d more", remaining))
			break
		}

		if i > 0 {
			sb.WriteString("  ")
		}

		if i == m.completionState.selectedIdx {
			sb.WriteString(">") // Highlight selected
		} else {
			sb.WriteString(" ")
		}

		// Clean up the display for file paths and task IDs
		display := suggestion
		if m.completionState.type_ == completionFilePath && len(display) > 30 {
			display = "..." + display[len(display)-27:]
		}

		sb.WriteString(display)
	}

	return sb.String()
}

// getCompletionTypeDescription returns a human-readable description of completion type
func (m *model) getCompletionTypeDescription() string {
	if m.completionState == nil {
		return ""
	}

	switch m.completionState.type_ {
	case completionCommand:
		return "command"
	case completionFilePath:
		return "file"
	case completionTaskID:
		return "task"
	default:
		return ""
	}
}

// isCompleting returns true if we're in completion mode for a specific type
func (m *model) isCompleting(type_ completionType) bool {
	return m.completionState != nil && m.completionState.type_ == type_
}

// formatCompletionHelp returns help text for the current completion mode
func (m *model) formatCompletionHelp() string {
	if !m.hasCompletions() {
		return ""
	}

	return fmt.Sprintf("[%s] Tab: next  Shift+Tab: prev  Enter: accept  Esc: cancel",
		m.getCompletionTypeDescription())
}
