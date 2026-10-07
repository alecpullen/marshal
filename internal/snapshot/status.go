package snapshot

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// This file renders `marshal snapshots status`: the READ-ONLY store report.
//
// It is a rendering of two measurements that already exist — the versioned
// store's per-workspace accounting (usage.go) and the legacy survey (legacy.go) —
// and it deliberately adds no storage behaviour of its own. Nothing here
// reconciles, reclaims, mutates, or repacks; nothing here needs a provider, a
// model, or a TUI. A user can run it on a machine with no configuration at all.
//
// Two properties of the report are load-bearing:
//
//  1. EVERY NUMBER IS A MEASUREMENT. Retained bytes, budgets, and the migration
//     closure are measured or configured, never estimated from a file's age.
//  2. EVERY UNKNOWN IS STATED AS UNKNOWN. A legacy store records only a
//     workspace hash, so its root is reported as unknown rather than invented;
//     a closure that could not be enumerated is reported as unknown rather than
//     as zero, because "unknown" must never read as "nothing".

// StoreStatus is the read-only, renderable state of the snapshot store.
type StoreStatus struct {
	// Root is the snapshots root.
	Root string
	// Limits are the effective budgets.
	Limits Limits
	// GlobalAllocated is the measured size of every managed store together.
	GlobalAllocated int64
	// V2Allocated and LegacyAllocated split that total by layout. The split
	// matters because only the versioned side is ever automatically reclaimed.
	V2Allocated     int64
	LegacyAllocated int64
	// Workspaces lists each versioned workspace store.
	Workspaces []WorkspaceStatus
	// Legacy lists each pre-v2 store.
	Legacy []*LegacyInfo
	// Remnants lists stores an interrupted reset renamed aside.
	Remnants []LegacyRemnant
	// Warnings collects every structured problem found.
	Warnings []MaintenanceWarning
	// UsageUnavailable is set when the whole-root accounting could not be read,
	// in which case the totals above are meaningless and say so.
	UsageUnavailable error
}

// WorkspaceStatus is one versioned workspace store's reported state.
type WorkspaceStatus struct {
	// Workspace is the workspace hash.
	Workspace string
	// Root is the canonical workspace root, or "" when the checkout was deleted
	// before the root was ever recorded.
	Root string
	// Allocated is the measured size of the versioned store.
	Allocated int64
	// Generations lists each generation with its measured size and protection.
	Generations []GenerationUsage
	// Unlisted lists generation directories the manifest does not describe.
	Unlisted []*GenerationUsage
	// StagingAllocated is the measured size of capture scratch.
	StagingAllocated int64
	// Unpublished is the measured size of known abandoned artifacts.
	Unpublished int64
	// Corrupt is set when the manifest could not be read.
	Corrupt bool
	// OverBudget reports whether the store is over its per-workspace ceiling.
	OverBudget bool
	// Warnings are this workspace's problems.
	Warnings []MaintenanceWarning
}

// BuildStoreStatus performs the read-only measurements the status report renders.
//
// It requires the root to exist; an absent root reports an empty store rather
// than an error, so a user on a fresh machine gets a useful answer ("nothing has
// been captured") instead of a failure.
func (m *Manager) BuildStoreStatus(ctx context.Context) (*StoreStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}
	out := &StoreStatus{Root: m.root, Limits: limits}

	usage, usageErr := m.UsageContext(ctx)
	if usageErr != nil {
		// A store that cannot be measured is reported as unmeasurable rather
		// than as empty: the two mean very different things to the operator.
		out.UsageUnavailable = usageErr
	} else {
		out.GlobalAllocated = usage.GlobalAllocated
		out.V2Allocated = usage.V2Allocated
		out.LegacyAllocated = usage.LegacyAllocated
		for _, ws := range sortedWorkspaceHashes(usage) {
			st, err := m.workspaceStatus(ctx, ws, usage, limits)
			if err != nil {
				return nil, err
			}
			out.Workspaces = append(out.Workspaces, st)
			out.Warnings = append(out.Warnings, st.Warnings...)
		}
	}

	survey, err := m.SurveyLegacy(ctx)
	if err != nil {
		return nil, err
	}
	out.Legacy = survey.Stores
	out.Remnants = survey.Remnants
	out.Warnings = append(out.Warnings, survey.Warnings...)
	return out, nil
}

// workspaceStatus measures one versioned workspace for the report.
func (m *Manager) workspaceStatus(ctx context.Context, workspace string, usage *StoreUsage, limits Limits) (WorkspaceStatus, error) {
	st := WorkspaceStatus{Workspace: workspace}
	gens, err := m.MeasureWorkspaceGenerations(ctx, workspace)
	if err != nil {
		return st, err
	}
	st.Allocated = gens.Allocated
	st.StagingAllocated = gens.StagingAllocated
	st.Unpublished = gens.AbandonedAllocated
	st.Corrupt = gens.Corrupt
	for _, id := range gens.Order {
		if gu := gens.ByID[id]; gu != nil {
			st.Generations = append(st.Generations, *gu)
		}
	}
	st.Unlisted = gens.Unlisted
	st.OverBudget = st.Allocated > limits.WorkspaceMaxBytes
	// The workspace root is read from a manifest that was already validated by
	// the measurement. A store with no recorded root (a legacy-origin catalog
	// written by a migration, or a checkout deleted before the root was ever
	// recorded) reports an empty root, which the renderer shows as unknown.
	cat, err := m.Catalog(workspace)
	if err != nil {
		return st, err
	}
	if man, err := cat.Load(); err == nil && man != nil {
		st.Root = man.Root
	}
	if gens.Corrupt {
		st.Warnings = append(st.Warnings, MaintenanceWarning{
			Reason:    ReasonUnreadableFile,
			Workspace: workspace,
			Path:      cat.ManifestPath(),
			Message:   "manifest is unreadable, so this workspace is quarantined from capture and from deletion",
		})
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// RenderStoreStatus renders the status report as plain text.
//
// The layout is fixed and greppable: one section per question the operator has
// ("what is there?", "is anything over budget?", "what can I recover?"), with
// every unknown spelled out. It is deliberately plain text rather than styled:
// `marshal snapshots status` must work with no TUI, no terminal, and no theme.
func RenderStoreStatus(st *StoreStatus) string {
	if st == nil {
		return "No snapshot store state was measured."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Marshal snapshot store: %s\n", st.Root)
	fmt.Fprintf(&b, "Budgets: workspace %s per workspace, global %s across all workspaces\n\n",
		formatBytes(st.Limits.WorkspaceMaxBytes), formatBytes(st.Limits.GlobalMaxBytes))

	if st.UsageUnavailable != nil {
		fmt.Fprintf(&b, "USAGE UNAVAILABLE: the store could not be measured, so no total is reported.\n")
		fmt.Fprintf(&b, "  %s\n\n", st.UsageUnavailable)
	} else {
		fmt.Fprintf(&b, "Measured usage: %s total (%s versioned, %s old-format)\n",
			formatBytes(st.GlobalAllocated), formatBytes(st.V2Allocated), formatBytes(st.LegacyAllocated))
		if st.GlobalAllocated > st.Limits.GlobalMaxBytes {
			fmt.Fprintf(&b, "  OVER the global budget by %s\n",
				formatBytes(st.GlobalAllocated-st.Limits.GlobalMaxBytes))
		}
	}

	// Versioned stores.
	fmt.Fprintf(&b, "\nVersioned workspaces (%d):\n", len(st.Workspaces))
	if len(st.Workspaces) == 0 {
		fmt.Fprintf(&b, "  none\n")
	}
	for i := range st.Workspaces {
		ws := &st.Workspaces[i]
		root := ws.Root
		if root == "" {
			root = "unknown (never recorded)"
		}
		fmt.Fprintf(&b, "\n  workspace %s\n", ws.Workspace)
		fmt.Fprintf(&b, "    root:          %s\n", root)
		fmt.Fprintf(&b, "    used:          %s of %s\n",
			formatBytes(ws.Allocated), formatBytes(st.Limits.WorkspaceMaxBytes))
		if ws.Corrupt {
			fmt.Fprintf(&b, "    state:         QUARANTINED (manifest unreadable)\n")
		}
		if ws.OverBudget {
			fmt.Fprintf(&b, "    state:         over budget by %s\n",
				formatBytes(ws.Allocated-st.Limits.WorkspaceMaxBytes))
		}
		fmt.Fprintf(&b, "    scrub:         %s capture scratch, %s unpublished\n",
			formatBytes(ws.StagingAllocated), formatBytes(ws.Unpublished))
		fmt.Fprintf(&b, "    generations:   %d\n", len(ws.Generations))
		for _, g := range ws.Generations {
			fmt.Fprintf(&b, "      %s  %s  %s%s\n", g.ID, formatBytes(g.Allocated),
				generationStateLabel(g), protectedLabel(g.Protected))
		}
		for _, g := range ws.Unlisted {
			fmt.Fprintf(&b, "      %s  %s  unlisted in the manifest%s\n",
				g.ID, formatBytes(g.Allocated), protectedLabel(g.Protected))
		}
	}

	// Legacy stores.
	feasible := 0
	for _, l := range st.Legacy {
		if l.MigrationFeasible(st.Limits) {
			feasible++
		}
	}
	fmt.Fprintf(&b, "\nOld-format workspaces (%d):\n", len(st.Legacy))
	if len(st.Legacy) == 0 {
		fmt.Fprintf(&b, "  none\n")
	}
	for _, l := range st.Legacy {
		fmt.Fprintf(&b, "\n  workspace %s\n", l.Workspace)
		fmt.Fprintf(&b, "    root:            unknown (the old layout records only the workspace hash)\n")
		fmt.Fprintf(&b, "    store:           %s\n", l.Path)
		fmt.Fprintf(&b, "    used:            %s of %s\n",
			formatBytes(l.Bytes), formatBytes(st.Limits.WorkspaceMaxBytes))
		if l.RefsUnreadable {
			fmt.Fprintf(&b, "    snapshot refs:   UNKNOWN (could not be read: %v)\n", l.RefListingError)
		} else {
			fmt.Fprintf(&b, "    snapshot refs:   %d\n", l.SnapshotRefs)
		}
		fmt.Fprintf(&b, "    branch refs:     %d (%s)\n", len(l.Branches), headNames(l))
		if l.ClosureError != nil {
			fmt.Fprintf(&b, "    reachable:       UNKNOWN (closure could not be enumerated: %v)\n", l.ClosureError)
		} else {
			fmt.Fprintf(&b, "    reachable:       %d objects, %s\n", l.Objects, formatBytes(l.ParentsNeeded))
		}
		fmt.Fprintf(&b, "    abandoned temps: %d (%s)\n", len(l.Disposable), formatBytes(l.DisposableBytes))
		for _, d := range boundedDisposables(l.Disposable) {
			fmt.Fprintf(&b, "      %s  %s\n", formatBytes(d.Bytes), d.Path)
		}
		fmt.Fprintf(&b, "    retained packs:  %d\n", len(l.Packs))
		if l.RetainsHistory() {
			migratable := "no — see the reason above"
			if l.MigrationFeasible(st.Limits) {
				migratable = "yes"
			}
			fmt.Fprintf(&b, "    migration:       %s\n", migratable)
			if !l.MigrationFeasible(st.Limits) && l.ClosureError == nil && !l.RefsUnreadable {
				need, err := addInt64(l.ParentsNeeded, l.Bytes)
				if err == nil {
					fmt.Fprintf(&b, "                     the closure (%s) plus the original (%s) = %s, which exceeds a budget;\n",
						formatBytes(l.ParentsNeeded), formatBytes(l.Bytes), formatBytes(need))
					fmt.Fprintf(&b, "                     a hash-preserving migration is refused rather than copying only part of the history\n")
				}
			}
		} else {
			fmt.Fprintf(&b, "    migration:       nothing to migrate (no snapshots, no branches)\n")
		}
	}

	// Interrupted-reset remnants.
	if len(st.Remnants) > 0 {
		fmt.Fprintf(&b, "\nInterrupted resets awaiting cleanup (%d):\n", len(st.Remnants))
		for _, r := range st.Remnants {
			fmt.Fprintf(&b, "  workspace %s  %s  %s\n", r.Workspace, formatBytes(r.Bytes), r.Path)
		}
	}

	// Warnings.
	if len(st.Warnings) > 0 {
		fmt.Fprintf(&b, "\nWarnings (%d):\n", len(st.Warnings))
		for _, w := range dedupeWarnings(st.Warnings) {
			fmt.Fprintf(&b, "  %s\n", w.String())
		}
	}

	// The ownership uncertainty is stated unconditionally, because it is a
	// property of the old layout rather than of any particular store.
	fmt.Fprintf(&b, "\nOwnership note:\n")
	fmt.Fprintf(&b, "  Old Marshal binaries do not honour the new advisory store lock and cannot be detected by this\n")
	fmt.Fprintf(&b, "  command. Any old-format store below may be being written by a running old instance. Recovering\n")
	fmt.Fprintf(&b, "  one requires stopping every old instance first; see 'marshal snapshots cleanup'.\n")

	// Next commands.
	fmt.Fprintf(&b, "\nNext:\n")
	if abandoned := totalDisposable(st.Legacy); abandoned > 0 {
		fmt.Fprintf(&b, "  marshal snapshots cleanup              remove %s of abandoned temporary files\n", formatBytes(abandoned))
	}
	fmt.Fprintf(&b, "  marshal snapshots migrate --workspace <id>   move an old store into the new layout\n")
	fmt.Fprintf(&b, "  marshal snapshots reset --workspace <id>     delete an old store (requires the full ID)\n")
	return b.String()
}

// totalDisposable sums the recognized abandoned artifacts across legacy stores.
func totalDisposable(stores []*LegacyInfo) int64 {
	var total int64
	for _, l := range stores {
		total += l.DisposableBytes
	}
	return total
}

// boundedDisposables caps how many artifact paths the report prints. The COUNT
// and the byte total are always complete; only the path list is truncated, so a
// store holding thousands of abandoned temporary packs cannot flood the report.
func boundedDisposables(all []legacyDisposable) []legacyDisposable {
	const max = 8
	if len(all) <= max {
		return all
	}
	return all[:max]
}

// generationStateLabel renders a generation's state, including whether it is
// currently a capture target or a retained rollback archive.
func generationStateLabel(g GenerationUsage) string {
	if !g.Listed {
		return "not listed in the manifest"
	}
	if g.Refs < 0 {
		return "refs unreadable"
	}
	return fmt.Sprintf("%d snapshots", g.Refs)
}

// protectedLabel annotates a protected generation.
func protectedLabel(protected bool) string {
	if protected {
		return " [PROTECTED: never auto-reclaimed]"
	}
	return ""
}

// headNames renders a bounded branch list.
func headNames(l *LegacyInfo) string {
	heads := l.Heads()
	if len(heads) == 0 {
		return "none"
	}
	const max = 4
	out := append([]string(nil), heads...)
	if len(out) > max {
		out = append(out[:max], fmt.Sprintf("… and %d more", len(heads)-max))
	}
	return strings.Join(out, ", ")
}

// dedupeWarnings collapses repeated warnings, preserving the first order.
func dedupeWarnings(in []MaintenanceWarning) []MaintenanceWarning {
	seen := make(map[string]bool, len(in))
	var out []MaintenanceWarning
	for _, w := range in {
		key := string(w.Reason) + "\x00" + w.Workspace + "\x00" + w.Path + "\x00" + w.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, w)
	}
	return out
}

// RenderCleanupReport renders what a cleanup pass did.
func RenderCleanupReport(r *CleanupReport, root string) string {
	if r == nil {
		return "Nothing to clean."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Snapshot cleanup: %s\n", root)
	if len(r.Reconciled) > 0 || r.StagingRemoved > 0 || len(r.GenerationsRemoved) > 0 {
		fmt.Fprintf(&b, "\nVersioned store (reconciled automatically):\n")
		fmt.Fprintf(&b, "  workspaces reconciled: %d\n", len(r.Reconciled))
		fmt.Fprintf(&b, "  capture scratch removed: %d\n", r.StagingRemoved)
		fmt.Fprintf(&b, "  generations removed: %d\n", len(r.GenerationsRemoved))
		for _, id := range r.GenerationsRemoved {
			fmt.Fprintf(&b, "    %s\n", id)
		}
	}
	if len(r.RemnantsRemoved) > 0 {
		fmt.Fprintf(&b, "\nInterrupted resets finished: %d (%s)\n", len(r.RemnantsRemoved), formatBytes(r.RemnantBytes))
	}
	if len(r.TempArtifactsRemoved) > 0 {
		fmt.Fprintf(&b, "\nAbandoned temporary files removed: %d (%s)\n", len(r.TempArtifactsRemoved), formatBytes(r.TempBytes))
		for _, a := range r.TempArtifactsRemoved {
			fmt.Fprintf(&b, "  %s  %s  %s\n", a.Workspace, formatBytes(a.Bytes), a.Path)
		}
	} else if !r.Confirmed {
		fmt.Fprintf(&b, "\nOld-format stores were left untouched: no offline acknowledgement was obtained.\n")
	}
	if len(r.LegacyUntouched) > 0 {
		fmt.Fprintf(&b, "  old-format stores inspected and left as they were: %d\n", len(r.LegacyUntouched))
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintf(&b, "\nWarnings (%d):\n", len(r.Warnings))
		for _, w := range dedupeWarnings(r.Warnings) {
			fmt.Fprintf(&b, "  %s\n", w.String())
		}
	}
	return b.String()
}

// RenderMigrationReport renders what a migration did.
func RenderMigrationReport(r *MigrationReport, root string) string {
	if r == nil {
		return "Nothing to migrate."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Snapshot migration: %s\n", root)
	fmt.Fprintf(&b, "  workspace:        %s\n", r.Workspace)
	if r.GenerationID == "" {
		fmt.Fprintf(&b, "  result:           nothing to migrate\n")
	} else {
		fmt.Fprintf(&b, "  new generation:   %s (sealed, PROTECTED, legacy origin)\n", r.GenerationID)
		fmt.Fprintf(&b, "  objects copied:   %d (%s)\n", r.Objects, formatBytes(r.Bytes))
		fmt.Fprintf(&b, "  snapshots kept:   %d (%s)\n", len(r.SnapshotHashes), hashList(r.SnapshotHashes))
		fmt.Fprintf(&b, "  branch tips kept: %d (%s)\n", len(r.BranchHashes), hashList(r.BranchHashes))
		if r.DuplicateRemoved {
			fmt.Fprintf(&b, "  original store:   removed after every hash was verified to resolve in the new location\n")
		} else if r.RemnantPath != "" {
			fmt.Fprintf(&b, "  original store:   renamed to %s; run 'marshal snapshots cleanup' to finish removing it\n", r.RemnantPath)
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintf(&b, "\nWarnings (%d):\n", len(r.Warnings))
		for _, w := range dedupeWarnings(r.Warnings) {
			fmt.Fprintf(&b, "  %s\n", w.String())
		}
	}
	return b.String()
}

// RenderResetReport renders what a reset did.
func RenderResetReport(r *ResetReport) string {
	if r == nil {
		return "Nothing to reset."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Snapshot reset: workspace %s\n", r.Workspace)
	fmt.Fprintf(&b, "  target:           %s\n", r.Path)
	fmt.Fprintf(&b, "  snapshots removed: %d (%s)\n", len(r.SnapshotHashes), hashList(r.SnapshotHashes))
	fmt.Fprintf(&b, "  branches removed:  %d (%s)\n", len(r.Branches), strings.Join(r.Branches, ", "))
	if r.Removed {
		fmt.Fprintf(&b, "  removed:          %s freed\n", formatBytes(r.Bytes))
		fmt.Fprintf(&b, "  untouched:        your project's files and the project's own .git directory\n")
	} else if r.RemnantPath != "" {
		fmt.Fprintf(&b, "  removed:          not finished; the store was renamed to %s\n", r.RemnantPath)
		fmt.Fprintf(&b, "                    run 'marshal snapshots cleanup' to finish removing it\n")
	}
	return b.String()
}

// RenderDiscardReport renders what a history discard did.
func RenderDiscardReport(r *DiscardReport) string {
	if r == nil {
		return "Nothing to discard."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Snapshot history discard: workspace %s\n", r.Workspace)
	fmt.Fprintf(&b, "  generation:       %s\n", r.GenerationID)
	fmt.Fprintf(&b, "  snapshots removed: %d (%s)\n", len(r.Hashes), hashList(r.Hashes))
	if r.Removed {
		fmt.Fprintf(&b, "  removed:          %s freed; protection lifted and the generation deleted\n", formatBytes(r.Bytes))
	} else {
		fmt.Fprintf(&b, "  removed:          no\n")
	}
	return b.String()
}

// sortLegacyBySize orders legacy stores largest first, which is the order an
// operator wants when deciding what to recover.
func sortLegacyBySize(stores []*LegacyInfo) {
	sort.SliceStable(stores, func(i, j int) bool {
		if stores[i].Bytes != stores[j].Bytes {
			return stores[i].Bytes > stores[j].Bytes
		}
		return stores[i].Workspace < stores[j].Workspace
	})
}
