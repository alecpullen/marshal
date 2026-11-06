package tui

import "fmt"

// scaffoldSDDPlan writes a runnable starter plan into the configured plans
// directory and tells the user where it landed.
//
// This replaces the old empty-state row, whose value dispatched
// `/sdd generate` and failed with "no such file" — the only action offered
// to a first-time user was a guaranteed error. The template and the
// "already exists" guard live in sddplans, so the agent side writes the file
// and the TUI only reports where it landed.
func (m *Model) scaffoldSDDPlan() {
	result, err := m.sess.ScaffoldPlan(m.ctx)
	if err != nil {
		// The agent side's errors read "create <dir>: <err>" and
		// "write <path>: <err>", so this reads as the two failure notes it
		// replaced.
		m.note("Could not " + err.Error())
		return
	}
	if result.Existed {
		m.note(fmt.Sprintf("%s already exists — edit it, then run /sdd.", result.Path))
		return
	}
	m.note(fmt.Sprintf(
		"Wrote a starter plan to %s.\n"+
			"Edit its tasks, then run /sdd and pick it. Each `## Task N:` heading "+
			"becomes one implementer dispatch, one review, and one commit. "+
			"Task 3 demonstrates deterministic `marshal.*` blocks that Marshal applies "+
			"exactly; /sdd preflight shows the strategy and operation counts before you start.", result.Path))
}
