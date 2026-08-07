// Package sddplans discovers and describes the plan files a /sdd run can
// execute. It exists so the TUI can show a plan's task count and resume
// state at pick time instead of discovering them only after the user has
// committed to a run.
package sddplans

import (
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/pipeline"
)

// Candidate is one plan file the picker can offer.
type Candidate struct {
	Path  string
	Name  string
	Slug  string
	Tasks int
	Done  int
	Err   error
}

// Resumable reports whether a previous run left this plan partly complete.
func (c Candidate) Resumable() bool {
	return c.Err == nil && c.Tasks > 0 && c.Done > 0 && c.Done < c.Tasks
}

// Discover lists every *.md file in plansDir (relative to repoRoot), parses
// each, and reads its ledger progress. It never fails: a missing directory
// yields no candidates, and a plan that will not parse is returned with its
// error set so the picker can show why rather than silently omitting it.
func Discover(repoRoot, plansDir string) []Candidate {
	matches, err := filepath.Glob(filepath.Join(repoRoot, plansDir, "*.md"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)

	out := make([]Candidate, 0, len(matches))
	for _, path := range matches {
		name := filepath.Base(path)
		c := Candidate{
			Path: path,
			Name: name,
			Slug: strings.TrimSuffix(name, filepath.Ext(name)),
		}
		plan, err := pipeline.ParsePlan(path)
		if err != nil {
			c.Err = err
			out = append(out, c)
			continue
		}
		c.Tasks = len(plan.Tasks)
		c.Done = completedCount(repoRoot, c.Slug)
		out = append(out, c)
	}
	return out
}

// completedCount reads how many tasks a previous run finished. A missing
// ledger means nothing has run, which is zero, not an error.
func completedCount(repoRoot, slug string) int {
	paths, err := pipeline.NewPaths(repoRoot, slug)
	if err != nil {
		return 0
	}
	done, err := (pipeline.Ledger{Path: paths.Ledger()}).CompletedTasks()
	if err != nil {
		return 0
	}
	return len(done)
}
