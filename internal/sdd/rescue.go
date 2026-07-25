package sdd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AssembleRescueBundle writes <ws.Dir()>/reports/orchestrator-rescue-bundle.md
// containing the current DAG, RepoState, progress log tail, and all report
// files. Returns the bundle path. The rescue *dispatch* (sending the bundle
// to the sdd-rescue role) is P4; P3 just assembles the evidence.
func AssembleRescueBundle(ws *Workspace) (string, error) {
	if _, err := ws.Ensure(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Orchestrator Rescue Bundle\n\n")

	// DAG
	b.WriteString("## DAG (dag.json)\n\n```\n")
	dag, err := LoadDAG(ws)
	if err == nil && dag != nil {
		b.WriteString(formatDAG(dag))
	} else if err != nil {
		b.WriteString("error: " + err.Error())
	}
	b.WriteString("\n```\n\n")

	// RepoState
	b.WriteString("## RepoState (state/repo.json)\n\n```\n")
	rs, err := LoadRepoState(ws)
	if err == nil && rs != nil {
		b.WriteString(fmt.Sprintf("branch=%s target=%s head=%s merged=%v\n", rs.Branch, rs.TargetBranch, rs.Head, rs.Merged))
	} else if err != nil {
		b.WriteString("error: " + err.Error())
	}
	b.WriteString("\n```\n\n")

	// Progress tail
	b.WriteString("## Progress (last 100 lines)\n\n```\n")
	var p Progress
	lines, _ := p.Tail(ws, 100)
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("```\n\n")

	// Reports
	b.WriteString("## Reports\n\n")
	reportsDir := filepath.Join(ws.Dir(), "reports")
	entries, _ := os.ReadDir(reportsDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(reportsDir, e.Name()))
		if err != nil {
			continue
		}
		b.WriteString("### ")
		b.WriteString(e.Name())
		b.WriteString("\n\n```\n")
		b.WriteString(string(data))
		b.WriteString("\n```\n\n")
	}

	path := filepath.Join(ws.Dir(), "reports", "orchestrator-rescue-bundle.md")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return "", fmt.Errorf("sdd rescue: write bundle: %w", err)
	}
	return path, nil
}

// AssembleBranchPackage writes <ws.Dir()>/reports/branch-package-<branch>.md
// containing the DAG, RepoState, and progress log for the given branch.
// Returns the package path.
func AssembleBranchPackage(ws *Workspace, branch string) (string, error) {
	if _, err := ws.Ensure(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Branch Package: ")
	b.WriteString(branch)
	b.WriteString("\n\n")

	b.WriteString("## DAG (dag.json)\n\n```\n")
	dag, err := LoadDAG(ws)
	if err == nil && dag != nil {
		b.WriteString(formatDAG(dag))
	} else if err != nil {
		b.WriteString("error: " + err.Error())
	}
	b.WriteString("\n```\n\n")

	b.WriteString("## RepoState (state/repo.json)\n\n```\n")
	rs, err := LoadRepoState(ws)
	if err == nil && rs != nil {
		b.WriteString(fmt.Sprintf("branch=%s target=%s head=%s merged=%v\n", rs.Branch, rs.TargetBranch, rs.Head, rs.Merged))
	} else if err != nil {
		b.WriteString("error: " + err.Error())
	}
	b.WriteString("\n```\n\n")

	b.WriteString("## Progress (last 50 lines)\n\n```\n")
	var p Progress
	lines, _ := p.Tail(ws, 50)
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("```\n")

	path := filepath.Join(ws.Dir(), "reports", "branch-package-"+branch+".md")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return "", fmt.Errorf("sdd branch package: write: %w", err)
	}
	return path, nil
}

func formatDAG(dag *DAG) string {
	var b strings.Builder
	b.WriteString("spec_path=")
	b.WriteString(dag.SpecPath)
	b.WriteString("\n")
	for _, t := range dag.Tasks {
		b.WriteString(fmt.Sprintf("- %s [%s] %s\n", t.ID, t.Status, t.Title))
	}
	return b.String()
}
