package app

import (
	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/tui"
)

// testTUIModelWith builds a model over an adapter made from d.
func testTUIModelWith(d inproc.Deps, opts ...tui.Option) tui.Model {
	host, sess := inproc.New(d)
	return tui.New(host, sess, opts...)
}
