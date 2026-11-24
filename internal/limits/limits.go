// Package limits holds default bounds that more than one package needs.
//
// It exists because a bound has consumers on both sides of an import edge: the
// scanner in internal/repo enforces the retained-content cap, and
// internal/app/config supplies the default for it. A constant declared in
// either package cannot be used by the other — internal/repo reaches
// internal/app/config through internal/db, internal/subagentwork and
// internal/worktree — so the value lives in this leaf and both refer to it.
package limits

// MaxTotalContentBytes is the most file content one index scan retains, across
// all files. It is a total, not a per-file bound: a tree of many small files
// passes every per-file cap and still sums to an unbounded working set.
const MaxTotalContentBytes int64 = 512 << 20
