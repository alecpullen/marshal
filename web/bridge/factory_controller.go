package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

func factoryRoundCIState(i FactoryItem, r FactoryRound) string {
	if r.CI == nil || len(i.Approvals) == 0 || r.CI.HeadSHA != r.HeadSHA {
		return "unavailable"
	}
	conf := i.Approvals[len(i.Approvals)-1].Config
	state, _ := EvaluateFactoryCI(*r.CI, conf.ExpectedChecks, conf.NoRequiredCIAcknowledged)
	return state
}
func factoryAllowance(i FactoryItem) int {
	n := 0
	for _, g := range i.Grants {
		n += g.AdditionalRounds
	}
	return n
}
func factoryWorker(i FactoryItem, role string) *FactoryWorker {
	for n := len(i.Workers) - 1; n >= 0; n-- {
		if i.Workers[n].Role == role {
			w := i.Workers[n]
			return &w
		}
	}
	return nil
}
func factoryImplementerRouteChanged(i FactoryItem) bool {
	if len(i.Approvals) < 2 {
		return false
	}
	a := i.Approvals[len(i.Approvals)-1].Config
	b := i.Approvals[len(i.Approvals)-2].Config
	return !bytes.Equal(a.ImplementerRouting, b.ImplementerRouting) || a.Native != b.Native || a.Workspace != b.Workspace
}
func factoryLastEffect(i FactoryItem) *FactoryEffect {
	if len(i.Effects) == 0 {
		return nil
	}
	return &i.Effects[len(i.Effects)-1]
}
func factoryDecision(i *FactoryItem, kind, reason string, now time.Time) {
	i.State = "needs_input"
	i.Decision = &FactoryDecision{ID: fmt.Sprintf("decision-%s-%d", i.ID, len(i.History)+len(i.Effects)+1), Kind: kind, Reason: reason, CreatedAt: now}
	i.History = append(i.History, FactoryEvent{At: now, Kind: kind, Message: reason})
}
func factoryNext(i *FactoryItem, kind, role string, round int, now time.Time) *FactoryEffect {
	e := FactoryEffect{ID: fmt.Sprintf("%s-%d", i.ID, len(i.Effects)+1), Kind: kind, Status: "prepared", WorkerRole: role, Round: round, CreatedAt: now}
	if len(i.Approvals) > 0 {
		e.ApprovalRevision = i.Approvals[len(i.Approvals)-1].Revision
	}
	if w := factoryWorker(*i, role); w != nil {
		e.Worker = w
	}
	if len(i.Rounds) > 0 {
		r := i.Rounds[len(i.Rounds)-1]
		e.ExpectedHead = r.HeadSHA
		e.ExpectedBase = r.BaseSHA
	}
	if e.ExpectedBase == "" {
		e.ExpectedBase = i.BaseSHA
	}
	i.Effects = append(i.Effects, e)
	switch kind {
	case "implement":
		if round > 1 {
			i.State = "repairing"
		} else {
			i.State = "implementing"
		}
		i.Rounds = append(i.Rounds, FactoryRound{Number: round, ApprovalRevision: e.ApprovalRevision, ImplementEffectID: e.ID, BaseSHA: i.BaseSHA, StartedAt: now})
	case "review":
		i.State = "reviewing"
	case "set_ready":
		i.State = "checking"
	case "spawn", "seed", "create_pr":
		i.State = "preparing"
	}
	return &e
}

// AdvanceFactory derives one next effect from durable state and fresh observations.
func AdvanceFactory(item FactoryItem, observed FactoryObservation, now time.Time) (FactoryItem, *FactoryEffect, error) {
	i := factoryCopy(item)
	now = now.UTC()
	if i.State == "queued" && i.BaseSHA == "" {
		if observed.BaseSHA == "" {
			return i, nil, nil
		}
		i.BaseSHA = observed.BaseSHA
	}
	if i.Decision != nil || i.State == "draft" || i.State == "stopped" || i.State == "stopping" || i.State == "recovering" || i.State == "closed" {
		return i, nil, nil
	}
	for _, e := range i.Effects {
		if e.Status != "completed" {
			return i, nil, nil
		}
	}
	if i.State == "ready" {
		if len(i.PRs) == 0 {
			return i, nil, fmt.Errorf("factory: ready item has no PR")
		}
		if observed.PR != nil && observed.PR.State != "open" {
			i.State = "closed"
			return i, nil, nil
		}
		if observed.PR != nil && (observed.PR.HeadSHA != i.PRs[len(i.PRs)-1].HeadSHA || observed.PR.BaseSHA != i.PRs[len(i.PRs)-1].BaseSHA) {
			e := factoryNext(&i, "set_draft", "", 0, now)
			i.State = "checking"
			return i, e, nil
		}
		return i, nil, nil
	}
	last := factoryLastEffect(i)
	kind := ""
	role := ""
	round := 0
	if last == nil {
		if i.State != "queued" {
			return i, nil, nil
		}
		kind = "spawn"
		role = "implementer"
	} else {
		switch last.Kind {
		case "spawn":
			if last.WorkerRole == "implementer" && len(i.PRs) == 0 {
				kind = "seed"
				role = "implementer"
			} else if last.WorkerRole == "reviewer" {
				kind = "review"
				role = "reviewer"
			} else {
				kind = "implement"
				role = "implementer"
			}
		case "seed":
			kind = "publish"
			role = "implementer"
		case "implement":
			if last.Outcome != nil && last.Outcome.Report != nil && last.Outcome.Report.Outcome == "needs_input" {
				kind = "implement"
				role = "implementer"
			} else {
				kind = "publish"
				role = "implementer"
			}
		case "publish":
			if len(i.PRs) == 0 {
				kind = "create_pr"
			} else {
				kind = "park"
				role = "implementer"
			}
		case "create_pr", "resume":
			kind = "implement"
			role = "implementer"
		case "park":
			if last.WorkerRole == "implementer" {
				if len(i.Rounds) == 0 {
					return i, nil, fmt.Errorf("factory: no round for review")
				}
				r := &i.Rounds[len(i.Rounds)-1]
				if observed.CI != nil && observed.CI.HeadSHA == r.HeadSHA {
					c := factoryCopy(*observed.CI)
					r.CI = &c
				}
				if r.CI == nil || r.CI.HeadSHA != r.HeadSHA {
					i.State = "checking"
					return i, nil, nil
				}
				conf := i.Approvals[len(i.Approvals)-1].Config
				ciState, _ := EvaluateFactoryCI(*r.CI, conf.ExpectedChecks, conf.NoRequiredCIAcknowledged)
				switch ciState {
				case "passed", "failed":
					kind = "spawn"
					role = "reviewer"
				case "waiting":
					i.State = "checking"
					since := r.StartedAt
					for n := len(i.Effects) - 1; n >= 0; n-- {
						if i.Effects[n].Kind == "publish" {
							since = i.Effects[n].CreatedAt
							break
						}
					}
					if !since.IsZero() && now.Sub(since) >= time.Duration(conf.CIWaitSeconds)*time.Second {
						factoryDecision(&i, "ci_wait", "required checks are still pending", now)
					}
					return i, nil, nil
				default:
					factoryDecision(&i, "ci_unavailable", "CI evidence unavailable or blocked", now)
					return i, nil, nil
				}
			} else {
				if len(i.Rounds) > 0 {
					r := i.Rounds[len(i.Rounds)-1]
					if r.Review != nil && r.ApprovalRevision == i.Approvals[len(i.Approvals)-1].Revision && r.Review.Outcome == "complete" && len(r.Review.Findings) == 0 && factoryRoundCIState(i, r) == "passed" {
						kind = "set_ready"
					} else {
						if r.ApprovalRevision != i.Approvals[len(i.Approvals)-1].Revision && factoryImplementerRouteChanged(i) {
							kind = "spawn"
							role = "implementer"
						} else {
							kind = "resume"
							role = "implementer"
						}
					}
				} else {
					return i, nil, fmt.Errorf("factory: no round after reviewer park")
				}
			}
		case "review":
			kind = "park"
			role = "reviewer"
		case "set_ready":
			if last.Outcome != nil && last.Outcome.Confirmed && last.Outcome.Error == "" && last.ApprovalRevision == i.Approvals[len(i.Approvals)-1].Revision {
				i.State = "ready"
				return i, nil, nil
			}
			if w := factoryWorker(i, "implementer"); w != nil && w.Parked {
				kind = "resume"
				role = "implementer"
			} else {
				kind = "implement"
				role = "implementer"
			}
		case "set_draft":
			i.State = "needs_input"
			return i, nil, nil
		default:
			return i, nil, fmt.Errorf("factory: unsupported completed effect %q", last.Kind)
		}
	}
	if kind == "implement" {
		round = len(i.Rounds) + 1
		if len(i.Rounds) > 0 {
			prev := i.Rounds[len(i.Rounds)-1]
			if prev.Review != nil && prev.ApprovalRevision == i.Approvals[len(i.Approvals)-1].Revision && prev.Review.Outcome == "complete" && len(prev.Review.Findings) == 0 && factoryRoundCIState(i, prev) == "passed" {
				kind = "set_ready"
				role = ""
				round = 0
			} else if prev.ApprovalRevision == i.Approvals[len(i.Approvals)-1].Revision && len(i.Rounds) > 1 && prev.TreeSHA != "" && prev.TreeSHA == i.Rounds[len(i.Rounds)-2].TreeSHA && prev.Review != nil && len(prev.Review.Findings) > 0 && (len(i.Grants) < 2 || !i.Grants[len(i.Grants)-1].At.After(prev.FinishedAt)) {
				factoryDecision(&i, "no_progress", "repair did not change the tracked tree", now)
				return i, nil, nil
			}
		}
		if kind == "implement" && round > factoryAllowance(i) {
			factoryDecision(&i, "exhausted", "implementation round allowance exhausted", now)
			return i, nil, nil
		}
	}
	if kind == "set_ready" {
		if !factoryReadyEvidence(i, observed) {
			if observed.PR != nil && len(i.Rounds) > 0 && (observed.PR.HeadSHA != i.Rounds[len(i.Rounds)-1].HeadSHA || observed.PR.BaseSHA != i.Rounds[len(i.Rounds)-1].BaseSHA) {
				factoryDecision(&i, "conflict", "PR revision changed after review", now)
			} else {
				i.State = "checking"
			}
			return i, nil, nil
		}
	}
	e := factoryNext(&i, kind, role, round, now)
	return i, e, nil
}
func factoryReadyEvidence(i FactoryItem, o FactoryObservation) bool {
	if len(i.Rounds) == 0 || len(i.PRs) == 0 || o.PR == nil || o.CI == nil {
		return false
	}
	r := i.Rounds[len(i.Rounds)-1]
	p := o.PR
	approval := i.Approvals[len(i.Approvals)-1]
	conf := approval.Config
	if r.ApprovalRevision != approval.Revision {
		return false
	}
	if p.State != "open" || !p.Draft || p.HeadSHA == "" || p.BaseSHA == "" || p.HeadSHA != r.HeadSHA || p.BaseSHA != r.BaseSHA || o.CI.HeadSHA != r.HeadSHA || r.Review == nil || r.Review.Outcome != "complete" || !r.Review.InputsAccessible || r.Review.BaseSHA != r.BaseSHA || r.Review.HeadSHA != r.HeadSHA || r.Review.Findings == nil || len(r.Review.Findings) != 0 {
		return false
	}
	status, _ := EvaluateFactoryCI(*o.CI, conf.ExpectedChecks, conf.NoRequiredCIAcknowledged)
	return status == "passed"
}
func CompleteFactoryEffect(item FactoryItem, effectID string, outcome FactoryOutcome, now time.Time) (FactoryItem, error) {
	i := factoryCopy(item)
	if len(i.Effects) == 0 {
		return i, ErrFactoryConflict
	}
	idx := len(i.Effects) - 1
	e := &i.Effects[idx]
	if e.ID != effectID || e.Status == "completed" || e.Status == "uncertain" || outcome.EffectID != effectID || !outcome.Confirmed {
		return i, ErrFactoryConflict
	}
	e.Status = "completed"
	o := factoryCopy(outcome)
	e.Outcome = &o
	i.Stage = e.Kind
	now = now.UTC()
	if outcome.Error != "" {
		if e.Kind == "set_ready" && outcome.PR != nil && len(i.PRs) > 0 {
			i.PRs[len(i.PRs)-1] = *outcome.PR
		}
		kind := "worker_failed"
		if e.Kind == "set_ready" {
			kind = "conflict"
		}
		factoryDecision(&i, kind, outcome.Error, now)
		return i, nil
	}
	switch e.Kind {
	case "spawn":
		if outcome.Worker == nil || outcome.Worker.Role != e.WorkerRole {
			return item, fmt.Errorf("factory: missing spawned worker")
		}
		i.Workers = append(i.Workers, *outcome.Worker)
	case "seed", "publish":
		if outcome.HeadSHA == "" || outcome.TreeSHA == "" {
			return item, fmt.Errorf("factory: missing Git evidence")
		}
		if e.Kind == "publish" && len(i.Rounds) > 0 {
			r := &i.Rounds[len(i.Rounds)-1]
			r.HeadSHA = outcome.HeadSHA
			r.TreeSHA = outcome.TreeSHA
		}
		if e.Kind == "publish" && len(i.PRs) > 0 {
			i.PRs[len(i.PRs)-1].HeadSHA = outcome.HeadSHA
		}
		if e.Kind == "publish" {
			i.State = "checking"
		}
	case "create_pr":
		if outcome.PR == nil || outcome.PR.Number <= 0 || !outcome.PR.Draft {
			return item, fmt.Errorf("factory: missing draft PR")
		}
		i.PRs = append(i.PRs, *outcome.PR)
	case "implement", "review":
		if outcome.Report == nil || outcome.Report.OperationID != effectID {
			return item, fmt.Errorf("factory: missing matching report")
		}
		if outcome.Report.Outcome == "needs_input" {
			factoryDecision(&i, "question", outcome.Report.Question, now)
			i.Decision.EffectID = e.ID
			break
		}
		if outcome.Report.Outcome != "complete" {
			factoryDecision(&i, "invalid_result", "invalid worker report", now)
			break
		}
		if len(i.Rounds) == 0 {
			return item, fmt.Errorf("factory: missing round")
		}
		r := &i.Rounds[len(i.Rounds)-1]
		if e.Kind == "implement" {
			r.Implementation = outcome.Report
			r.HeadSHA = outcome.HeadSHA
			r.TreeSHA = outcome.TreeSHA
		} else {
			r.Review = outcome.Report
			r.FinishedAt = now
		}
	case "park", "resume":
		for n := range i.Workers {
			if e.Worker != nil && i.Workers[n].AgentID == e.Worker.AgentID {
				i.Workers[n].Parked = e.Kind == "park"
				if e.Kind == "resume" && outcome.Worker != nil && outcome.Worker.AgentID == e.Worker.AgentID && outcome.Worker.SessionID != "" {
					i.Workers[n].SessionID = outcome.Worker.SessionID
				}
			}
		}
	case "set_ready":
		if outcome.PR == nil || outcome.PR.Draft || outcome.PR.HeadSHA != e.ExpectedHead || outcome.PR.BaseSHA != e.ExpectedBase {
			return item, fmt.Errorf("factory: ready confirmation mismatched")
		}
		i.PRs[len(i.PRs)-1] = *outcome.PR
		i.State = "ready"
	case "set_draft":
		if outcome.PR != nil && len(i.PRs) > 0 {
			i.PRs[len(i.PRs)-1] = *outcome.PR
		}
		factoryDecision(&i, "conflict", "PR revision changed while marking ready", now)
	}
	i.History = append(i.History, FactoryEvent{At: now, Kind: "effect_completed", EffectID: effectID, Message: e.Kind})
	return i, nil
}

// FactoryController owns scheduling after Task 5 explicitly opens its recovery gate.
type FactoryController struct {
	store       *FactoryStore
	effects     FactoryEffects
	now         func() time.Time
	mu          sync.Mutex
	ready       bool
	closing     bool
	active      sync.WaitGroup
	execCancels map[string]context.CancelFunc
	// Tests may pause after durable dispatch and before execution admission.
	beforeExecute func()
	// Tests may pause an admitted goroutine before its adapter call.
	afterAdmission func(context.Context)
	wake           chan struct{}
	readyWake      chan struct{}
	after          func(time.Duration) <-chan time.Time
	pauseUntil     time.Time
}

func NewFactoryController(store *FactoryStore, effects FactoryEffects, now func() time.Time) *FactoryController {
	if now == nil {
		now = time.Now
	}
	return &FactoryController{store: store, effects: effects, now: now, execCancels: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1), readyWake: make(chan struct{}, 1), after: time.After}
}
func (c *FactoryController) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
	select {
	case c.readyWake <- struct{}{}:
	default:
	}
}
func (c *FactoryController) admitAfterRecovery() {
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	c.Wake()
}
func (c *FactoryController) isReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready && !c.closing
}

// Quiesce closes dispatch admission and journals unresolved effect outcomes
// before the owning process releases the factory store.
func (c *FactoryController) Quiesce() error {
	c.mu.Lock()
	c.closing = true
	for _, cancel := range c.execCancels {
		cancel()
	}
	c.mu.Unlock()
	c.active.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, snapshot := range c.store.List() {
		for {
			item, err := c.store.Get(snapshot.ID)
			if err != nil {
				return err
			}
			if len(item.Effects) == 0 || item.Effects[len(item.Effects)-1].Status != "dispatched" {
				break
			}
			_, err = c.internalUpdate(item, "shutdown_uncertain", func(dst *FactoryItem) error {
				dst.Effects[len(dst.Effects)-1].Status = "uncertain"
				factoryDecision(dst, "recovery", "bridge shut down before effect outcome was confirmed", c.now())
				return nil
			})
			if errors.Is(err, ErrFactoryConflict) {
				continue
			}
			if err != nil {
				return err
			}
			break
		}
	}
	return nil
}
func (c *FactoryController) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() { defer close(monitorDone); c.monitorReady(runCtx) }()
	defer func() { cancel(); <-monitorDone }()
	for {
		if c.isReady() {
			if err := c.tick(runCtx); err != nil {
				return err
			}
		}
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case <-c.wake:
		case <-c.after(c.pollDelay()):
		}
	}
}
func (c *FactoryController) monitorReady(ctx context.Context) {
	for {
		if c.isReady() {
			c.monitorReadyOnce(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-c.readyWake:
		case <-c.after(c.pollDelay()):
		}
	}
}
func (c *FactoryController) monitorReadyOnce(ctx context.Context) {
	for _, i := range c.store.List() {
		if i.State != "ready" {
			continue
		}
		o, err := c.effects.Observe(ctx, i)
		if err != nil {
			continue
		}
		next, _, err := AdvanceFactory(i, o, c.now())
		if err != nil || reflect.DeepEqual(next, i) {
			continue
		}
		c.mu.Lock()
		if c.closing {
			c.mu.Unlock()
			return
		}
		if _, err = c.internalUpdate(i, "ready_observe", func(dst *FactoryItem) error { *dst = next; dst.Version = i.Version; return nil }); err == nil {
			c.Wake()
		}
		c.mu.Unlock()
	}
}
func (c *FactoryController) pollDelay() time.Duration {
	delay := 15 * time.Second
	for _, i := range c.store.List() {
		if i.State == "checking" || i.State == "ready" {
			if len(i.Approvals) > 0 {
				delay = time.Duration(i.Approvals[len(i.Approvals)-1].Config.CIPollSeconds) * time.Second
			}
			break
		}
	}
	if delay <= 0 {
		delay = 15 * time.Second
	}
	if until := time.Until(c.pauseUntil); until > delay {
		delay = until
	}
	return delay
}
func (c *FactoryController) internalUpdate(i FactoryItem, tag string, mutate func(*FactoryItem) error) (FactoryItem, error) {
	raw, _ := json.Marshal(map[string]any{"internal": tag, "version": i.Version})
	return c.store.Update(i.ID, fmt.Sprintf("internal-%s-%s-%d", i.ID, tag, i.Version), i.Version, raw, mutate, c.now())
}
func factoryPick(items []FactoryItem, idle func(FactoryItem) bool) *FactoryItem {
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].ApprovedAt.Equal(items[b].ApprovedAt) {
			return items[a].ID < items[b].ID
		}
		return items[a].ApprovedAt.Before(items[b].ApprovedAt)
	})
	for n := range items {
		i := &items[n]
		if i.State == "needs_input" && !idle(*i) {
			return i
		}
		switch i.State {
		case "preparing", "implementing", "checking", "reviewing", "repairing", "stopping", "recovering":
			return i
		}
	}
	for n := range items {
		if items[n].State == "queued" {
			return &items[n]
		}
	}
	return nil
}
func (c *FactoryController) tick(ctx context.Context) error {
	if !c.isReady() {
		return nil
	}
	items := c.store.List()
	for _, item := range items {
		for _, effect := range item.Effects {
			if effect.Status == "dispatched" || effect.Status == "uncertain" {
				return nil
			}
		}
	}
	active := factoryPick(items, func(i FactoryItem) bool {
		for _, w := range i.Workers {
			if !w.Parked {
				o, err := c.effects.Observe(ctx, i)
				return err == nil && o.RuntimeKnown && o.TurnIdle && o.ActiveWorkKnown && o.WorkIdle
			}
		}
		return true
	})
	if active == nil {
		return nil
	}
	i := *active
	for _, e := range i.Effects {
		if e.Status == "uncertain" || e.Status == "dispatched" {
			return nil
		}
	}
	if len(i.Effects) > 0 {
		e := i.Effects[len(i.Effects)-1]
		if e.Status == "prepared" && i.State != "stopping" {
			return c.dispatch(ctx, i, e)
		}
	}
	if i.State == "stopping" {
		for _, w := range i.Workers {
			if !w.Parked {
				o, err := c.effects.Observe(ctx, i)
				if err != nil || !o.RuntimeKnown || !o.TurnIdle || !o.ActiveWorkKnown || !o.WorkIdle {
					return nil
				}
				next := factoryCopy(i)
				e := factoryNext(&next, "park", w.Role, 0, c.now())
				saved, err := c.internalUpdate(i, "stop_park", func(dst *FactoryItem) error { *dst = next; dst.Version = i.Version; return nil })
				if err != nil {
					return err
				}
				return c.dispatch(ctx, saved, *e)
			}
		}
		_, err := c.internalUpdate(i, "stopped", func(dst *FactoryItem) error { dst.State = "stopped"; return nil })
		return err
	}
	obs, err := c.effects.Observe(ctx, i)
	if err != nil {
		if delay, ok := retryAfterDuration(err); ok {
			c.pauseUntil = c.now().Add(delay)
			return nil
		}
		_, updateErr := c.internalUpdate(i, "observe_error", func(dst *FactoryItem) error { factoryDecision(dst, "ci_unavailable", err.Error(), c.now()); return nil })
		return updateErr
	}
	next, e, err := AdvanceFactory(i, obs, c.now())
	if err != nil {
		return err
	}
	if e == nil && reflect.DeepEqual(next, i) {
		return nil
	}
	next, err = c.internalUpdate(i, "advance", func(dst *FactoryItem) error { *dst = next; dst.Version = i.Version; return nil })
	if err != nil {
		return err
	}
	if e != nil {
		return c.dispatch(ctx, next, *e)
	}
	return nil
}
func (c *FactoryController) dispatch(ctx context.Context, i FactoryItem, e FactoryEffect) error {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return nil
	}
	if e.Status != "prepared" {
		c.mu.Unlock()
		return nil
	}
	dispatched, err := c.internalUpdate(i, "dispatch", func(dst *FactoryItem) error { dst.Effects[len(dst.Effects)-1].Status = "dispatched"; return nil })
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if c.beforeExecute != nil {
		c.beforeExecute()
	}
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return nil
	}
	done := make(chan error, 1)
	execCtx, cancel := context.WithCancel(ctx)
	c.execCancels[e.ID] = cancel
	c.active.Add(1)
	go func() {
		defer c.active.Done()
		defer func() { c.mu.Lock(); delete(c.execCancels, e.ID); c.mu.Unlock(); cancel() }()
		if c.afterAdmission != nil {
			c.afterAdmission(execCtx)
		}
		if execCtx.Err() != nil {
			done <- nil
			return
		}
		done <- c.finishDispatch(execCtx, dispatched, e)
	}()
	c.mu.Unlock()
	if e.Kind == "implement" || e.Kind == "review" {
		return nil
	}
	return <-done
}
func (c *FactoryController) finishDispatch(ctx context.Context, dispatched FactoryItem, e FactoryEffect) error {
	out, runErr := c.effects.Execute(ctx, dispatched, e)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil
	}
	latest, getErr := c.store.Get(dispatched.ID)
	if getErr != nil {
		return getErr
	}
	if len(latest.Effects) == 0 || latest.Effects[len(latest.Effects)-1].ID != e.ID || latest.Effects[len(latest.Effects)-1].Status != "dispatched" {
		return ErrFactoryConflict
	}
	dispatched = latest
	if runErr != nil {
		out.Error = runErr.Error()
		out.EffectID = e.ID
		if errors.Is(runErr, ErrFactoryInvalidResult) || errors.Is(runErr, ErrFactoryWorkerFailed) {
			out.Confirmed = true
		} else if !errors.Is(runErr, ErrFactoryRefused) {
			_, _ = c.internalUpdate(dispatched, "uncertain", func(dst *FactoryItem) error {
				dst.Effects[len(dst.Effects)-1].Status = "uncertain"
				factoryDecision(dst, "recovery", runErr.Error(), c.now())
				return nil
			})
			return nil
		}
		out.Confirmed = true
	}
	if out.EffectID == "" {
		out.EffectID = e.ID
	}
	next, err := CompleteFactoryEffect(dispatched, e.ID, out, c.now())
	if dispatched.State == "stopping" {
		next.State = "stopping"
		next.Decision = nil
	}
	if err != nil {
		_, _ = c.internalUpdate(dispatched, "uncertain", func(dst *FactoryItem) error {
			dst.Effects[len(dst.Effects)-1].Status = "uncertain"
			factoryDecision(dst, "recovery", err.Error(), c.now())
			return nil
		})
		return nil
	}
	_, err = c.internalUpdate(dispatched, "complete", func(dst *FactoryItem) error { *dst = next; dst.Version = dispatched.Version; return nil })
	if err == nil {
		c.Wake()
	}
	return err
}
func (c *FactoryController) Command(ctx context.Context, itemID string, cmd FactoryCommand) (FactoryItem, error) {
	if cmd.Kind == "reconcile" || cmd.Kind == "recover" {
		before, err := c.store.Get(itemID)
		if err != nil {
			return FactoryItem{}, err
		}
		if cmd.ConfirmPreviousWorkerStopped {
			if len(before.Workers) == 0 || !strings.Contains(cmd.Reason, before.Workers[len(before.Workers)-1].AgentID) {
				return FactoryItem{}, ErrFactoryConflict
			}
		}
		raw, err := json.Marshal(cmd)
		if err != nil {
			return FactoryItem{}, err
		}
		saved, err := c.store.Update(itemID, cmd.RequestID, cmd.ExpectedVersion, raw, func(dst *FactoryItem) error { return nil }, c.now())
		if err != nil || before.Version != cmd.ExpectedVersion {
			return saved, err
		}
		result, err := c.reconcile(ctx, saved, cmd.ConfirmPreviousWorkerStopped, cmd.Reason)
		if err == nil {
			c.Wake()
		}
		return result, err
	}
	if cmd.Kind == "retry" {
		current, err := c.store.Get(itemID)
		if err != nil {
			return FactoryItem{}, err
		}
		if current.State == "recovering" {
			return FactoryItem{}, ErrFactoryConflict
		}
		for _, effect := range current.Effects {
			if effect.Status == "dispatched" || effect.Status == "uncertain" {
				return FactoryItem{}, ErrFactoryConflict
			}
		}
		active := false
		attested := factoryLastAttested(current)
		for _, w := range current.Workers {
			if !w.Parked {
				active = true
				break
			}
		}
		if active && !attested {
			obs, err := c.effects.Observe(ctx, current)
			if err != nil || !obs.RuntimeKnown || !obs.TurnIdle || !obs.ActiveWorkKnown || !obs.WorkIdle {
				return FactoryItem{}, ErrFactoryConflict
			}
		}
	}
	before, _ := c.store.Get(itemID)
	raw, err := json.Marshal(cmd)
	if err != nil {
		return FactoryItem{}, err
	}
	i, err := c.store.Update(itemID, cmd.RequestID, cmd.ExpectedVersion, raw, func(i *FactoryItem) error { return factoryApplyCommand(i, cmd, c.now()) }, c.now())
	if err == nil {
		if cmd.Kind == "stop" && before.Version == cmd.ExpectedVersion {
			if canceler, ok := c.effects.(interface {
				Cancel(context.Context, FactoryItem) error
			}); ok {
				if cancelErr := canceler.Cancel(ctx, i); cancelErr != nil {
					latest, _ := c.store.Get(i.ID)
					_, _ = c.internalUpdate(latest, "cancel_error", func(dst *FactoryItem) error {
						dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "cancel_uncertain", Message: cancelErr.Error()})
						return nil
					})
				}
			}
		}
		c.Wake()
	}
	return i, err
}
func factoryApplyCommand(i *FactoryItem, c FactoryCommand, now time.Time) error {
	switch c.Kind {
	case "approve":
		if i.State != "draft" {
			return ErrFactoryConflict
		}
		a := FactoryApproval{Revision: uint64(len(i.Approvals) + 1), Plan: i.Draft.Plan, Spec: i.Draft.Spec, PlanHash: FactoryPlanHash(i.Draft.Plan, i.Draft.Spec), Config: i.Draft.Config, ApprovedAt: now}
		i.Approvals = append(i.Approvals, a)
		i.Grants = append(i.Grants, FactoryGrant{ID: c.RequestID, Reason: "initial approval", AdditionalRounds: a.Config.MaxRounds, At: now})
		i.State = "queued"
		i.ApprovedAt = now
	case "answer":
		if i.Decision == nil || i.Decision.ID != c.DecisionID || i.Decision.Kind != "question" || strings.TrimSpace(c.Answer) == "" || len(i.Effects) == 0 {
			return ErrFactoryConflict
		}
		e := i.Effects[len(i.Effects)-1]
		if i.Decision.EffectID == "" || i.Decision.EffectID != e.ID || e.Status != "completed" || e.Outcome == nil || e.Outcome.Report == nil || e.Outcome.Report.Outcome != "needs_input" {
			return ErrFactoryConflict
		}
		i.History = append(i.History, FactoryEvent{At: now, Kind: "answer", Message: c.Answer})
		i.Decision = nil
		i.State = "repairing"
	case "stop":
		if i.State == "draft" || i.State == "closed" {
			return ErrFactoryConflict
		}
		if len(i.Effects) > 0 && i.Effects[len(i.Effects)-1].Status == "prepared" {
			e := &i.Effects[len(i.Effects)-1]
			e.Status = "completed"
			e.Outcome = &FactoryOutcome{EffectID: e.ID, Error: "stopped before dispatch", Confirmed: true}
		}
		i.State = "stopping"
	case "retry":
		if i.Decision == nil || i.Decision.ID != c.DecisionID || c.AdditionalRounds <= 0 || strings.TrimSpace(c.Reason) == "" {
			return ErrFactoryConflict
		}
		i.Grants = append(i.Grants, FactoryGrant{ID: c.RequestID, Reason: c.Reason, AdditionalRounds: c.AdditionalRounds, At: now})
		if c.Replacement != nil {
			r := *c.Replacement
			if err := factoryNormalize(&r); err != nil {
				return err
			}
			a := FactoryApproval{Revision: uint64(len(i.Approvals) + 1), Plan: r.Plan, Spec: r.Spec, PlanHash: FactoryPlanHash(r.Plan, r.Spec), Config: r.Config, ApprovedAt: now}
			i.Approvals = append(i.Approvals, a)
		}
		attested := factoryLastAttested(*i)
		i.Decision = nil
		i.State = "repairing"
		if attested {
			factoryNext(i, "resume", "implementer", 0, now)
		}
	case "reconcile", "recover":
		return ErrFactoryConflict
	default:
		return ErrFactoryConflict
	}
	return nil
}
