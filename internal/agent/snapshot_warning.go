package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/strutil"
)

// This file owns the user-visible story about missing rollback protection.
//
// Before it, a snapshot failure or skip was logged at Warn and nothing else
// reached the user (execute.go's pre-write hook and runner.go's turn-start
// hook). Once storage bounds made a capture legitimately *skippable* — budget
// exhausted, insufficient free space, pending offline legacy recovery — that
// silence became a defect: a write could proceed with no usable rollback
// snapshot while the transcript still looked as though rollback protection
// existed. The old pre-write hook had the same hole structurally: Track
// returning ("", nil) matched neither of its two branches, so a skip produced
// no signal at all.
//
// Everything here is additive. It never changes tool continuation, approval,
// or permission policy; it never blocks a tool; and it never claims a snapshot
// exists when it does not.

// Recovery commands. They are spelled once, here, because the warning is only
// useful if the user can copy them verbatim. They name the `marshal snapshots`
// CLI that the storage-bounds recovery work provides.
//
// The WORKSPACE_ARGUMENT placeholder is part of the migrate command rather than
// an omission: `marshal snapshots migrate` has no default target and refuses to
// run without an explicit workspace ID, so a warning that printed the bare
// subcommand would send the user straight to a usage error. `status` is what
// tells them which ID to substitute, which is why it always comes first.
const (
	snapshotStatusCommand  = "marshal snapshots status"
	snapshotCleanupCommand = "marshal snapshots cleanup"
	snapshotMigrateCommand = "marshal snapshots migrate --workspace " + snapshotWorkspaceArgument
	// snapshotWorkspaceArgument is the placeholder in the migrate command. It
	// is angle-bracketed so it cannot be mistaken for a literal to type.
	snapshotWorkspaceArgument = "<workspace-id>"

	snapshotUnknownWorkspace = "unknown workspace"

	// snapshotCauseRunes bounds the cause text quoted into a transcript
	// message: an error chain can be arbitrarily long and the transcript is
	// the wrong place for it (the full text is always in the log).
	snapshotCauseRunes = 300
)

// SnapshotPhase names where a snapshot operation was attempted. It only
// changes the warning's wording; every phase shares one policy.
type SnapshotPhase string

const (
	// SnapshotPhaseTurnStart is the capture at the top of a user turn.
	SnapshotPhaseTurnStart SnapshotPhase = "turn-start"
	// SnapshotPhasePreWrite is the capture immediately before a
	// non-read-only tool call.
	SnapshotPhasePreWrite SnapshotPhase = "pre-write"
	// SnapshotPhaseMaintenance is a startup/close reconciliation or prune
	// that the runtime performs outside any turn.
	SnapshotPhaseMaintenance SnapshotPhase = "maintenance"
)

// snapshotPhaseLabel is the human-readable phase prefix of a warning.
func snapshotPhaseLabel(p SnapshotPhase) string {
	switch p {
	case SnapshotPhaseTurnStart:
		return "Turn-start snapshot"
	case SnapshotPhasePreWrite:
		return "Pre-write snapshot"
	case SnapshotPhaseMaintenance:
		return "Snapshot maintenance"
	default:
		return "Snapshot"
	}
}

// snapshotReason is the agent package's local projection of the snapshot
// store's structured failure reason (internal/snapshot.StoreReason).
//
// It is a local string type rather than an import of internal/snapshot, on
// purpose. The Snapshotter surface is deliberately defined here in terms of
// session.Snapshotter so that internal/agent and internal/snapshot import
// neither each other nor a shared cycle — Runner is also built from a bare
// struct literal in tests and by the swarm, so a package dependency would be
// felt everywhere. To recover a structured reason without that import we try
// two mechanisms, in order:
//
//  1. an optional structural accessor: any error exposing a SnapshotReason()
//     string method is asked directly (see snapshotReasoner). *snapshot.StoreError
//     does not implement it today; adding the method there later is picked up
//     here with no change on this side.
//  2. a token match against the store's documented reason identifiers, which
//     StoreError.Error() prints verbatim as its first field
//     (internal/snapshot/errors.go). The tokens are lowercase snake_case and
//     cannot collide with ordinary error prose.
//
// The reason feeds wording and deduplication only. Nothing branches on it for
// control flow, so a misclassification degrades to poorer copy, never to a
// policy change.
type snapshotReason string

const (
	// Reasons mirrored from internal/snapshot/errors.go (StoreReason values).
	reasonBudgetExhausted        snapshotReason = "budget_exhausted"
	reasonInsufficientFreeSpace  snapshotReason = "insufficient_free_space"
	reasonUnreadableFile         snapshotReason = "unreadable_file"
	reasonLockTimeout            snapshotReason = "lock_timeout"
	reasonInterruptedCapture     snapshotReason = "interrupted_capture"
	reasonLegacyRecoveryRequired snapshotReason = "legacy_recovery_required"
	reasonAccountingOverflow     snapshotReason = "accounting_overflow"
	reasonUnsupportedPlatform    snapshotReason = "unsupported_platform"
	reasonInvalidLimits          snapshotReason = "invalid_limits"
	reasonNotOwned               snapshotReason = "store_not_owned"
	reasonSymlinkEscape          snapshotReason = "symlink_escape"
	reasonInternalError          snapshotReason = "internal_error"

	// Reasons this package derives from the capture outcome itself.
	//
	// reasonCaptureSkipped is Track returning ("", nil): no error, but no
	// snapshot either. This is the regression the storage-bounds work
	// introduced an honest reason for.
	reasonCaptureSkipped snapshotReason = "capture_skipped"
	// reasonCaptureFailed is a capture that returned an error carrying no
	// recognised reason.
	reasonCaptureFailed snapshotReason = "capture_failed"
	// reasonCaptureUnrecorded is a capture that PUBLISHED a hash but whose
	// session database row could not be written. It is deliberately distinct
	// from a capture failure: the snapshot exists in the store, it is simply
	// not listed for this session.
	reasonCaptureUnrecorded snapshotReason = "capture_unrecorded"
)

// storeReasonTokens are the snapshot store's stable reason identifiers, most
// specific first where it matters. A store error's message begins with its
// reason, so a containment check on the lowercased message is enough.
var storeReasonTokens = []snapshotReason{
	reasonBudgetExhausted,
	reasonInsufficientFreeSpace,
	reasonLegacyRecoveryRequired,
	reasonInterruptedCapture,
	reasonLockTimeout,
	reasonUnreadableFile,
	reasonAccountingOverflow,
	reasonUnsupportedPlatform,
	reasonInvalidLimits,
	reasonNotOwned,
	reasonSymlinkEscape,
	reasonInternalError,
}

// snapshotReasoner is the optional structural accessor described above.
type snapshotReasoner interface{ SnapshotReason() string }

// snapshotReasonFor derives the reason for err, which must be non-nil.
func snapshotReasonFor(err error) snapshotReason {
	if err == nil {
		return reasonCaptureFailed
	}
	var r snapshotReasoner
	if errors.As(err, &r) {
		if s := strings.TrimSpace(r.SnapshotReason()); s != "" {
			return snapshotReason(s)
		}
	}
	msg := strings.ToLower(err.Error())
	for _, known := range storeReasonTokens {
		if strings.Contains(msg, string(known)) {
			return known
		}
	}
	return reasonCaptureFailed
}

// snapshotIsCancellation reports whether err is the expected result of the
// caller's context ending (turn abort, shutdown) rather than a storage fault.
//
// A cancelled context is not a "recovery required" event: pointing the user at
// a recovery workflow when they simply stopped the agent is noise, and at
// shutdown it is noise they cannot act on. The test is error-based first — a
// store error wrapping ctx.Err() is recognised by errors.Is — and only treats
// our own context's expiry as cancellation, so a store-internal timeout while
// the caller's context is still live stays visible.
func snapshotIsCancellation(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if ctx != nil {
		if cerr := ctx.Err(); cerr != nil && errors.Is(err, cerr) {
			return true
		}
	}
	return false
}

// snapshotReasonDetail is the one-line explanation of a reason.
func snapshotReasonDetail(reason snapshotReason) string {
	switch reason {
	case reasonBudgetExhausted:
		return "the workspace or global snapshot storage budget is exhausted"
	case reasonInsufficientFreeSpace:
		return "the filesystem does not have enough free space to hold the capture"
	case reasonLegacyRecoveryRequired:
		return "an older snapshot store must be recovered offline before capture can continue"
	case reasonInterruptedCapture:
		return "a previous capture was interrupted and has not been reconciled"
	case reasonLockTimeout:
		return "another process holds the snapshot store lock"
	case reasonUnreadableFile:
		return "a path in the snapshot store could not be read, so usage cannot be accounted for"
	case reasonAccountingOverflow:
		return "snapshot size accounting overflowed"
	case reasonUnsupportedPlatform:
		return "safe snapshot storage is not available on this platform"
	case reasonInvalidLimits:
		return "the configured snapshot budgets are not valid (check snapshots.workspace_max_bytes and snapshots.global_max_bytes in /settings)"
	case reasonNotOwned:
		return "the snapshot store was used without ownership of it"
	case reasonSymlinkEscape:
		return "a path in the snapshot store resolved outside it"
	case reasonInternalError:
		return "the snapshot store reported an internal error"
	case reasonCaptureSkipped:
		return "the capture was skipped without recording a snapshot"
	case reasonCaptureUnrecorded:
		return "the snapshot was captured but could not be recorded for this session"
	default:
		return "the capture failed"
	}
}

// snapshotLimitLine names the limit that applies to reason, or "" when the
// failure is not a limit failure. The numbers come from the session's merged
// config (falling back to the built-in defaults when a value is unset or
// invalid, since a budget is always in force).
func snapshotLimitLine(st *session.State, reason snapshotReason) string {
	if reason != reasonBudgetExhausted && reason != reasonInsufficientFreeSpace && reason != reasonInvalidLimits {
		return ""
	}
	ws, global := int64(0), int64(0)
	if st != nil {
		ws = st.Config.Snapshots.WorkspaceMaxBytes
		global = st.Config.Snapshots.GlobalMaxBytes
	}
	if ws <= 0 {
		ws = config.DefaultWorkspaceMaxBytes
	}
	if global <= 0 {
		global = config.DefaultGlobalMaxBytes
	}
	switch reason {
	case reasonInsufficientFreeSpace:
		// The free-space reserve is not a configured budget, so no number is
		// quoted: claiming one would misstate what the user has to fix.
		return fmt.Sprintf("Limit: filesystem free space (not a configured budget). Configured budgets: workspace %s, global %s.",
			snapshotBytes(ws), snapshotBytes(global))
	case reasonInvalidLimits:
		return fmt.Sprintf("Configured budgets: workspace %s, global %s (one of them is invalid).",
			snapshotBytes(ws), snapshotBytes(global))
	default:
		return fmt.Sprintf("Limit: workspace budget %s, global budget %s (configured).",
			snapshotBytes(ws), snapshotBytes(global))
	}
}

// snapshotRecoverySubcommand is the workspace recovery subcommand for reason,
// or "" when no recovery subcommand applies (a cancelled capture, a config
// fix, a bug report, or a store that simply needs a retry).
func snapshotRecoverySubcommand(reason snapshotReason) string {
	switch reason {
	case reasonBudgetExhausted, reasonInsufficientFreeSpace, reasonInterruptedCapture,
		reasonUnreadableFile, reasonAccountingOverflow, reasonCaptureSkipped,
		reasonCaptureFailed:
		return snapshotCleanupCommand
	case reasonLegacyRecoveryRequired:
		return snapshotMigrateCommand
	default:
		return ""
	}
}

// snapshotRecovery is the recovery instruction rendered into the warning.
// `marshal snapshots status` always comes first: it reports the workspace ID
// and the current store health, which is what makes the follow-up subcommand
// actionable.
func snapshotRecovery(reason snapshotReason) string {
	if sub := snapshotRecoverySubcommand(reason); sub != "" {
		return snapshotStatusCommand + ", then " + sub
	}
	return snapshotStatusCommand
}

// snapshotBytes renders a byte count the way the settings UI does, so a limit
// quoted in a warning matches the value the user sees in /settings.
func snapshotBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(b)
	for _, u := range units {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PB", v)
}

// snapshotProblem is one failed, skipped, or unrecorded snapshot operation.
type snapshotProblem struct {
	phase     SnapshotPhase
	workspace string
	reason    snapshotReason
	detail    string
	// hash is the published snapshot hash when a capture succeeded; it is set
	// only for reasonCaptureUnrecorded, where the warning must not claim the
	// snapshot is missing.
	hash     string
	limit    string
	recovery string
	err      error
}

// snapshotWarningMessage renders the single visible warning. The clauses are
// fixed across every phase and reason: which snapshot, which workspace, the
// structured reason, the explicit statement that work may proceed without a
// rollback snapshot, the applicable limit when there is one, and the recovery
// command.
func snapshotWarningMessage(p snapshotProblem) string {
	var b strings.Builder
	if p.reason == reasonCaptureUnrecorded {
		fmt.Fprintf(&b, "%s not recorded for %s (%s): %s.",
			snapshotPhaseLabel(p.phase), p.workspace, string(p.reason), p.detail)
	} else {
		fmt.Fprintf(&b, "%s unavailable in %s (%s): %s.",
			snapshotPhaseLabel(p.phase), p.workspace, string(p.reason), p.detail)
	}
	b.WriteString("\nNo rollback snapshot was recorded, so the tool may proceed WITHOUT a rollback snapshot; a later /rewind cannot undo this change.")
	if p.hash != "" {
		fmt.Fprintf(&b, "\nA snapshot WAS captured (%s) and exists in the store; only its session database record failed: %s",
			shortSnapshotHash(p.hash), snapshotCauseText(p.err))
	} else if p.err != nil {
		fmt.Fprintf(&b, "\nCause: %s", snapshotCauseText(p.err))
	}
	if p.limit != "" {
		b.WriteString("\n")
		b.WriteString(p.limit)
	}
	if p.recovery != "" {
		fmt.Fprintf(&b, "\nRecover: %s.", p.recovery)
	}
	return b.String()
}

// snapshotCauseText bounds and single-lines the cause quoted into a message.
func snapshotCauseText(err error) string {
	if err == nil {
		return "unknown error"
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if msg == "" {
		return "unknown error"
	}
	return strutil.Truncate(msg, snapshotCauseRunes, true)
}

// shortSnapshotHash abbreviates a hash for readability; the full value is in
// the log and in the store.
func shortSnapshotHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// snapshotWorkspace names the store a problem concerns.
//
// In-turn captures go through a snapshotter whose target root follows the
// session's active root, so a worktree is named. Runtime maintenance targets
// the project-root store (that is what the runtime's prune prunes), so the
// project root is named there instead.
func snapshotWorkspace(st *session.State, phase SnapshotPhase) string {
	if st == nil {
		return snapshotUnknownWorkspace
	}
	var root string
	if phase == SnapshotPhaseMaintenance {
		root = st.WorkingDir
	} else {
		root = st.Workspace().ActiveRoot
		if root == "" {
			root = st.WorkingDir
		}
	}
	if strings.TrimSpace(root) == "" {
		return snapshotUnknownWorkspace
	}
	return root
}

// snapshotLogger returns a logger for st, falling back to the process default
// so a warning can never panic on a half-built session state.
func snapshotLogger(st *session.State) *slog.Logger {
	if st == nil {
		return slog.Default()
	}
	return st.Logger()
}

// SnapshotWarningState carries the visible-warning suppression state. One is
// held per runner (for in-turn captures) and one per runtime (for
// startup/close maintenance); both feed the same policy, and they are kept
// separate on purpose so an in-turn capture never silences the runtime's own
// maintenance warning.
//
// The suppression key is (workspace, reason, recovery) — the plan's
// "workspace, reason, meaningful recovery state": two failures that would send
// the user to the same place with the same explanation are one event. Every
// occurrence still reaches the log; only the transcript is deduplicated.
//
// A SUCCESSFUL capture clears the state, so a relapse warns again. A
// cancellation does not touch it: an expected shutdown is neither a success
// nor a new status change.
//
// The zero value is ready to use, and every method is nil-receiver safe: a
// Runner built from a bare struct literal (tests, and any future construction
// path that skips NewRunner) still warns — it just cannot suppress, which is
// the safe direction to fail in.
type SnapshotWarningState struct {
	mu sync.Mutex
	// key is the last VISIBLE warning; "" means nothing is suppressed.
	key string
}

// NewSnapshotWarningState returns an empty suppression state.
func NewSnapshotWarningState() *SnapshotWarningState { return &SnapshotWarningState{} }

// shouldReport records key and reports whether it is a meaningful change.
func (w *SnapshotWarningState) shouldReport(key string) bool {
	if w == nil {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.key == key {
		return false
	}
	w.key = key
	return true
}

// Reset forgets the suppressed warning. It is called after a capture is
// PUBLISHED (or after maintenance finds the store healthy), so a later
// failure of the same kind is visible again.
func (w *SnapshotWarningState) Reset() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.key = ""
	w.mu.Unlock()
}

// reset is the internal spelling used by the capture path.
func (w *SnapshotWarningState) reset() { w.Reset() }

// report logs one problem and, when it is a meaningful status change, adds the
// visible warning to the session transcript. It never returns an error: a
// warning can never fail a turn or a tool call.
func (w *SnapshotWarningState) report(st *session.State, p snapshotProblem) {
	if p.detail == "" {
		p.detail = snapshotReasonDetail(p.reason)
	}
	key := p.workspace + "\x00" + string(p.reason) + "\x00" + p.recovery
	visible := w.shouldReport(key)
	snapshotLogger(st).Warn("snapshot capture unavailable",
		"phase", string(p.phase),
		"workspace", p.workspace,
		"reason", string(p.reason),
		"recovery", p.recovery,
		"visible", visible,
		"error", p.err)
	if !visible || st == nil {
		return
	}
	// The transcript is the same projection the TUI and ACP render, so no
	// transport-specific plumbing is needed: session.RoleSystem plus
	// ContentTypePlain is what the interrupted-turn note already uses.
	st.AddMessage(session.RoleSystem, snapshotWarningMessage(p), session.ContentTypePlain)
}

// SetSnapshotWarningState installs the runner's suppression state. It exists
// so the application can give one runner and one Runtime ONE state: an in-turn
// capture warning and a runtime maintenance warning that describe the same
// workspace and reason are the same event to the user, and a capture that
// succeeds afterwards must clear whichever of them was showing. A nil argument
// is ignored (the runner keeps whatever it has).
func (r *Runner) SetSnapshotWarningState(w *SnapshotWarningState) {
	if r == nil || w == nil {
		return
	}
	r.snapshotWarnings = w
}

// ReportMaintenanceWarning surfaces one runtime maintenance failure (a startup
// reconciliation or a store that still needs attention at close) with the same
// wording and suppression policy as the in-turn capture warnings.
//
// It routes through the runner's suppression state when the runtime installed
// one, so a maintenance warning and an in-turn capture warning about the same
// workspace and reason are ONE user-visible event. A runner built without a
// state (a bare struct literal in a test) still warns.
func (r *Runner) ReportMaintenanceWarning(phase SnapshotPhase, err error, cancelled bool) {
	if r == nil || err == nil {
		return
	}
	reason := snapshotReasonFor(err)
	if cancelled || snapshotIsCancellation(nil, err) {
		snapshotLogger(r.State).Debug("snapshot maintenance cancelled",
			"phase", string(phase), "error", err)
		return
	}
	r.reportSnapshotProblem(phase, snapshotProblem{reason: reason, err: err})
}

// ReportMaintenance surfaces one runtime maintenance failure (a startup
// reconciliation or a store that still needs attention) with the same wording
// and suppression policy as the in-turn capture warnings.
//
// cancelled reports an expected shutdown: the failure is logged at Debug and
// never shown, because a prune abandoned on the way out is not a storage fault
// the user can act on.
func (w *SnapshotWarningState) ReportMaintenance(st *session.State, phase SnapshotPhase, err error, cancelled bool) {
	if err == nil {
		return
	}
	reason := snapshotReasonFor(err)
	workspace := snapshotWorkspace(st, phase)
	if cancelled || snapshotIsCancellation(nil, err) {
		snapshotLogger(st).Debug("snapshot maintenance cancelled",
			"phase", string(phase), "workspace", workspace, "error", err)
		return
	}
	w.report(st, snapshotProblem{
		phase:     phase,
		workspace: workspace,
		reason:    reason,
		detail:    snapshotReasonDetail(reason),
		limit:     snapshotLimitLine(st, reason),
		recovery:  snapshotRecovery(reason),
		err:       err,
	})
}

// snapshotCapture runs one capture for phase and routes every outcome through
// the shared warning helper. It replaces the ad-hoc logging that used to sit
// inline in the turn-start hook (runner.go) and the pre-write hook
// (execute.go), so those two sites can no longer disagree about what a
// failure looks like.
//
// The contract is deliberately the one it replaces:
//
//   - a capture is attempted only when a Snapshotter is configured;
//   - a session database row is written only for a capture that PUBLISHED a
//     hash (Track's empty hash means "no snapshot", whether it came from a
//     decline or a skip), and only in the pre-write phase — which is where the
//     row has always been written;
//   - continuation policy is untouched: snapshotCapture never returns an
//     error, never blocks the tool call, and never touches permissions or
//     approval.
func (r *Runner) snapshotCapture(ctx context.Context, phase SnapshotPhase, files []string) {
	if r == nil || r.Snapshotter == nil {
		return
	}
	hash, err := r.Snapshotter.Track(ctx)
	switch {
	case err != nil:
		if snapshotIsCancellation(ctx, err) {
			// Expected abort/shutdown: log it, show nothing, and leave the
			// suppression state alone (this is neither a success nor a new
			// status).
			snapshotLogger(r.State).Debug("snapshot capture cancelled",
				"phase", string(phase), "error", err)
			return
		}
		r.reportSnapshotProblem(phase, snapshotProblem{reason: snapshotReasonFor(err), err: err})
	case hash == "":
		// Track returned ("", nil): no error, but no snapshot either. The old
		// pre-write hook matched neither of its branches here, so this skip
		// was completely invisible — the defect this task fixes.
		r.reportSnapshotProblem(phase, snapshotProblem{reason: reasonCaptureSkipped})
	default:
		// A published capture is the one outcome that proves rollback
		// protection exists. Clear suppression first so that a later relapse
		// is visible again, including a relapse of the record step below.
		r.snapshotWarnings.reset()
		if phase != SnapshotPhasePreWrite || r.SnapshotRecorder == nil || r.State == nil {
			return
		}
		at := time.Now()
		if r.Now != nil {
			at = r.Now()
		}
		if _, saveErr := r.SnapshotRecorder.SaveSnapshot(r.State.SessionID(), r.State.TurnIndex(), hash, files, at); saveErr != nil {
			r.reportSnapshotProblem(phase, snapshotProblem{
				reason: reasonCaptureUnrecorded,
				hash:   hash,
				err:    saveErr,
			})
		}
	}
}

// reportSnapshotProblem fills in the session-derived parts of p and reports it.
func (r *Runner) reportSnapshotProblem(phase SnapshotPhase, p snapshotProblem) {
	p.phase = phase
	p.workspace = snapshotWorkspace(r.State, phase)
	if p.detail == "" {
		p.detail = snapshotReasonDetail(p.reason)
	}
	if p.limit == "" {
		p.limit = snapshotLimitLine(r.State, p.reason)
	}
	if p.recovery == "" {
		p.recovery = snapshotRecovery(p.reason)
	}
	r.snapshotWarnings.report(r.State, p)
}
