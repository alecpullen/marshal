package snapshot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file reclaims storage, and it does so one way only: by removing ENTIRE
// generations.
//
// There is deliberately no per-object sweep and no repack anywhere in it. Git
// objects are content-addressed, so an object no ref reaches today may be
// byte-identical to one that a snapshot a live ref still needs; deleting it
// individually would silently change what that snapshot restores. Removing a
// whole generation is the only removal that cannot do that, because a
// generation's objects are reachable exactly when its own refs name them — its
// repository has no branch and no reflog to retain anything else.
//
// The rules below are the ones the design fixes, and each is load-bearing:
//
//   - Time expiry requires EVERY snapshot in the generation to be older than
//     the cutoff. A generation holding one recent snapshot is not expired,
//     however old its oldest snapshot is.
//   - Budget pressure may reclaim YOUNGER native history. That is the deliberate
//     trade: a bounded store prefers losing the oldest rollback points it may
//     lose over refusing to capture at all.
//   - Workspace pressure searches that workspace first. Global pressure searches
//     every managed workspace, ordered by last-published timestamp and then by
//     the stable generation identifier.
//   - Never automatically reclaimed, for any reason: legacy-origin and otherwise
//     protected generations, legacy bare repositories, directories the manifest
//     does not list, generations whose refs cannot be read, anything whose
//     accounting is unknown, and the active generation.
//   - A generation marked for deletion is still counted, because its bytes are
//     not physically gone until the removal completes.

// maxReclaimIterations bounds one reclamation pass. Each iteration removes at
// least one generation or stops, so the bound is a safety net for a bug rather
// than a policy.
const maxReclaimIterations = MaxGenerationsPerWorkspace + 8

// ReclaimedGeneration records one generation that was reclaimed.
type ReclaimedGeneration struct {
	// Workspace is the workspace hash the generation belonged to.
	Workspace string
	// ID is the generation identifier.
	ID string
	// Bytes is the generation's measured size immediately before removal.
	Bytes int64
	// Snapshots is the number of snapshot refs the generation held, or -1 when
	// they could not be read.
	Snapshots int
	// Cause names why it became eligible: expired, workspace pressure, or global
	// pressure.
	Cause string
	// Error is set when the removal did not complete. The generation then stays
	// in its manifest in state `deleting`, so its bytes remain counted and the
	// next reconciliation finishes the removal.
	Error error
}

// ReclaimReport is the outcome of one reclamation pass.
type ReclaimReport struct {
	// Reclaimed lists the generations reclaimed, oldest first.
	Reclaimed []ReclaimedGeneration
	// Bytes is the measured size of the generations whose removal completed.
	Bytes int64
	// Warnings carries the structured problems found while reclaiming.
	Warnings []MaintenanceWarning
}

// String renders a one-line summary for diagnostics.
func (r *ReclaimReport) String() string {
	if r == nil || len(r.Reclaimed) == 0 {
		return "no generations reclaimed"
	}
	return fmt.Sprintf("%d generations reclaimed, %d bytes freed", len(r.Reclaimed), r.Bytes)
}

// ---------------------------------------------------------------------------
// Reclamation under admission pressure
// ---------------------------------------------------------------------------

// ReclaimForAdmission reclaims whole generations because a reservation does not
// fit, and returns the identifiers it removed, oldest first.
//
// needed is the reservation that did not fit. excludeGenerationID is the
// generation the caller is about to write into, which must never be reclaimed
// out from under it.
//
// The two pressures are evaluated in a deliberate order, because their remedies
// differ: a shortfall against the per-workspace ceiling can only be relieved by
// reclaiming from that workspace, whereas a global or free-space shortfall can
// be relieved from any managed workspace, oldest-published first.
func (m *Manager) ReclaimForAdmission(ctx context.Context, cat *Catalog, needed int64, excludeGenerationID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, interruptedCapture(err)
	}
	if !m.Owned() {
		return nil, storeErrorf(ReasonNotOwned, "reclamation requires exclusive ownership of the snapshot store")
	}
	if needed < 0 {
		return nil, storeErrorf(ReasonInvalidLimits, "reclamation need must not be negative: %d", needed)
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}

	state := &reclaimState{attempted: make(map[string]bool)}

	// Phase A: workspace pressure, searched in this workspace only.
	workspace := m.reclaimUntil(ctx, reclaimScope{
		workspace: cat.Workspace(),
		exclude:   excludeGenerationID,
		cause:     "workspace pressure",
		workspaceOK: func(wsAllocated int64) bool {
			return wsAllocated+needed > limits.WorkspaceMaxBytes
		},
	}, state)
	if workspace == nil {
		// The store could not be measured, so nothing is known about what
		// could be freed. Failing closed means the caller refuses rather than
		// growing a store whose accounting is unknown.
		return nil, m.storeErrorf(ReasonUnreadableFile, cat.Root(),
			"the snapshot store could not be measured, so no generation can be reclaimed")
	}
	if len(workspace) == 0 && !state.workspaceShort {
		// The workspace ceiling was never the binding constraint, so the
		// workspace phase had nothing to do.
		workspace = nil
	}

	// Phase B: global and free-space pressure, searched across every managed
	// workspace and ordered by last publication.
	global := m.reclaimUntil(ctx, reclaimScope{
		storeOK: func(usage *StoreUsage) bool {
			if usage.GlobalAllocated+needed > limits.GlobalMaxBytes {
				return false
			}
			return freeSpaceFits(m, needed)
		},
		exclude: excludeGenerationID,
		cause:   "global pressure",
	}, state)
	if global == nil {
		return nil, m.storeErrorf(ReasonUnreadableFile, m.Root(),
			"the snapshot store could not be measured, so no generation can be reclaimed")
	}
	return append(workspace, global...), nil
}

// reclaimScope describes one reclamation loop.
type reclaimScope struct {
	// workspace names the single workspace to search, or "" for all of them.
	workspace string
	// exclude is a generation identifier that must never be reclaimed.
	exclude string
	// cause names the pressure in the report.
	cause string
	// workspaceOK reports whether the searched workspace is still over its own
	// ceiling. It is evaluated only for the scoped workspace.
	workspaceOK func(wsAllocated int64) bool
	// storeOK reports whether the store-level pressures are still unmet.
	storeOK func(usage *StoreUsage) bool
}

// reclaimState carries what one reclamation pass has learned across its phases,
// so the second phase does not repeat work the first already did.
type reclaimState struct {
	// attempted records generations already tried, so a failed removal is not
	// retried forever within one pass.
	attempted map[string]bool
	// workspaceShort records whether the per-workspace ceiling was ever the
	// binding constraint in this pass.
	workspaceShort bool
}

// reclaimUntil removes eligible whole generations, oldest first, until the
// scope's pressure is relieved or nothing eligible remains.
//
// It returns nil when the store could not be measured at all, which is a
// failure to reclaim rather than a successful reclaim of nothing: an
// under-measured store is how a budget later admits a write it cannot afford.
// An empty non-nil result means the pressure was already satisfied or nothing
// was eligible.
func (m *Manager) reclaimUntil(ctx context.Context, scope reclaimScope, state *reclaimState) []string {
	reclaimed := []string{}
	for i := 0; i < maxReclaimIterations; i++ {
		if err := ctx.Err(); err != nil {
			return reclaimed
		}
		usage, err := m.UsageContext(ctx)
		if err != nil {
			return nil
		}
		if !scopeNeedsReclaim(scope, usage) {
			return reclaimed
		}
		if scope.workspaceOK != nil && scope.workspaceOK(workspaceStoreAllocated(usage, scope.workspace)) {
			state.workspaceShort = true
		}
		candidates := m.reclaimCandidates(ctx, scope, usage, state.attempted)
		if len(candidates) == 0 {
			return reclaimed
		}
		removed := false
		for _, cand := range candidates {
			if err := ctx.Err(); err != nil {
				return reclaimed
			}
			state.attempted[cand.workspace+"\x00"+cand.id] = true
			if removeErr := m.reclaimGeneration(ctx, cand); removeErr != nil {
				// A failed removal leaves the generation in state `deleting`,
				// so its bytes stay counted and the next reconciliation
				// finishes the job. Trying the next candidate is still
				// progress, so this is not fatal to the pass.
				continue
			}
			reclaimed = append(reclaimed, cand.id)
			removed = true
			// Re-measure immediately rather than deducting the estimate:
			// whether the removal actually relieved the pressure is a
			// measurement, not a deduction.
			break
		}
		if !removed {
			return reclaimed
		}
	}
	return reclaimed
}

// scopeNeedsReclaim reports whether the scope's pressure is still unmet.
func scopeNeedsReclaim(scope reclaimScope, usage *StoreUsage) bool {
	if scope.workspaceOK != nil && scope.workspaceOK(workspaceStoreAllocated(usage, scope.workspace)) {
		return true
	}
	if scope.storeOK != nil && !scope.storeOK(usage) {
		return true
	}
	return false
}

// freeSpaceFits reports whether the filesystem has room for need plus the
// reserve. An unreadable free-space value is treated as "does not fit", so a
// store that cannot verify its own headroom never grows.
func freeSpaceFits(m *Manager, need int64) bool {
	return m.CheckFreeSpace(need) == nil
}

// reclaimCandidate is one generation eligible for automatic reclamation.
type reclaimCandidate struct {
	workspace string
	id        string
	bytes     int64
	// order is the oldest-first ordering key: last publication, then creation.
	order string
	// snapshots is the number of snapshot refs, or -1 when unread.
	snapshots int
}

// reclaimCandidates returns the generations eligible for automatic reclamation
// in this scope, ordered oldest-first.
//
// The ordering and the eligibility rules ARE the design's reclamation policy,
// so both live here rather than being spread across call sites:
//
//	order: last-published timestamp, then the stable generation identifier
//	(whose leading creation sequence makes lexical order chronological).
//
// Eligibility is deliberately narrow. A generation is a candidate only when it
// is listed in a readable manifest, sealed, unprotected, and measurable.
// Everything else — a protected or legacy-origin generation, an unlisted
// directory, a generation whose refs could not be read, a corrupt or absent
// manifest, a legacy bare repository, the active generation — is excluded,
// because in each case the store cannot state durably that the history is
// disposable.
func (m *Manager) reclaimCandidates(ctx context.Context, scope reclaimScope, usage *StoreUsage, attempted map[string]bool) []reclaimCandidate {
	var out []reclaimCandidate
	for _, ws := range scopeWorkspaces(scope, usage) {
		if err := ctx.Err(); err != nil {
			return out
		}
		cat, err := m.Catalog(ws)
		if err != nil {
			continue
		}
		man, err := cat.Load()
		if err != nil || man == nil {
			// A corrupt or absent manifest quarantines the workspace from
			// reclamation: nothing durable describes what its directories are.
			continue
		}
		gens := usage.Generations[ws]
		for i := range man.Generations {
			rec := &man.Generations[i]
			if err := ctx.Err(); err != nil {
				return out
			}
			if !reclaimable(man, gens, rec, scope.exclude) {
				continue
			}
			if attempted[ws+"\x00"+rec.ID] {
				continue
			}
			bytes, refs := int64(0), -1
			if gu := gens.ByID[rec.ID]; gu != nil {
				bytes, refs = gu.Allocated, gu.Refs
			}
			out = append(out, reclaimCandidate{
				workspace: ws,
				id:        rec.ID,
				bytes:     bytes,
				order:     generationOrderKey(rec),
				snapshots: refs,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		if out[i].id != out[j].id {
			return out[i].id < out[j].id
		}
		return out[i].workspace < out[j].workspace
	})
	return out
}

// scopeWorkspaces returns the workspaces a scope searches. A scope naming one
// workspace searches only that one; otherwise every measured versioned
// workspace is searched, in hash order so a pass with no ordering difference
// still behaves identically on every run. The oldest-first decision itself is
// applied by the candidate sort, not here.
func scopeWorkspaces(scope reclaimScope, usage *StoreUsage) []string {
	if scope.workspace != "" {
		return []string{scope.workspace}
	}
	if usage == nil {
		return nil
	}
	out := make([]string, 0, len(usage.Generations))
	for ws := range usage.Generations {
		out = append(out, ws)
	}
	sort.Strings(out)
	return out
}

// reclaimable applies the exclusion rules to one generation record.
func reclaimable(man *Manifest, gens *WorkspaceGenerations, rec *GenerationRecord, exclude string) bool {
	if man == nil || gens == nil || rec == nil {
		return false
	}
	if rec.ID == exclude {
		// The generation the caller is about to write into.
		return false
	}
	if man.ActiveID == rec.ID || rec.State == GenerationActive {
		// The active generation must be sealed before its history can become
		// eligible. Reclaiming it would remove the capture target mid-flight.
		return false
	}
	if rec.State != GenerationSealed {
		// `creating` and `deleting` are reconciliation's business: a creating
		// generation may be about to publish, and a deleting one is already
		// being finished.
		return false
	}
	if rec.IsProtected() {
		// Legacy-origin history is the only surviving copy of rollback history
		// that predates bounded generations. Migration INTO a protected
		// generation is not confirmation to discard it later: removing that
		// protection requires an explicit confirmation, which is a later
		// task's job. This code simply never reclaims it.
		return false
	}
	gu := gens.ByID[rec.ID]
	if gu == nil || !gu.Listed || gu.Protected {
		// Listed but not measured, or measured as unlisted/protected: the
		// manifest and the filesystem disagree, so there is nothing this pass
		// can safely remove.
		return false
	}
	if gu.Refs < 0 {
		// "Could not list the refs" is not "nothing is published", so the
		// generation is excluded rather than assumed to hold no snapshots.
		return false
	}
	return true
}

// reclaimGeneration removes one whole generation using the durable
// intent-first sequence reconciliation also uses: mark, remove, forget.
//
// Marking first is what makes the removal recoverable. A crash after the mark
// leaves a `deleting` record the next pass finishes; a crash before it leaves a
// generation nothing will touch.
func (m *Manager) reclaimGeneration(ctx context.Context, cand reclaimCandidate) error {
	if err := ctx.Err(); err != nil {
		return interruptedCapture(err)
	}
	cat, err := m.Catalog(cand.workspace)
	if err != nil {
		return err
	}
	if _, err := cat.MarkDeleting(cand.id); err != nil {
		return err
	}
	if err := cat.RemoveGenerationDirectory(cand.id); err != nil {
		return err
	}
	// Write boundary: the generation directory is gone, so its bytes have
	// actually been released. Until this point the generation was still
	// counted, which is what stops a pending deletion from being spent twice.
	m.observeWrite(WriteEvent{Kind: "reclaim", Path: cand.id, Workspace: cand.workspace})
	// Only now is the record dropped.
	return cat.ForgetGeneration(cand.id)
}

// ---------------------------------------------------------------------------
// Retention expiry
// ---------------------------------------------------------------------------

// ReclaimExpired reclaims, in every managed workspace, each generation whose
// snapshots have ALL expired.
func (m *Manager) ReclaimExpired(ctx context.Context, retentionDays int) (*ReclaimReport, error) {
	return m.reclaimExpired(ctx, "", retentionDays)
}

// ReclaimExpiredWorkspace reclaims expired generations in one workspace. It is
// the narrow variant a per-workspace caller uses.
func (m *Manager) ReclaimExpiredWorkspace(ctx context.Context, cat *Catalog, retentionDays int) (*ReclaimReport, error) {
	if cat == nil {
		return nil, storeErrorf(ReasonInternal, "retention needs a catalog")
	}
	return m.reclaimExpired(ctx, cat.Workspace(), retentionDays)
}

// reclaimExpired is the shared retention pass. scopeWorkspace is a workspace
// hash, or "" for every managed workspace.
//
// RetentionDays is preserved exactly as the legacy prune understood it:
//
//   - a NEGATIVE value disables expiry entirely. The legacy code returned early
//     there, and a negative retention is a nonsense rather than an instruction
//     to delete everything.
//   - zero expires every snapshot older than NOW, which is precisely what
//     `time.Now().AddDate(0, 0, -0)` always meant.
func (m *Manager) reclaimExpired(ctx context.Context, scopeWorkspace string, retentionDays int) (*ReclaimReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, interruptedCapture(err)
	}
	if !m.Owned() {
		return nil, storeErrorf(ReasonNotOwned, "retention requires exclusive ownership of the snapshot store")
	}
	report := &ReclaimReport{}
	if retentionDays < 0 {
		return report, nil
	}
	cutoff := m.Now().AddDate(0, 0, -retentionDays).Unix()

	// A single-workspace pass measures ONLY that workspace. A whole-root
	// accounting scan is expensive on a store holding hundreds of gigabytes of
	// legacy history, and it is exactly what a per-session retention call must
	// not pay: the user's session would be held open by bytes it will never
	// touch.
	if scopeWorkspace != "" {
		cat, err := m.Catalog(scopeWorkspace)
		if err != nil {
			return nil, err
		}
		return report, m.reclaimExpiredWorkspace(ctx, cat, cutoff, report)
	}

	usage, err := m.UsageContext(ctx)
	if err != nil {
		return nil, err
	}
	for _, ws := range scopeWorkspaces(reclaimScope{}, usage) {
		if err := ctx.Err(); err != nil {
			return nil, interruptedCapture(err)
		}
		cat, err := m.Catalog(ws)
		if err != nil {
			continue
		}
		if err := m.reclaimExpiredWorkspace(ctx, cat, cutoff, report); err != nil {
			return nil, err
		}
	}
	return report, nil
}

// reclaimExpiredWorkspace expires the generations of one workspace.
//
// The active generation is handled explicitly. A generation may only be
// reclaimed once it is sealed, and the design's instruction for the active
// generation is "seal it first if its history must become eligible" — sealing
// it here is exactly that, and it is done only when every snapshot the
// generation holds has ALREADY expired, so the seal changes nothing about which
// rollback points survive.
func (m *Manager) reclaimExpiredWorkspace(ctx context.Context, cat *Catalog, cutoff int64, report *ReclaimReport) error {
	// Two passes: the first seals any fully expired active generation, the
	// second reclaims the generations that are now eligible.
	for pass := 0; pass < 2; pass++ {
		// Only THIS workspace is measured. The whole-root scan would make a
		// per-session retention call pay for every other store on the machine.
		gens, err := m.MeasureWorkspaceGenerations(ctx, cat.Workspace())
		if err != nil {
			return err
		}
		man, err := cat.Load()
		if err != nil || man == nil {
			// Corrupt or absent: quarantined from reclamation.
			return nil
		}
		candidates, err := m.expiredCandidates(ctx, cat, man, gens, cutoff)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		for _, cand := range candidates {
			if err := ctx.Err(); err != nil {
				return interruptedCapture(err)
			}
			if cand.sealFirst {
				// Sealing an expired active generation is what makes it
				// reclaimable, and the seal is safe because every snapshot it
				// holds has already expired.
				if sealErr := cat.SealGeneration(cand.id); sealErr != nil {
					report.Warnings = append(report.Warnings, MaintenanceWarning{
						Reason:    ReasonOf(sealErr),
						Workspace: cand.workspace,
						Path:      cat.Root(),
						Message:   "the expired active generation could not be sealed: " + sealErr.Error(),
					})
				}
				continue
			}
			entry := ReclaimedGeneration{
				Workspace: cand.workspace,
				ID:        cand.id,
				Bytes:     cand.bytes,
				Snapshots: cand.snapshots,
				Cause:     "expired",
			}
			if removeErr := m.reclaimGeneration(ctx, cand.reclaimCandidate); removeErr != nil {
				entry.Error = removeErr
				report.Warnings = append(report.Warnings, MaintenanceWarning{
					Reason:    ReasonOf(removeErr),
					Workspace: cand.workspace,
					Path:      cat.Root(),
					Message: "the expired generation could not be removed; it stays marked for deletion " +
						"and is still counted until its bytes are gone: " + removeErr.Error(),
				})
			} else if bytes, err := addInt64(report.Bytes, cand.bytes); err == nil {
				report.Bytes = bytes
			}
			report.Reclaimed = append(report.Reclaimed, entry)
		}
	}
	return nil
}

// expiredCandidate is a generation eligible for time expiry.
type expiredCandidate struct {
	reclaimCandidate
	// sealFirst marks the active generation, which must be sealed before it can
	// be reclaimed.
	sealFirst bool
}

// expiredCandidates returns the generations in one workspace whose snapshots
// have ALL expired, oldest first.
//
// "All" is the whole of the rule. A generation holding one recent snapshot is
// not expired however old its oldest snapshot is, because reclaiming it would
// discard that recent rollback point without the user ever having asked for the
// generation to go.
func (m *Manager) expiredCandidates(ctx context.Context, cat *Catalog, man *Manifest,
	gens *WorkspaceGenerations, cutoff int64) ([]expiredCandidate, error) {

	if man == nil || gens == nil {
		return nil, nil
	}
	var out []expiredCandidate
	for i := range man.Generations {
		rec := &man.Generations[i]
		if err := ctx.Err(); err != nil {
			return nil, interruptedCapture(err)
		}
		if rec.IsProtected() {
			continue
		}
		gu := gens.ByID[rec.ID]
		if gu == nil || !gu.Listed || gu.Protected || gu.Refs < 0 {
			// Unlisted, missing, protected, or with unreadable refs: excluded.
			// "Could not list the refs" is never "nothing is published".
			continue
		}
		active := man.ActiveID == rec.ID || rec.State == GenerationActive
		switch {
		case active && rec.State != GenerationActive:
			// A `creating` or `deleting` active record is reconciliation's
			// business, not retention's.
			continue
		case active:
			// Handled below: an expired active generation is sealed first.
		case rec.State == GenerationSealed:
			// The normal expiry case.
		default:
			continue
		}

		times, err := m.generationRefTimes(ctx, cat, rec.ID)
		if err != nil {
			// An unreadable listing is an error, never "nothing is published":
			// treating it as empty would make a generation full of recent
			// snapshots look fully expired, which is how a retention pass
			// would delete a rollback point the user still needed.
			return nil, err
		}
		if len(times) == 0 && active {
			// An active generation holding no snapshots at all is left alone:
			// nothing in it has expired, and sealing it would only interrupt
			// the workspace's capture target.
			continue
		}
		if !allBefore(times, cutoff) {
			continue
		}
		out = append(out, expiredCandidate{
			reclaimCandidate: reclaimCandidate{
				workspace: cat.Workspace(),
				id:        rec.ID,
				bytes:     gu.Allocated,
				order:     generationOrderKey(rec),
				snapshots: gu.Refs,
			},
			sealFirst: active,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

// allBefore reports whether every timestamp is strictly before cutoff. An empty
// list is vacuously "all expired".
func allBefore(times []int64, cutoff int64) bool {
	for _, t := range times {
		if t >= cutoff {
			return false
		}
	}
	return true
}

// generationRefTimes returns the committer time of every snapshot ref in a
// generation, read-only.
//
// The listing is deliberately read-only and deliberately fatal on failure. It
// never writes a ref, expires a reflog, or repacks: a retention pass must be
// able to inspect a repository it is not going to touch.
func (m *Manager) generationRefTimes(ctx context.Context, cat *Catalog, id string) ([]int64, error) {
	dir, err := cat.GenerationDir(id)
	if err != nil {
		return nil, err
	}
	if !repositoryLooksInitialised(dir) {
		// Not a repository: there are no snapshots to expire.
		return nil, nil
	}
	out, err := m.gitOutput(ctx, dir, "for-each-ref", "--format=%(committerdate:unix)", snapshotRefPrefix)
	if err != nil {
		return nil, err
	}
	var times []int64
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ts, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return nil, m.storeErrorf(ReasonUnreadableFile, dir,
				"snapshot ref carries an unparseable commit time %q", line)
		}
		times = append(times, ts)
	}
	return times, nil
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

// Status reports a structured problem when the store needs attention, or nil
// when it does not.
//
// It exists so the runtime can distinguish "nothing to prune" from "this store
// cannot grow any further" without inventing a reason from a raw error. It is
// read-only apart from the ownership assumption its caller provides, and it
// deliberately does NOT reconcile: the runtime calls it on the shutdown path,
// where heavy storage work must not run.
//
// scopeWorkspace names one workspace to report on, or "" for every versioned
// workspace. The SCOPED form is what a per-session caller uses, because a
// whole-root scan costs real time on a store holding hundreds of gigabytes of
// legacy history — and none of those bytes are the session's business.
func (m *Manager) Status(ctx context.Context) error {
	return m.status(ctx, "")
}

// StatusWorkspace reports on one workspace's versioned store.
func (m *Manager) StatusWorkspace(ctx context.Context, workspace string) error {
	if !validWorkspaceID(workspace) {
		return storeErrorf(ReasonInternal, "workspace identifier %q is not a hex workspace hash", workspace)
	}
	return m.status(ctx, workspace)
}

// status implements both entry points.
func (m *Manager) status(ctx context.Context, scopeWorkspace string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkReady(); err != nil {
		return err
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return err
	}

	var problems []string
	var workspaces []string
	if scopeWorkspace != "" {
		workspaces = []string{scopeWorkspace}
	} else {
		usage, err := m.UsageContext(ctx)
		if err != nil {
			return err
		}
		if usage.GlobalAllocated > limits.GlobalMaxBytes {
			problems = append(problems, fmt.Sprintf("managed snapshot storage holds %d bytes, over the %d byte global limit",
				usage.GlobalAllocated, limits.GlobalMaxBytes))
		}
		workspaces = sortedWorkspaceHashes(usage)
	}

	for _, ws := range workspaces {
		gens, err := m.MeasureWorkspaceGenerations(ctx, ws)
		if err != nil {
			// A workspace whose measurement cannot be read is a problem to
			// report, not a reason to hide every other workspace's state.
			problems = append(problems, fmt.Sprintf("workspace %s could not be measured: %v", ws, err))
			continue
		}
		if gens.Corrupt {
			problems = append(problems, fmt.Sprintf("workspace %s has an unreadable manifest and is quarantined from capture and deletion", ws))
			continue
		}
		if gens.Allocated > limits.WorkspaceMaxBytes {
			problems = append(problems, fmt.Sprintf("workspace %s holds %d bytes, over its %d byte limit",
				ws, gens.Allocated, limits.WorkspaceMaxBytes))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return m.storeError(ReasonBudgetExhausted, m.Root(), errors.New(strings.Join(problems, "; ")))
}

// sortedWorkspaceHashes returns the measured workspace hashes in a stable
// order.
func sortedWorkspaceHashes(usage *StoreUsage) []string {
	if usage == nil {
		return nil
	}
	out := make([]string, 0, len(usage.Generations))
	for ws := range usage.Generations {
		out = append(out, ws)
	}
	sort.Strings(out)
	return out
}
