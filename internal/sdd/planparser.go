package sdd

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// taskHeadingRe matches "### Task N: <title>" headings. Capture groups: N, title.
var taskHeadingRe = regexp.MustCompile(`^### Task (\d+):\s*(.+)$`)

// ParsePlan reads a plan file and extracts `### Task N: <title>` headings into
// a draft DAG. Each task gets ID "T<N>", the heading title, Status pending,
// and empty Deps/Files/Acceptance. The controller emits a draft spec.md from
// this DAG; the spec parser reads the spec's YAML tasks: block (which has
// deps/files/acceptance) to produce the final DAG.
func ParsePlan(planPath string) (*DAG, error) {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return nil, fmt.Errorf("sdd plan: read %s: %w", planPath, err)
	}
	dag := &DAG{SpecPath: planPath}
	for _, line := range strings.Split(string(data), "\n") {
		m := taskHeadingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		dag.Tasks = append(dag.Tasks, DAGTask{
			ID:     "T" + m[1],
			Title:  strings.TrimSpace(m[2]),
			Status: TaskPending,
		})
	}
	return dag, nil
}
