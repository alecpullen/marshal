package fleet

import (
	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// maxEvents is how many lines the Events ring keeps: the last 200, newest
// last.
const maxEvents = 200

// addEvent appends one line to the Events ring, stamped with the local time
// from the model's clock. Once the ring is full the oldest line is dropped.
func (m *Model) addEvent(text string) {
	m.events = append(m.events, m.now().Format("15:04")+" "+text)
	if len(m.events) > maxEvents {
		// Keep the ring bounded: drop the oldest line and reuse the array.
		copy(m.events, m.events[len(m.events)-maxEvents:])
		m.events = m.events[:maxEvents]
	}
}

// transitionText is the Events line for one transition.
func transitionText(t transition) string {
	switch t.Kind {
	case transitionNeedsYou:
		if t.AgentID == "" {
			// A submission names no agent: its title is what the operator
			// reads.
			return "intake: " + t.Name
		}
		switch t.Pending {
		case contract.PendingApproval:
			return t.Name + " needs you: approval"
		case contract.PendingQuestion:
			return t.Name + " needs you: question"
		}
		return t.Name + " needs you"
	case transitionReady:
		return t.Name + " is ready"
	case transitionFailed:
		return t.Name + " failed"
	}
	return ""
}

// addConnectionEvents adds a line for each change of the stream's connection
// since prev. The epoch rule suppresses agent events only (decision 7), never
// these: after a reconnect the screen says it is connected again.
func (m *Model) addConnectionEvents(prev, next acpclient.FleetState) {
	// A connection change is a change of the state, or of the reason while
	// the stream is down.
	if prev.Conn == next.Conn && !(next.Conn == acpclient.FleetDown && prev.ConnErr != next.ConnErr) {
		return
	}
	switch next.Conn {
	case acpclient.FleetConnected:
		m.addEvent("connected to " + next.Server)
	case acpclient.FleetReconnecting:
		m.addEvent("reconnecting to " + next.Server)
	case acpclient.FleetDown:
		m.addEvent(next.Server + " is down: " + next.ConnErr)
	default:
		// FleetConnecting is before the first connect, which has no line.
	}
}
