package snapshot

import (
	"context"
	"errors"
	"fmt"
)

// This file is the gate every managed write passes through.
//
// Admission is a RESERVATION, not a measurement. The preceding tasks made a
// capture's worst-case write estimate a proven upper bound; admission is where
// that bound is compared against the ceilings and the filesystem BEFORE a
// single store byte is touched. A design that wrote first and measured
// afterwards would already have filled the disk by the time it noticed, which
// is precisely the failure this work exists to remove.
//
// The order inside admission is deliberate:
//
//  1. Reconcile. Interrupted work from a previous run is finished first, so
//     admission never reasons about a half-written store.
//  2. Plan. planCapture writes nothing and returns a proven upper bound.
//  3. Measure usage. An unmeasurable store is an error, never zero.
//  4. Compare the reservation against the per-workspace ceiling, the
//     user-global ceiling read FRESH while the lock is held, and available
//     filesystem space.
//  5. If it does not fit, reclaim whole generations and try again.
//  6. If it still does not fit, refuse with a structured reason and leave the
//     store exactly as it was.

// rotationTargetDivisor splits the effective workspace ceiling into the
// generation rotation target. One quarter is the approved design's choice: it
// leaves room for a replacement generation to be created and admitted while the
// generation being rotated away from is still on disk.
const rotationTargetDivisor int64 = 4

// maxRotationAttempts bounds the rotate-and-retry loops. Rotation is only ever
// attempted after admission has permitted the new generation's baseline, so each
// attempt makes progress; the bound exists so a bug can never turn admission
// into an unbounded loop spinning while it holds the store lock.
const maxRotationAttempts = 4

// minimumCaptureBytes is the smallest write allowance a capture is admitted
// with. A capture admitted for fewer bytes than this has no chance of creating
// its object directories, its manifest replacement, its ref, and its staging
// record, so admitting one would only produce a confusing budget failure deep
// inside the writer.
const minimumCaptureBytes int64 = 64 << 10 // 64 KiB

// AdmissionOutcome classifies what admission decided, so a caller (and a test)
// can distinguish "this fit as it was" from "this forced a rotation" from "this
// was refused" without matching on error text.
type AdmissionOutcome string

const (
	// AdmissionAdmitted means the capture fit the existing active generation.
	AdmissionAdmitted AdmissionOutcome = "admitted"
	// AdmissionRotated means the active generation was sealed and a replacement
	// generation admitted.
	AdmissionRotated AdmissionOutcome = "rotated"
	// AdmissionRefused means nothing was written and the store was left as it
	// was.
	AdmissionRefused AdmissionOutcome = "refused"
)

// AdmissionResult is what admission decided.
type AdmissionResult struct {
	// Outcome is admitted, rotated, or refused.
	Outcome AdmissionOutcome
	// GenerationID is the generation the capture must be written into. It is
	// empty when Outcome is AdmissionRefused.
	GenerationID string
	// Allowance is the admitted write allowance. It is never smaller than the
	// plan's own estimate, so the plan remains an upper bound on the spend.
	Allowance int64
	// Maintenance is the reserved maintenance allowance that sits ON TOP of
	// Allowance and is never raidable by the capture itself.
	Maintenance int64
	// Reserved is the total the store commits for this capture: the allowance,
	// the maintenance reserve, and the manifest replacement the capture's own
	// bookkeeping costs.
	Reserved int64
	// RotationReason explains why a replacement generation was opened, or is
	// empty when the capture used the existing active generation.
	RotationReason string
	// ReclaimedGenerations lists the generations removed to make room, oldest
	// first. A generation marked for deletion but not yet physically gone is
	// deliberately absent: it is still counted in usage.
	ReclaimedGenerations []string
	// Plan is the capture plan the allowance came from.
	Plan *capturePlan
	// Usage is the store measurement admission decided on, taken after any
	// rotation so it describes the store the capture will actually write to.
	Usage *StoreUsage
	// Reason and Err describe a refusal. Reason is empty on success.
	Reason StoreReason
	Err    error
}

// AdmitCapture decides whether a capture may proceed, and under what allowance.
//
// It REQUIRES the store lock to be held: it removes directories, rewrites
// manifests, and measures usage that is only meaningful while no other capture
// can be writing. Because everything happens under that lock, two concurrent
// callers cannot both reserve the same headroom.
//
// A refusal returns a *StoreError carrying ReasonBudgetExhausted or
// ReasonInsufficientFreeSpace and leaves the store byte-identical, apart from
// reconciliation of work a previous run had already interrupted.
func (m *Manager) AdmitCapture(ctx context.Context, cat *Catalog, req CaptureRequest) (*AdmissionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, interruptedCapture(err)
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	if cat == nil {
		return nil, storeErrorf(ReasonInternal, "admission needs a catalog")
	}
	if err := cat.requireOwned(); err != nil {
		return nil, err
	}
	// The limits are read first: a limit this store cannot honour at all is a
	// configuration problem rather than an exhausted budget, and saying so is
	// more useful than reporting the wrong cause.
	if _, err := m.EffectiveLimits(); err != nil {
		return nil, err
	}

	// 1. Reconcile.
	if _, err := m.ReconcileWorkspace(ctx, cat.Workspace()); err != nil {
		return nil, err
	}

	// 2. Plan. planCapture writes nothing: discovering and estimating are
	//    deliberately separate from capturing so a capture that cannot possibly
	//    be admitted is refused before the first directory is created.
	plan, err := m.planCapture(ctx, cat, req)
	if err != nil {
		return nil, err
	}
	if len(plan.Entries) == 0 {
		// An empty capture is refused here rather than in the writer: admitting
		// one would reserve a full manifest replacement for a snapshot that
		// cannot exist.
		return nil, storeErrorf(ReasonInternal, cat.Workspace(),
			"refusing to admit a capture of a workspace with no eligible paths")
	}

	result := &AdmissionResult{Plan: plan, Maintenance: plan.Maintenance}

	for attempt := 0; attempt < maxRotationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, interruptedCapture(err)
		}
		// 3. Measure.
		usage, err := m.UsageContext(ctx)
		if err != nil {
			return nil, err
		}

		// 4. Find (or create) the generation this capture writes into. A
		//    replacement is only created after its baseline has been admitted.
		target, err := m.admissionTarget(ctx, cat, usage)
		if err != nil {
			var refused *StoreError
			if errors.As(err, &refused) && isAdmissionRefusal(refused.Reason) {
				return refuseAdmission(result, refused), refused
			}
			return nil, err
		}
		result.GenerationID = target.id
		result.RotationReason = target.rotationReason
		result.Outcome = AdmissionAdmitted
		if target.rotated {
			result.Outcome = AdmissionRotated
		}
		result.ReclaimedGenerations = append(result.ReclaimedGenerations, target.reclaimed...)

		if target.created {
			// The creation changed the store, so the measurement above is
			// stale by exactly the metadata that was just written. Re-measuring
			// is what keeps the comparison honest rather than optimistically
			// small.
			if usage, err = m.UsageContext(ctx); err != nil {
				return nil, err
			}
		}
		result.Usage = usage

		// 5. Compute the reservation for THIS generation.
		reservation, err := m.generationReservation(plan)
		if err != nil {
			return nil, err
		}
		result.Allowance = reservation.allowance
		result.Reserved = reservation.total

		// 6. Compare against every ceiling.
		reason, err := m.checkReservation(ctx, cat, usage, reservation)
		if err != nil {
			return nil, err
		}
		if reason == nil {
			return result, nil
		}

		// 7. Does not fit. Reclaim whole eligible generations and try again.
		reclaimed, err := m.ReclaimForAdmission(ctx, cat, reservation.total, target.id)
		if err != nil {
			return nil, err
		}
		result.ReclaimedGenerations = append(result.ReclaimedGenerations, reclaimed...)
		if len(reclaimed) == 0 {
			// Nothing could be reclaimed, so a retry would measure the same
			// store and reach the same conclusion. Refuse, preserving the
			// reason that describes what actually did not fit.
			return refuseAdmission(result, reason), reason
		}
	}

	return refuseAdmission(result, m.storeErr(ReasonBudgetExhausted, cat.Root(),
		"admission did not converge after %d rotation attempts; the configured workspace limit %d is "+
			"too small for this workspace's baseline plus one capture",
		maxRotationAttempts, m.Limits().WorkspaceMaxBytes)), nil
}

// admissionGeneration is the generation a capture will be written into.
type admissionGeneration struct {
	id string
	// record is the manifest record as it was before this admission pass, or
	// nil when the generation was just created.
	record *GenerationRecord
	// rotated is set when a previous active generation was sealed and replaced.
	rotated bool
	// created is set when this generation was created by this pass.
	created bool
	// rotationReason explains why a replacement was needed.
	rotationReason string
	// reclaimed lists generations removed while making room for the
	// replacement's baseline.
	reclaimed []string
}

// admissionTarget picks the generation this capture writes into.
//
//   - No generations at all, or none usable → create a baseline generation.
//   - An active generation still inside its rotation target → use it.
//   - An active generation at or over its target → seal it and create a
//     replacement, because a generation may only be reclaimed whole and a
//     sealed one is what makes its history eligible.
//   - Only sealed generations → open a fresh active one.
func (m *Manager) admissionTarget(ctx context.Context, cat *Catalog, usage *StoreUsage) (*admissionGeneration, error) {
	man, err := cat.Load()
	if err != nil {
		return nil, err
	}
	gens := usage.Generations[cat.Workspace()]

	active := (*GenerationRecord)(nil)
	if man != nil {
		active = man.Active()
	}
	if active != nil && active.IsProtected() {
		// A protected generation must never be captured into: reclaiming it is
		// forbidden, so growing it would build storage that can never be
		// released.
		active = nil
	}
	if active != nil {
		if atTarget, why := m.generationAtRotationTarget(cat, gens, active.ID); !atTarget {
			return &admissionGeneration{id: active.ID, record: active}, nil
		} else {
			return m.rotateToReplacement(ctx, cat, usage, gens, active, why)
		}
	}
	return m.rotateToReplacement(ctx, cat, usage, gens, nil, "the workspace has no usable active generation")
}

// rotateToReplacement creates and activates a replacement generation, but only
// after admission has permitted its baseline.
//
// ActivateGeneration seals the previous active generation in the same manifest
// write that makes the new one active, so a workspace never transiently has two
// capture targets.
func (m *Manager) rotateToReplacement(ctx context.Context, cat *Catalog, usage *StoreUsage,
	gens *WorkspaceGenerations, active *GenerationRecord, why string) (*admissionGeneration, error) {

	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}
	baseline, err := baselineGenerationReservationBytes(limits, gens)
	if err != nil {
		return nil, err
	}

	var reclaimed []string
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, interruptedCapture(err)
		}
		fresh, err := m.UsageContext(ctx)
		if err != nil {
			return nil, err
		}
		reason, err := m.checkBaseline(ctx, cat, fresh, baseline)
		if err != nil {
			return nil, err
		}
		if reason == nil {
			break
		}
		if attempt >= maxRotationAttempts {
			// The workspace cannot hold even an empty generation's metadata
			// plus its manifests. Looping would never converge, so this is
			// refused with an explicit message instead of retried.
			return nil, m.storeErr(ReasonBudgetExhausted, cat.Root(),
				"the configured workspace limit %d cannot hold a generation baseline (%d bytes of "+
					"metadata) plus the manifests this workspace requires; raise "+
					"snapshots.workspace_max_bytes or set snapshots.enabled = false",
				limits.WorkspaceMaxBytes, baseline)
		}
		freed, err := m.ReclaimForAdmission(ctx, cat, baseline, "")
		if err != nil {
			return nil, err
		}
		if len(freed) == 0 {
			return nil, reason
		}
		reclaimed = append(reclaimed, freed...)
	}

	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		return nil, err
	}
	if err := cat.ActivateGeneration(rec.ID); err != nil {
		return nil, err
	}
	return &admissionGeneration{
		id:     rec.ID,
		record: rec,
		// A replacement generation was opened, whether or not one was replaced:
		// the first capture into a workspace rotates from "nothing at all".
		rotated:        true,
		created:        true,
		rotationReason: why,
		reclaimed:      reclaimed,
	}, nil
}

// generationAtRotationTarget reports whether a generation has reached its
// rotation target and a replacement must be opened.
//
// A generation that is protected, unlisted, or whose refs cannot be read is
// reported as being at the target: capturing into a generation the store cannot
// measure is not a decision admission should make on a guess.
func (m *Manager) generationAtRotationTarget(cat *Catalog, gens *WorkspaceGenerations, id string) (bool, string) {
	target := m.rotationTarget()
	if gens == nil {
		return true, "the workspace store has no per-generation measurement"
	}
	gu := gens.ByID[id]
	if gu == nil {
		return true, "the active generation is not listed in the store's measurement"
	}
	if !gu.Listed {
		return true, "the active generation is not listed in the manifest"
	}
	if gu.Protected {
		return true, "the active generation is protected from reclamation"
	}
	if gu.Refs < 0 {
		return true, "the active generation's snapshot refs could not be read"
	}
	if gu.Allocated >= target {
		return true, "the active generation has reached its rotation target"
	}
	return false, ""
}

// rotationTarget is ONE QUARTER of the effective workspace ceiling.
//
// It is a rotation target, not a quota: a single admitted capture may exceed it
// and is then sealed immediately, because the alternative — refusing a
// legitimate capture — would trade correctness for tidiness.
func (m *Manager) rotationTarget() int64 {
	limits := m.Limits()
	if limits.WorkspaceMaxBytes <= 0 {
		return limits.WorkspaceMaxBytes
	}
	return limits.WorkspaceMaxBytes / rotationTargetDivisor
}

// generationReservation is what one capture commits the store to.
type generationReservation struct {
	// allowance is the capture's own write allowance.
	allowance int64
	// total is the allowance plus the maintenance reserve.
	total int64
	// manifest is the manifest replacement component of the PLAN's estimate.
	// It is reported separately so a caller can see how much of the allowance
	// an atomic replace needs; it is deliberately NOT added to total again.
	manifest int64
	// maintenance is the separately reserved maintenance allowance.
	maintenance int64
}

// generationReservation computes the reservation for one capture.
//
// The allowance already covers everything the capture writes. The plan's
// estimate is the sum of the blobs, the trees, the commit, the object
// directories, the SIMULTANEOUS old and new manifest files an atomic replace
// holds, the publication ref, and the staging record, each rounded to
// allocation units. Adding the manifest replacement again here would
// double-count it, which is why the plan is the single source of the write
// estimate.
//
// What is added ON TOP is the maintenance allowance, which is deliberately not
// raidable by the capture: sealing a generation, removing a stage directory, and
// reconciling an interrupted capture all need headroom of their own.
func (m *Manager) generationReservation(plan *capturePlan) (*generationReservation, error) {
	allowance := plan.Allowance
	if allowance < minimumCaptureBytes {
		allowance = minimumCaptureBytes
	}
	maintenance := plan.Maintenance
	if maintenance <= 0 {
		limits, err := m.EffectiveLimits()
		if err != nil {
			return nil, err
		}
		if maintenance, err = m.maintenanceAllowance(limits); err != nil {
			return nil, err
		}
	}
	total, err := addInt64(allowance, maintenance)
	if err != nil {
		return nil, err
	}
	return &generationReservation{
		allowance:   allowance,
		total:       total,
		manifest:    plan.Breakdown.ManifestBytes,
		maintenance: maintenance,
	}, nil
}

// baselineGenerationReservationBytes bounds what creating one generation costs
// before any capture writes into it: its directory and subdirectories, its git
// metadata files, its manifest record, and the manifests the workspace must
// hold at once.
//
// The bound is deliberately generous. It decides whether creating a replacement
// generation is permissible at all, and a bound that under-estimated the
// baseline would authorise a creation the store cannot pay for.
func baselineGenerationReservationBytes(limits Limits, gens *WorkspaceGenerations) (int64, error) {
	unit, err := limits.RoundUp(0)
	if err != nil {
		return 0, err
	}
	// One allocation unit per generation directory, plus HEAD and config.
	entries, err := addInt64(int64(len(generationDirs)), 3)
	if err != nil {
		return 0, err
	}
	dirs, err := mulDiv(entries, unit, 1)
	if err != nil {
		return 0, err
	}
	// The generation's manifest record.
	recordBound, err := limits.RoundUp(1024)
	if err != nil {
		return 0, err
	}
	// The manifests: an atomic replace holds both the old and the new file, so
	// a first manifest costs two bounded files and a later one costs at least
	// the same.
	manifest, err := mulDiv(2, unit, 1)
	if err != nil {
		return 0, err
	}
	if gens != nil && gens.ManifestAllocated > manifest {
		manifest = gens.ManifestAllocated
	}
	// A generation directory entry is charged by the workspace measurement as
	// well; charging it here too can only over-reserve, never under-reserve.
	total, err := addInt64(dirs, recordBound)
	if err != nil {
		return 0, err
	}
	return addInt64(total, manifest)
}

// checkReservation compares one reservation against every ceiling admission
// enforces and returns nil when it fits.
//
// The three checks are independent on purpose: a store can be well inside both
// configured budgets and still be unable to write because the filesystem is
// nearly full, and reporting that as "budget exhausted" would send the user
// looking at the wrong setting.
func (m *Manager) checkReservation(ctx context.Context, cat *Catalog, usage *StoreUsage,
	res *generationReservation) (*StoreError, error) {

	if err := ctx.Err(); err != nil {
		return nil, interruptedCapture(err)
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}

	// (a) The per-workspace ceiling.
	wsAllocated := workspaceStoreAllocated(usage, cat.Workspace())
	wsProjected, err := addInt64(wsAllocated, res.total)
	if err != nil {
		return nil, err
	}
	if wsProjected > limits.WorkspaceMaxBytes {
		return m.storeErr(ReasonBudgetExhausted, cat.Root(),
			"capture needs %d bytes (allowance %d + maintenance %d) but the workspace store holds "+
				"%d of its %d byte limit, over by %d; the store is not grown",
			res.total, res.allowance, res.maintenance, wsAllocated, limits.WorkspaceMaxBytes,
			wsProjected-limits.WorkspaceMaxBytes), nil
	}

	// (b) The user-global ceiling. EffectiveLimits consults the injected reader
	//     on every call, so a ceiling another process lowered takes effect here
	//     without a restart and without a stale session value.
	globalProjected, err := addInt64(usage.GlobalAllocated, res.total)
	if err != nil {
		return nil, err
	}
	if globalProjected > limits.GlobalMaxBytes {
		return m.storeErr(ReasonBudgetExhausted, m.Root(),
			"capture needs %d bytes but every managed store holds %d of the %d byte global limit, "+
				"over by %d; the store is not grown",
			res.total, usage.GlobalAllocated, limits.GlobalMaxBytes,
			globalProjected-limits.GlobalMaxBytes), nil
	}

	// (c) Available filesystem space, including the reserve.
	if err := m.CheckFreeSpace(res.total); err != nil {
		var se *StoreError
		if errors.As(err, &se) {
			return se, nil
		}
		return nil, err
	}
	return nil, nil
}

// checkBaseline compares a generation baseline reservation against the ceilings.
// It is checkReservation's narrower sibling, used before a generation exists.
func (m *Manager) checkBaseline(ctx context.Context, cat *Catalog, usage *StoreUsage, baseline int64) (*StoreError, error) {
	if err := ctx.Err(); err != nil {
		return nil, interruptedCapture(err)
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}
	wsAllocated := workspaceStoreAllocated(usage, cat.Workspace())
	if wsProjected, err := addInt64(wsAllocated, baseline); err != nil {
		return nil, err
	} else if wsProjected > limits.WorkspaceMaxBytes {
		return m.storeErr(ReasonBudgetExhausted, cat.Root(),
			"a new generation needs %d bytes of baseline metadata but the workspace store holds %d "+
				"of its %d byte limit; raise snapshots.workspace_max_bytes or set snapshots.enabled = false",
			baseline, wsAllocated, limits.WorkspaceMaxBytes), nil
	}
	if globalProjected, err := addInt64(usage.GlobalAllocated, baseline); err != nil {
		return nil, err
	} else if globalProjected > limits.GlobalMaxBytes {
		return m.storeErr(ReasonBudgetExhausted, m.Root(),
			"a new generation needs %d bytes of baseline metadata but every managed store holds %d of "+
				"the %d byte global limit", baseline, usage.GlobalAllocated, limits.GlobalMaxBytes), nil
	}
	if err := m.CheckFreeSpace(baseline); err != nil {
		var se *StoreError
		if errors.As(err, &se) {
			return se, nil
		}
		return nil, err
	}
	return nil, nil
}

// refuseAdmission records a refusal on the result and returns it.
func refuseAdmission(result *AdmissionResult, err *StoreError) *AdmissionResult {
	if result == nil {
		result = &AdmissionResult{}
	}
	result.Outcome = AdmissionRefused
	result.GenerationID = ""
	result.Reason = err.Reason
	result.Err = err
	return result
}

// isAdmissionRefusal reports whether a reason is a decision admission reports as
// a refusal rather than as a failure to decide.
func isAdmissionRefusal(reason StoreReason) bool {
	return reason == ReasonBudgetExhausted || reason == ReasonInsufficientFreeSpace
}

// storeErr builds a path-carrying *StoreError. It is the *StoreError-returning
// sibling of Manager.storeErrorf, for the call sites here that carry the reason
// forward rather than only reporting it.
func (m *Manager) storeErr(reason StoreReason, path, format string, args ...any) *StoreError {
	se := storeError(reason, fmt.Errorf(format, args...))
	se.Path = path
	return se
}

// ---------------------------------------------------------------------------
// Capture lifecycle: admission, staging, publication intent, cleanup
// ---------------------------------------------------------------------------

// AdmitAndCaptureWithCleanup is the whole managed capture lifecycle: admit,
// reserve staging, record publication intent, capture, publish, clear.
//
// It exists so no caller has to remember the order, because the order is what
// makes an interruption recoverable:
//
//  1. Admit, which reconciles first and reserves the worst case.
//  2. Reserve the staging directory and persist the in-flight capture record.
//  3. Capture with a publication-intent hook wired to the catalog, so the
//     intent is durable BEFORE the ref is written. A crash between the ref
//     landing and the manifest catching up then leaves a record naming the ref
//     to look for, which is how reconciliation can tell "published" from
//     "never published".
//  4. Clear the record and its scratch — only after the ref is durable, so a
//     failure here leaves the intent for reconciliation rather than losing it.
//
// It returns the published hash (empty on every failure) plus the admission
// result, so a caller can report what was decided even when the capture failed.
func (m *Manager) AdmitAndCaptureWithCleanup(ctx context.Context, cat *Catalog, req CaptureRequest) (string, *AdmissionResult, error) {
	admission, err := m.AdmitCapture(ctx, cat, req)
	if err != nil {
		return "", admission, err
	}

	state, err := cat.ReserveStaging(ctx, admission.GenerationID)
	if err != nil {
		return "", admission, err
	}
	req.Allowance = admission.Allowance
	req.beforePublish = func(hash string) error {
		_, err := cat.SetPublicationIntent(state.StagingID, hash)
		return err
	}

	result, err := m.captureToGeneration(ctx, cat, admission.GenerationID, admission.Plan, req)
	if err != nil {
		// The capture did not publish. The in-flight record and its scratch are
		// cleared; anything the attempt wrote is counted as abandoned by the
		// next reconciliation rather than deleted here, because a Git object
		// may be shared with a snapshot a live ref still needs.
		if clearErr := cat.ClearCapture(); clearErr != nil {
			return "", admission, errors.Join(err, clearErr)
		}
		return "", admission, err
	}
	if result == nil || result.Hash == "" {
		return "", admission, storeErrorf(ReasonInternal, cat.Workspace(),
			"capture completed without a published snapshot")
	}

	// The ref is durable, so the generation now retains a rollback point.
	if err := cat.recordPublication(admission.GenerationID); err != nil {
		return result.Hash, admission, err
	}
	if err := cat.ClearCapture(); err != nil {
		return result.Hash, admission, err
	}
	// Sealing a generation that has outgrown its rotation target is what makes
	// its history eligible for whole-generation reclamation. It happens AFTER
	// publication, so a seal that failed could never orphan a published
	// snapshot.
	if m.overRotationTarget(ctx, cat, admission.GenerationID) {
		if err := cat.SealGeneration(admission.GenerationID); err != nil {
			return result.Hash, admission, err
		}
	}
	return result.Hash, admission, nil
}

// overRotationTarget reports whether a generation now exceeds its rotation
// target and must be sealed immediately.
//
// A generation whose measurement cannot be read is NOT sealed: the next
// admission pass re-measures it and decides then, and sealing on an
// unmeasurable figure would rotate a healthy generation for no reason.
func (m *Manager) overRotationTarget(ctx context.Context, cat *Catalog, id string) bool {
	gens, err := m.MeasureWorkspaceGenerations(ctx, cat.Workspace())
	if err != nil {
		return false
	}
	gu := gens.ByID[id]
	if gu == nil || gu.Protected || gu.Refs < 0 {
		return false
	}
	return gu.Allocated >= m.rotationTarget()
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// generationOrderKey returns the ordering key for oldest-first reclamation: the
// last-publication time, falling back to the creation time for a generation
// that has never published. It is a fixed-width timestamp string so lexical
// comparison is chronological comparison.
func generationOrderKey(rec *GenerationRecord) string {
	if rec == nil {
		return ""
	}
	if !rec.LastPublishedAt.IsZero() {
		return rec.LastPublishedAt.UTC().Format("20060102150405.000000000")
	}
	if !rec.CreatedAt.IsZero() {
		return rec.CreatedAt.UTC().Format("20060102150405.000000000")
	}
	return ""
}

// workspaceStoreAllocated returns the measured allocated size of a versioned
// workspace store, or 0 when it has none.
func workspaceStoreAllocated(usage *StoreUsage, workspace string) int64 {
	if usage == nil {
		return 0
	}
	if gens := usage.Generations[workspace]; gens != nil {
		return gens.Allocated
	}
	if u := usage.Workspaces[workspace]; u != nil {
		return u.Allocated
	}
	return 0
}
