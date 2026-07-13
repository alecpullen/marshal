package sdd

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// PlanTask is one task extracted from a markdown plan.
type PlanTask struct {
	Number int
	Title  string
	Body   string
}

// Plan is a parsed implementation plan.
type Plan struct {
	Title             string
	GlobalConstraints string
	Tasks             []PlanTask
}

var (
	taskHeadingRe       = regexp.MustCompile(`^#{2,4}\s+Task\s+(\d+)\s*:\s*(.*)$`)
	globalConstraintsRe = regexp.MustCompile(`(?im)^##\s+Global\s+Constraints\s*$`)
)

// ParsePlan reads a markdown plan file and extracts the title, global
// constraints section, and each "### Task N: Title" heading block. Headings
// inside fenced code blocks are ignored.
func ParsePlan(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sdd: read plan: %w", err)
	}
	return ParsePlanContent(string(data))
}

// ParsePlanContent parses a markdown plan string and returns the extracted
// Plan. It handles the same logic as ParsePlan but accepts the content
// directly.
func ParsePlanContent(content string) (*Plan, error) {
	lines := strings.Split(content, "\n")

	plan := &Plan{}

	// Title: first "# " heading.
	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			plan.Title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			break
		}
	}

	// Global constraints: from the "## Global Constraints" heading to the
	// next "## " heading or "---" separator or first "### Task" heading.
	plan.GlobalConstraints = extractGlobalConstraints(lines)

	// Tasks: walk lines, tracking fence state. When outside a fence and a
	// line matches taskHeadingRe, start a new task. The task body runs
	// until the next task heading (outside a fence) or end of file.
	var currentTask *PlanTask
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		}
		if !inFence {
			if m := taskHeadingRe.FindStringSubmatch(line); m != nil {
				if currentTask != nil {
					plan.Tasks = append(plan.Tasks, *currentTask)
				}
				var num int
				fmt.Sscanf(m[1], "%d", &num)
				currentTask = &PlanTask{
					Number: num,
					Title:  strings.TrimSpace(m[2]),
				}
				continue
			}
		}
		if currentTask != nil {
			currentTask.Body += line + "\n"
		}
	}
	if currentTask != nil {
		plan.Tasks = append(plan.Tasks, *currentTask)
	}

	if len(plan.Tasks) == 0 {
		return nil, fmt.Errorf("sdd: no tasks found in plan (no headings matching '### Task N: ...')")
	}
	return plan, nil
}

// extractGlobalConstraints finds the "## Global Constraints" heading and
// returns the text between it and the next "## " heading, "### " heading,
// or "---" separator.
func extractGlobalConstraints(lines []string) string {
	startIdx := -1
	for i, line := range lines {
		if globalConstraintsRe.MatchString(line) {
			startIdx = i + 1
			break
		}
	}
	if startIdx < 0 {
		return ""
	}
	var b strings.Builder
	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") || strings.TrimSpace(line) == "---" {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}
