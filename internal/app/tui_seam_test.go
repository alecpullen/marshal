package app

import (
	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
)

// testTUIModel builds a TUI model the way the app does: through the in-process
// adapter, with the state handed in as the legacy option. It is the package
// app counterpart of the tui package's testModel.
func testTUIModel(state *session.State, opts ...tui.Option) tui.Model {
	host, sess := inproc.New(inproc.Deps{State: state})
	all := append([]tui.Option{tui.WithLegacyState(state)}, opts...)
	return tui.New(host, sess, all...)
}

// testTUIModelWith builds a model over an adapter made from d, with d.State
// handed in as the legacy option.
func testTUIModelWith(d inproc.Deps, opts ...tui.Option) tui.Model {
	host, sess := inproc.New(d)
	all := append([]tui.Option{tui.WithLegacyState(d.State)}, opts...)
	return tui.New(host, sess, all...)
}
