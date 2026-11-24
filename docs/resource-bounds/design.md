# Resource bounds: index scan memory, snapshot capture cost, index defaults

Status: approved design, not yet implemented.
Date: 2026-11-22.

## Context

On a workstation whose workspace root resolved to the user's home directory,
Marshal drove the machine into swap and made every agent command take 30–43
seconds. The evidence from that incident:

- A single `marshal` process reached **24.9 GiB RSS** during the startup index
  scan, which then failed after ~11 minutes with
  `scan repo: context deadline exceeded`.
- `/proc/<pid>/io` recorded **~29.8 GB read** during that scan.
- Pre-write snapshots measured **30.1–33.5 s** each, hitting the snapshot
  manager's 30 s command deadline, and reported
  `snapshot capture unavailable … reason unreadable_file`.
- Sustained swap-out of ~70–126 MB/s and `io full avg60 ≈ 23%` PSI: the desktop
  stalled on I/O while the CPU sat 79% idle.

Three independent code defects produce this. None is a configuration mistake,
and the repository's own documented idiom decides how each is fixed: capture
"fails closed … a listing that cannot be trusted is an error, never a shorter
list" (`internal/snapshot/eligibility.go:26`), and "a bound that silently
disappears is how unbounded work gets admitted"
(`internal/snapshot/eligibility.go:68`). Every bound added here is an absolute
default that **records the reason it fired**.

## 1. Scanner total content budget (A1)

### Defect

`Scanner.walk` (`internal/repo/scanner.go:165`) appends every regular file to a
single `results` slice as `ScannedFile{FileIndex, Content, ReadErr}`
(`internal/repo/scanned.go:11`), and `ScanDetailed`
(`internal/repo/scanner.go:187`) populates `Content` with `os.ReadFile`. Every
file's bytes are therefore resident simultaneously until the walk completes.

`repo.Config` bounds only *per-file* size (`MaxIndexableFileBytes`, default
25 MiB, enforced at `scanner.go:159`). A home directory of many small files
passes that cap on every file and sums to tens of gigabytes. There is no bound
on the total.

### Change

The value lives in the leaf package `internal/limits`, because both the scanner
and `internal/app/config` need it and the config package cannot import
`internal/repo` (see "Config plumbing"). `internal/repo` names it directly, so
there is exactly one declaration of the number.

```go
// limits.MaxTotalContentBytes is the most file content one scan retains. It is
// a total, not a per-file bound: a tree of many small files passes every
// per-file cap and still sums to an unbounded working set.
```

`Config` gains:

```go
// MaxTotalContentBytes caps the total file content one scan retains across all
// files. A file beyond the budget is still hashed and indexed (path, hash,
// language, size) but its Content is nil and ScannedFile.ContentOmitted is set.
// Zero means limits.MaxTotalContentBytes; a bound never disappears.
MaxTotalContentBytes int64
```

`ScannedFile` gains `ContentOmitted bool` with:

```go
// ContentOmitted reports that the scan deliberately retained no content for
// this file: the total content budget was exhausted, or the file's size could
// not be measured. It is not a read failure; ReadErr is nil for such a file
// unless even its hash could not be computed, and its FileIndex is valid either
// way. Consumers must not infer this state from a nil Content: an empty file
// legitimately has nil Content too.
ContentOmitted bool
```

The explicit field is required, not cosmetic — see "Reindex hazard" below.

### Data flow

1. `walk`'s callback becomes
   `func(path, rel string, retain bool) (db.FileIndex, []byte, error)`.
   `walk` has exactly one caller — `ScanDetailed` at `scanner.go:188` — so no
   other call site needs updating.
2. `walk` stats each entry **once** (today it stats only when
   `MaxIndexableFileBytes > 0`, `scanner.go:159`) and computes
   `retain := sizeKnown && !exhausted && totalContent+size <= budget`.
   `totalContent` grows by `len(content)` only for retained files. The first
   file the budget cannot hold sets `exhausted`, and every later file is
   omitted too, so the retained files form a prefix of the walk and the single
   warning's "starting at …" names the right boundary. A file whose size cannot
   be measured is likewise indexed without content (so it can sit after retained
   files), and the first such file is named in its own warning so it is not
   silently dropped.
3. `ScanDetailed` keeps its current path for retained files (`os.ReadFile` then
   `hashBytes`). For non-retained files it calls a new `hashFile(path)` that
   streams through `sha256` in 64 KiB chunks, returning `(hash, size, error)`.
   `Content` is nil and `ContentOmitted` is true.
4. The first omission appends exactly one scanner warning naming the first
   omitted path and the budget that fired.

`hashBytes` loses its dead `error` result: it always returned `nil`
(`scanner.go:238`), so `hashErr` was unreachable.

### Reindex hazard (why `ContentOmitted` is mandatory)

`Indexer.Reindex` (`internal/index/indexer.go:46`) marks
`seen[sf.Path] = true` and skips only on `sf.ReadErr != nil`. It then treats a
file whose `Chunk` yields nothing as changed-and-empty and calls
`DeleteFileChunks`. A budget-omitted file presented as a plain `nil` Content
would therefore have its valid embeddings **purged on every index pass**.

`Reindex` must become:

```go
seen[sf.Path] = true
if sf.ReadErr != nil || sf.ContentOmitted {
    continue
}
```

so an omitted file keeps its existing chunks, which is the same conservative
outcome the existing transient-read-error test
(`TestReindexTransientReadErrorKeepsExistingChunks`) already establishes.

### Consumer changes in `internal/index/run.go`

- Propagate scanner diagnostics. `Run` currently discards both
  `Scanner.Skipped()` and `Scanner.Warnings()`, so today every skip reason and
  gitignore parse diagnostic is silently lost. Append
  `scanner.Warnings()...` to `rep.Warnings`.
- In the symbol loop, skip on
  `if sf.ReadErr != nil || sf.ContentOmitted { continue }` so omitted files do
  not produce a bogus tree-sitter parse error.

Truncation is on *symbols only*. The file index stays complete, because
`FileIndex` is built from path/hash/language/size and needs no content.

### Known limits

The budget bounds retained *content*, not the scan itself: every file is still
enumerated, and each is read once to compute its hash. The returned slice also
holds one `db.FileIndex` (including a path string) per file, so a pathological
tree remains bounded in memory by file count and path bytes rather than by
content, and its scan time stays proportional to the tree. Bounding those needs
a streaming index pass and is out of scope here.

## 2. Snapshot capture cost (B1, B2)

### Defect

Capture selection is bounded in entries (`defaultCaptureMaxEntries = 100_000`),
path bytes (`defaultCaptureMaxPathBytes = 4096`) and total path bytes
(`defaultCaptureMaxTotalPathBytes = 16 MiB`), plus a 2 MB per-file cap — see
`internal/snapshot/eligibility.go:32-44`. Enumeration is not bounded in *time*:
`git ls-files --others --exclude-standard` over a very large workspace runs
until the manager's `commandDeadline`, which defaults to 30 s
(`internal/snapshot/manager.go:413`). The pre-write capture is on the tool-call
path (`internal/agent/execute.go:1078` →
`Runner.snapshotCapture`, `internal/agent/snapshot_warning.go:615`), whose own
doc comment promises it "never blocks the tool call".

A workspace whose root is the home directory is a **supported** case, not a
misconfiguration: `TestEligibilityExcludesTheManagedStoreInsideTheWorkspace`
covers it explicitly. So the fix makes capture cheap; it does not refuse it.

### B1 — capture backoff

After a capture fails for a workspace, suppress further capture attempts for
that root for a cooldown window instead of retrying on every tool call. A timed
backoff, deliberately **not** a permanent negative cache: a transient
`unreadable_file` must not poison the rest of the session. The suppressed state
and its reason stay visible through the existing `StoreReason` reporting, so
`marshal snapshots status` keeps telling the truth.

Implementation: a `captureBackoff = 5 * time.Minute` constant and a
`Manager`-owned map keyed by canonical workspace root, holding the last
`StoreReason` and the instant the cooldown expires. The map is guarded by the
manager's existing mutex and is never persisted — a restart retries once,
which is the correct cost.

Only a failure of the capture itself suppresses. Admission refusals
(`budget_exhausted`, `insufficient_free_space`, `no_eligible_paths`) are
decisions made before a byte is written and leave the store byte-identical, so
suppressing them would make the recovery their own warning recommends
(cleanup, then retry) inert for the whole window. A cancellation
(`interrupted_capture`) is an expected abort. A caller's own deadline is a
timeout, not an abort: it is classified `command_timeout` so it is reported
honestly and backed off, which is what makes B1 and B2 compose. Classification
is by the context error, not by whichever guard the timer happened to
interrupt, so the same event produces the same reason wherever it lands. A
failure that happens before admission — waiting for the service's operation
semaphore or the store lock, or store setup cut short — is recorded through the
same helper, so that variant is backed off too rather than paying its cost on
every tool call.

### B2 — interactive deadline

`Manager.deadlineFor` already lets a caller's sooner deadline win
(`internal/snapshot/manager.go:993`), so no new knob is needed. The **pre-write
path** wraps its context in a 5-second `context.WithTimeout` inside
`snapshotCapture`, instead of inheriting the 30 s `commandDeadline`. A capture
that cannot enumerate within that bound reports `ReasonCommandTimeout`
promptly and returns; the tool call proceeds. The bound is phase-gated: a
turn-start capture keeps the manager's full command deadline, so a workspace
that legitimately takes longer than five seconds to enumerate does not lose the
turn's baseline rollback point. The bound is injectable on `Runner` for tests,
which pin it precisely instead of spending five real seconds.

## 3. Index defaults (C1, C2, C4)

### C1 — ignore defaults

`walk` applies `isIgnored` to directories as well as files — the directory
branch calls it before pushing a nested `.gitignore` (`scanner.go:141-145`) —
and a pattern is matched against **both** the relative path and the basename
(`scanner.go:252`). A bare basename pattern therefore prunes at any depth.

Everything goes in the `Indexing.Ignore` default list, which is
user-overridable. `defaultIgnoredDirs` (`scanner.go:41`) stays reserved for
paths that must never be walked at all (`.git`, `.marshal`).

- Directories added to `Ignore`: `.cache`, `.lmstudio`, `.cargo`, `.rustup`,
  `.gradle`, `.m2`, `.npm`, `.venv`, `venv`, `__pycache__`, `target`, `.next`,
  `.turbo`, `Steam`.
- File patterns added to `Ignore`: `*.safetensors`, `*.gguf`, `*.onnx`,
  `*.pak`, `*.iso`, `*.dmg`.

Note the existing defaults use `node_modules/**`, which via `filepath.Match`
matches only at the root; nested `node_modules` is caught by the basename
match and by `defaultIgnoredDirs`. The new basename-style entries have no such
gap.

### C2 — per-file cap

`MaxIndexableFileBytes` 25 MiB → 2 MiB (`internal/app/config/defaults.go:62`).
A 25 MiB source file is not useful to index, and the 25 MiB figure was never
what protected against this failure — the many-small-files case did. Files over
the cap are still skipped rather than truncated; the scanner records each skip
and `index.Run` reports the count ("scan skipped N entries; first: …"), so the
coverage change is visible in the pass report rather than silent.

### C4 — pathological root

When the resolved index root is the user's home directory or a filesystem root,
emit a warning through the pass's `Report.Warnings` naming the root. Warn only;
never skip. Indexing a non-git project directory stays supported.

### Config plumbing

`IndexingConfig` gains `MaxTotalContentBytes int64` with
`toml:"max_total_content_bytes"` (`internal/app/config/types.go:776`), a
pointer in the file schema (`file_types.go:114`), a `set(...)` in
`merge.go:100`, and a value in `save.go:319`. `index.Deps` gains a matching
field, threaded at `internal/app/hosts.go:540` (`MaxBytes:` sits beside it).

`config/defaults.go` references `limits.MaxTotalContentBytes`, not a
`repo.DefaultMaxTotalContentBytes`: `internal/repo` already reaches
`internal/app/config` through `internal/db`, `internal/subagentwork` and
`internal/worktree`, so naming the scanner's constant from config would close a
cycle. The leaf package `internal/limits` is the single declaration of the
number; `internal/repo` references it directly and declares no second name for
it.

Because the merged `indexing.ignore` list is additive, saving a config that sets
any entry writes the shipped prune defaults alongside it: the union is taken
against the live defaults, which is what keeps a new default reaching an
existing user. That is the accepted cost of the behaviour, and the merged list
is what the save surface writes.

## Testing

- `internal/repo` (`scanner_test.go`): the budget trips exactly at the
  boundary; files past it carry `ContentOmitted`, a valid `Hash` and a nil
  `ReadErr`; `hashFile` and `hashBytes` agree byte-for-byte on the same input;
  `TestScanDetailedReturnsContent` still passes for an in-budget tree. A tree
  larger than a shrunk test budget must never exceed that budget in retained
  bytes.
- `internal/index`: a `ContentOmitted` file is skipped for symbols and **keeps**
  its existing chunks across two passes — the direct regression test for the
  purge hazard, mirroring `TestReindexTransientReadErrorKeepsExistingChunks`.
  Separately, `Run` surfaces `Scanner.Warnings()` in `Report.Warnings`.
- `internal/app/config`: a merge/save round-trip for `max_total_content_bytes`,
  and a defaults test pinning 2 MiB and 512 MiB.
- `internal/snapshot`: the backoff suppresses a second attempt inside the window
  and expires after it.
- `internal/agent`: a pre-write capture that times out neither blocks nor fails
  the tool call, extending the existing expectations in `interrupt_test.go`.

Verification:

```bash
scripts/test.sh ./internal/repo ./internal/index ./internal/app/config ./internal/snapshot ./internal/agent
go vet ./... && gofmt -l .
```

## Decisions & trade-offs

- **A1 over A2.** A total content budget is one file plus tests. The streaming
  rewrite (A2) would give O(largest file) memory with no truncation, but forces
  `Indexer.Reindex` to consume a stream and touches ~10 existing tests. A1 is
  taken now and A2 is deferred; A1 leaves `ScanDetailed`'s signature intact so
  A2 stays incremental.
- **Truncate symbols, keep the file index.** Past 512 MiB of retained content,
  symbol and embedding coverage degrades and says so. Search, navigation and
  file hashing stay complete. The alternative — fail the whole pass — is worse:
  the incident shows a failed scan costs 11 minutes and leaves nothing.
- **Backoff, not a permanent negative cache.** A single bad capture must not
  disable snapshots for the session.
- **Warn on a pathological root, do not refuse.** Refusing would break
  non-git project directories, which the snapshot package explicitly supports.
- **`ContentOmitted` over `nil`.** Required to avoid purging valid embeddings;
  see the Reindex hazard.

## Assumptions

- The 512 MiB budget is a sensible default: it is far above any real source
  tree's retained content and far below a machine's memory. It is a config
  field, so a user who wants more can raise it.
- A 2 MiB per-file index cap does not lose real source files in practice.
- The snapshot package's tested support for a home-directory workspace means
  such a workspace should be made cheap, not rejected.
- 5 minutes is the right backoff window; it is a constant, not a config field,
  until someone needs otherwise.

## Non-goals

- **A2, the streaming scanner.** Deferred by explicit decision.
- **Changing `snapshots.enabled` or index enable defaults.** Those defaults are
  load-bearing.
- **Cleaning the 83 GB snapshot store.** Operational (`marshal snapshots
  cleanup`), not a code change.
- **zram priority / `vm.swappiness 150` / zswap.** Host configuration, not this
  repository.
- **The USB mouse dropping off bus 1 port 5.** Unrelated hardware fault.

## Open questions

None.
