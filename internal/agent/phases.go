package agent

import (
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
)

// TurnPhase is one measured slice of RunTask's preflight: the work that runs
// between the user's prompt being published and the first provider request
// being issued. None of it produces a transcript row, which is why a slow
// turn used to be indistinguishable from a slow model.
//
// Durations are per-phase wall clock, recorded in the order the phases ran.
// Phases do not overlap today, but nothing here assumes that a sum of
// durations equals the preflight total: use PromptReadyMs for wall time.
type TurnPhase struct {
	Name       string
	DurationMs int64
}

// preflightWarnThreshold is the prompt-ready latency above which the phase
// breakdown is logged at Info rather than Debug. Below it the detail is only
// interesting when chasing something, so it stays out of the log by default.
const preflightWarnThreshold = 750 * time.Millisecond

// phaseClock records named preflight phases in execution order. It is
// confined to the preflight, which runs on the turn's own goroutine before
// the provider loop starts, so it needs no locking. It reads time through the
// runner's injected clock so a test can drive it deterministically.
type phaseClock struct {
	now  func() time.Time
	orig time.Time
	ph   []TurnPhase
	cur  string
	at   time.Time
}

// newPhaseClock starts a recorder whose prompt-ready total is measured from
// startedAt, the turn's own start. Pass the same value the turn used so the
// total includes everything RunTask did before the first request.
func newPhaseClock(now func() time.Time, startedAt time.Time) *phaseClock {
	return &phaseClock{now: now, orig: startedAt, at: startedAt}
}

// begin opens an unnamed span. It is a no-op while another span is open, so a
// nested begin can never silently drop time; use span for every phase.
func (c *phaseClock) begin(name string) {
	if c == nil || c.cur != "" {
		return
	}
	c.cur = name
	c.at = c.now()
}

// end closes the span opened by begin and appends it.
func (c *phaseClock) end() {
	if c == nil || c.cur == "" {
		return
	}
	finished := c.now()
	c.ph = append(c.ph, TurnPhase{Name: c.cur, DurationMs: finished.Sub(c.at).Milliseconds()})
	c.cur = ""
	c.at = finished
}

// span measures fn as the named phase.
func (c *phaseClock) span(name string, fn func()) {
	if c == nil {
		fn()
		return
	}
	c.begin(name)
	defer c.end()
	fn()
}

// promptReadyMs is the wall time from turn start to the end of preflight.
func (c *phaseClock) promptReadyMs() int64 {
	if c == nil {
		return 0
	}
	return c.now().Sub(c.orig).Milliseconds()
}

// phases returns a copy of the recorded phases, so a caller cannot mutate the
// recorder's storage.
func (c *phaseClock) phases() []TurnPhase {
	if c == nil || len(c.ph) == 0 {
		return nil
	}
	out := make([]TurnPhase, len(c.ph))
	copy(out, c.ph)
	return out
}

// sessionTimings converts the recorder's phases to the session collector's
// type. It is the one place the two vocabularies meet, which keeps the agent
// package from leaking its own type across the boundary.
func (c *phaseClock) sessionTimings() []session.PhaseTiming {
	ph := c.phases()
	if len(ph) == 0 {
		return nil
	}
	out := make([]session.PhaseTiming, len(ph))
	for i, p := range ph {
		out[i] = session.PhaseTiming{Name: p.Name, DurationMs: p.DurationMs}
	}
	return out
}

// String renders the breakdown as one compact line for logs and diagnostics,
// e.g. "title=120ms snapshot=482ms history=31ms". Phases with no measurable
// cost are omitted; a sub-millisecond phase is reported as "<1ms" rather than
// vanishing, so "it ran and was fast" stays distinguishable from "it did not
// run".
func (c *phaseClock) String() string {
	if c == nil || len(c.ph) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range c.ph {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		if p.DurationMs <= 0 {
			fmt.Fprintf(&b, "%s=<1ms", p.Name)
			continue
		}
		fmt.Fprintf(&b, "%s=%dms", p.Name, p.DurationMs)
	}
	return b.String()
}
