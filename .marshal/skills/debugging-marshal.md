---
name: debugging-marshal
description: Use when debugging a bug, test failure, crash, or unexpected behaviour in marshal itself — this repository's own Go code (agent loop, TUI, tools, config, ACP) — before forming a hypothesis or changing code. Use instead of systematic-debugging when the failure is in marshal's own code or tooling, especially when a marshal tool behaves unexpectedly or rejects a call.
risk: read_only
---

# Debugging Marshal

The goal is the cause. Marshal's own postmortem record — per-session reports
of tool failures and friction — is evidence: the same failures recur across
sessions, and a symptom you are seeing may already be known. Check the
record before forming a hypothesis.

## The loop

1. **Reproduce it.** Get a reliable command that shows the failure. If you
   cannot reproduce it, everything after this is speculation. For
   intermittent failures, run it in a loop until you have a rate.
2. **Read the actual error.** The whole message, the whole stack trace, the
   actual line. Not the summary you assumed it said.
3. **Check the postmortem record — is this failure already known?** Scan the
   known-friction table below. If the symptom involves marshal's own tool
   behaviour, also run the live consultation procedure on the collected
   reports. A match means you may be looking at a known issue, not a fresh
   bug — check whether it was already fixed on main before debugging fresh.
4. **Form one hypothesis.** State what you think is wrong and — critically —
   what you would expect to observe if you were right.
5. **Test that hypothesis cheaply.** A log line, a targeted test, reading
   the function. One variable at a time.
6. **Confirm or discard, then repeat.** A discarded hypothesis is progress:
   write down what it ruled out.
7. **Only now, fix.** With the cause identified, the fix is usually small
   and obvious.
8. **Verify.** Re-run the original reproduction. Then run the wider suite
   for regressions.

## Known friction (from the postmortem record)

Every row below is a real, repeated failure from this project's collected
postmortem reports. When a tool call fails, find the symptom here before
retrying.

| Symptom | Cause | Correct move |
|---|---|---|
| `repo.search` rejected: "context must be between 0 and 3" | context arg caps at 3 | Use `context: 3`. The most-repeated failure across every session — never pass 4+. |
| `file.read` rejected: "too large to read (limit 200000 bytes)" | The whole-file byte limit applies BEFORE line-range selection. `internal/app/tui/model.go` (~207KB) and `model_test.go` (~297KB) exceed it. | Switch to `file.page` immediately. Line ranges do NOT bypass the limit — a different range is still the same rejected read. Never call `file.read` on that file again this session. |
| "path must be relative" / "escapes root" | `file.read`, `file.write`, `file.write_patch`, `repo.search` resolve paths against the workspace root. | Pass workspace-relative paths. Absolute paths and `../..` retries both fail. |
| `stat .marshal/worktrees/<branch>/...` doubled path | Inside a worktree, workspace-relative paths already resolve from the worktree root. | Drop the `.marshal/worktrees/<branch>/` prefix. |
| "file was never read this session; read it before editing" | Read-before-edit guard. | `file.read` the target (via `file.page` for large files) before `file.write_patch`. |
| "patch validation failed: search block not found" | Search blocks authored from assumed content. | Re-read the exact region first; verify tabs from the file content, not assumed counts. |
| REPLACE block closed with `=======` instead of `>>>>>>> REPLACE` | Patch format. | Close every block with `>>>>>>> REPLACE`. |
| `file.read` on a postmortem report fails: outside workspace root | Reports live in `~/.config/marshal/postmortems/<project-slug>/`. | Use `shell.run` with `cat`/`jq` — never `file.read`, not even once to check. The failure is already documented; verifying it yourself burns a call. |
| `repo.search` returns 0 matches for a term you can see in the file | `path` filters to a directory; a file path yields nothing. | Use `include` for a file glob, `path` for a directory. |
| `ls "$HOME/..."` → `No such file or directory` | `$HOME` is empty in `shell.run`. | Use `~` (tilde expansion works), not `"$HOME"`. |

## Consulting the postmortem record

1. Reports live at `~/.config/marshal/postmortems/<project-slug>/` (project
   slug = repo dir name lowercased, runs of non-alphanumerics → single dash;
   for this repo: `marshal`). One JSON file per session, named
   `<session-id>.json`.
2. `file.read` cannot reach them — they are outside the workspace root (a
   documented failure in the reports themselves). Use `shell.run` with
   `cat` or `jq`.
3. To scan all reports for a symptom, aggregate the tool failures:

   ```bash
   jq -s '[.[].tool_failures[]?] | group_by(.error) | map({error: .[0].error, tool: .[0].tool, count: map(.count) | add}) | sort_by(-.count)' \
     ~/.config/marshal/postmortems/marshal/*.json
   ```

   Use `~`, never `"$HOME"` — `$HOME` is empty in `shell.run`.
4. Also read `agent_observations` — they capture what mechanical extraction
   misses (e.g. "search blocks authored from assumed tab counts").
5. If the current symptom matches a report entry, treat it as a known
   issue: check whether it was already fixed on main (`git log`,
   `repo.search`) before debugging fresh. If it was fixed, the failure may
   be a stale binary or wrong branch.
6. If nothing matches, proceed with the normal loop — and note the new
   pattern so the next postmortem records it.

## When the problem is slowness or wasted work

Postmortems capture tool failures, not time. For "a workflow is slow" or
"subagents spend too long", measure the session database
(`.marshal/marshal.db`, open it with `sqlite3 -readonly`):

- **Subagent runs:** `turn_metrics` where `role = 'subtask'`. Look at
  `duration_ms`, `iterations`, `tool_calls`, `prompt_tokens` and `goal` (the
  brief's opening words). Child transcripts are not persisted, so this and
  the child's final report are the only record of what it did.
- **Child reports:** `messages` where `content_type = 'subagent_report'`.
  These show what the child spent time on: re-proving failures, verifying
  beyond its brief, deviations.
- **Briefs:** `tool_calls` where `tool_name = 'agent.run'`; the prompt is in
  `json_extract(args_json, '$.prompt')`.
- **Skill mix:** `tool_calls` where `tool_name = 'skill.load'`, grouped by
  session. Two skills for the same job, or a user skill shadowing a
  built-in, show up here.

Correlate cost with the input that drives it, for example task duration
against how concrete each plan task is, before blaming the agent.

## Narrowing

- **Bisect.** Last known-good commit vs now, or comment out half.
- **Check the boundary.** Most bugs live where two components meet: what one
  produces, what the other expects.
- **Question the assumption.** "That can't be nil" and "that's always sorted"
  are where bugs hide. Verify, don't assert.

## Red flags

- "Let me just try changing this and see" — that is guessing. Form a
  hypothesis first.
- "It's probably a caching issue" — probably is not a diagnosis.
- Fixing a symptom you found while looking for the cause, then declaring
  victory.
- Adding a retry, sleep, or `try/catch` to make a failure stop appearing. The
  bug is still there and now it is invisible.
- **Retrying a rejected tool call with unchanged arguments** — the postmortem
  record shows this exact pattern recurring across sessions. A rejection is
  information: read the error, find the row above, change the call.
- **"A different line range is a different read"** — it is not. The byte
  limit applies to the whole file; any `file.read` on a rejected file fails.
- **"I should verify the documented failure myself"** — no. If the table
  says a call fails, it fails. Do not spend a call confirming a known
  rejection.
