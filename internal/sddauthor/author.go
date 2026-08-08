// Package sddauthor runs a scoped child that converts an approved feature
// design into one reviewed Marshal executable SDD plan. The child may inspect
// the repository and write exactly one plan artifact; it cannot modify source
// files, run implementation commands, commit, or start /sdd.
package sddauthor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/pipeline"
	"marshal/internal/repo"
)

// Request is the input to one authoring run.
type Request struct {
	Goal     string
	Design   string
	RepoRoot string
	PlanPath string
}

// Result carries the written plan and its shared inspection back to the
// caller for review.
type Result struct {
	Request    Request
	Inspection *pipeline.Inspection
}

// ModelRunner is the minimal model contract the authoring child needs: run
// one prompt to completion.
type ModelRunner interface {
	Run(context.Context, string) error
}

// Runner drives one authoring run: it renders the handoff prompt, runs the
// model, then inspects the written candidate.
type Runner struct {
	Model ModelRunner
}

// Factory builds a Runner for one request. It is wired by the app layer so
// the TUI can start authoring without knowing how the child is constructed.
type Factory func(Request) (*Runner, error)

// NewRunner returns a Runner that drives the given model.
func NewRunner(model ModelRunner) *Runner {
	return &Runner{Model: model}
}

// Run validates the request, runs the model with the rendered handoff
// prompt, and inspects the written candidate. Inspection diagnostics are
// returned inside Result; only construction, model, filesystem, and hard
// inspection errors are returned as Go errors.
func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	if r.Model == nil {
		return Result{}, fmt.Errorf("sddauthor: model runner is nil")
	}
	if !filepath.IsAbs(req.PlanPath) {
		return Result{}, fmt.Errorf("sddauthor: plan path must be absolute: %q", req.PlanPath)
	}
	// Symlink-aware containment: a symlinked plans_dir or repo root must
	// not let the candidate land outside the repository.
	repoRoot := repo.Canonical(req.RepoRoot)
	planPath := repo.Canonical(req.PlanPath)
	rel, err := filepath.Rel(repoRoot, planPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Result{}, fmt.Errorf("sddauthor: plan path %q is outside the repository root", req.PlanPath)
	}
	if err := r.Model.Run(ctx, RenderPrompt(req)); err != nil {
		return Result{}, fmt.Errorf("sddauthor: model: %w", err)
	}
	info, err := os.Stat(req.PlanPath)
	if err != nil || !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("sddauthor: candidate plan was not written: %w", err)
	}
	inspection, err := pipeline.InspectPlan(req.PlanPath, req.RepoRoot, pipeline.StrategyAuto)
	if err != nil {
		return Result{}, fmt.Errorf("sddauthor: inspect candidate: %w", err)
	}
	return Result{Request: req, Inspection: inspection}, nil
}

// RemoveCandidate removes one generated plan artifact. It resolves the
// configured plans directory, verifies that path is a regular file directly
// beneath that directory, and removes only that generated candidate. It
// rejects source paths and traversal, including paths that resolve through
// symlinks outside the plans directory.
func RemoveCandidate(path, repoRoot, plansDir string) error {
	dir := filepath.Join(repoRoot, plansDir)
	// Resolve symlinks for both the plans directory and the candidate so
	// containment cannot be bypassed by a symlink.
	absDir := repo.Canonical(dir)
	abs := repo.Canonical(path)
	rel, err := filepath.Rel(absDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("sddauthor: %q is not inside the plans directory", path)
	}
	if rel == "." || strings.Contains(rel, string(filepath.Separator)) {
		return fmt.Errorf("sddauthor: %q is not a direct plan artifact", path)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("sddauthor: %q is not a regular file", path)
	}
	return os.Remove(path)
}
