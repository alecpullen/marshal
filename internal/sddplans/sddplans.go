// Package sddplans discovers and describes the plan files a /sdd run can
// execute. It exists so the TUI can show a plan's task count and resume
// state at pick time instead of discovering them only after the user has
// committed to a run.
package sddplans

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"marshal/internal/pipeline"
	"marshal/internal/repo"
)

// Candidate is one plan file the picker can offer.
type Candidate struct {
	Path  string
	Name  string
	Slug  string
	Tasks int
	Done  int
	Err   error
	// LedgerErr is set when the plan parsed but its progress ledger could
	// not be read. It is distinct from Err (an unparseable plan): a plan
	// with an unreadable ledger is not a fresh run, and must not be shown
	// as "0 done" as if it had never started.
	LedgerErr error
	// NonTerminal indicates a manifest or checkpoint exists and the run
	// has not reached a terminal state.
	NonTerminal bool

	// DetOps is the number of deterministic operations in the compiled plan.
	DetOps int
	// AgentOps is the number of fallback/agent operations in the compiled plan.
	AgentOps int
	// EstimatedCalls is the estimated number of model calls the plan needs.
	EstimatedCalls int
	// HasBlocks reports whether the plan contains any marshal.* blocks.
	HasBlocks bool
	// Strategy is the effective execution strategy for the plan.
	Strategy pipeline.Strategy
	// Diagnostics carries compile/preflight diagnostics for the plan.
	Diagnostics []pipeline.Diagnostic
}

// Resumable reports whether a previous run left this plan partly complete.
func (c Candidate) Resumable() bool {
	if c.Err != nil || c.LedgerErr != nil || c.Tasks == 0 {
		return false
	}
	if c.NonTerminal {
		return true
	}
	return c.Done > 0 && c.Done < c.Tasks
}

// Discover lists every *.md file in plansDir (relative to repoRoot), parses
// each, and reads its ledger progress. It never fails: a missing directory
// yields no candidates, and a plan that will not parse is returned with its
// error set so the picker can show why rather than silently omitting it.
func Discover(repoRoot, plansDir string) []Candidate {
	root := filepath.Join(repoRoot, plansDir)
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)

	out := make([]Candidate, 0, len(matches))
	for _, path := range matches {
		name := filepath.Base(path)
		rel, relErr := filepath.Rel(root, path)
		if relErr == nil && filepath.Dir(rel) != "." {
			// Keep picker rows readable while making run state unique for
			// same-named plans in different subdirectories.
			name = filepath.ToSlash(rel)
		}
		slug := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
		slug = strings.NewReplacer("/", "-", "\\", "-").Replace(slug)
		c := Candidate{
			Path: path,
			Name: name,
			Slug: slug,
		}
		plan, err := pipeline.ParsePlan(path)
		if err != nil {
			c.Err = err
			out = append(out, c)
			continue
		}
		c.Tasks = len(plan.Tasks)
		c.Done, c.LedgerErr, c.NonTerminal = completedCount(repoRoot, c.Slug)

		// Populate execution metadata from the shared inspection. A plan
		// with diagnostics is still listed; the picker shows why.
		if inspection, ierr := pipeline.InspectPlan(path, repoRoot, pipeline.StrategyAuto); ierr == nil {
			c.DetOps = inspection.Report.Total.DetOps
			c.AgentOps = inspection.Report.Total.AgentOps
			c.EstimatedCalls = inspection.Report.Total.EstCalls
			c.HasBlocks = inspection.HasMarshalBlocks
			c.Strategy = inspection.EffectiveStrategy
			c.Diagnostics = inspection.Diagnostics
		}
		out = append(out, c)
	}
	return out
}

// completedCount reads how many tasks a previous run finished, and whether
// the run is still in-flight. A missing ledger means nothing has run, which
// is zero, not an error. A ledger that exists but cannot be read is surfaced
// as an error so the picker does not masquerade a corrupted ledger as a fresh
// run. A manifest or in-flight checkpoint that has not reached a terminal
// state marks the run non-terminal so it stays resumable even with no ledger
// progress.
func completedCount(repoRoot, slug string) (int, error, bool) {
	paths, err := pipeline.NewPaths(repoRoot, slug)
	if err != nil {
		return 0, err, false
	}
	rs := pipeline.NewRunStore(paths)
	// Check for a manifest; if present, this is a real run.
	if _, err := rs.Manifest(); err == nil {
		last, ok, _ := rs.LastCheckpoint()
		if ok {
			if last.Phase == "run_finished" {
				// Terminal — count done from ledger.
				done, err := (pipeline.Ledger{Path: paths.Ledger()}).CompletedTasks()
				return len(done), err, false
			}
			done, _ := (pipeline.Ledger{Path: paths.Ledger()}).CompletedTasks()
			return len(done), nil, true // non-terminal
		}
	}
	done, err := (pipeline.Ledger{Path: paths.Ledger()}).CompletedTasks()
	if err != nil {
		return 0, err, false
	}
	return len(done), nil, false
}

// slugRe matches characters that are not lowercase ASCII letters, digits, or
// hyphens; they are replaced with a hyphen during slugification.
var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// assertPlansInsideRepo confirms the resolved plans directory is contained
// inside the resolved repository root. Symlinked plans_dir paths that
// point outside the repo are rejected.
func assertPlansInsideRepo(repoRoot, plansDir string) error {
	repoResolved := repo.Canonical(repoRoot)
	plansResolved := repo.Canonical(plansDir)
	if repoResolved == "" || plansResolved == "" {
		return fmt.Errorf("sddplans: empty repository or plans directory")
	}
	rel, err := filepath.Rel(repoResolved, plansResolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("sddplans: plans directory %q is outside the repository root", plansDir)
	}
	return nil
}

// DraftPath resolves the path for a new plan candidate under the configured
// plans directory (relative to repoRoot). It creates no files. The goal is
// slugified to lowercase ASCII hyphenated text, prefixed with the date as
// YYYY-MM-DD-, and suffixed with -2, -3, and so on when the candidate path
// already exists so a draft never overwrites an existing plan.
//
// The configured plans directory must resolve inside the repository. A
// symlinked plans_dir pointing outside the repo is rejected so the child
// authoring runner cannot be tricked into writing elsewhere.
func DraftPath(repoRoot, plansDir, goal string, now time.Time) (string, error) {
	dir := filepath.Join(repoRoot, plansDir)
	if err := assertPlansInsideRepo(repoRoot, dir); err != nil {
		return "", err
	}
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(goal), "-"), "-")
	if slug == "" {
		slug = "plan"
	}
	base := fmt.Sprintf("%s-%s", now.Format("2006-01-02"), slug)
	path := filepath.Join(dir, base+".md")
	for n := 2; ; n++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.md", base, n))
	}
}
