package snapshot

import (
	"errors"
	"fmt"
	"strings"
)

// StoreError is the structured failure type for every storage-bounds
// operation. Later tasks and the UI switch on Reason instead of matching error
// strings, so the wrapped message can stay human-readable.
type StoreError struct {
	// Reason identifies the class of failure.
	Reason StoreReason
	// Path is the store path the failure concerns, when there is one.
	Path string
	// Workspace is the workspace hash the failure concerns, when known.
	Workspace string
	// Err is the underlying cause.
	Err error
}

func (e *StoreError) Error() string {
	if e == nil {
		return "snapshot store error"
	}
	var b strings.Builder
	b.WriteString(string(e.Reason))
	if e.Workspace != "" {
		b.WriteString(" workspace=")
		b.WriteString(e.Workspace)
	}
	if e.Path != "" {
		b.WriteString(" path=")
		b.WriteString(e.Path)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *StoreError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StoreReason is a stable identifier for a class of storage failure. The values
// are the design's list of structured capture-failure reasons plus the
// accounting and ownership failures this task introduces.
type StoreReason string

const (
	// ReasonBudgetExhausted means a configured workspace or global ceiling
	// cannot accommodate the proposed write even after reclamation.
	ReasonBudgetExhausted StoreReason = "budget_exhausted"
	// ReasonInsufficientFreeSpace means the filesystem does not have the
	// proposed write allowance plus the free-space reserve available.
	ReasonInsufficientFreeSpace StoreReason = "insufficient_free_space"
	// ReasonUnreadableFile means discovery or accounting could not read a path
	// in the store. Unknown accounting errors fail closed rather than counting
	// as zero.
	ReasonUnreadableFile StoreReason = "unreadable_file"
	// ReasonLockTimeout means ownership of the store was not acquired before
	// the caller's context ended.
	ReasonLockTimeout StoreReason = "lock_timeout"
	// ReasonInterruptedCapture means a capture was cancelled or crashed part
	// way through and needs reconciliation.
	ReasonInterruptedCapture StoreReason = "interrupted_capture"
	// ReasonLegacyRecoveryRequired means an unmanaged legacy store must be
	// recovered offline before growth can continue.
	ReasonLegacyRecoveryRequired StoreReason = "legacy_recovery_required"
	// ReasonAccountingOverflow means a size or total overflowed int64.
	ReasonAccountingOverflow StoreReason = "accounting_overflow"
	// ReasonUnsupportedPlatform means safe ownership or accounting is not
	// available on this platform, so the store fails closed.
	ReasonUnsupportedPlatform StoreReason = "unsupported_platform"
	// ReasonInvalidLimits means a configured limit was nonsensical.
	ReasonInvalidLimits StoreReason = "invalid_limits"
	// ReasonNotOwned means an operation that requires ownership was attempted
	// without the store lock held.
	ReasonNotOwned StoreReason = "store_not_owned"
	// ReasonSymlinkEscape means a path inside the store resolved outside it.
	ReasonSymlinkEscape StoreReason = "symlink_escape"
	// ReasonUnsupportedFileType means a capture selected a path whose type
	// cannot be captured (a named pipe, socket, or device) or read a path whose
	// type changed to one that cannot. It is an abort, never a skip: a snapshot
	// that silently omitted a path claims a rollback point it does not have.
	ReasonUnsupportedFileType StoreReason = "unsupported_file_type"
	// ReasonUnverifiedObject means a capture could not prove that the bytes it
	// hashed are the bytes it wrote: a length-prefixed object that describes
	// different content is corruption, not a tolerable race.
	ReasonUnverifiedObject StoreReason = "unverified_object"
	// ReasonInternal means the manager is misconfigured (for example a nil
	// dependency seam).
	ReasonInternal StoreReason = "internal_error"
	// ReasonSnapshotExpired means a well-formed snapshot hash is no longer
	// retained by any usable generation of its workspace: the rollback point
	// was reclaimed by retention or budget pressure. It is deliberately
	// distinct from ReasonSnapshotNotFound (the workspace never captured) and
	// from ReasonUnreadableFile (the store cannot be read at all), because the
	// user's remedy differs: nothing to do, nothing ever existed, or repair
	// the store.
	ReasonSnapshotExpired StoreReason = "snapshot_expired"
	// ReasonSnapshotNotFound means the workspace has no snapshot store at all,
	// so the requested hash was never captured here.
	ReasonSnapshotNotFound StoreReason = "snapshot_not_found"
	// ReasonInvalidObjectHash means the caller supplied something that is not a
	// plain hexadecimal object id. It is refused before any Git invocation:
	// hash text must never reach Git as a revision expression or an option.
	ReasonInvalidObjectHash StoreReason = "invalid_object_hash"
)

// Sentinel values for errors.Is. Each reason has one so callers can compare
// either structurally (errors.As to *StoreError and switch on Reason) or with
// errors.Is.
var (
	ErrBudgetExhausted        = errors.New(string(ReasonBudgetExhausted))
	ErrInsufficientFreeSpace  = errors.New(string(ReasonInsufficientFreeSpace))
	ErrUnreadableFile         = errors.New(string(ReasonUnreadableFile))
	ErrLockTimeout            = errors.New(string(ReasonLockTimeout))
	ErrInterruptedCapture     = errors.New(string(ReasonInterruptedCapture))
	ErrLegacyRecoveryRequired = errors.New(string(ReasonLegacyRecoveryRequired))
	ErrAccountingOverflow     = errors.New(string(ReasonAccountingOverflow))
	ErrUnsupportedPlatform    = errors.New(string(ReasonUnsupportedPlatform))
	ErrInvalidLimits          = errors.New(string(ReasonInvalidLimits))
	ErrNotOwned               = errors.New(string(ReasonNotOwned))
	ErrSymlinkEscape          = errors.New(string(ReasonSymlinkEscape))
	ErrUnsupportedFileType    = errors.New(string(ReasonUnsupportedFileType))
	ErrUnverifiedObject       = errors.New(string(ReasonUnverifiedObject))
	ErrStoreInternal          = errors.New(string(ReasonInternal))
	ErrSnapshotExpired        = errors.New(string(ReasonSnapshotExpired))
	ErrSnapshotNotFound       = errors.New(string(ReasonSnapshotNotFound))
	ErrInvalidObjectHash      = errors.New(string(ReasonInvalidObjectHash))
)

// sentinelForReason maps a reason to its sentinel.
func sentinelForReason(r StoreReason) error {
	switch r {
	case ReasonBudgetExhausted:
		return ErrBudgetExhausted
	case ReasonInsufficientFreeSpace:
		return ErrInsufficientFreeSpace
	case ReasonUnreadableFile:
		return ErrUnreadableFile
	case ReasonLockTimeout:
		return ErrLockTimeout
	case ReasonInterruptedCapture:
		return ErrInterruptedCapture
	case ReasonLegacyRecoveryRequired:
		return ErrLegacyRecoveryRequired
	case ReasonAccountingOverflow:
		return ErrAccountingOverflow
	case ReasonUnsupportedPlatform:
		return ErrUnsupportedPlatform
	case ReasonInvalidLimits:
		return ErrInvalidLimits
	case ReasonNotOwned:
		return ErrNotOwned
	case ReasonSymlinkEscape:
		return ErrSymlinkEscape
	case ReasonUnsupportedFileType:
		return ErrUnsupportedFileType
	case ReasonUnverifiedObject:
		return ErrUnverifiedObject
	case ReasonInternal:
		return ErrStoreInternal
	case ReasonSnapshotExpired:
		return ErrSnapshotExpired
	case ReasonSnapshotNotFound:
		return ErrSnapshotNotFound
	case ReasonInvalidObjectHash:
		return ErrInvalidObjectHash
	}
	return nil
}

// storeError builds a StoreError whose Unwrap also matches the reason's
// sentinel, so both errors.Is(err, ErrLockTimeout) and a Reason switch work.
func storeError(reason StoreReason, err error) *StoreError {
	se := &StoreError{Reason: reason, Err: err}
	if sentinel := sentinelForReason(reason); sentinel != nil {
		se.Err = errors.Join(sentinel, err)
	}
	return se
}

// storeErrorf is storeError with a formatted cause.
func storeErrorf(reason StoreReason, format string, args ...any) *StoreError {
	return storeError(reason, fmt.Errorf(format, args...))
}

// ReasonOf returns the structured reason behind err, or "" when err carries
// none. It is the supported way for callers to branch on failure class.
func ReasonOf(err error) StoreReason {
	var se *StoreError
	if errors.As(err, &se) {
		return se.Reason
	}
	return ""
}

// IsUnsupportedPlatform reports whether err is the fail-closed result of
// running on a platform without safe ownership or accounting.
func IsUnsupportedPlatform(err error) bool {
	return errors.Is(err, ErrUnsupportedPlatform)
}
