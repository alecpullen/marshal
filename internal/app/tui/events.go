package tui

// jobCountMsg is the tea.Msg the job broker pump emits when a JobEvent
// arrives. Handling it sets the model's cached job count so the status
// line renders without polling session.State.
type jobCountMsg struct {
	count int
}

// steeringMsg is the tea.Msg the steering broker pump emits when a
// SteeringEvent lands (F16). Handling it updates the cached queued
// count so the status line shows "queued <n>" and re-arms the pump.
type steeringMsg struct {
	queueLen int
	message  string
}
