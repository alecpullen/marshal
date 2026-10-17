<!-- tools: git.status, git.diff, git.log, git.blame -->

# Repository state

Four read-only tools answer questions about the repository's history and working
tree. All four are risk `read_only` and live in `internal/tools/native/git.go`.

| Tool | Anchor | Args | Notes |
|---|---|---|---|
| `git.status` | `git.go:17` | none | `git status --short`; summary is `working tree clean` or `working tree has changes` |
| `git.diff` | `git.go:38` | `path` (optional) | `git diff --`, or `git diff -- <path>`; summary `no diff` / `diff present` |
| `git.log` | `git.go:101` | `path` (optional), `limit` (default 20, max 100) | `git log --format='%h %ad %an %s' --date=short` |
| `git.blame` | `git.go:142` | `path` (required), `start_line`, `end_line` | whole-file blame caps at 200 lines with a marker; range blame uses `-L` |

**Failure mode to know:** `git.blame: start_line and end_line must be given
together with 1 <= start_line <= end_line` (`git.go:156`). The range is
all-or-nothing: passing only `start_line` is rejected rather than guessed at.

These tools exist so that repository state is a structured call rather than a
shell command whose output has to be parsed. `git.status` and `git.diff` are
what the plan-execution workflow checks before staging, and `git.log` is what it
reports at the end.

## Merge and stash hygiene

`shell.run` carries a nudge for two commands (`internal/tools/native/command.go:108`):

```go
func gitHygieneNote(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	switch {
	case strings.HasPrefix(trimmed, "git merge ") || trimmed == "git merge":
		return "note: for session work prefer workspace.finish over hand-merging; pop or drop any stash you create before finishing."
	case strings.HasPrefix(trimmed, "git stash"):
		return "note: remember to pop or drop this stash when done."
	}
	return ""
}
```

**The timing problem with this note.** It fires *after* the command has already
run, so the cost of the wrong call is already paid — the merge happened, or the
stash was created. That is why the same guidance is now paid up front, in the
cached system-prompt prefix (the `git.*` / workspace rule) and in `shell.run`'s
own description. A note that arrives after the fact can only help the next call;
a rule in the prefix helps this one.

## How this relates to workspace.finish

`workspace.finish` (`internal/tools/native/workspace_finish.go:27`) is the tool
that actually merges session work, and its description already carries the
pointer (`workspace_finish.go:31`):

> Use this instead of shell.run git merge, and pop or drop any stash you created
> before calling it.

So the division is: `git.*` reads repository state, `workspace.finish` changes
it. Hand-merging through `shell.run` bypasses the guards `workspace.finish`
enforces — it refuses when the project checkout is dirty or its HEAD moved — and
leaves the worktree and branch behind. See `workspace-interaction.md` for that
tool's full surface.

## Call sequence

Before staging: `git.status` to see what changed, `git.diff` to read the change.
Before reporting: `git.log` for the commit list. When investigating a line:
`git.blame` with a range. When finishing session work: `workspace.finish`, not
`git merge`.
