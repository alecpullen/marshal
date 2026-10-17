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
(`internal/tools/native/file.go:601` for the patch path, `file.go:811` for
`file.write`):

```go
				return registry.ToolResult{}, fmt.Errorf(
					"file %s was never read this session; read it before editing", fp.Path)
```

A file whose modification time is newer than the recorded read is rejected as
stale, with a `changedOnDiskError` naming the change. The point is that an edit
is applied against content the agent has actually seen, not content it assumed.

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
