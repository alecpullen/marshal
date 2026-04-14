// internal/tui/fuzzy.go
// Fuzzy file picker overlay for pinning files to the composer.
// Live search, preview pane, keyboard navigation.

package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// fuzzyResult is a scored file match.
type fuzzyResult struct {
	path  string
	score int
}

// FuzzyModel is the file picker overlay.
type FuzzyModel struct {
	query     textinput.Model
	allFiles  []string      // indexed at init
	results   []fuzzyResult // filtered + scored
	cursor    int           // selected result
	width     int
	height    int
	selected  bool   // user pressed enter
	cancelled bool   // user pressed esc
	selection string // path to pin
	repoRoot  string // root directory for indexing
}

// fuzzyInitMsg is sent when file indexing completes.
type fuzzyInitMsg struct {
	files []string
}

func newFuzzyModel(repoRoot string) FuzzyModel {
	query := textinput.New()
	query.Prompt = "🔍 "
	query.Placeholder = "type to filter files..."
	query.CharLimit = 0
	query.Focus()

	m := FuzzyModel{
		query:    query,
		results:  []fuzzyResult{},
		repoRoot: repoRoot,
	}

	return m
}

// WithFiles populates the model with indexed files.
func (m FuzzyModel) WithFiles(files []string) FuzzyModel {
	m.allFiles = files
	m.updateResults()
	return m
}

func (m FuzzyModel) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		func() tea.Msg {
			// Index files asynchronously
			files := indexFiles(m.repoRoot)
			return fuzzyInitMsg{files: files}
		},
	)
}

func (m FuzzyModel) Update(msg tea.Msg) (FuzzyModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case fuzzyInitMsg:
		m.allFiles = msg.files
		m.updateResults()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.query.Width = min(40, msg.Width-12)

	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			m.cancelled = true
			return m, nil

		case "enter":
			if m.cursor < len(m.results) {
				m.selected = true
				m.selection = m.results[m.cursor].path
			}
			return m, nil

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case "down", "j":
			if m.cursor < len(m.results)-1 {
				m.cursor++
			}
			return m, nil

		case "ctrl+k":
			m.query.SetValue("")
			m.updateResults()
			return m, nil
		}
	}

	// Update query input
	prevQuery := m.query.Value()
	var cmd tea.Cmd
	m.query, cmd = m.query.Update(msg)
	cmds = append(cmds, cmd)

	// Re-filter if query changed
	if m.query.Value() != prevQuery {
		m.updateResults()
		m.cursor = 0
	}

	return m, tea.Batch(cmds...)
}

func (m FuzzyModel) updateResults() {
	q := strings.ToLower(m.query.Value())
	if q == "" {
		// Show recent/frequent files or just empty
		m.results = []fuzzyResult{}
		return
	}

	var scored []fuzzyResult
	for _, f := range m.allFiles {
		lower := strings.ToLower(f)
		score := 0

		// Exact match bonus
		if lower == q {
			score = 1000
		} else if strings.HasPrefix(lower, q) {
			score = 500
		} else if strings.Contains(lower, q) {
			score = 100
		}

		// Bonus for shorter paths (closer to root)
		depth := strings.Count(f, string(filepath.Separator))
		score -= depth * 10

		if score > 0 {
			scored = append(scored, fuzzyResult{path: f, score: score})
		}
	}

	// Sort by score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Limit results
	if len(scored) > 20 {
		scored = scored[:20]
	}
	m.results = scored
}

func (m FuzzyModel) View(w, h int) string {
	innerW := w - 8
	if innerW < 40 {
		innerW = 40
	}

	// Query bar with match count
	matchCount := lipgloss.NewStyle().
		Foreground(colTx3).
		Render("  " + formatMatchCount(len(m.results)))

	queryBar := lipgloss.JoinHorizontal(lipgloss.Left,
		m.query.View(),
		matchCount,
	)

	// Results list (fixed height)
	resultH := 8
	results := m.renderResults(innerW, resultH)

	// Preview pane
	preview := m.renderPreview(innerW, h-resultH-8)

	// Hint
	hint := stylePromptHint.Render("ctrl+k clear · ↵ pin · esc close")

	content := lipgloss.JoinVertical(lipgloss.Left,
		queryBar,
		"",
		results,
		"",
		preview,
		"",
		hint,
	)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBr3).
		Background(colBg2).
		Padding(1, 2).
		Width(w - 4).
		Height(h - 2).
		Render(content)
}

func (m FuzzyModel) renderResults(w, h int) string {
	if len(m.results) == 0 {
		if m.query.Value() == "" {
			return lipgloss.NewStyle().
				Foreground(colTx3).
				Italic(true).
				Height(h).
				Render("type to search files...")
		}
		return lipgloss.NewStyle().
			Foreground(colTx3).
			Italic(true).
			Height(h).
			Render("no matches")
	}

	var lines []string
	for i, r := range m.results {
		if i >= h {
			break
		}

		style := lipgloss.NewStyle().Foreground(colTx2)
		prefix := "  "
		if i == m.cursor {
			style = lipgloss.NewStyle().
				Foreground(colTx).
				Background(colBl).
				Bold(true)
			prefix = "❯ "
		}

		display := truncate(r.path, w-4)
		lines = append(lines, style.Render(prefix+display))
	}

	// Pad to height
	for len(lines) < h {
		lines = append(lines, "")
	}

	return strings.Join(lines[:h], "\n")
}

func (m FuzzyModel) renderPreview(w, h int) string {
	if m.cursor >= len(m.results) {
		return lipgloss.NewStyle().
			Height(h).
			Foreground(colTx3).
			Render("")
	}

	path := m.results[m.cursor].path

	// Try to read file
	content, err := os.ReadFile(path)
	if err != nil {
		return lipgloss.NewStyle().
			Height(h).
			Foreground(colRd).
			Render("  error reading file")
	}

	// File info header
	info := "  " + path
	lines := strings.Split(string(content), "\n")
	lineCount := len(lines)
	ext := filepath.Ext(path)
	if ext != "" {
		ext = ext[1:] // remove leading dot
	}
	info += "    " + formatInt(lineCount) + " lines  " + ext

	header := lipgloss.NewStyle().
		Background(colBg3).
		Foreground(colTx).
		Width(w).
		Render(info)

	// Preview content (first h-2 lines)
	previewH := h - 2
	var previewLines []string
	for i, line := range lines {
		if i >= previewH {
			break
		}
		previewLines = append(previewLines, truncate(line, w-2))
	}

	preview := lipgloss.NewStyle().
		Foreground(colTx3).
		Height(previewH).
		Render(strings.Join(previewLines, "\n"))

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		preview,
	)
}

func (m FuzzyModel) SelectedFile() (path string, ok bool) {
	if !m.selected {
		return "", false
	}
	return m.selection, true
}

func (m FuzzyModel) Cancelled() bool {
	return m.cancelled
}

// indexFiles walks the repo and returns all non-skipped file paths.
func indexFiles(root string) []string {
	var files []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}

		// Skip directories
		if info.IsDir() {
			name := filepath.Base(path)
			if ftSkip[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip files
		name := filepath.Base(path)
		if ftSkip[name] || strings.HasPrefix(name, ".") {
			return nil
		}

		// Store relative path
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		files = append(files, rel)
		return nil
	})

	if err != nil {
		return []string{}
	}

	return files
}

func formatMatchCount(n int) string {
	if n == 0 {
		return "no matches"
	}
	if n == 1 {
		return "1 match"
	}
	return formatInt(n) + " matches"
}

func formatInt(n int) string {
	if n < 1000 {
		return string(rune('0'+n/100)) + string(rune('0'+(n/10)%10)) + string(rune('0'+n%10))
	}
	return ">999"
}
