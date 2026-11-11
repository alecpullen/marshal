package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/trust"
)

// errNotATrust is what applyClientTrust refuses: only a permanent or a session
// trust changes the running session. The host refuses these already; this is
// the last line of defence.
var errNotATrust = errors.New("app: only a permanent or a session trust applies in place")

// applyClientTrust applies a permanent or session trust to the running session
// in place (P4 D8, decision 6): load the trusted layers, record, mark trusted,
// reload, replace the layers, write the note. A failure before the reload
// changes nothing; a failed reload unmarks the session, and a recorded
// permanent trust stays recorded.
func (rt *Runtime) applyClientTrust(ctx context.Context, decision trust.Decision) error {
	switch decision {
	case trust.DecisionTrustPermanent, trust.DecisionTrustSession:
	default:
		return errNotATrust
	}
	// A cancelled call records nothing and writes no note.
	if err := ctx.Err(); err != nil {
		return err
	}

	// Load the trusted layers first: a config that does not load fails the
	// call before anything is recorded, so the session keeps its answer.
	layers, err := config.LoadSessionLayers(rt.HomeDir, rt.WorkingDir, true)
	if err != nil {
		return rt.trustNotApplied(fmt.Errorf("load the trusted config: %w", err))
	}
	// Same template-default rebind as the startup loader and the layer
	// reloader, so an in-session reload cannot resurrect a stale false.
	provider.RebindTemplateDefaults(&layers.Merged)

	// A permanent trust is recorded exactly as embedded mode records it: the
	// canonical directory, the config hash, and SetTrust. A session trust
	// records nothing.
	if decision == trust.DecisionTrustPermanent {
		abs := trust.Canonicalize(rt.WorkingDir)
		hash, _ := trust.ConfigHashFor(rt.WorkingDir)
		if err := trust.NewStore(config.DataDir(rt.HomeDir)).SetTrust(abs, true, hash); err != nil {
			return rt.trustNotApplied(fmt.Errorf("record the trust: %w", err))
		}
	}

	// Mark the session trusted so the layer reloader and the status read the
	// new answer, then reload with the merged config.
	rt.State.SetTrusted(true)
	if rt.ConfigReloader != nil {
		if err := rt.ConfigReloader(layers.Merged); err != nil {
			// The reload changed nothing; the recorded permanent trust stays
			// recorded, and the session is untrusted again.
			rt.State.SetTrusted(false)
			return rt.trustNotApplied(err)
		}
	}
	rt.State.SetLayers(layers)

	note := "Project config trusted for this session; it is in force now."
	if decision == trust.DecisionTrustPermanent {
		note = "Project config trusted; it is in force now."
	}
	rt.State.AddMessage(session.RoleSystem, note, session.ContentTypePlain)
	return nil
}

// trustNotApplied writes the failure note D6's failures use, naming the
// error's first line, and returns err unchanged.
func (rt *Runtime) trustNotApplied(err error) error {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	rt.State.AddMessage(session.RoleSystem, "✗ Trust not applied: "+msg, session.ContentTypePlain)
	return err
}
