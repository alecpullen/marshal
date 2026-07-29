package tui

// jobCountMsg is the tea.Msg the job broker pump emits when a JobEvent
// arrives. Handling it sets the model's cached job count so the status
// line renders without polling session.State.
type jobCountMsg struct {
	count int
}

// steeringMsg is the tea.Msg the steering broker pump emits when a
// SteeringEvent lands. Handling it updates the cached queued
// count so the status line shows "queued <n>" and re-arms the pump.
type steeringMsg struct {
	queueLen int
	message  string
}

// workspaceMsg is the tea.Msg the workspace broker pump emits when the
// session's active root changes. Handling it re-reads git info for the new
// root so the status line's branch and wt: segments follow immediately,
// then re-arms the pump.
type workspaceMsg struct {
	activeRoot string
}
