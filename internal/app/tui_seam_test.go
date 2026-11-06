package app

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
)

// modelStateOf returns the agent-side state behind a seam model's session, or
// nil when the model is not built over an in-process adapter. It is this
// package's counterpart of the tui package's stateOf, and it walks past the
// recorders a test may wrap the adapter's session in.
func modelStateOf(model tea.Model) *session.State {
	m, ok := model.(interface{ Session() acpclient.Session })
	if !ok {
		return nil
	}
	sess := m.Session()
	for {
		if in, ok := sess.(*inproc.Session); ok {
			return in.State()
		}
		wrapped, ok := sess.(interface{ Unwrap() acpclient.Session })
		if !ok {
			return nil
		}
		sess = wrapped.Unwrap()
	}
}

// testTUIModel builds a TUI model the way the app does: through the in-process
// adapter over state. It is the package app counterpart of the tui package's
// testModel.
func testTUIModel(state *session.State, opts ...tui.Option) tui.Model {
	host, sess := inproc.New(inproc.Deps{State: state})
	return tui.New(host, sess, opts...)
}

// testTUIModelWith builds a model over an adapter made from d.
func testTUIModelWith(d inproc.Deps, opts ...tui.Option) tui.Model {
	host, sess := inproc.New(d)
	return tui.New(host, sess, opts...)
}
