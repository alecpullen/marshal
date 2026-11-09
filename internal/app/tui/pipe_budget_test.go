package tui

import (
	"sort"
	"testing"
	"time"

	"marshal/internal/acp/acptest"
	"marshal/internal/acp/service"
	"marshal/internal/app/session/sessiontest"
)

// medianOf returns the median of durations.
func medianOf(durations []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// TestAKeyPressOverThePipeBudget holds R10's key press: the TUI now renders
// from a mirror, and one keystroke plus one render over the pipe must cost at
// most a millisecond more than in process (P3 plan 8).
func TestAKeyPressOverThePipeBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	withoutMemoCheck(t)

	// In process: the service itself, as the TUI ran on it before the switch.
	servedHost, served := service.New(service.Deps{State: sessiontest.LargeSession(t)})
	inProcess := New(servedHost, served)

	// Over the pipe: the test agent, with the reads unsettled so the
	// measurement is the transport's and not the settle's comparison.
	pipeHost, pipeSess := acptest.Open(t, service.Deps{State: sessiontest.LargeSession(t)}, acptest.Unsettled())
	overPipe := New(pipeHost, pipeSess)

	for _, m := range []Model{inProcess, overPipe} {
		m.resize(100, 40)
		m.refreshViewport()
	}

	// measure sends one keystroke and renders once, thirty times, and returns
	// the median of the pairs.
	measure := func(m Model) time.Duration {
		durations := make([]time.Duration, 0, 30)
		for i := 0; i < 30; i++ {
			start := time.Now()
			updated, _ := m.Update(keyPress("a"))
			m = updated.(Model)
			_ = m.View()
			durations = append(durations, time.Since(start))
		}
		return medianOf(durations)
	}

	inProcessMedian := measure(inProcess)
	pipeMedian := measure(overPipe)
	if pipeMedian > inProcessMedian+time.Millisecond {
		t.Fatalf("key press over the pipe: median %v, in process %v (budget %v)",
			pipeMedian, inProcessMedian, inProcessMedian+time.Millisecond)
	}
	t.Logf("key press: in process %v, over the pipe %v", inProcessMedian, pipeMedian)
}
