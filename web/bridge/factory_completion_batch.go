package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// factoryMergeStrategies are the reviewed merge strategies a batch step may
// carry. A strategy outside them is not something a forge could apply, so it is
// refused as input rather than stored and rejected by the forge later.
var factoryMergeStrategies = map[string]bool{"merge": true, "squash": true, "rebase": true}

// factoryApplyMergeCommand journals the ordered batch a user confirmed.
//
// The command is the user's own decision about pull requests they reviewed, so
// it is a Studio command: an MCP client's grant names actions of a run, and
// merging reviewed pull requests is not one of them.
//
// Everything the batch will rely on later is checked here against the journal
// alone: the item's version (the store's own expected-version check), the order
// of the reviewed pull requests, the strategy, and the exact head and base each
// review named. A review that no longer describes the linked pull request is
// refused with a conflict rather than adapted, because adapting it would merge
// something the user did not review.
//
// Nothing external happens in this mutation. The batch it stores is the
// authorization every later step is driven under.
func factoryApplyMergeCommand(i *FactoryItem, c FactoryCommand, p *factoryPrincipal, now time.Time) error {
	if p != nil {
		return fmt.Errorf("%w: %s is not an action an MCP client may take", ErrFactoryConflict, c.Kind)
	}
	cmd := c.Completion
	if cmd == nil {
		return fmt.Errorf("%w: %s requires the reviewed pull requests it merges", errFactoryInput, c.Kind)
	}
	// The kind decides the intent. A command that names the other one is
	// refused rather than corrected: completing the item is a separate
	// decision, and silently taking the command's word for it would let a
	// merge-only request complete an item.
	completeAfter := c.Kind == "merge_and_complete"
	if cmd.CompleteAfter != completeAfter {
		return fmt.Errorf("%w: %s cannot carry a different completion intent", errFactoryInput, c.Kind)
	}
	completion := factoryCompletionOf(*i)
	if completion.Batch != nil && completion.Batch.State == "active" {
		return fmt.Errorf("%w: merge batch %s is still active", ErrFactoryConflict, completion.Batch.ID)
	}
	repoID := factoryConfiguredRepoID(*i)
	if repoID == "" {
		return fmt.Errorf("%w: the work item names no repository to merge pull requests in", errFactoryConfig)
	}
	steps, err := factoryMergeSteps(*i, completion, cmd, repoID)
	if err != nil {
		return err
	}
	runChoice, err := factoryMergeRunChoice(*i, cmd.RunChoice)
	if err != nil {
		return err
	}
	completion.Batch = &FactoryMergeBatch{
		ID:            "batch-" + i.ID + "-" + c.RequestID,
		RequestID:     c.RequestID,
		State:         "active",
		CompleteAfter: completeAfter,
		RunChoice:     runChoice,
		Steps:         steps,
	}
	i.Completion = &completion
	i.History = append(i.History, p.event("completion_batch_authorized", factoryBatchAuthorization(steps, cmd.Strategy), "", now))
	return nil
}

// factoryMergeSteps turns the reviewed pull requests into the ordered steps the
// batch drives, in the order the user named them.
func factoryMergeSteps(i FactoryItem, completion FactoryCompletion, cmd *FactoryCompletionCommand, repoID string) ([]FactoryMergeStep, error) {
	if !factoryMergeStrategies[strings.TrimSpace(cmd.Strategy)] {
		return nil, fmt.Errorf("%w: the merge strategy must be merge, squash or rebase", errFactoryInput)
	}
	if len(cmd.PRs) == 0 {
		return nil, fmt.Errorf("%w: a merge batch names the reviewed pull requests in order", errFactoryInput)
	}
	seen := make(map[string]bool, len(cmd.PRs))
	steps := make([]FactoryMergeStep, 0, len(cmd.PRs))
	for _, pr := range cmd.PRs {
		if pr.Number <= 0 || strings.TrimSpace(pr.NodeID) == "" || strings.TrimSpace(pr.HeadSHA) == "" || strings.TrimSpace(pr.BaseRef) == "" {
			return nil, fmt.Errorf("%w: every reviewed pull request names its number, node, head and base", errFactoryInput)
		}
		if seen[pr.NodeID] {
			return nil, fmt.Errorf("%w: pull request %s is reviewed twice", errFactoryInput, pr.NodeID)
		}
		seen[pr.NodeID] = true
		// The review has to describe a linked pull request of the configured
		// repository, at exactly the head and base the user reviewed. A
		// mismatch is the evidence having moved on, not bad input: the surface
		// refreshes the review and asks again.
		if pr.RepoID != repoID {
			return nil, fmt.Errorf("%w: pull request #%d is not the configured repository's; refresh the review", ErrFactoryConflict, pr.Number)
		}
		n, ok := factoryLinkIndex(completion.Links, pr.RepoID, pr.NodeID)
		if !ok {
			return nil, fmt.Errorf("%w: pull request #%d is not linked with its reviewed identity; refresh the review", ErrFactoryConflict, pr.Number)
		}
		link := completion.Links[n].PR
		if link.Number != pr.Number || link.HeadSHA != pr.HeadSHA || link.BaseRef != pr.BaseRef {
			return nil, fmt.Errorf("%w: pull request #%d changed since it was reviewed; refresh the review", ErrFactoryConflict, pr.Number)
		}
		steps = append(steps, FactoryMergeStep{
			RepoID:   pr.RepoID,
			Number:   pr.Number,
			NodeID:   pr.NodeID,
			HeadSHA:  pr.HeadSHA,
			BaseRef:  pr.BaseRef,
			Strategy: cmd.Strategy,
			State:    "unattempted",
		})
	}
	return steps, nil
}

// factoryMergeRunChoice validates the decision the batch records about the run
// that follows it. While the item's own run is active — or its state cannot be
// established from the journal — the user has to say what happens to it, so a
// silent batch can never cancel or race a turn it did not account for.
func factoryMergeRunChoice(i FactoryItem, choice string) (string, error) {
	choice = strings.TrimSpace(choice)
	if choice != "" && choice != "continue" && choice != "request_stop" {
		return "", fmt.Errorf("%w: the run choice must be continue or request_stop", errFactoryInput)
	}
	if choice == "" && factoryBatchRunActive(i) {
		return "", fmt.Errorf("%w: choose continue or request_stop for the work item's active run", ErrFactoryConflict)
	}
	return choice, nil
}

// factoryBatchRunActive reports whether the item's own run is active, judged
// from the journal alone: work the factory still owns, with a worker that is
// running or an effect that is in flight. An item that is draft, closed or
// stopped owns no run, and an unknown state does own one.
func factoryBatchRunActive(i FactoryItem) bool {
	if !factoryOwnsState(i.State) {
		return false
	}
	for _, w := range i.Workers {
		if !w.Parked {
			return true
		}
	}
	for _, e := range i.Effects {
		if e.Status == "prepared" || e.Status == "dispatched" {
			return true
		}
	}
	return false
}

// factoryBatchAuthorization renders the journalled statement of what was
// authorized: the order the pull requests will merge in and the strategy.
func factoryBatchAuthorization(steps []FactoryMergeStep, strategy string) string {
	numbers := make([]string, 0, len(steps))
	for _, s := range steps {
		numbers = append(numbers, fmt.Sprintf("#%d", s.Number))
	}
	return fmt.Sprintf("%s merge of %s", strategy, strings.Join(numbers, ", "))
}

// factoryBatchNextStep returns the first entry that is not confirmed, which is
// the only entry the batch may act on.
func factoryBatchNextStep(b *FactoryMergeBatch) (int, bool) {
	if b == nil {
		return 0, false
	}
	for n := range b.Steps {
		if b.Steps[n].State != "confirmed" {
			return n, true
		}
	}
	return 0, false
}

// factoryMergeStepMerged reports whether pr proves the step's reviewed pull
// request merged at exactly the reviewed identity. A closed or re-headed pull
// request proves nothing of the kind.
func factoryMergeStepMerged(pr FactoryPR, step FactoryMergeStep) bool {
	return pr.NodeID == step.NodeID && pr.HeadSHA == step.HeadSHA && pr.BaseRef == step.BaseRef && pr.State == "merged"
}

// factoryCompletionMergeForge opens the item's forge for a merge batch: the
// capability the reconcile reads pull requests with, plus the base forge the
// merge capability is looked up on. A value without the unexported forge
// method — a test double, or no effects at all — has no forge, and the batch
// states that instead of guessing at what a pull request is.
func (c *FactoryController) completionMergeForge(ctx context.Context, item FactoryItem) (Repo, FactoryForge, Forge, Credential, error) {
	opener, ok := c.effects.(factoryForgeOpener)
	if !ok {
		return Repo{}, nil, nil, Credential{}, fmt.Errorf("%w: no forge is available to merge reviewed pull requests", errFactoryConfig)
	}
	repo, base, forge, cred, err := opener.forge(ctx, item)
	if err != nil {
		return Repo{}, nil, nil, Credential{}, err
	}
	if forge == nil {
		return Repo{}, nil, nil, Credential{}, fmt.Errorf("%w: no forge is available to merge reviewed pull requests", errFactoryConfig)
	}
	return repo, forge, base, cred, nil
}

// advanceCompletionBatches drives the active merge batches forward.
//
// One batch mutates per pass, so at most one reviewed merge is sent per tick
// and an entry that could not be proven holds every later entry of its own
// batch until a later pass reads the forge again. A pass that changed nothing —
// a forge that will not open, a pull request the forge will not describe — sent
// nothing and dispatched nothing, so it must not hold the next item's batch
// back: that is what keeps one broken repository from stalling every other
// item's merges. Whether a batch moved is read from the journal rather than
// assumed, because the journal write is what authorizes every merge.
func (c *FactoryController) advanceCompletionBatches(ctx context.Context) error {
	if c == nil || c.store == nil {
		return nil
	}
	for _, item := range c.store.List() {
		completion := factoryCompletionOf(item)
		if completion.Batch == nil || completion.Batch.State != "active" {
			continue
		}
		if err := c.advanceCompletionBatch(ctx, item); err != nil {
			return err
		}
		if after, err := c.store.Get(item.ID); err != nil || after.Version != item.Version {
			// The batch moved, so this pass already spent its one mutation and
			// may have sent a merge: nothing else may go out beside it.
			return nil
		}
	}
	return nil
}

// advanceCompletionBatch advances the item's active batch by one step.
func (c *FactoryController) advanceCompletionBatch(ctx context.Context, item FactoryItem) error {
	completion := factoryCompletionOf(item)
	batch := completion.Batch
	if batch == nil || batch.State != "active" || len(batch.Steps) == 0 {
		return nil
	}
	n, pending := factoryBatchNextStep(batch)
	if !pending {
		// Every entry confirmed: this is the recovery of the window between
		// the final merge and its completion, and the deterministic record
		// identity is what makes the completion appear exactly once.
		return c.finalizeCompletionBatch(item.ID)
	}
	if state := batch.Steps[n].State; state == "dispatched" || state == "uncertain" {
		return c.reconcileCompletionStep(ctx, item, n)
	}
	return c.runCompletionStep(ctx, item, n)
}

// reconcileCompletionStep settles an entry a previous pass dispatched by
// reading the authoritative pull request state and head. Only a merge proven at
// the reviewed identity confirms it; everything else stops the batch with the
// uncertainty recorded, because a merge that may have landed must never be
// sent again.
func (c *FactoryController) reconcileCompletionStep(ctx context.Context, item FactoryItem, n int) error {
	repo, forge, _, cred, err := c.completionMergeForge(ctx, item)
	if err != nil {
		// An earlier pass dispatched this entry, so its outcome is genuine
		// uncertainty: the forge that would prove the merge cannot be reached.
		// Recording that and stopping is what the design requires — waiting
		// silently would leave the batch active for good, hold every later
		// merge, and never tell the user why.
		return c.stopCompletionStep(item.ID, n, "uncertain", "the merge could not be observed: "+err.Error())
	}
	step := item.Completion.Batch.Steps[n]
	pr, readErr := forge.GetFactoryPR(ctx, repo, step.Number, cred)
	if readErr == nil && factoryMergeStepMerged(pr, step) {
		return c.confirmCompletionStep(item.ID, n, pr, "merged")
	}
	reason := "the reviewed pull request is not merged at its reviewed head"
	switch {
	case readErr != nil:
		reason = "the merge could not be observed: " + readErr.Error()
	case pr.State == "closed":
		reason = "pull request #" + fmt.Sprint(step.Number) + " is closed without merging"
	case pr.NodeID != step.NodeID || pr.HeadSHA != step.HeadSHA || pr.BaseRef != step.BaseRef:
		reason = "pull request #" + fmt.Sprint(step.Number) + " no longer matches its reviewed identity"
	}
	return c.stopCompletionStep(item.ID, n, "uncertain", reason)
}

// runCompletionStep prepares and dispatches one reviewed merge.
//
// The fresh read comes first: a pull request that moved, closed or already
// merged since the review is settled from that observation alone, and never by
// sending a merge. Only a reviewed pull request that is still open at its
// reviewed head is prepared, dispatched and merged, in that order, so a crash
// anywhere leaves a journal a later pass can reconcile.
func (c *FactoryController) runCompletionStep(ctx context.Context, item FactoryItem, n int) error {
	repo, forge, base, cred, err := c.completionMergeForge(ctx, item)
	if err != nil {
		// Nothing was dispatched: the batch never got as far as sending a
		// merge, so this entry failed rather than became uncertain, and the
		// batch stops with the reason the surface renders. Retrying a forge
		// that will not open would wedge the batch and every later merge
		// behind an active batch nobody can clear.
		return c.stopCompletionStep(item.ID, n, "failed", err.Error())
	}
	step := item.Completion.Batch.Steps[n]
	pr, readErr := forge.GetFactoryPR(ctx, repo, step.Number, cred)
	if readErr != nil {
		// A read repeats safely, and nothing was dispatched: the entry stays
		// exactly as it is until the forge answers again.
		return nil
	}
	if pr.NodeID != step.NodeID || pr.HeadSHA != step.HeadSHA || pr.BaseRef != step.BaseRef {
		return c.stopCompletionStep(item.ID, n, "failed", fmt.Sprintf("pull request #%d no longer matches its reviewed identity", step.Number))
	}
	if pr.State == "closed" {
		return c.stopCompletionStep(item.ID, n, "failed", fmt.Sprintf("pull request #%d is closed without merging", step.Number))
	}
	if pr.State == "merged" {
		// A merge that already happened at the reviewed identity is confirmed
		// with external provenance, and no merge is sent for it.
		return c.confirmCompletionStep(item.ID, n, pr, "external merge")
	}
	if wrote, err := c.setCompletionStepState(item.ID, n, "prepared", ""); err != nil || !wrote {
		return err
	}
	if wrote, err := c.setCompletionStepState(item.ID, n, "dispatched", ""); err != nil || !wrote {
		return err
	}
	merger, mergerErr := FactoryPRMergerFor(base)
	if mergerErr != nil {
		return c.stopCompletionStep(item.ID, n, "failed", mergerErr.Error())
	}
	mergeErr := merger.MergeFactoryPR(ctx, repo, FactoryPRMergeRequest{
		Number:   step.Number,
		NodeID:   step.NodeID,
		HeadSHA:  step.HeadSHA,
		BaseRef:  step.BaseRef,
		Strategy: step.Strategy,
	}, cred)
	// The outcome is read, never assumed: the merge call's own result is only
	// evidence of what the pull request now says.
	after, afterErr := forge.GetFactoryPR(ctx, repo, step.Number, cred)
	if afterErr == nil && factoryMergeStepMerged(after, step) {
		return c.confirmCompletionStep(item.ID, n, after, "merged")
	}
	if errors.Is(mergeErr, ErrFactoryMergeRejected) {
		return c.stopCompletionStep(item.ID, n, "failed", mergeErr.Error())
	}
	reason := fmt.Sprintf("the merge of #%d is not proven", step.Number)
	if afterErr != nil {
		reason = "the merge of #" + fmt.Sprint(step.Number) + " could not be observed after dispatch: " + afterErr.Error()
	} else if mergeErr != nil {
		reason = "the merge of #" + fmt.Sprint(step.Number) + " is unproven: " + mergeErr.Error()
	}
	return c.stopCompletionStep(item.ID, n, "uncertain", reason)
}

// setCompletionStepState journals one step transition. The journal write is
// what authorizes the step, so a write the journal refused reports that it did
// not happen and the caller makes no forge call: reported says whether the
// mutation was appended, and a conflict is reported as "not written" rather
// than as an error, because the next pass re-reads the entry the winner wrote.
func (c *FactoryController) setCompletionStepState(itemID string, n int, state, result string) (bool, error) {
	current, err := c.store.Get(itemID)
	if err != nil {
		return false, nil
	}
	now := c.now()
	_, err = c.internalUpdate(current, "completion_step_"+state, func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		batch := completion.Batch
		if batch == nil || batch.State != "active" || n >= len(batch.Steps) || !factoryMergeStepOpen(batch.Steps[n].State) {
			return ErrFactoryConflict
		}
		batch.Steps[n].State = state
		if result != "" {
			batch.Steps[n].Result = result
		}
		completion.Batch = batch
		dst.Completion = &completion
		if state == "dispatched" {
			dst.History = append(dst.History, FactoryEvent{At: now, Kind: "completion_merge_dispatched", Message: fmt.Sprintf("%s merge of #%d", batch.Steps[n].Strategy, batch.Steps[n].Number)})
		}
		return nil
	})
	if errors.Is(err, ErrFactoryConflict) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// factoryMergeStepOpen reports whether a step is still waiting to be driven.
// A confirmed, failed or uncertain entry is settled history.
func factoryMergeStepOpen(state string) bool {
	return state == "unattempted" || state == "prepared"
}

// confirmCompletionStep journals one confirmed merge. The pull request's fresh
// observation moves with the confirmation, so the link the surface renders and
// the step the batch drove are never out of step, and an all-confirmed batch
// is finalized in the same mutation.
func (c *FactoryController) confirmCompletionStep(itemID string, n int, pr FactoryPR, result string) error {
	current, err := c.store.Get(itemID)
	if err != nil {
		return nil
	}
	now := c.now()
	_, err = c.internalUpdate(current, "completion_step_confirmed", func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		batch := completion.Batch
		if batch == nil || batch.State != "active" || n >= len(batch.Steps) || batch.Steps[n].State == "confirmed" {
			return ErrFactoryConflict
		}
		step := batch.Steps[n]
		step.State = "confirmed"
		step.Result = result
		batch.Steps[n] = step
		completion.Batch = batch
		if idx, ok := factoryLinkIndex(completion.Links, step.RepoID, step.NodeID); ok {
			completion.Links[idx].PR = pr
			completion.Links[idx].ObservedAt = now
			completion.Links[idx].Fresh = true
		}
		if _, pending := factoryBatchNextStep(batch); !pending {
			completion = factoryCompleteBatch(dst, completion, now)
		}
		dst.Completion = &completion
		dst.History = append(dst.History, FactoryEvent{At: now, Kind: "completion_merge_confirmed", Message: fmt.Sprintf("#%d %s", step.Number, result)})
		return nil
	})
	if errors.Is(err, ErrFactoryConflict) {
		return nil
	}
	return err
}

// stopCompletionStep journals a failed or uncertain entry and stops the batch.
// Every later entry stays unattempted: a stopped batch needs a new explicit
// user decision and fresh evidence, and nothing resumes it on its own.
func (c *FactoryController) stopCompletionStep(itemID string, n int, state, reason string) error {
	current, err := c.store.Get(itemID)
	if err != nil {
		return nil
	}
	now := c.now()
	_, err = c.internalUpdate(current, "completion_step_"+state, func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		batch := completion.Batch
		if batch == nil || batch.State != "active" || n >= len(batch.Steps) || batch.Steps[n].State == "confirmed" {
			return ErrFactoryConflict
		}
		step := batch.Steps[n]
		step.State = state
		step.Result = reason
		batch.Steps[n] = step
		batch.State = "stopped"
		completion.Batch = batch
		dst.Completion = &completion
		dst.History = append(dst.History, FactoryEvent{At: now, Kind: "completion_batch_stopped", Message: fmt.Sprintf("#%d %s: %s", step.Number, state, reason)})
		return nil
	})
	if errors.Is(err, ErrFactoryConflict) {
		return nil
	}
	return err
}

// finalizeCompletionBatch records an all-confirmed batch's already-authorized
// completion. It runs on the recovery path — a crash between the final merge
// and the completion, or a batch whose last confirmation landed without it —
// and the deterministic record identity is what makes it appear exactly once.
func (c *FactoryController) finalizeCompletionBatch(itemID string) error {
	current, err := c.store.Get(itemID)
	if err != nil {
		return nil
	}
	now := c.now()
	_, err = c.internalUpdate(current, "completion_batch_finalize", func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		if completion.Batch == nil || completion.Batch.State != "active" {
			return ErrFactoryConflict
		}
		if _, pending := factoryBatchNextStep(completion.Batch); pending {
			return ErrFactoryConflict
		}
		completion = factoryCompleteBatch(dst, completion, now)
		dst.Completion = &completion
		return nil
	})
	if errors.Is(err, ErrFactoryConflict) {
		return nil
	}
	return err
}

// factoryCompleteBatch marks an all-confirmed batch complete and, when the batch
// was authorized to complete the item, appends the completion it already
// authorized. The record names the batch, so a recovery can prove the
// completion exists before writing a second one, and a reopen that landed after
// the merges keeps its decision: the record is append-only and is never the
// second copy of itself.
func factoryCompleteBatch(dst *FactoryItem, completion FactoryCompletion, now time.Time) FactoryCompletion {
	batch := completion.Batch
	if batch == nil || batch.State == "complete" {
		return completion
	}
	batch.State = "complete"
	id := "completion-" + batch.ID
	if batch.CompleteAfter && factoryCompletionStatus(*dst) == "open" && !factoryCompletionRecorded(completion, id) {
		criteria := ""
		if n := len(dst.Approvals); n > 0 {
			criteria = dst.Approvals[n-1].Spec
		}
		completion.Records = append(completion.Records, FactoryCompletionRecord{
			ID:         id,
			At:         now,
			Actor:      factoryStudioRevisionActor,
			Goal:       dst.Title,
			Criteria:   criteria,
			Assessment: factoryCloneAssessment(completion.Assessment),
			Links:      factoryCopy(completion.Links),
			RunChoice:  batch.RunChoice,
		})
		completion.Status = "complete"
	}
	dst.History = append(dst.History, FactoryEvent{At: now, Kind: "completion_batch_completed", Message: fmt.Sprintf("merge batch %s completed", batch.ID)})
	return completion
}

// factoryCompletionRecorded reports whether a completion record with this
// identity is already journalled.
func factoryCompletionRecorded(completion FactoryCompletion, id string) bool {
	for _, r := range completion.Records {
		if r.ID == id {
			return true
		}
	}
	return false
}
