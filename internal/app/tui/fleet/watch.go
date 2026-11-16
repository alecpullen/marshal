package fleet

import (
	"time"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// burstWindow is how long notifications for one key collapse into one (F8,
// decision 5).
const burstWindow = 2 * time.Second

// bell is the terminal bell: the program's half of a notification, written
// without the renderer (decision 6).
const bell = "\a"

// Notifier shows a desktop notification. Implementations must not block.
type Notifier interface {
	Notify(n Notification)
}

// Notification is one thing the user should look at (decision 4).
type Notification struct {
	Key  string // the agent's or the submission's ID: the burst's key
	Text string
}

// Watch turns the fleet's states into notifications across programs
// (decision 3). It is safe for one program at a time.
type Watch struct {
	// n shows the desktop notification; nil rings the bell alone.
	n Notifier
	// now is the burst's clock, and the injected one in tests.
	now func() time.Time

	// last is the last state observed, and seen says whether there is one:
	// the first observed state is a baseline, not a batch of changes.
	last acpclient.FleetState
	seen bool

	// sent is each key's last notification time: the burst's record.
	sent map[string]time.Time
}

// NewWatch watches with n, which may be nil for the bell alone. now is the
// burst's clock.
func NewWatch(n Notifier, now func() time.Time) *Watch {
	if now == nil {
		now = time.Now
	}
	return &Watch{n: n, now: now, sent: map[string]time.Time{}}
}

// Observe compares state with the last one observed, sends what survives
// the burst to the notifier, and returns it, so the program can ring the
// bell.
func (w *Watch) Observe(state acpclient.FleetState) []Notification {
	prev, first := w.last, !w.seen
	w.seen, w.last = true, state

	// The first state the watch ever sees is a baseline, and so is the first
	// of each epoch: a fresh full read is not a batch of changes (F8). The
	// epoch check is the watch's own because the watch outlives a program:
	// the state before the change is what the change is compared with, even
	// when the program reading it has just started.
	if first || prev.Epoch != state.Epoch {
		return nil
	}

	now := w.now()
	var out []Notification
	for _, t := range transitions(prev, state) {
		n, ok := notificationOf(t)
		if !ok {
			continue
		}
		if sent, ok := w.sent[n.Key]; ok && now.Sub(sent) < burstWindow {
			continue
		}
		w.sent[n.Key] = now
		out = append(out, n)
		if w.n != nil {
			w.n.Notify(n)
		}
	}
	return out
}

// notificationOf is the notification for one transition, and whether it is
// one at all: a failed agent is an Events line, not a notification (decision
// 4).
func notificationOf(t transition) (Notification, bool) {
	switch t.Kind {
	case transitionNeedsYou:
		if t.AgentID == "" {
			// A submission: its title is what the operator reads, and its ID
			// is the burst's key, so two submissions that share a title are
			// still two notifications.
			return Notification{
				Key:  t.SubmissionID,
				Text: t.Name + " needs you: intake",
			}, true
		}
		switch t.Pending {
		case contract.PendingApproval:
			return Notification{Key: t.AgentID, Text: t.Name + " needs you: approval"}, true
		case contract.PendingQuestion:
			return Notification{Key: t.AgentID, Text: t.Name + " needs you: question"}, true
		}
		// An agent parked on nothing the fleet names still needs the
		// operator; the text says only what is known, as the Events line
		// does.
		return Notification{Key: t.AgentID, Text: t.Name + " needs you"}, true
	case transitionReady:
		return Notification{Key: t.AgentID, Text: t.Name + " is ready"}, true
	default:
		return Notification{}, false
	}
}
