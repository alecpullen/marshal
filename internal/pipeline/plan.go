// Package pipeline executes an already-written implementation plan
// task-by-task using a cheap implementer subagent, a per-task reviewer,
// and a final whole-branch reviewer. The controller is deterministic Go;
// no model drives the loop.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// taskHeadingRe matches "## Task N: <title>", "## Task N (label): <title>",
// "### Task N: <title>", or "### Task N (label): <title>" headings.
// Capture groups: N, title.
var taskHeadingRe = regexp.MustCompile(`^#{2,3} Task (\d+)(?:\s*\([^)]*\))?:\s*(.+)$`)

// TaskSpec is one task extracted from a plan file. Body is the task's
// verbatim markdown, from its task heading to the line before the next task
// heading, including any nested "###" headings inside it.
type TaskSpec struct {
	N     int
	Title string
	Body  string
}

// Plan is a parsed plan file. Slug is the file's base name without its
// extension; it names the pipeline's scratch directory.
type Plan struct {
	Path              string
	Slug              string
	GlobalConstraints string
	Tasks             []TaskSpec
}

// ParsePlan reads a plan file and extracts its Global Constraints section
// and its "## Task N:" / "## Task N (label):" / "### Task N:" /
// "### Task N (label):" sections, in file order. It returns an error if the
// plan contains no tasks — an unexecutable plan is a caller error, not an
// empty run.
func ParsePlan(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pipeline plan: read %s: %w", path, err)
	}
	base := filepath.Base(path)
	p := &Plan{
		Path: path,
		Slug: strings.TrimSuffix(base, filepath.Ext(base)),
	}
	lines := strings.Split(string(data), "\n")

	// Task sections: a heading starts a new task; everything up to the next
	// task heading belongs to it (nested "###" headings included).
	var cur *TaskSpec
	var body []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Body = strings.TrimRight(strings.Join(body, "\n"), "\n") + "\n"
		p.Tasks = append(p.Tasks, *cur)
		cur, body = nil, nil
	}
	for _, line := range lines {
		if m := taskHeadingRe.FindStringSubmatch(line); m != nil {
			flush()
			n, err := strconv.Atoi(m[1])
			if err != nil {
				return nil, fmt.Errorf("pipeline plan: task number %q: %w", m[1], err)
			}
			cur = &TaskSpec{N: n, Title: strings.TrimSpace(m[2])}
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	flush()

	if len(p.Tasks) == 0 {
		return nil, fmt.Errorf("pipeline plan: %s contains no `## Task N:` or `### Task N:` sections", path)
	}
	p.GlobalConstraints = extractSection(lines, "## Global Constraints")
	return p, nil
}

// extractSection returns the body of the named "## " section: every line
// after the heading up to the next "## " or "### " heading, with a leading
// "---" separator line and surrounding blank lines trimmed.
func extractSection(lines []string, heading string) string {
	var out []string
	in := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == heading {
			in = true
			continue
		}
		if in && (strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ")) {
			break
		}
		if in {
			if trimmed == "---" {
				break
			}
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Task returns the task with the given number.
func (p *Plan) Task(n int) (TaskSpec, bool) {
	for _, t := range p.Tasks {
		if t.N == n {
			return t, true
		}
	}
	return TaskSpec{}, false
}

// WriteBrief writes a task's verbatim body to path. The brief is the
// implementer's single source of requirements; nothing is added to it and
// nothing is summarised out of it.
func WriteBrief(path string, t TaskSpec) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline brief: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(t.Body), 0o644); err != nil {
		return fmt.Errorf("pipeline brief: write %s: %w", path, err)
	}
	return nil
}
