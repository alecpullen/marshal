package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"marshal/web/bridge/contract"
)

// The observer's cadence. A pull request whose checks have not settled is
// checked every 30 seconds; one whose checks have is checked every 5 minutes,
// because it changes only when a person acts on it. Several records share one
// token's rate-limit budget, so the idle cadence is not tighter than that.
const (
	pullsPendingInterval = 30 * time.Second
	pullsIdleInterval    = 5 * time.Minute
	// pullsTickInterval is how often the observer looks for due records.
	pullsTickInterval = 15 * time.Second
	// pullLogTailLimit is how much of a failed check's log the detail route
	// carries: the tail, where the failure is.
	pullLogTailLimit = 16 << 10
)

// startPullsObserver runs the pull request observer until the fleet closes. It
// is started with the poller, so one goroutine owns the fleet's own polling.
func (f *Fleet) startPullsObserver() {
	go func() {
		observe := func() {
			ctx, cancel := context.WithTimeout(context.Background(), pullsTickInterval)
			defer cancel()
			f.observePulls(ctx)
		}
		observe()
		t := time.NewTicker(pullsTickInterval)
		defer t.Stop()
		for {
			select {
			case <-f.done:
				return
			case <-t.C:
				observe()
			}
		}
	}()
}

// observePulls refreshes every record that is due. A factory item's record is
// copied from the item's own observation and never polled: the controller
// already asked the forge for that evidence, and asking again would spend the
// same rate-limit budget twice for the same answer.
func (f *Fleet) observePulls(ctx context.Context) {
	pass := &pullPass{repos: map[string]*pullRepoPass{}}
	for _, rec := range f.pulls() {
		if ctx.Err() != nil {
			return
		}
		if rec.Origin == contract.PullOriginFactory {
			f.syncFactoryPull(rec)
			continue
		}
		if !f.pullDue(rec) {
			continue
		}
		f.observePull(ctx, rec, pass)
	}
}

// pullDue reports whether a record's evidence is old enough to read again.
func (f *Fleet) pullDue(rec contract.PullRecord) bool {
	if rec.ObservedAt == 0 {
		return true
	}
	every := pullsIdleInterval
	for _, c := range rec.Checks {
		if c.State == contract.CheckRunning || c.State == contract.CheckPending {
			every = pullsPendingInterval
			break
		}
	}
	return f.now().Sub(time.UnixMilli(rec.ObservedAt)) >= every
}

// pullPass is one observer pass: the forge and the open pull requests of each
// repo it touched, so several records on one repo cost one listing.
type pullPass struct {
	repos map[string]*pullRepoPass
}

// pullRepoPass is one repo's state within a pass.
type pullRepoPass struct {
	forge Forge
	cred  Credential
	// forgeErr is why the repo has no usable forge: no forge declared, or a
	// credential that cannot call its API. It is not a transient failure.
	forgeErr error
	// listErr is the open listing's own error, retried next pass.
	listErr error
	open    []PRInfo
	listed  bool
}

// repo resolves the forge for a repo once per pass.
func (f *Fleet) pullPassRepo(ctx context.Context, pass *pullPass, repo Repo) *pullRepoPass {
	if p, ok := pass.repos[repo.ID]; ok {
		return p
	}
	p := &pullRepoPass{}
	p.forge, p.cred, p.forgeErr = f.forgeFor(ctx, repo)
	pass.repos[repo.ID] = p
	return p
}

// list returns the repo's open pull requests once per pass.
func (p *pullRepoPass) list(ctx context.Context, repo Repo) ([]PRInfo, error) {
	if !p.listed {
		p.listed = true
		p.open, p.listErr = p.forge.ListPRs(ctx, repo, PRQuery{}, p.cred)
	}
	return p.open, p.listErr
}

// observePull reads one record's pull request and its checks from the forge.
func (f *Fleet) observePull(ctx context.Context, rec contract.PullRecord, pass *pullPass) {
	repo, ok := f.ws.Repo(rec.RepoID)
	if !ok {
		return
	}
	rp := f.pullPassRepo(ctx, pass, repo)
	if rp.forgeErr != nil {
		// No forge, or a credential that cannot call it. The record says so
		// and is never shown as passing.
		f.notePullUnavailable(rec.ID, rp.forgeErr)
		return
	}
	open, err := rp.list(ctx, repo)
	if err != nil {
		// The listing itself failed. That is a transient read, not evidence
		// about the pull request, so what the record holds stands and the
		// next pass tries again.
		return
	}
	info, found := openPull(open, rec.Number)
	if !found {
		// The open list is the forge's answer: a pull request that is not in
		// it was closed or merged, and the index keeps only open ones.
		f.dropPull(rec.ID)
		return
	}

	now := f.now().UnixMilli()
	next := rec
	next.Number = info.Number
	next.Title, next.URL = info.Title, info.URL
	next.HeadRef, next.BaseRef, next.Draft = info.HeadRef, info.BaseRef, info.Draft
	if info.HeadSHA != "" && info.HeadSHA != rec.HeadSHA {
		// Evidence belongs to a head: the old head's checks are discarded,
		// never carried over, and the new head has none until it reports.
		next.HeadSHA = info.HeadSHA
		next.Checks = []contract.PullCheck{}
	}
	checks, cErr := rp.forge.ListChecks(ctx, repo, next.HeadSHA, rp.cred)
	switch {
	case cErr == nil:
		next.Unavailable = ""
		next.Checks = checks
	case errors.Is(cErr, ErrChecksUnavailable), len(rec.Checks) == 0:
		// Nothing observed for this head can stand in for a check that could
		// not be read.
		next.Unavailable = cErr.Error()
		next.Checks = []contract.PullCheck{}
	default:
		// The checks already held are this head's, from an earlier pass; a
		// failed read does not make them untrue.
		next.Unavailable = ""
	}
	next.ObservedAt = now
	f.applyPull(rec, next)
	if cur, ok := f.pull(rec.ID); ok {
		// A project that asked for automatic fixes gets one here, without a
		// click. A factory item's record is never fixed this way: the factory
		// already repairs inside its own allowance.
		f.autoFixPull(ctx, cur)
	}
}

// openPull finds one pull request in a repo's open list.
func openPull(open []PRInfo, number int) (PRInfo, bool) {
	for _, p := range open {
		if p.Number == number {
			return p, true
		}
	}
	return PRInfo{}, false
}

// notePullUnavailable records why a record's checks cannot be read. A record
// that already says so is left alone: a delta for the same reason every pass is
// noise.
func (f *Fleet) notePullUnavailable(id string, err error) {
	rec, ok := f.pull(id)
	if !ok {
		return
	}
	msg := err.Error()
	if rec.Unavailable == msg && len(rec.Checks) == 0 {
		return
	}
	next := rec
	next.Unavailable = msg
	next.Checks = []contract.PullCheck{}
	next.ObservedAt = f.now().UnixMilli()
	f.applyPull(rec, next)
}

// applyPull stores the record a pass computed. It announces the record only
// when something other than the observation time changed: a poll that found
// nothing new is not a change on the stream.
func (f *Fleet) applyPull(prev, next contract.PullRecord) {
	next.State = f.pullState(next)
	if pullEvidenceEqual(prev, next) {
		if err := f.ws.PutPull(next); err != nil {
			slog.Default().Warn("webbridge: store pull observation", "pull", next.ID, "err", err)
		}
		return
	}
	next.UpdatedAt = next.ObservedAt
	if err := f.recordPull(next); err != nil {
		slog.Default().Warn("webbridge: store pull observation", "pull", next.ID, "err", err)
	}
}

// pullEvidenceEqual reports whether two records hold the same evidence. The
// timestamps and the derived state are not evidence: they follow from it.
func pullEvidenceEqual(a, b contract.PullRecord) bool {
	a.ObservedAt, b.ObservedAt = 0, 0
	a.UpdatedAt, b.UpdatedAt = 0, 0
	a.State, b.State = "", ""
	return reflect.DeepEqual(a, b)
}

// syncFactoryPull copies a factory item's own evidence onto its record. The
// item's pull request and its round's CI are the controller's observation; this
// never calls the forge.
func (f *Fleet) syncFactoryPull(rec contract.PullRecord) {
	item, ok := f.factoryItem(rec.FactoryItem)
	if !ok {
		f.dropPull(rec.ID)
		return
	}
	var pr *FactoryPR
	for i := range item.PRs {
		if item.PRs[i].Number == rec.Number {
			pr = &item.PRs[i]
			break
		}
	}
	if pr == nil {
		f.dropPull(rec.ID)
		return
	}
	if pr.State == "closed" || pr.State == "merged" {
		f.dropPull(rec.ID)
		return
	}
	now := f.now().UnixMilli()
	next := rec
	next.Title = item.Title
	next.URL = pr.URL
	next.HeadRef, next.BaseRef, next.Draft = pr.HeadRef, pr.BaseRef, pr.Draft
	if pr.HeadSHA != "" {
		next.HeadSHA = pr.HeadSHA
	}
	next.Unavailable = ""
	next.Checks = []contract.PullCheck{}
	if ci := factoryRoundCI(item, next.HeadSHA); ci != nil {
		next.Checks = factoryPullChecks(ci.Checks, next.HeadSHA)
	}
	if next.Fix != nil {
		// A fix on a factory item's pull request ends when the item's round
		// pushes a new head, or when the item settles without one: the repair
		// is the factory's round, and only a state the controller is working in
		// can still be carrying it.
		if next.HeadSHA != next.Fix.HeadSHA || !factoryFixRunning(item.State) {
			next.Fix = nil
		}
	}
	next.ObservedAt = now
	f.applyPull(rec, next)
}

// dropPull removes a record the index no longer keeps, logging a failure.
func (f *Fleet) dropPull(id string) {
	if err := f.removePull(id); err != nil {
		slog.Default().Warn("webbridge: remove a pull record", "pull", id, "err", err)
	}
}

// autoFixPull dispatches a fix for a record's first failing check when the
// project asked for failures to be fixed automatically. It is the same dispatch
// a click makes, so every guard, limit and ask applies, and it runs once per
// head: a failure that survives a fix is not fixed again on its own.
func (f *Fleet) autoFixPull(ctx context.Context, rec contract.PullRecord) {
	if rec.Origin == contract.PullOriginFactory || rec.Fix != nil {
		return
	}
	if !f.projectAutoFixesPulls(rec.ProjectRoot) {
		return
	}
	check, ok := pullFailingCheck(rec, "")
	if !ok {
		return
	}
	if !f.claimPullAutoFix(rec.ID, rec.HeadSHA) {
		return
	}
	if _, err := f.dispatchPullFix(ctx, rec.ID, contract.PullFixRequest{Check: check.Name}, DefaultOwnerID); err != nil {
		slog.Default().Warn("webbridge: automatic pull fix", "pull", rec.ID, "err", err)
	}
}

// projectAutoFixesPulls reports whether a project asked for automatic fixes.
func (f *Fleet) projectAutoFixesPulls(root string) bool {
	if root == "" {
		return false
	}
	ps := f.ws.ProjectSettingsFor(root)
	return ps.Automations != nil && ps.Automations.AutoFix
}

// claimPullAutoFix marks that an automatic fix was dispatched for head, and
// reports false when one already was.
func (f *Fleet) claimPullAutoFix(id, head string) bool {
	f.auto.mu.Lock()
	defer f.auto.mu.Unlock()
	if f.auto.pullsAutoFix == nil {
		f.auto.pullsAutoFix = map[string]string{}
	}
	if f.auto.pullsAutoFix[id] == head {
		return false
	}
	f.auto.pullsAutoFix[id] = head
	return true
}

// factoryItem returns one factory item, or false without a controller.
func (f *Fleet) factoryItem(id string) (FactoryItem, bool) {
	if f.factory == nil || id == "" {
		return FactoryItem{}, false
	}
	return f.factory.Item(id)
}

// factoryFixRunning reports whether the controller has work in flight on the
// item, which is the only case a fix dispatched through RequestRepair can still
// be carried by. Every other state — needs_input, ready, stopped, closed, and
// the ones before work starts — has settled without a round, so a fix left on
// the item's record would name a worker that is no longer running.
func factoryFixRunning(state string) bool {
	switch state {
	case "preparing", "implementing", "checking", "reviewing", "repairing", "stopping", "recovering":
		return true
	}
	return false
}

// factoryRoundCI is the item's newest CI observation, when it is evidence for
// head. A round's CI is the controller's observation of the current round's
// push, so it is the item's own answer for the pull request.
func factoryRoundCI(item FactoryItem, head string) *FactoryCI {
	for i := len(item.Rounds) - 1; i >= 0; i-- {
		ci := item.Rounds[i].CI
		if ci == nil {
			continue
		}
		if ci.HeadSHA != head {
			return nil
		}
		c := *ci
		return &c
	}
	return nil
}

// factoryPullChecks maps the item's checks for head onto the index's checks.
func factoryPullChecks(checks []FactoryCheck, head string) []contract.PullCheck {
	out := make([]contract.PullCheck, 0, len(checks))
	for _, c := range checks {
		if head != "" && c.SHA != "" && c.SHA != head {
			continue
		}
		out = append(out, contract.PullCheck{Name: c.Key.Name, State: factoryCheckState(c), URL: c.URL})
	}
	return out
}

// factoryCheckState maps one factory check's status and conclusion.
func factoryCheckState(c FactoryCheck) contract.CheckState {
	if c.Status != "completed" {
		if c.Status == "in_progress" {
			return contract.CheckRunning
		}
		return contract.CheckPending
	}
	switch c.Conclusion {
	case "success", "neutral", "skipped":
		return contract.CheckPassed
	case "failure", "error", "cancelled", "timed_out", "action_required", "stale", "startup_failure":
		return contract.CheckFailed
	default:
		return contract.CheckPending
	}
}

// pullState is a record's derived state. The factory's own verdict is evidence
// the forge cannot give: an item the controller calls ready has passed its
// required checks and its review.
func (f *Fleet) pullState(rec contract.PullRecord) contract.PullState {
	ready := false
	if rec.Origin == contract.PullOriginFactory {
		if item, ok := f.factoryItem(rec.FactoryItem); ok && item.State == "ready" {
			ready = true
		}
	}
	return derivePullState(rec, ready)
}

// derivePullState is the state a client shows, derived from the evidence on the
// current head. A dispatched fix outranks the checks it has not produced yet;
// checks that cannot be read are their own state and never a pass.
func derivePullState(rec contract.PullRecord, factoryReady bool) contract.PullState {
	if rec.Fix != nil {
		return contract.PullBeingFixed
	}
	if rec.Unavailable != "" {
		return contract.PullChecksUnavailable
	}
	failed := 0
	pending := 0
	for _, c := range rec.Checks {
		switch c.State {
		case contract.CheckFailed:
			failed++
		case contract.CheckRunning, contract.CheckPending:
			pending++
		}
	}
	if failed > 0 {
		return contract.PullFailing
	}
	if pending == 0 && (len(rec.Checks) > 0 && !rec.Draft || factoryReady) {
		return contract.PullReady
	}
	return contract.PullInProgress
}

// pullReport renders the engineer's narrated report for a factory item, when it
// has one. It is the detail route's evidence, not the index's.
func (f *Fleet) pullReport(rec contract.PullRecord) string {
	item, ok := f.factoryItem(rec.FactoryItem)
	if !ok {
		return ""
	}
	for i := len(item.Rounds) - 1; i >= 0; i-- {
		r := item.Rounds[i]
		if r.Implementation != nil && r.Implementation.Narration != nil {
			return factoryNarrationText(*r.Implementation.Narration)
		}
	}
	return ""
}

// factoryNarrationText renders one narration's sections, the empty ones left
// out. The pull request body is the full composition; this is the same account
// without the CI and preview sections.
func factoryNarrationText(n FactoryNarration) string {
	var b strings.Builder
	section := func(heading, body string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", heading, strings.TrimSpace(body))
	}
	section("Issue", n.Issue)
	section("Approach", n.Approach)
	section("Existing behaviour", n.ExistingBehaviour)
	section("Alternatives", n.Alternatives)
	section("Trade-offs", n.Tradeoffs)
	section("Implementation choice", n.Choice)
	section("Verification", n.Verification)
	section("Limitations", n.Limitations)
	return strings.TrimRight(b.String(), "\n")
}
