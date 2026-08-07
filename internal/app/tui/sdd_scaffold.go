package tui

import (
	"fmt"
	"os"
	"path/filepath"

	"marshal/internal/app/session"
)

// sddStarterPlan is the template written when a user picks "Write a starter
// plan" from an empty plans directory. It is deliberately runnable: the
// headings match what pipeline.ParsePlan requires, so the user can edit the
// bodies and run it without first learning the format from source.
const sddStarterPlan = `# Starter Plan

Replace this file's tasks with your own, then run it with ` + "`/sdd`" + `.

Each task is executed by a fresh implementer subagent that sees only that
task's body, reviewed by a second subagent, and committed by Marshal itself
once the build and test gate passes.

## Global Constraints

Requirements that apply to every task. The implementer sees these with each
task, so put project-wide rules here rather than repeating them.

- Every change must keep ` + "`go build ./...`" + ` and ` + "`go test ./...`" + ` passing.
- Match the surrounding code's style; do not restructure unrelated files.

## Task 1: Describe the first unit of work

Write what a competent engineer with no context needs: which files to touch,
what the behavior should be, and how to tell it worked. One task should be
one commit's worth of change.

## Task 2: Describe the second unit of work

Tasks run in order. Task 2 may rely on Task 1 being complete, but its body
must still stand alone — the subagent implementing it never sees Task 1.
`

// sddStarterPlanName is the scaffolded file's name. Fixed rather than
// generated so the "already exists" guard is meaningful.
const sddStarterPlanName = "starter-plan.md"

// scaffoldSDDPlan writes a runnable starter plan into the configured plans
// directory and tells the user where it landed.
//
// This replaces the old empty-state row, whose value dispatched
// `/sdd generate` and failed with "no such file" — the only action offered
// to a first-time user was a guaranteed error.
func (m *Model) scaffoldSDDPlan() {
	dir := filepath.Join(m.state.WorkingDir, m.state.Config.SDD.PlansDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.state.AddMessage(session.RoleSystem,
			fmt.Sprintf("Could not create %s: %v", dir, err), session.ContentTypePlain)
		return
	}
	path := filepath.Join(dir, sddStarterPlanName)
	if _, err := os.Stat(path); err == nil {
		m.state.AddMessage(session.RoleSystem,
			fmt.Sprintf("%s already exists — edit it, then run /sdd.", path),
			session.ContentTypePlain)
		return
	}
	if err := os.WriteFile(path, []byte(sddStarterPlan), 0o644); err != nil {
		m.state.AddMessage(session.RoleSystem,
			fmt.Sprintf("Could not write %s: %v", path, err), session.ContentTypePlain)
		return
	}
	m.state.AddMessage(session.RoleSystem, fmt.Sprintf(
		"Wrote a starter plan to %s.\n"+
			"Edit its tasks, then run /sdd and pick it. Each `## Task N:` heading "+
			"becomes one implementer dispatch, one review, and one commit.", path),
		session.ContentTypePlain)
}
