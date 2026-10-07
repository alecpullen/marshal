package snapshot

import (
	"math"
)

// Production defaults for managed snapshot storage. They mirror the approved
// design's 2 GiB per-workspace and 10 GiB global ceilings. The snapshot package
// deliberately does not import the app config layer; the app converts its
// configuration into a Limits value and passes it in.
const (
	// DefaultWorkspaceMaxBytes bounds one workspace's managed snapshot store.
	DefaultWorkspaceMaxBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GiB
	// DefaultGlobalMaxBytes bounds every Marshal-managed snapshot store
	// together.
	DefaultGlobalMaxBytes int64 = 10 * 1024 * 1024 * 1024 // 10 GiB
	// DefaultFreeSpaceMarginBytes is the floor of the free-space reserve kept
	// beyond a proposed write allowance.
	DefaultFreeSpaceMarginBytes int64 = 64 * 1024 * 1024 // 64 MiB
	// DefaultFreeSpaceMarginPercent is the proportional part of that reserve,
	// applied to the proposed write allowance. The reserve is the larger of
	// the two, so a large capture leaves proportionally more headroom.
	DefaultFreeSpaceMarginPercent int64 = 2
	// DefaultAllocationUnitBytes is the allocation granularity assumed when
	// rounding measured and proposed writes up. Filesystems may allocate in
	// larger units; accounting takes the larger of the measured allocation and
	// this rounding, so the assumption can only over-count, never under-count.
	DefaultAllocationUnitBytes int64 = 4096
)

// Limits carries the effective storage ceilings and the margins that keep a
// capture from filling the filesystem.
//
// A zero field means "unset" and is replaced by the production default by
// Normalize; budgets are never disabled by a zero. Only Normalize is applied
// automatically, so a caller that wants to reject unset values can call
// Validate on the raw value.
type Limits struct {
	// WorkspaceMaxBytes is the ceiling for one workspace's managed store.
	WorkspaceMaxBytes int64
	// GlobalMaxBytes is the ceiling for every managed store together.
	GlobalMaxBytes int64
	// FreeSpaceMarginBytes is the floor of the free-space reserve.
	FreeSpaceMarginBytes int64
	// FreeSpaceMarginPercent is the proportional part of the free-space
	// reserve, in whole percent of the proposed write allowance.
	FreeSpaceMarginPercent int64
	// AllocationUnitBytes is the rounding granularity for measured and
	// proposed writes.
	AllocationUnitBytes int64
}

// DefaultLimits returns the production limits.
func DefaultLimits() Limits {
	return Limits{
		WorkspaceMaxBytes:      DefaultWorkspaceMaxBytes,
		GlobalMaxBytes:         DefaultGlobalMaxBytes,
		FreeSpaceMarginBytes:   DefaultFreeSpaceMarginBytes,
		FreeSpaceMarginPercent: DefaultFreeSpaceMarginPercent,
		AllocationUnitBytes:    DefaultAllocationUnitBytes,
	}
}

// Normalize returns l with every unset (zero) field replaced by its production
// default. Negative fields are left untouched so Validate can reject them
// rather than silently repairing a value the user asked for.
func (l Limits) Normalize() Limits {
	if l.WorkspaceMaxBytes == 0 {
		l.WorkspaceMaxBytes = DefaultWorkspaceMaxBytes
	}
	if l.GlobalMaxBytes == 0 {
		l.GlobalMaxBytes = DefaultGlobalMaxBytes
	}
	if l.FreeSpaceMarginBytes == 0 {
		l.FreeSpaceMarginBytes = DefaultFreeSpaceMarginBytes
	}
	if l.AllocationUnitBytes == 0 {
		l.AllocationUnitBytes = DefaultAllocationUnitBytes
	}
	return l
}

// Validate rejects nonsensical limits. Zero is not itself invalid (it means
// "use the production default"); only negative ceilings, a negative margin, an
// out-of-range percentage, and a negative allocation unit are errors.
func (l Limits) Validate() error {
	if l.FreeSpaceMarginBytes < 0 {
		return invalidLimit("free_space_margin_bytes", l.FreeSpaceMarginBytes)
	}
	if l.WorkspaceMaxBytes < 0 {
		return invalidLimit("workspace_max_bytes", l.WorkspaceMaxBytes)
	}
	if l.GlobalMaxBytes < 0 {
		return invalidLimit("global_max_bytes", l.GlobalMaxBytes)
	}
	if l.FreeSpaceMarginPercent < 0 || l.FreeSpaceMarginPercent > 100 {
		return invalidLimit("free_space_margin_percent", l.FreeSpaceMarginPercent)
	}
	if l.AllocationUnitBytes < 0 {
		return invalidLimit("allocation_unit_bytes", l.AllocationUnitBytes)
	}
	return nil
}

func invalidLimit(field string, value int64) error {
	return storeErrorf(ReasonInvalidLimits, "%s must not be negative or out of range: %d", field, value)
}

// FreeSpaceReserve returns the bytes that must remain free beyond a proposed
// write allowance before that write is admitted: the larger of the configured
// floor and the configured percentage of the allowance.
func (l Limits) FreeSpaceReserve(allowance int64) (int64, error) {
	if allowance < 0 {
		return 0, storeErrorf(ReasonInvalidLimits, "write allowance must not be negative: %d", allowance)
	}
	n := l.Normalize()
	proportional, err := mulDiv(n.FreeSpaceMarginPercent, allowance, 100)
	if err != nil {
		return 0, err
	}
	return maxInt64(proportional, n.FreeSpaceMarginBytes), nil
}

// RoundUp rounds n up to a whole allocation unit, with a floor of one unit so
// that an empty file still counts for the storage it occupies.
func (l Limits) RoundUp(n int64) (int64, error) {
	if n < 0 {
		return 0, storeErrorf(ReasonInvalidLimits, "cannot round a negative size: %d", n)
	}
	return roundUpAllocation(n, l.Normalize().AllocationUnitBytes)
}

// roundUpAllocation rounds n up to the next multiple of unit, with a floor of
// one unit. It reports an error instead of wrapping when the rounded value
// would overflow.
func roundUpAllocation(n, unit int64) (int64, error) {
	if unit <= 0 {
		unit = DefaultAllocationUnitBytes
	}
	if n < 0 {
		n = 0
	}
	if n < unit {
		return unit, nil
	}
	rem := n % unit
	if rem == 0 {
		return n, nil
	}
	add := unit - rem
	if n > math.MaxInt64-add {
		return 0, overflowError("round-up", n, unit)
	}
	return n + add, nil
}

// mulDiv returns a*b/d without wrapping. All three arguments must be
// non-negative.
func mulDiv(a, b, d int64) (int64, error) {
	if a < 0 || b < 0 || d < 0 {
		return 0, storeErrorf(ReasonInvalidLimits, "mulDiv requires non-negative operands: %d, %d, %d", a, b, d)
	}
	if a == 0 || b == 0 {
		return 0, nil
	}
	if d == 0 {
		return 0, overflowError("mulDiv", a, b)
	}
	if a > math.MaxInt64/b {
		return 0, overflowError("mulDiv", a, b)
	}
	return (a * b) / d, nil
}

// addInt64 returns a+b without wrapping.
func addInt64(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, overflowError("add", a, b)
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, overflowError("add", a, b)
	}
	return a + b, nil
}

// maxInt64 returns the larger of a and b.
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// overflowError is the single constructor for an accounting overflow. Overflow
// is always an error, never a silent wrap: a wrapped total would under-report
// usage and admit writes the budget never allowed.
func overflowError(op string, a, b int64) error {
	return storeErrorf(ReasonAccountingOverflow, "%s overflow: %d and %d", op, a, b)
}
