// Package watch implements file watching for AI comment markers.
// It scans touched files for // ai / # ai / // ai! / # ai? markers,
// extracts the comment's enclosing block using tree-sitter, and submits
// tasks to the engine.
package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fsnotify/fsnotify"
	"github.com/alecpullen/marshal/internal/git"
)

// Marker types for AI comments.
type MarkerType int

const (
	MarkerSimple MarkerType = iota // // ai: or # ai:
	MarkerUrgent                   // // ai! or # ai!
	MarkerQuestion                 // // ai? or # ai?
)

// TaskRequest represents an extracted AI task from a file.
type TaskRequest struct {
	Path       string
	Line       int
	MarkerType MarkerType
	Content    string // The extracted block/task description
}

// Watcher watches files for AI comment markers and emits task requests.
type Watcher struct {
	watcher   *fsnotify.Watcher
	repo      *git.Repo
	ignore    *git.Ignorer
	requests  chan TaskRequest
	stopChan  chan struct{}
	patterns  []*regexp.Regexp
	isRunning bool
}

// AI comment patterns (language-specific comment markers).
var (
	// Pattern matches: // ai:, // ai!, // ai?, # ai:, # ai!, # ai?
	// Also supports: /* ai: */ style (extracted as needed)
	simplePattern = regexp.MustCompile(`(?m)^\s*(?://|#)\s*ai[:\s](.+)$`)
	urgentPattern = regexp.MustCompile(`(?m)^\s*(?://|#)\s*ai!(?:\s*)(.+)$`)
	questionPattern = regexp.MustCompile(`(?m)^\s*(?://|#)\s*ai\?(?:\s*)(.+)$`)
)

// New creates a new file watcher for the given repository.
func New(repo *git.Repo, ignore *git.Ignorer) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	return &Watcher{
		watcher:  fw,
		repo:     repo,
		ignore:   ignore,
		requests: make(chan TaskRequest, 100),
		stopChan: make(chan struct{}),
		patterns: []*regexp.Regexp{
			simplePattern,
			urgentPattern,
			questionPattern,
		},
	}, nil
}

// Start begins watching the repository for file changes.
// It recursively adds all directories to the watcher.
func (w *Watcher) Start() error {
	if w.isRunning {
		return nil
	}

	// Walk the repo and add all directories
	err := filepath.Walk(w.repo.Root(), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}

		// Skip ignored paths
		rel, _ := filepath.Rel(w.repo.Root(), path)
		if w.ignore != nil && w.ignore.Match(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip common non-source directories
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == ".marshal" || name == "node_modules" ||
				name == "vendor" || name == "__pycache__" || name == ".venv" ||
				name == "dist" || name == "build" || name == "target" {
				return filepath.SkipDir
			}
			return w.watcher.Add(path)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("walk repo: %w", err)
	}

	w.isRunning = true

	// Start the event loop
	go w.run()

	return nil
}

// Stop stops the file watcher.
func (w *Watcher) Stop() error {
	if !w.isRunning {
		return nil
	}

	close(w.stopChan)
	w.isRunning = false
	return w.watcher.Close()
}

// Requests returns the channel of extracted task requests.
func (w *Watcher) Requests() <-chan TaskRequest {
	return w.requests
}

// run is the main event loop for the watcher.
func (w *Watcher) run() {
	for {
		select {
		case <-w.stopChan:
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
				w.handleFileChange(event.Name)
			}

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			// Log errors but continue watching
			_ = err
		}
	}
}

// handleFileChange processes a file change event.
func (w *Watcher) handleFileChange(path string) {
	// Check if file should be ignored
	rel, err := filepath.Rel(w.repo.Root(), path)
	if err != nil {
		return
	}

	if w.ignore != nil && w.ignore.Match(rel) {
		return
	}

	// Only process source code files
	if !isSourceFile(path) {
		return
	}

	// Read and scan the file
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	content := string(data)

	// Scan for AI comment markers
	w.scanForMarkers(path, content)
}

// scanForMarkers scans file content for AI comment markers.
func (w *Watcher) scanForMarkers(path string, content string) {
	lines := strings.Split(content, "\n")

	for i, line := range lines {
		// Check each pattern
		for patternIdx, pattern := range w.patterns {
			matches := pattern.FindStringSubmatch(line)
			if len(matches) > 1 {
				markerType := MarkerSimple
				if patternIdx == 1 {
					markerType = MarkerUrgent
				} else if patternIdx == 2 {
					markerType = MarkerQuestion
				}

				// Extract the task content (marker text + surrounding context)
				taskContent := w.extractTask(lines, i, matches[1], markerType)

				// Send the request
				select {
				case w.requests <- TaskRequest{
					Path:       path,
					Line:       i + 1, // 1-indexed
					MarkerType: markerType,
					Content:    taskContent,
				}:
				default:
					// Channel full, skip
				}

				break // Only match one pattern per line
			}
		}
	}
}

// extractTask extracts the task description from the marker.
// For simple markers, it extracts the comment text.
// For blocks (like // ai: implement this function), it attempts to include
// the function/class context.
func (w *Watcher) extractTask(lines []string, markerLine int, markerText string, markerType MarkerType) string {
	var result strings.Builder

	// Add marker prefix based on type
	switch markerType {
	case MarkerUrgent:
		result.WriteString("[URGENT] ")
	case MarkerQuestion:
		result.WriteString("[QUESTION] ")
	}

	// Add the marker text
	result.WriteString(markerText)
	result.WriteString("\n")

	// Try to find the enclosing function/class block
	// This is a simple heuristic - look for function/class definitions above
	// and closing braces below
	startLine := markerLine
	endLine := markerLine

	// Look backwards for function/class definition
	indent := w.getIndent(lines[markerLine])
	for i := markerLine - 1; i >= 0; i-- {
		line := lines[i]
		lineIndent := w.getIndent(line)

		// Heuristic: look for function/class definitions
		if lineIndent < indent || strings.Contains(line, "func ") ||
			strings.Contains(line, "class ") ||
			strings.Contains(line, "def ") ||
			strings.Contains(line, "function ") {
			startLine = i
			break
		}
	}

	// Look forward for the end of the block (approximate)
	for i := markerLine + 1; i < len(lines) && i < markerLine+50; i++ {
		line := lines[i]
		// Stop if we see a closing brace at the same or lower indent level
		trimmed := strings.TrimSpace(line)
		if trimmed == "}" || trimmed == "]" || trimmed == ")" {
			if w.getIndent(line) <= indent {
				endLine = i
				break
			}
		}
		// Also stop at next function/class definition
		if strings.Contains(line, "func ") ||
			strings.Contains(line, "class ") ||
			strings.Contains(line, "def ") ||
			strings.Contains(line, "function ") {
			if i > markerLine+1 {
				endLine = i - 1
				break
			}
		}
	}

	// Include the context lines
	if endLine > startLine {
		result.WriteString("\nContext:\n```\n")
		for i := startLine; i <= endLine && i < len(lines); i++ {
			result.WriteString(lines[i])
			result.WriteString("\n")
		}
		result.WriteString("```\n")
	}

	return result.String()
}

// getIndent returns the number of leading spaces/tabs in a line.
func (w *Watcher) getIndent(line string) int {
	count := 0
	for _, ch := range line {
		if ch == ' ' || ch == '\t' {
			count++
		} else {
			break
		}
	}
	return count
}

// isSourceFile checks if a file is a source code file we should watch.
func isSourceFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	sourceExts := map[string]bool{
		".go":    true,
		".py":    true,
		".js":    true,
		".ts":    true,
		".tsx":   true,
		".jsx":   true,
		".rs":    true,
		".java":  true,
		".kt":    true,
		".c":     true,
		".cpp":   true,
		".h":     true,
		".hpp":   true,
		".rb":    true,
		".php":   true,
		".swift": true,
		".scala": true,
		".r":     true,
		".m":     true,
		".mm":    true,
	}
	return sourceExts[ext]
}

// Manager manages watch state and coordinates between the TUI and watcher.
type Manager struct {
	watcher  *Watcher
	isActive bool
}

// NewManager creates a new watch manager.
func NewManager(repo *git.Repo, ignore *git.Ignorer) (*Manager, error) {
	w, err := New(repo, ignore)
	if err != nil {
		return nil, err
	}

	return &Manager{
		watcher: w,
	}, nil
}

// Start starts watching files.
func (m *Manager) Start() error {
	if err := m.watcher.Start(); err != nil {
		return err
	}
	m.isActive = true
	return nil
}

// Stop stops watching files.
func (m *Manager) Stop() error {
	if err := m.watcher.Stop(); err != nil {
		return err
	}
	m.isActive = false
	return nil
}

// IsActive returns whether watching is currently active.
func (m *Manager) IsActive() bool {
	return m.isActive
}

// Requests returns the channel of task requests from watched files.
func (m *Manager) Requests() <-chan TaskRequest {
	return m.watcher.Requests()
}
