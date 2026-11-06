// Package inproc is the in-process transport of the client seam: the
// agent-side service (internal/acp/service), called directly. P3 plan 8
// deletes it.
package inproc

import "marshal/internal/acp/service"

type Deps = service.Deps
type Host = service.Host
type Session = service.Session
type Runner = service.Runner
type PlanRun = service.PlanRun
type EndService = service.EndService
type ReferenceFinder = service.ReferenceFinder

// New builds the in-process host and session over d (service.New).
func New(d Deps) (*Host, *Session) {
	return service.New(d)
}
