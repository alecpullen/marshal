<!-- tools: file.edit, file.write, file.write_patch -->

# Editing files

Three tools write file contents. Two are model-facing; one is internal. All
three share a single write path — `applyFilePatches`
(`internal/tools/native/file.go:574`) — so the guards, backups, diff
generation, and diagnostics are identical whichever one is used.

| Tool | Model-facing | Use for |
|---|---|---|
| `file.edit` | yes | targeted edits to a file you have read |
| `file.write` | yes | creating a file, or replacing most of one |
| `file.write_patch` | **no** — internal | envelope-mode patch actions only |

## file.edit

**For:** replacing one exact occurrence of a string in a file.

**Surface** (`internal/tools/native/file_edit.go:21`), risk `workspace_write`:

| Arg | Required | Notes |
|---|---|---|
| `path` | yes | relative to the workspace |
| `old_string` | yes | must match byte-for-byte, including indentation, and be unique |
| `new_string` | yes | |

Include surrounding lines in `old_string` to disambiguate. Edit several files
with several calls rather than one large call.

**Failure modes:**

- `file.edit: old_string is required; use file.write to create or overwrite a whole file` (`file_edit.go:36`)
- `file.edit: old_string and new_string are identical; nothing to change` (`file_edit.go:39`)

The second is worth reading carefully: it fires when the edit is a no-op, which
usually means the change was already applied — not that the tool is broken.

## file.write

**For:** creating a new file, or overwriting an existing one with exact content.

**Surface** (`internal/tools/native/file.go:759`), risk `workspace_write`:

| Arg | Required | Notes |
|---|---|---|
| `path` | yes | relative to the workspace |
| `content` | yes | exact file content |

Prefer this over `file.edit` when writing a whole new file or replacing most of
a file's content; use `file.edit` for targeted edits. Never write files via
`shell.run` redirection or heredocs — those bypass diff review and rollback.

New files skip the stale/read guard, because there is no on-disk version to be
stale against. Existing files must have been read this session.

## file.write_patch (internal)

**For:** applying search/replace patch blocks, including multi-file patches in a
single call.

**Surface** (`internal/tools/native/file.go:533`), risk `workspace_write`,
`Internal: true` (`file.go:547`).

**The model does not reach this tool by default.** It is reached only by
envelope-mode patch actions; the model edits through `file.edit`. The
`Internal` flag means the runtime can dispatch it, but it is never offered to
the model — no tool definition, no prompt listing, no deferred announcement.

What it accepts: one or more search/replace blocks, each editing one file, with
the search block matching current file content exactly; an empty search block
creates a new file. Unified diff format is also accepted and converted
internally. Each search/replace block must end with a terminator line, and
unified diffs use standard hunk syntax.

**Why the model no longer reaches it.** The postmortem corpus's single largest
failure bucket was 150 `file.write_patch` failures — malformed blocks, missing
terminators, and search text that did not match. That was closed structurally,
by making the tool internal and giving the model `file.edit`, rather than by
prompting the model to format patches more carefully. A format the model cannot
get wrong is worth more than an instruction it can.

## The guards

Both model-facing write tools enforce the same two guards.

### Read before edit

An existing file must have been read this session before it can be edited
(`applyFilePatches` in `internal/tools/native/file.go` for the patch path,
`fileWriteTool` for `file.write`):

```go
				return registry.ToolResult{}, fmt.Errorf(
					"file %s was never read this session; read it before editing", fp.Path)
```

The read/staleness guard is re-run immediately before each commit, after
admission and directory creation, so a path that disappeared or changed in the
window fails closed. An external writer that changes the file after that final
check and before `os.WriteFile` is the remaining check-vs-commit window; session
agents are additionally serialized by the global write gate.

The ledger is **per conversation, not per agent**: `file_reads` and `file_writes`
are keyed by `session_id`, and every agent in the conversation — the main agent
and each `agent.run` child — writes through the same `filetrack.Service`. One
working tree, one truth about it. Alongside the timestamps, both tables carry a
per-path revision: every committed write bumps `file_writes.revision`, so a
write by one agent invalidates every other agent's view of that path even when
the clock cannot separate them — two writes inside the same second are still
ordered.

The newest writer is exempt from its own write: a path whose recorded writer is
the calling agent needs no re-read. An external change after that write still
makes it stale, because the recorded write time no longer matches the file's
modification time. The exemption compares modification time with the recorded
write time; on a filesystem whose timestamps are too coarse to separate an
external write from the recorded one, that change is indistinguishable from the
writer's own commit, while the revision epoch still orders session writes. Any
read clears the epoch for all agents: a full `file.read` and the region embedded
in a stale error both record the write revision they observed for the path,
together with the time they observed it, so one agent's read satisfies the guard
for the rest.

When the staleness check does fire, the error carries what the caller needs to
act on it instead of paying for a whole-file re-read:

- the writer and the time it wrote — `(last written by a subagent at
  2026-01-02 03:04:05Z)`, with `; the file changed again after that write`
  appended when disk moved on after the write. A path with no session write
  says nothing rather than guessing, which is itself the signal that something
  outside the session (a checkout, a formatter) changed it;
- the displayed region pinned to exact line numbers;
- for the exact-match patch path (`file.edit`, `file.write_patch`), a refreshed
  read mark: the caller has just been shown the current bytes for the region it
  is editing, and a retried patch is still validated byte-for-byte against
  disk, so retrying from that text costs no extra tool call. The suffix says
  which it is — `; the current region is shown below` when the mark was
  refreshed, `; re-read it before editing` otherwise. A whole-file `file.write`
  cannot retry from one region and does not refresh: it keeps the full re-read.

`native.Options.AgentID` is the writer's stable identity and
`native.Options.AgentLabel` its display name ("the main agent", "a subagent",
"the plan author", "a pipeline worker"). Both are recorded with each committed
write: the label is what the error prints, the id is what the own-write
exemption compares, and an empty `AgentID` falls back to the label.

### Doubled worktree paths

A path that repeats the `.marshal/worktrees/<branch>/` prefix is rejected with a
suggestion, unless the literal path really exists
(`internal/tools/native/helpers.go:251-272`):

```go
	return fmt.Errorf("%w: path %q contains a duplicated worktree segment; relative paths resolve from the current worktree root — did you mean %q?", ErrPathEscapes, rel, suggestion)
```

Relative paths already resolve from the current worktree root, so prefixing them
with the worktree path doubles the segment. The check uses `Lstat`, not `Stat`,
so a dangling symlink at the literal path is still treated as real and not
second-guessed as a typo.

## Call sequence

`file.read` the target → `file.edit` with a unique `old_string` → `shell.run` the
narrowest useful validation. For a new file: `file.write` → validate. For a
multi-file change: one `file.edit` per file, then validate once.
