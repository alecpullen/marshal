package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// The call queue (P3 R6).
//
// A converted asynchronous call is never made inside the handler that wants
// it: the handler enqueues it and Update starts the head of the queue after
// routing, on the Update goroutine, reading the model as it is then. The call
// itself runs off that goroutine, and its result is handled back on it. One
// call runs at a time, in the order the calls were made, so a call made after
// another sees the model the first one left — the order a blocking call gave
// before, without the freeze.
//
// A step makes no call. It runs on the Update goroutine when it reaches the
// head of the queue and the queue moves on at once. A prompt submitted while
// the queue holds anything waits as a step, so it acts on the model the
// queued calls leave behind.

// callOutcome handles a converted call's result on the Update goroutine
// (P3 R6). It may return a command, as an Update handler does. It is not
// named callResult: spine.go's callResult renders a settled tool call.
type callOutcome func(m *Model) tea.Cmd

// callRun runs a converted call off the Update goroutine and returns the
// handling of its result.
type callRun func(ctx context.Context) callOutcome

// callDoneMsg carries the in-flight call's result back to Update.
type callDoneMsg struct {
	result callOutcome
}

// callQueueEntry is one entry of the queue: a call's start function, or a
// step that makes no call. Exactly one of the two is set.
type callQueueEntry struct {
	// start opens a converted call. It runs on the Update goroutine when the
	// entry reaches the head of the queue, and returns the run to perform off
	// it, or nil when there is nothing left to call.
	start func(m *Model) callRun
	// step runs on the Update goroutine when the entry reaches the head, and
	// the queue moves on at once.
	step func(m *Model) tea.Cmd
}

// enqueueCall appends a converted call. start runs on the Update goroutine
// when the call reaches the head of the queue, reading the model as it is
// then. It returns the run, or nil when there is nothing left to call, in
// which case the queue moves on.
func (m *Model) enqueueCall(start func(m *Model) callRun) {
	m.calls = append(m.calls, callQueueEntry{start: start})
}

// enqueueStep appends a step that makes no call: it runs on the Update
// goroutine when it reaches the head, and the queue moves on at once.
func (m *Model) enqueueStep(step func(m *Model) tea.Cmd) {
	m.calls = append(m.calls, callQueueEntry{step: step})
}

// callsIdle reports whether no call is in flight and nothing is queued.
func (m Model) callsIdle() bool {
	return !m.callInFlight && len(m.calls) == 0
}

// serveCalls runs queued steps from the head until one starts a call or the
// queue is empty, and returns their commands batched. Update calls it after
// every routed message, beside detailQueryCmds.
func (m *Model) serveCalls() tea.Cmd {
	var cmds []tea.Cmd
	for !m.callInFlight && len(m.calls) > 0 {
		entry := m.calls[0]
		m.calls = m.calls[1:]
		if entry.step != nil {
			cmds = append(cmds, entry.step(m))
			continue
		}
		run := entry.start(m)
		if run == nil {
			continue
		}
		// The call borrows m.ctx so the program's shutdown reaches it, and a
		// cancel of its own so a quit can unblock it first.
		ctx, cancel := context.WithCancel(m.ctx)
		m.callInFlight = true
		m.callCancel = cancel
		cmds = append(cmds, callCmd(ctx, run, cancel))
		break
	}
	return tea.Batch(cmds...)
}

// callCmd runs one converted call off the Update goroutine and returns its
// result as a callDoneMsg.
func callCmd(ctx context.Context, run callRun, cancel context.CancelFunc) tea.Cmd {
	return func() tea.Msg {
		result := run(ctx)
		// The call is over, so its context is released here rather than
		// waiting for the result to be handled. Cancelling twice is safe.
		cancel()
		return callDoneMsg{result: result}
	}
}

// handleCallDone handles the in-flight call's result on the Update goroutine.
func (m *Model) handleCallDone(msg callDoneMsg) tea.Cmd {
	m.callInFlight = false
	m.callCancel = nil
	var cmd tea.Cmd
	if msg.result != nil {
		cmd = msg.result(m)
	}
	if m.callQuitWaiting {
		// The quit waited for this call so the session it closes is the one
		// the call left. Nothing is in flight now, so finishQuit proceeds.
		m.callQuitWaiting = false
		return tea.Batch(cmd, m.finishQuit())
	}
	return cmd
}
