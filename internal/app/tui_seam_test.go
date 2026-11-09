package app

import (
	"marshal/internal/acp/service"
	"marshal/internal/app/tui"
)

// testTUIModelWith builds a model over a session made from d. It keeps the
// service in process rather than dialling it over the pipe: package app cannot
// import internal/acp (internal/acp imports app), and these tests drive app's
// builders, not the transport (P3 plan 8, decision 11).
func testTUIModelWith(d service.Deps, opts ...tui.Option) tui.Model {
	host, sess := service.New(d)
	return tui.New(host, sess, opts...)
}
