# Filetracker shared stale epoch

Status: approved for implementation.

## Context

`file.edit` and `file.write` are gated by a read-before-write ledger
(`internal/filetrack`) so an agent edits bytes it has actually seen. The ledger
is keyed by conversation, not by agent: the main agent and every `agent.run`
child share one `filetrack.Service`, one working tree, and one truth about it.

Field reports showed two failure modes around delegated work:

- an edit rejected as `file changed on disk since last read`, even though the
  agent had read the file earlier in the conversation; and
- an edit rejected as `file was never read this session` after another agent
  had read the path.

The first pass added writer attribution (`file_writes.written_by`) and embedded
the current region with exact line numbers in the stale error. It did not
distinguish *who* had observed a revision. Every committed write still called
`RecordRead`, which refreshed the one shared read mark and made the write look
read by everyone. A second agent could therefore edit a path whose current
content it had never observed. The reported expectation is the inverse:

- a write by one agent invalidates every other agent's view of that path;
- the writer itself may continue from the bytes it just produced; and
- one re-read of the path satisfies all agents, because there is one working
  tree.

This spec defines that shared stale epoch and the writer exemption.

## Approved design

### Scope

Extend the existing `file_reads` and `file_writes` tables with a per-path
revision. Every committed write advances the revision. A read records the
revision it observed. The guard compares revisions to decide staleness, and
keeps the file modification time as the detector for changes made outside the
session.

The existing global write gate remains unchanged. File writes are still
serialized across agents by `swarm.WriteLock`; this feature does not introduce
per-path locks.

### Data model

`file_reads` gains:

```sql
revision INTEGER NOT NULL DEFAULT 0
```

`file_writes` gains:

```sql
revision   INTEGER NOT NULL DEFAULT 0,
written_id TEXT NOT NULL DEFAULT ''
```

`written_by` remains the human-readable label (`"a subagent (branch review)"`);
`written_id` is the stable actor identity used by the writer exemption.

`file_writes` remains one upserted row per `(session_id, path)`. Each successful
write increments `revision` atomically in the upsert. `file_reads` remains one
upserted row per `(session_id, path)` and stores the write revision observed by
the reader.

Existing rows default to revision 0. A pre-feature write (revision 0) and a
pre-feature read (revision 0) form no epoch and therefore cannot produce a false
stale. A read recorded after the feature starts observes the current revision;
a later write advances it.

`file_reads` is added to `db.allowedTableInfo` so migration introspection can
inspect it. The base schema and `db.migrationColumns` both gain the new columns,
so fresh and existing databases converge on the same shape.

### Service contract

`internal/filetrack` exposes:

```go
type Actor struct {
    ID    string
    Label string
}

type ReadState struct {
    Revision int64
    At       time.Time
    OK       bool
}

type WriteState struct {
    Revision int64
    At       time.Time
    Actor    Actor
    OK       bool
}

func (s *Service) RecordRead(path string, at time.Time, revision int64) error
func (s *Service) ReadState(path string) (ReadState, error)

func (s *Service) RecordWrite(path string, at time.Time, actor Actor) error
func (s *Service) WriteState(path string) (WriteState, error)
```

`LastReadTime` and `LastWriter` are replaced by `ReadState` and `WriteState`.
Both state queries are session-scoped. A missing row returns `OK == false` and a
zero time; a malformed stored timestamp returns an error with `OK == false`.
`RecordWrite` increments the revision; it does not read the path or refresh the
shared read mark. `RecordRead` stores the caller's observed revision and never
regresses an existing higher revision:

```sql
ON CONFLICT(session_id, path) DO UPDATE SET
  revision = MAX(file_reads.revision, excluded.revision),
  read_at  = CASE
               WHEN excluded.revision >= file_reads.revision
                 THEN excluded.read_at
               ELSE file_reads.read_at
             END
```

The `MAX`/`CASE` form prevents a late, lower-revision read from making a newer
shared read look stale.

`internal/tools/native.FileTracker` exposes the same four methods, using
`filetrack.Actor`, `filetrack.ReadState`, and `filetrack.WriteState`.

### Read flow

`file.read` observes the current write revision *before* reading the bytes, then
records that revision with the read:

1. resolve the path and stat it;
2. query `WriteState` (best effort; a failure is logged and observation falls
   back to revision 0);
3. open, size-check, and read the file;
4. `RecordRead(path, time.Now(), observedRevision)`.

Observing before the read is deliberately conservative. If another agent writes
between steps 2 and 3, the reader records the older revision, and its next edit
is stale. It never records a revision whose bytes it may not have seen.

When a stale edit error embeds the current region, it records that same observed
revision. The embedded region therefore counts as a read and clears the shared
epoch for every agent, but only up to the revision whose bytes were shown. A
newer write still leaves the path stale.

### Guard

For an existing file, with `read := ReadState(path)`, `write := WriteState(path)`,
and `info := os.Stat(path)`:

1. If `write.OK` and the current actor is the newest writer:
   - if `info.ModTime().After(write.At)`, the file changed externally after the
     write, so reject as stale;
   - otherwise allow, without advancing the shared read revision.
2. If there is no read:
   - reject with the existing “never read this session” error.
3. If `write.OK && write.Revision > read.Revision`, reject as stale.
4. If `info.ModTime().After(read.At)`, reject as stale (an external change).
5. Otherwise allow.

The current actor's identity is `AgentID`, falling back to `AgentLabel` when
`AgentID` is empty so stand-alone toolsets and tests still get a writer
exemption. If both are empty, there is no exemption.

Consequences:

- writer W writes revision 1; other agents are stale; W may edit again;
- W writes revision 1, another agent reads revision 1, W writes revision 2;
  the reader is stale, W is exempt;
- W writes revision 1, an external formatter changes the file; W's next edit is
  stale because `mtime > write.At`;
- the newest writer writes again without reading; it is exempt and does not
  clear the epoch for anyone else;
- any agent's full read or embedded-region read advances the shared read
  revision and clears the epoch for all.

New-file creation keeps its existing path: a path that does not exist has no
on-disk version to be stale against. After the write, the writer is exempt and
other agents must read the path.

### Error handling

The stale error keeps the stable substring `changed on disk since last read`.
It is assembled as:

```text
file %s changed on disk since last read%s%s
```

The first `%s` is the writer note and the second is the retry suffix. The writer
note is empty when no session writer is known. Otherwise it is:

```text
 (last written by <label-or-id> at <timestamp>)
```

The timestamp is `at.UTC().Format("2006-01-02 15:04:05Z")`. If `mtime` is newer
than the named write, the note is instead:

```text
 (last written by <label-or-id> at <timestamp>; the file changed again after that write)
```

If the label is empty, the actor ID is used; if both are empty, the writer note
is omitted. This keeps the writer from being falsely blamed for an external
change.

For a non-refresh rejection the suffix is `; re-read it before editing`. For a
refreshed exact-match patch the suffix is
`; the current region is shown below`. The error then appends a blank line, the
`current on-disk content near the target: lines N-M` header, the region, and a
blank line before the retry sentence.

The error retains the failing chunk's nearest-matching region with exact
`lines N-M` numbers. For exact-match patch paths (`file.edit` and the internal
`file.write_patch`), it also records the observed revision and appends:

```text
Retry with an old_string taken from those lines; the read mark was refreshed
with that region, so the edit is accepted without a separate file.read.
```

The refresh suffix and retry sentence are used only when a region was found and
recording the read succeeded. Otherwise the error falls back to the non-refresh
suffix and the region, if any. The wording does not claim the whole file is
current. If a newer write lands, the retry is rejected again and the guard shows
the newer region.

A whole-file `file.write` cannot retry from one region. Its stale error reports
the writer and region when available but does not refresh the read mark and
retains the `re-read it before editing` instruction.

If the guard cannot read the ledger, it fails closed with a clear verification
error rather than allowing an edit. Failures to record a successful read are
logged and do not fail the read itself, matching current behavior.

### Actor identity and wiring

`native.Options` gains `AgentID string`; `AgentLabel` remains the display name.

| Agent | Actor ID | Label |
|---|---|---|
| Main agent | `main` | `the main agent` |
| Plan author | unique per constructed author | `the plan author` |
| Pipeline worker | unique per constructed worker | `a pipeline worker` |
| `agent.run` child | the child's `WorkID` | `a subagent (<description>)` |

The subagent handler already has the unique `WorkID` before it calls the
runner factory. `SubagentRequest` gains `ActorID` and `ActorLabel`; the factory
passes them into `childOpts`, and the child ledger write records both. Using the
description rather than the transient card number is deliberate: the card
number is assigned after the child toolset exists, while the description is
stable and more useful in an error. Numeric card IDs are a non-goal.

Pipeline workers are concurrent, so each constructed worker mints a unique ID
(an atomic counter in the factory is sufficient; the ledger is session-scoped).
Plan authors mint a unique ID per constructed author for the same reason: the
fixed-ID design relied on an unenforced “one author per conversation”
assumption, and two authors sharing an ID would exempt each other's writes.

### Migration and docs

- Update `internal/db/migrations.go` base schema.
- Update `internal/db/db.go` `migrationColumns` and `allowedTableInfo`.
- Update `internal/filetrack/service_test.go`'s in-memory schema.
- Update `docs/tools/editing.md` to describe the shared epoch, writer
  exemption, observed-revision reads, actor IDs, and the region-refresh rule.
- This spec lives at `docs/filetrack/feature-spec.md` and is committed with the
  implementation, per `AGENTS.md`.

### Testing

Filetrack:

- a write increments `revision`, records actor ID/label/time, and does not
  create or refresh a read;
- a read stores the observed revision; a later read at a lower revision does not
  regress it;
- a read after a write advances the shared revision and clears the epoch;
- pre-feature rows at revision 0 produce no stale;
- session isolation and unparseable timestamps still behave correctly.

Native guard:

- parent reads, child writes: parent is stale, names the child, and shows the
  exact line range;
- the child writer may edit again without a read;
- an external `mtime` after the child's write makes the child stale too;
- a read by another agent clears the epoch for the parent;
- an embedded region clears the epoch for another toolset at the observed
  revision;
- a newer write after the embedded region leaves the path stale;
- whole-file `file.write` does not refresh from one region;
- a non-writer that never read is still rejected with “never read”.

Wiring:

- `SubagentRequest` carries the unique actor identity and description label;
- main, plan-author, and pipeline toolsets receive their configured IDs/labels;
- an empty actor ID falls back to the label.

Verification is the narrow package set plus the full suite:

```bash
CGO_ENABLED=1 gofmt -w internal/
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./internal/filetrack/... ./internal/tools/native/... ./internal/db/... ./internal/app/... ./internal/agent/...
CGO_ENABLED=1 go test ./...
```

## Decisions and trade-offs

- **Extend the existing tables rather than add a path-state table.** The read
  and write rows already share the session/path key; a revision column is the
  smallest change that carries the epoch. The cost is keeping the two
  revision-bearing rows consistent in the service.
- **Shared epoch, not strict per-agent reads.** The user chose one re-read
  clearing the stale state for everyone. This matches the one-working-tree
  model and avoids forcing every agent through a full read.
- **Writer exemption instead of a post-write global read.** A write must
  invalidate other readers, so it cannot call `RecordRead`. The writer is
  exempted by actor identity and remains stale if the file changes externally
  after its write.
- **Revision plus mtime.** Revisions attribute session writes; mtime still
  catches formatters, checkouts, and other processes. Content hashing was
  rejected as too expensive for large files.
- **Observe before reading.** A read records the revision it saw before the
  bytes were read, so a concurrent write leaves the path stale rather than
  falsely current.
- **Global write gate retained.** Per-path locks were rejected for this pass;
  the existing lock already serializes the write tool's check-and-commit
  window.

## Assumptions

- SQLite serializes the revision upsert; no additional service-level lock is
  needed.
- Revision 0 is the correct “no epoch” value for rows created before this
  feature.
- Plan authors and pipeline workers mint a unique actor ID per constructed
  runner; neither relies on a concurrency assumption. The main agent keeps the
  fixed `main` ID because there is exactly one per conversation.
- `WorkID` is unique for the life of an `agent.run` child and is available
  before the child runner is built.
- File modification times remain the external-change signal; the existing
  clock-granularity limitations are unchanged.
- `file.read` may read a line range, but it records a read of the path, as it
  does today.

## Explicit non-goals

- Per-path locking or any change to the global write gate.
- Content hashing or byte-level fingerprints.
- Strict per-agent read rows; the epoch is shared.
- A numeric subagent card number in the writer label.
- Changes to `todo.write` or `skill.load`; the current tree already implements
  the behavior described in the field report.
- Automatic repair of external changes beyond showing the current region and
  the writer.

## Open questions

None.
