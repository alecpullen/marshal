# P0 safe removal — implementation plan

Date: 2026-10-27. Status: executed 2026-10-27 (tasks 1–3 committed as
`943d090d`, `88a558e2`, `573d6d00`; merged into the target branch as
`ddd97952`). Execution results are recorded at the end of this plan.
Base: `bdfc45902cb08fc3a12fc9f2d7f1f5ff37928127` (`HEAD` when authored).
Requirements: [P0 triage](../triage.md#p0--contain-skill-removal) and
[audit](../audit.md#skill-removal-reconstructs-the-wrong-filesystem-path).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** a confirmed skill removal deletes only the selected discovered file
or bundle inside its own skill store, and skill/plugin confirmations cannot
carry over to another selection or a later visit.

**Non-goals:** change skill loading precedence, refresh the runtime index,
change skill installation paths, redesign plugin installation/removal, or fix
other audit findings. Those remain in the triage backlog.

**Assumptions and decisions:**

- The triage document is the user-selected requirements baseline; this plan
  pins the implementation decisions for review before execution.
- `ListScopes` discovers a top-level `.md` file or a directory containing
  `SKILL.md`. Record the direct child of the selected store, not the parsed
  frontmatter name. Never remove the store itself.
- Preserve discovery and loading behavior for symlinked entries, but reject
  their removal with a visible error. Accept a symlinked *store root* through
  `os.OpenRoot`, as ordinary relocated home/workspace paths may use aliases.
- Use Go's `os.Root` to confine recursive removal. Compare the current direct
  child's `os.FileInfo` with the discovery snapshot using `os.SameFile`; a
  missing or replaced child is an error, not a successful removal. This check
  detects a replacement before removal but is not an atomic identity lock:
  `os.Root` is the containment boundary if an entry changes after the check.
- Name-only confirmation state is unsafe across scopes. Confirmations are
  keyed by scope and source identity for skills, and scope and lockfile name
  for plugins. Leaving the Remove row or its detail frame disarms it.
- `TestStatusLineUntrustedIsWarningColored` is a known baseline failure: it
  expects ANSI 172 while the current warning slot is 215. Record it as a
  baseline result, not a reason to skip other tests or alter the palette here.
- The main checkout has unrelated untracked work; stage only named files. Do
  not commit this plan or start execution before user review. Repository
  instructions say feature plans travel with their work, so include this plan
  and the relevant audit/triage documents in the final documentation commit.

Tasks run sequentially. Each task has an independent verification and may be
committed after it passes, using `p0-safe-removal: task N — <title>`. No task
is parallel-safe: Task 2 consumes Task 1's API; Task 3 shares confirmation
semantics and follows the completed skill change.

## Contracts

Task 1 extends the existing `internal/skills/loader.go` declaration and
exports the removal operation consumed by Task 2:

```go
type ScopedSkill struct {
    Skill      Skill
    Scope      string
    SourceName string      // direct child of the corresponding store
    SourceInfo os.FileInfo // Lstat snapshot of that child at discovery
}

// RemoveScopedSkill removes the discovered direct child under root.
// It rejects invalid, symlinked, missing, or replaced entries.
func RemoveScopedSkill(root string, skill ScopedSkill) error
```

`SourceName` and `SourceInfo` are filesystem provenance, never derived from
`Skill.Name`. `SourceInfo` is an in-process snapshot, not a persisted or wire
value. The caller selects the root from the known scope, rejecting any other
scope before calling `RemoveScopedSkill`.

## Task 1: Discover source identity and confine removal

**Goal:** `internal/skills` can remove an unchanged discovered direct child
without trusting skill metadata or escaping the selected store.

**Files:** `internal/skills/loader.go`; create
`internal/skills/removal.go` and `internal/skills/removal_test.go`.

**Steps:**

1. In `ListScopes`, preserve the direct child name: for a top-level file use
   `filepath.Base(p)`; for a bundle's `SKILL.md` use the basename of its
   containing directory. Obtain its `os.Lstat` snapshot under the corresponding
   root and attach both fields to the returned `ScopedSkill`. If the entry
   vanishes or cannot be inspected during discovery, skip that entry as the
   existing parser already skips unreadable entries; do not fabricate identity.
   Keep the current scope ordering and parsing behavior.
2. Add `RemoveScopedSkill` in `removal.go`. Reject an empty source name,
   `.` or `..`, a name containing a path separator, a non-local name according
   to `filepath.IsLocal`, nil `SourceInfo`, and an unknown scope. Open the
   caller-supplied store with `os.OpenRoot`; `defer Close`. Use `Root.Lstat`
   on the direct child, reject symlinks and any type other than a regular
   file or directory, and reject a child that is not `os.SameFile` as the
   discovery snapshot. Return a descriptive error for each rejection or I/O
   failure. Call `Root.RemoveAll(SourceName)` only after these checks; return
   its error. Do not follow or recursively remove a symlink target.
3. Add these isolated tests in `removal_test.go`, each using `t.TempDir()`:
   - `TestListScopesRecordsRemovalSource`: discover a top-level `actual.md`
     with a different frontmatter name and a `bundle-dir/SKILL.md` with a
     different name; assert the source names, scopes, and non-nil snapshots.
   - `TestRemoveScopedSkillExactSource`: remove each of those entries in both
     scopes, assert the selected file or bundle is gone, and assert sibling
     files and the roots survive.
   - `TestRemoveScopedSkillRejectsTraversalAndInvalidIdentity`: use metadata
     name `../../victim`, forged source names `../victim`, `.`, empty and an
     absolute path, nil snapshot, and unknown scope. Assert errors where the
     identity is forged; removing a legitimately discovered skill with the
     malicious metadata removes only its actual child. The outside sentinel
     must survive every case.
   - `TestRemoveScopedSkillRejectsSymlinkAndReplacement`: test a symlinked
     direct child pointing outside, a missing child, and a discovered child
     replaced before removal. Each returns an error and preserves the outside
     sentinel/replacement. Skip only the symlink subtest when the platform
     cannot create a symlink.
   - `TestRemoveScopedSkillAllowsSymlinkedStoreRoot`: open an alias to a real
     store, remove its discovered regular child, and verify siblings survive;
     skip only if creating the root symlink is unsupported.

**Verify:**

```bash
go test ./internal/skills -run 'Test(ListScopesRecordsRemovalSource|RemoveScopedSkill)' -count=1
go test ./internal/skills -count=1
gofmt -l internal/skills/loader.go internal/skills/removal.go internal/skills/removal_test.go
```

Expected: both test commands pass and `gofmt -l` prints nothing. This task
introduces the safe API; the existing TUI remains vulnerable until Task 2.

## Task 2: Use the discovered skill and disarm its confirmation

**Goal:** the skills panel removes the selected source through Task 1's
confined operation and requires a fresh confirmation after navigation.

**Files:** `internal/app/tui/skills/panel.go`,
`internal/app/tui/skills/panel_test.go`.

**Steps:**

1. Change `runRemove` to select `globalSkillsDir()` for `global` and
   `projectSkillsDir()` for `project`; return a `removeResultMsg` error for
   any other scope. Call `skills.RemoveScopedSkill(root, s)` inside the
   existing command and propagate its error through the existing result path.
   Do not reconstruct the target from `s.Skill.Name` or silently treat a
   missing source as success.
2. Key `removeArmed` by a private comparable identity containing `s.Scope`
   and `s.SourceName`. In `detailFrame`, bind a `settings.SetFieldDisarm`
   callback to the Remove row that clears this identity. In `Panel.Update`,
   invoke `DisarmCurrent` on the active list before Escape pops or closes the
   frame. Keep the two-Enter interaction and current success/error messages.
3. Add tests through `Panel.Update` and the emitted commands:
   - `TestRemoveUsesDiscoveredSource`: remove a single-file skill and a bundle
     whose source names differ from frontmatter; assert only the selected entry
     is removed. A malicious metadata name must leave an outside sentinel.
   - `TestRemoveReportsStaleOrSymlinkSource`: replace or remove the selected
     entry after discovery, and separately select a symlink; assert a surfaced
     error and no removal of the replacement or outside target.
   - `TestRemoveConfirmationDisarmsOnNavigationAndEscape`: arm Remove, move
     away and back, and separately leave/reopen the detail; the next Enter
     must arm, not delete. Assert two consecutive confirmations do delete.
   - `TestRemoveConfirmationIsScopedBySource`: same-named skills in global
     and project stores and two distinct same-name entries in one store cannot
     share an armed confirmation. Verify the other entry survives.

**Verify:**

```bash
go test ./internal/app/tui/skills -run 'TestRemove' -count=1
go test ./internal/app/tui/skills ./internal/skills -count=1
gofmt -l internal/app/tui/skills/panel.go internal/app/tui/skills/panel_test.go
```

Expected: tests pass, errors appear in the active detail, and `gofmt -l` is
empty. Existing tests that construct `ScopedSkill` without provenance may
continue to test non-removal behavior; removal tests must use discovery.

## Task 3: Scope and expire plugin removal confirmation

**Goal:** plugin Remove needs a fresh confirmation for the selected plugin
after navigation or a change of scope.

**Files:** `internal/app/tui/plugins/panel.go`,
`internal/app/tui/plugins/panel_test.go`.

**Steps:**

1. Replace name-only `removeArmed` keys with a private comparable identity
   containing the `scopedEntry`'s scope and name. Register a
   `settings.SetFieldDisarm` callback on the Remove field. Before Escape pops
   or closes a panel frame, disarm the active row via `DisarmCurrent`; keep
   the existing scan cleanup behavior and plugin removal implementation.
2. Add `TestRemoveConfirmationDisarmsOnNavigationAndEscape`: arm Remove,
   move away and back, and separately exit/reopen the detail; the next Enter
   only arms. Add `TestRemoveConfirmationIsScoped`: same-named global and
   project entries do not share an armed flag. Use local lockfile/temp-store
   fixtures, and assert the unselected plugin and lockfile entry survive.

**Verify:**

```bash
go test ./internal/app/tui/plugins -run 'TestRemoveConfirmation' -count=1
go test ./internal/app/tui/plugins -count=1
gofmt -l internal/app/tui/plugins/panel.go internal/app/tui/plugins/panel_test.go
```

Expected: both tests and the package pass; `gofmt -l` prints nothing.

## Final verification and integration

Run from the repository root after Tasks 1–3:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p0-safe-removal ./cmd/marshal
go test ./internal/skills ./internal/app/tui/skills ./internal/app/tui/plugins -count=1
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/skills internal/app/tui/skills internal/app/tui/plugins
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p0-safe-removal go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Record actual outputs. The full suite must pass except for any independently
confirmed baseline failure; do not hide or silently skip failures. The scripted
usability suite currently exercises help, not removal, so the new fixture tests
are the removal regression evidence. Update the P0 audit/triage status with the
actual results, then commit the named documentation files with this plan. Leave
P1/P2 work open.

## Plan self-review

- The safe helper and both panels have explicit interfaces, file ownership,
  named tests, and independent verify commands; Task 2 explicitly completes the
  security fix introduced as an API in Task 1.
- Discovery, symlink, traversal, replacement, cross-scope confirmation and
  failed removal cases are assigned to an owning task. `os.Root` confines
  deletion even though identity comparison is not atomic against replacement.
- Anchors were checked against the base tree; no implementation or prototype
  tests have been run for this plan. The existing baseline test failure and
  limited scripted usability coverage are disclosed above.

## Execution results

Run 2026-10-27 in an isolated worktree at base `bdfc4590`, tasks executed
strictly sequentially (the plan marks none parallel-safe) with every task's
verify block re-run by the controller before its commit.

- **Commits:** `943d090d` (task 1), `88a558e2` (task 2), `573d6d00` (task 3);
  merged into the target as `ddd97952`.
- **Task 1:** `go test ./internal/skills -run 'Test(ListScopesRecordsRemovalSource|RemoveScopedSkill)' -count=1` → `ok`; `go test ./internal/skills -count=1` → `ok`; `gofmt -l` → no output.
- **Task 2:** `go test ./internal/app/tui/skills -run 'TestRemove' -count=1` →
  all 9 subtests pass; `go test ./internal/app/tui/skills ./internal/skills -count=1` → `ok`; `gofmt -l` → no output.
- **Task 3:** `go test ./internal/app/tui/plugins -run 'TestRemoveConfirmation' -count=1` → `ok`; `go test ./internal/app/tui/plugins -count=1` → `ok`; `gofmt -l` → no output.
- **Final build:** `CGO_ENABLED=1 go build -o /private/tmp/marshal-p0-safe-removal ./cmd/marshal` → exit 0.
- **Targeted tests:** `go test ./internal/skills ./internal/app/tui/skills ./internal/app/tui/plugins -count=1` → all three `ok`.
- **`go vet ./...`** → exit 0. **`gofmt -l internal/skills internal/app/tui/skills internal/app/tui/plugins`** → no output. **`git diff --check`** → exit 0.
- **Full suite** (`go test ./... -timeout 5m`): 22 pre-existing failures, byte-identical by test name to the baseline captured at `bdfc4590` before any edit — `internal/app`, `internal/snapshot`, `internal/tools/desktop/browser`, `internal/worktree`. No new failure was introduced. `TestStatusLineUntrustedIsWarningColored` did not fail at this base and is not on the baseline list.
- **Scripted usability:** `USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p0-safe-removal go test ./test/usability/... -run TestScripted -timeout 5m` → `ok marshal/test/usability`.

Task-level notes (small gaps settled during execution, none of them design
changes):

- Scope strings live in unexported `scopeGlobal`/`scopeProject` constants in
  `internal/skills/loader.go`, reused by discovery and by `RemoveScopedSkill`.
- Discovery parses first and snapshots second, so a malformed file is skipped
  before any `Lstat`, matching how unreadable entries were already skipped.
- The confirmation key's derivation helper is `removalIdentity` in both panels
  (`ScopedSkill` for skills, `scopedEntry` for plugins).
- Plugin tests open the detail via `detailFrame` rather than drilling from the
  root list, because the cross-scope fixture legitimately yields two rows with
  the same `plugin.dup` field ID.

Deviation from the plan: none. `TestStatusLineUntrustedIsWarningColored` was
listed above as a known baseline failure but did not reproduce at this base,
so it was treated as not-on-the-baseline rather than investigated. P1/P2 work
remains open.
