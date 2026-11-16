package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"marshal/web/bridge/contract"
)

// A fix's outcomes. A pushed fix is the only one that needs no ask: the record
// returns to in progress until the new head's checks report. Every other
// outcome leaves a "pull" item in the Inbox naming the pull request, the check,
// what was tried and what the user can do.
const (
	pullFixPushed          = "pushed"
	pullFixNotReproduced   = "not_reproduced"
	pullFixGaveUp          = "gave_up"
	pullFixForbiddenChange = "forbidden_change"
	pullFixStopped         = "stopped"
	pullFixHeadMoved       = "head_moved"
	pullFixInvestigated    = "investigated"
)

// A user-requested fix runs under the project's CI fixer limits when it has
// them, and under these otherwise.
const (
	pullFixDefaultMinutes = 30
	pullFixDefaultUSD     = 2
)

// Fix request errors. Each maps to its own status on POST /api/pulls/{id}/fix.
var (
	// ErrPullFixNotFailing is a request naming a check that is not failing on
	// the record's current head. Nothing is dispatched.
	ErrPullFixNotFailing = errors.New("bridge: that check is not failing on the pull request's current head")
	// ErrInvalidPullFixMode is a mode other than fix or investigate.
	ErrInvalidPullFixMode = errors.New(`bridge: fix mode must be "fix" or "investigate"`)
)

// pullFixRunning is ErrPullFixRunning for a named fix, so the refusal carries
// the running fix's id.
type pullFixRunning struct{ ID, Check string }

func (e pullFixRunning) Error() string {
	return fmt.Sprintf("%s: fix %s on %s", ErrPullFixRunning.Error(), e.ID, e.Check)
}

func (e pullFixRunning) Is(target error) bool { return target == ErrPullFixRunning }

// dispatchPullFix validates a fix request, records it, and starts the worker.
// It answers at once: the work is asynchronous.
func (f *Fleet) dispatchPullFix(ctx context.Context, id string, req contract.PullFixRequest, by string) (contract.PullRecord, error) {
	rec, ok := f.pull(id)
	if !ok {
		return contract.PullRecord{}, ErrUnknownPull
	}
	mode := strings.TrimSpace(req.Mode)
	switch mode {
	case "":
		mode = contract.PullFixModeFix
	case contract.PullFixModeFix, contract.PullFixModeInvestigate:
	default:
		return contract.PullRecord{}, ErrInvalidPullFixMode
	}
	check, ok := pullFailingCheck(rec, strings.TrimSpace(req.Check))
	if !ok {
		return contract.PullRecord{}, fmt.Errorf("%w: %q", ErrPullFixNotFailing, req.Check)
	}
	fix := contract.PullFix{ID: newAutoID(), Check: check.Name, HeadSHA: rec.HeadSHA,
		Mode: mode, StartedAt: f.now().UnixMilli()}
	updated, err := f.updatePull(id, func(r *contract.PullRecord) error {
		// Two requests that race are serialised here: the second sees the
		// first's fix and is refused with its id.
		if r.Fix != nil {
			return pullFixRunning{ID: r.Fix.ID, Check: r.Fix.Check}
		}
		r.Fix = &fix
		return nil
	})
	if err != nil {
		return contract.PullRecord{}, err
	}
	// A new dispatch replaces whatever the last one left in the Inbox.
	if err := f.ws.ClearPullAsk(id); err != nil {
		slog.Default().Warn("webbridge: clear pull ask", "pull", id, "err", err)
	}
	f.auditf(AuditEvent{Event: AuditPullFixDispatched, OwnerID: by, RepoID: rec.RepoID,
		Detail: fmt.Sprintf("%s %s %s", id, fix.Check, fix.Mode)})
	f.startPullFix(updated, fix, req)
	return updated, nil
}

// pullFailingCheck is the check a request names, when it is failing on the
// record's current head. An empty name takes the first failing check.
func pullFailingCheck(rec contract.PullRecord, name string) (contract.PullCheck, bool) {
	for _, c := range rec.Checks {
		if c.State != contract.CheckFailed {
			continue
		}
		if name == "" || c.Name == name {
			return c, true
		}
	}
	return contract.PullCheck{}, false
}

// startPullFix runs a dispatched fix. The request has already been answered, so
// the work runs in the background; the seam lets a test run it inline.
func (f *Fleet) startPullFix(rec contract.PullRecord, fix contract.PullFix, req contract.PullFixRequest) {
	if seam := f.auto.pullFix; seam != nil {
		ctx, cancel := f.autoContext()
		defer cancel()
		if err := seam(ctx, rec, fix, req); err != nil {
			slog.Default().Warn("webbridge: pull fix", "pull", rec.ID, "err", err)
		}
		return
	}
	go func() {
		ctx, cancel := f.autoContext()
		defer cancel()
		if err := f.runPullFix(ctx, rec, fix, req); err != nil {
			slog.Default().Warn("webbridge: pull fix", "pull", rec.ID, "err", err)
		}
	}()
}

// runPullFix starts the worker a dispatch asked for. A dispatch is bound to the
// head it was asked about: a worker that starts after the head has moved is
// cancelled rather than run against different code.
func (f *Fleet) runPullFix(ctx context.Context, rec contract.PullRecord, fix contract.PullFix, req contract.PullFixRequest) error {
	if cur, ok := f.pull(rec.ID); !ok || cur.HeadSHA != fix.HeadSHA {
		f.finishPullFix(rec, fix, pullFixHeadMoved, "asked for a fix of "+fix.Check,
			"the pull request's head moved before the worker started, so nothing was run against it")
		return nil
	}
	if rec.Origin == contract.PullOriginFactory {
		return f.runFactoryPullFix(rec, fix, req)
	}
	return f.runFixerPullFix(ctx, rec, fix, req)
}

// runFactoryPullFix issues the item's own repair through the factory
// controller: the factory owns that pull request, so its round and its guards
// are the ones that fix it.
func (f *Fleet) runFactoryPullFix(rec contract.PullRecord, fix contract.PullFix, req contract.PullFixRequest) error {
	if f.factory == nil {
		f.finishPullFix(rec, fix, pullFixGaveUp, "asked the factory to repair its item",
			"this bridge has no factory controller")
		return nil
	}
	reason := "requested by the user"
	if note := strings.TrimSpace(req.Note); note != "" {
		reason += ": " + note
	}
	_, granted, err := f.factory.RequestRepair(rec.FactoryItem, fix.ID, reason)
	if err != nil {
		f.finishPullFix(rec, fix, pullFixGaveUp, "asked the factory to repair its item ("+reason+")", err.Error())
		return nil
	}
	if granted {
		// The allowance was spent, and the user's request bought one round.
		f.auditf(AuditEvent{Event: AuditPullFixGranted, OwnerID: DefaultOwnerID,
			RepoID: rec.RepoID, Detail: rec.ID + " " + rec.FactoryItem})
	}
	// The fix stays on the record while the factory carries it: the round's
	// push moves the head, and the item's own observation clears it then.
	return nil
}

// runFixerPullFix runs the CI fixer's own run against the pull request's head
// branch, pushing to that branch. It reuses startCIFix's recipe, its time and
// cost limits and forbiddenChange unchanged.
func (f *Fleet) runFixerPullFix(ctx context.Context, rec contract.PullRecord, fix contract.PullFix, req contract.PullFixRequest) error {
	repo, ok := f.ws.Repo(rec.RepoID)
	if !ok {
		f.finishPullFix(rec, fix, pullFixGaveUp, "read the repository's forge", "the repository is not registered")
		return nil
	}
	forge, cred, err := f.forgeFor(ctx, repo)
	if err != nil {
		f.finishPullFix(rec, fix, pullFixGaveUp, "read the failing check", err.Error())
		return nil
	}
	failed, err := forge.ListFailedChecks(ctx, repo, fix.HeadSHA, cred)
	if err != nil {
		f.finishPullFix(rec, fix, pullFixGaveUp, "read the failing check", err.Error())
		return nil
	}
	check, ok := failedCheckNamed(failed, fix.Check)
	if !ok {
		f.finishPullFix(rec, fix, pullFixNotReproduced, "read the failing check on the head",
			"the check is not failing on the head the fix was asked about")
		return nil
	}
	key := "pull-fix:" + rec.ID
	if !f.autoClaim(key) {
		f.finishPullFix(rec, fix, pullFixGaveUp, "started a worker", "another fix for this pull request is already running")
		return nil
	}
	ci := f.pullFixerSettings(rec)
	ev := checkEvent{repoID: repo.ID, ref: rec.HeadRef, sha: fix.HeadSHA}
	note := strings.TrimSpace(req.Note)
	if fix.Mode == contract.PullFixModeInvestigate {
		// Investigate-only may not change the repository. The bridge refuses
		// at the end as well, whatever the run did: nothing is pushed.
		note = strings.TrimSpace("Investigate only: do not change any file in the repository. " +
			"Reproduce the failure, find the cause, and report it.\n" + note)
	}
	agentID, err := f.startCIFixWith(ctx, rec.ProjectRoot, ci, repo, forge, cred, ev, check, key, note,
		func(res RecipeResult) { f.finishPullFixRun(rec, fix, res) })
	if err != nil {
		f.autoRelease(key)
		f.finishPullFix(rec, fix, pullFixGaveUp, "started the fix recipe", err.Error())
		return nil
	}
	// The worker's id is known as soon as the run exists, so a stop reaches the
	// worker for as long as it runs, not only once it has finished. A run that
	// ended before this line has already cleared the fix, so the note is then a
	// no-op.
	f.notePullFixAgent(rec.ID, fix.ID, agentID)
	return nil
}

// pullFixerSettings is the fix's settings: it always pushes to the pull
// request's own branch, and its limits are the project's CI fixer limits when
// they are set, else 30 minutes and 2 USD.
func (f *Fleet) pullFixerSettings(rec contract.PullRecord) *CIFixerSettings {
	ci := &CIFixerSettings{MaxMinutes: pullFixDefaultMinutes, MaxUSD: pullFixDefaultUSD}
	if _, project, ok := f.ciFixerSettings(rec.RepoID); ok {
		if project.MaxMinutes > 0 {
			ci.MaxMinutes = project.MaxMinutes
		}
		if project.MaxUSD > 0 {
			ci.MaxUSD = project.MaxUSD
		}
	}
	ci.Enabled, ci.RepoID = true, rec.RepoID
	ci.Push, ci.PushBranches = true, []string{rec.HeadRef}
	return ci
}

// finishPullFixRun applies the guards to a finished fix run and records its
// outcome. forbiddenChange is unchanged: a fix that deletes or disables a test,
// or edits CI configuration, never ships.
func (f *Fleet) finishPullFixRun(rec contract.PullRecord, fix contract.PullFix, res RecipeResult) {
	ctx, cancel := f.autoContext()
	defer cancel()
	// The run's result lands only while it is still the record's fix. A stop
	// clears the fix — and discards the worker — before the run reports, and a
	// record that is gone has nothing left to write to: neither may push, and
	// neither leaves a second ask, because the stop's own ask already says what
	// happened.
	if cur, ok := f.pull(rec.ID); !ok || cur.Fix == nil || cur.Fix.ID != fix.ID {
		slog.Default().Info("webbridge: pull fix finished after it was cleared",
			"pull", rec.ID, "fix", fix.ID)
		return
	}
	f.notePullFixAgent(rec.ID, fix.ID, res.AgentID)
	discard := func() {
		if res.AgentID == "" {
			return
		}
		if err := f.autoDiscard(ctx, res.AgentID); err != nil {
			slog.Default().Warn("webbridge: pull fix discard", "agent", res.AgentID, "err", err)
		}
	}

	result, parsed := res.Parsed.(CIResult)
	switch {
	case res.Err != "":
		f.finishPullFix(rec, fix, pullFixGaveUp, "ran the fix recipe", res.Err)
		discard()
		return
	case !parsed:
		f.finishPullFix(rec, fix, pullFixGaveUp, "ran the fix recipe", errNoStructuredResult)
		discard()
		return
	case !result.Reproduced:
		f.finishPullFix(rec, fix, pullFixNotReproduced, "ran the failing command", strings.TrimSpace(result.Notes))
		discard()
		return
	}
	if fix.Mode == contract.PullFixModeInvestigate {
		// Investigate-only always ends in an ask carrying the finding, and
		// nothing the run wrote leaves it.
		f.finishPullFix(rec, fix, pullFixInvestigated, "reproduced the failure", pullFixFinding(result))
		discard()
		return
	}
	if res.AgentID == "" {
		f.finishPullFix(rec, fix, pullFixGaveUp, "ran the fix recipe", "the run produced no worker")
		return
	}
	files, diffs, err := f.agentDiffs(ctx, res.AgentID)
	if err != nil {
		// A diff that cannot be read cannot be checked, so nothing ships.
		f.finishPullFix(rec, fix, pullFixGaveUp, "read the worker's diff", "could not read the agent's diff: "+err.Error())
		return
	}
	if what, bad := forbiddenChange(files, diffs); bad {
		f.finishPullFix(rec, fix, pullFixForbiddenChange, "checked the worker's diff",
			"refused a forbidden change ("+what+")")
		discard()
		return
	}
	if !result.Fixed {
		reason := "the worker did not fix it"
		if n := strings.TrimSpace(result.Notes); n != "" {
			reason += ": " + n
		}
		f.finishPullFix(rec, fix, pullFixGaveUp, "ran the fix recipe", reason)
		return
	}
	// Ship to the pull request's own branch, through the same gate as any
	// exit. No pull request is opened: the fix lands where it was asked for.
	if a, ok := f.ws.Agent(res.AgentID); ok {
		a.Name = fmt.Sprintf("Fix CI: %s @ %s", fix.Check, shortSHA(fix.HeadSHA))
		a.TargetBranch = rec.BaseRef
		if err := f.ws.PutAgent(a); err != nil {
			f.finishPullFix(rec, fix, pullFixGaveUp, "pushed the fix", err.Error())
			return
		}
	}
	exit, err := f.autoExit(ctx, res.AgentID, ExitOptions{
		CommitMessage: fmt.Sprintf("fix(ci): %s (%s)", fix.Check, shortSHA(fix.HeadSHA)),
		Branch:        rec.HeadRef,
		NoPR:          true,
	})
	switch {
	case err != nil:
		f.finishPullFix(rec, fix, pullFixGaveUp, "pushed the fix", "ship failed: "+err.Error())
	case exit.Blocked:
		f.finishPullFix(rec, fix, pullFixGaveUp, "pushed the fix", "the gate failed, so nothing was pushed")
	case exit.Destination != "push":
		f.finishPullFix(rec, fix, pullFixGaveUp, "pushed the fix", "the ship target is "+exit.Destination+", not push")
	default:
		f.finishPullFix(rec, fix, pullFixPushed, "", "")
	}
}

// pullFixFinding is what an investigate-only run found, in its own words.
func pullFixFinding(result CIResult) string {
	parts := make([]string, 0, 3)
	if result.Cause != "" {
		parts = append(parts, "Cause: "+strings.TrimSpace(result.Cause))
	}
	if result.Fixed {
		parts = append(parts, "The worker believes a fix exists; nothing was changed or pushed.")
	}
	if n := strings.TrimSpace(result.Notes); n != "" {
		parts = append(parts, n)
	}
	if len(parts) == 0 {
		return "the worker reported nothing"
	}
	return strings.Join(parts, "\n")
}

// pullAskAction is what the ask tells the user they can do next.
func pullAskAction(outcome string) string {
	switch outcome {
	case pullFixNotReproduced:
		return "Run the failing command yourself, or fix the check by hand."
	case pullFixForbiddenChange:
		return "The fix would have deleted or disabled a test or changed CI configuration. Fix it by hand, or ask a worker with a note."
	case pullFixStopped:
		return "Ask a worker again when you want it."
	case pullFixHeadMoved:
		return "Ask a worker again for the pull request's new head."
	case pullFixInvestigated:
		return "Open the finding, then fix it by hand or ask a worker."
	default:
		return "Open the check on the forge, fix it by hand, or ask a worker again with a note."
	}
}

// finishPullFix clears the fix and, unless it was pushed, leaves the ask the
// Inbox shows.
func (f *Fleet) finishPullFix(rec contract.PullRecord, fix contract.PullFix, outcome, tried, finding string) {
	if _, err := f.updatePull(rec.ID, func(r *contract.PullRecord) error {
		if r.Fix != nil && r.Fix.ID == fix.ID {
			r.Fix = nil
		}
		return nil
	}); err != nil {
		slog.Default().Warn("webbridge: clear pull fix", "pull", rec.ID, "err", err)
	}
	f.auditf(AuditEvent{Event: AuditPullFixOutcome, OwnerID: DefaultOwnerID,
		RepoID: rec.RepoID, Detail: rec.ID + " " + fix.Check + " " + outcome})
	if outcome == pullFixPushed {
		// A pushed fix needs no ask: the record is in progress until the new
		// head's checks report.
		return
	}
	ask := contract.PullAsk{
		Pull: rec.ID, Check: fix.Check, Outcome: outcome,
		Tried: tried, Finding: finding, Action: pullAskAction(outcome), At: f.now().UnixMilli(),
	}
	if err := f.ws.PutPullAsk(rec.ID, ask); err != nil {
		slog.Default().Warn("webbridge: store pull ask", "pull", rec.ID, "err", err)
	}
}

// notePullFixAgent records the worker a fix started, so a stop can reach it.
func (f *Fleet) notePullFixAgent(id, fixID, agentID string) {
	if agentID == "" {
		return
	}
	if _, err := f.updatePull(id, func(r *contract.PullRecord) error {
		if r.Fix != nil && r.Fix.ID == fixID {
			r.Fix.AgentID = agentID
		}
		return nil
	}); err != nil {
		slog.Default().Warn("webbridge: record pull fix worker", "pull", id, "err", err)
	}
}

// stopPullFix stops the fix on a pull request and clears it.
func (f *Fleet) stopPullFix(ctx context.Context, id, by string) (contract.PullRecord, error) {
	rec, ok := f.pull(id)
	if !ok {
		return contract.PullRecord{}, ErrUnknownPull
	}
	if rec.Fix == nil {
		return rec, nil
	}
	fix := *rec.Fix
	if err := f.stopPullFixWorker(ctx, rec, fix); err != nil {
		return contract.PullRecord{}, err
	}
	f.auditf(AuditEvent{Event: AuditPullFixStopped, OwnerID: by, RepoID: rec.RepoID,
		Detail: rec.ID + " " + fix.Check})
	f.finishPullFix(rec, fix, pullFixStopped, "the user stopped the worker", "")
	out, _ := f.pull(id)
	return out, nil
}

// stopPullFixWorker stops what the fix started: the factory's repair for a
// factory item, the worker's own run otherwise.
func (f *Fleet) stopPullFixWorker(ctx context.Context, rec contract.PullRecord, fix contract.PullFix) error {
	if rec.Origin == contract.PullOriginFactory {
		if f.factory == nil {
			return nil
		}
		item, ok := f.factory.Item(rec.FactoryItem)
		if !ok {
			return nil
		}
		_, err := f.factory.Command(ctx, rec.FactoryItem, FactoryCommand{
			RequestID:       newAutoID(),
			Kind:            "stop",
			ExpectedVersion: item.Version,
			Reason:          "the user stopped the fix on pull request " + rec.ID,
		})
		return err
	}
	if fix.AgentID == "" {
		return nil
	}
	return f.autoDiscard(ctx, fix.AgentID)
}
