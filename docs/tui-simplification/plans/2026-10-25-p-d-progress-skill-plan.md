# P-D progress-reporting skill — implementation plan

Date: 2026-10-25. Status: ready for review. Revised the same day after P-C merged.
Base: `main` at `e667a100` (the P-C merge) or later.
- Every anchor was first checked at `d7d9b39c`, then again at `e667a100`. None of the files P-D edits changed between the two.
- P-C added the live box, which is where the card is drawn. The skill's account of what the card shows (Task 2, body section 5) was checked against the box's code at `e667a100`.
- The byte counts below were measured at `e667a100` with `TestDefaultPromptBudget`'s own log line, using `TMPDIR=/private/tmp/pd-suite`. The prompt contains the test's temp working directory, so the count moves by a few bytes with the temp path; the tolerances allow for that.

Approved design: [feature-spec.md](../feature-spec.md) §4 (B10, B12), §6.4, §7 (P-D) and §8. The spec was updated on 2026-10-25 with the decisions below.
Previous phase: [P-C plan](2026-10-24-p-c-live-box-plan.md).
Background: [live-work spec](../../live-work/feature-spec.md), "Command-bound progress configuration" to "Profiles and truthful measurements", and its [examples](../../live-work/examples/).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. The skill's producers are described by behaviour and by the example each one ports. Executing the plan also tests how well Marshal follows a plan, so each step states its decision. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the agent can put a measured progress card on screen for long work.
- A built-in `progress-reporting` skill teaches the `progress` option and the `@@marshal-progress/v1` record format. It carries ready-to-write producers for a download, a transfer and a phased custom job.
- `shell.run`'s `progress` property points at the skill.
- Both additions fit the prompt and tool-schema budgets, and the prompt budget test passes again.

**Non-goals:**
- Any change to how progress is parsed, stored or drawn. `internal/progress` and the TUI are untouched.
- Producers for other test runners (pytest, jest). The skill says they can emit records with profile `tests`, and stops there.
- Wrapping a third-party tool's own progress output (`curl`, `rsync`).

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/pd-suite`. Create it first: `mkdir -p /private/tmp/pd-suite`.
- **Pre-existing failures,** measured at `d7d9b39c`:
  - `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree`, `TestSubagentShellOutputDoesNotReachParent`, `TestAccessTUIOptionsResolveWithoutConfigReload`, `TestDefaultPromptBudget` and `TestRuntimeNewAndResumedSessionResetAccess`;
  - `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`;
  - `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`;
  - `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`;
  - flaky under full-suite load, passing alone: `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`), and `TestACPChildApprovalUsesOwningSessionRegistry` (`internal/acp`).

  **`TestDefaultPromptBudget` leaves this list in Task 1** and must pass from then on. A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`.
- `python3` is on the machine. This machine has 3.9, so the producers use nothing newer than Python 3.8: no `match` statements and no `X | Y` type unions.

**Decisions this plan makes beyond the spec** (all now recorded in the spec's §6.4):
1. **Room in the system prompt.**
   - The prompt is 9,107 bytes against a 9,000 ceiling, and the skill's listing line adds 142.
   - The postmortem skill's description shrinks from 383 bytes to 121. Its one caller already names it ("Run the postmortem skill: …"), so the trigger text did nothing.
   - The prompt ends near 8,987 bytes.
2. **Room in the tool schemas.**
   - The model-facing schemas are 14,975 bytes against 15,000, and the pointer adds 65.
   - `shell.run`'s description drops "Commands are subject to conservative guardrails." (49 bytes), which gave the model nothing to act on.
   - The schemas end near 14,991. `test.run` is internal (`Internal: true`), so its pointer is not counted.
3. **Producers live in `.marshal/progress/`.**
   - The agent writes them with `file.write`: writes must stay in the workspace, and `.marshal/` is Marshal's own directory.
   - The `.marshal/.gitignore` that new projects get gains `progress/`.
   - In an older project the folder shows as untracked. The agent tells the user and does not edit the ignore file unasked.
4. **One shared helper.** The producers import `marshal_progress.py`, a port of the examples' `progress.py`. It is renamed so it cannot shadow a package called `progress`.
5. **Risk `workspace_write`.** The spec said `read_only`. The skill has the agent write files, and the built-in skills that lead to writes declare `workspace_write`. Skill risk gates nothing today.
6. **The body never names `docs/live-work/`.** User projects do not have it. `TestBuiltInSkillsAreRepoNeutral` gains it as a banned string.
7. **The embedded code is tested.**
   - Every example record in the body decodes with `progress.DecodeEvent`.
   - The download and custom producers run under `python3` in a test, and the transfer producer must at least import.

**Execution:**
- Branch first: `git switch -c p-d-progress-skill` from `main`.
- The first version of this plan and its spec edits reached `main` in `724ea858`. If `git status` shows this revision of them uncommitted, commit it first: `docs/tui-simplification/` as explicit paths, message `p-d-progress-skill: revise the P-D plan after P-C`.
- Run the tasks in order. Run each task's Verify and commit it as `p-d-progress-skill: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- "Delete test X" means delete the whole function. "Extend test X" means keep its assertions and add the ones the step names.

## Contracts

**Task 1, `internal/skills/builtin/postmortem.md`.** The frontmatter `description` becomes exactly:

```
Append observations to a session postmortem report's agent_observations. Use only when asked to run the postmortem skill.
```

**Task 2, `internal/skills/builtin/progress-reporting.md` (new).** Frontmatter, exactly:

```yaml
name: progress-reporting
description: Show a live progress bar for a long download, transfer, test run or phased job, using shell.run's progress option.
risk: workspace_write
```

**Task 2, the producers embedded in the body.** Each is one fenced `python` block whose first line is the comment `# .marshal/progress/<file>`. The test finds the blocks by that line.

| File | Arguments | Ports | Behaviour the test checks |
|---|---|---|---|
| `marshal_progress.py` | (module) | the examples' `progress.py` | A `Progress` class with `emit(metrics, *, phase=None, rows=None, detail="", force=False)`. It throttles to one record per 0.2 s unless `force`. Its sequence starts at 1 and increases, it cuts `detail` to 1,024 UTF-8 bytes, and it sets `primaryMetricId` to the first metric's id. It refuses a line over 16,384 bytes and prints the prefix plus the JSON to stdout, flushed. |
| `download.py` | `URL DEST` | `download.py` | Opens `DEST` with mode `xb`, so it refuses to overwrite. Metric `received` (label `Bytes received`, unit `bytes`) carries a rate, and a `total` only when `Content-Length` is a positive integer of at most 2^53−1. Forced records at the start and the end, with `detail` set to `DEST`. It finishes with the line `Downloaded <n> bytes to <DEST>`. |
| `transfer.py` | `SOURCE HOST REMOTE_FILE` | `transfer.py` | Pipes `SOURCE` into `ssh -T HOST "cat > <quoted REMOTE_FILE>"`, and rejects a `HOST` that starts with `-`. Metric `supplied` (label `Bytes supplied to SSH`, unit `bytes`), with `total` set to the file size when it is above 0. It exits with ssh's exit code. |
| `custom.py` | `MANIFEST FILE...` | `custom.py` | Phase `hash` (1 of 2) carries metric `hashed` (unit `bytes`, with `total` set to the summed size when it is above 0) and rows `hash` and `manifest`. Phase `manifest` (2 of 2) ends with metric `files` (unit `files`, current equal to total equal to the number of files). It writes `MANIFEST` with mode `x` as a JSON list of `{"path", "sha256"}`. |

Every producer starts with `from marshal_progress import Progress`. It runs as `python3 .marshal/progress/<file> …` from the workspace root, so the script's own directory is on the import path.

**Task 3, `internal/tools/native/command.go`.** The `progress` property in both the `shell.run` and `test.run` schemas gains, right after `"type":"object"`:

```
"description":"live progress card; see skill progress-reporting"
```

**Task 2, `internal/app/config/marshal_dir.go`.** `marshalDirIgnore` gains the line `progress/` after `skills/`.

## Task 1: Room in the system prompt

**Goal:** the postmortem skill's description is short, and the system prompt is back under its ceiling before anything is added.

**Files:** `internal/skills/builtin/postmortem.md`.

**Steps:**

1. Replace the frontmatter `description` with the Contracts' text. The body stays as it is.
2. Nothing else names the old text. `grep -rn "Extraction only; never synthesize" --include='*.go' internal/` prints nothing at `e667a100`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pd-suite go test ./internal/skills/... ./internal/commands/...
TMPDIR=/private/tmp/pd-suite go test ./internal/app/ -run TestDefaultPromptBudget -v 2>&1 | grep -E 'prompt_budget_test|^(--- |ok|FAIL)'
```

Expected:
- skills and commands pass;
- `TestDefaultPromptBudget` passes and logs a system prompt of 8,845 bytes (±5). A different figure means the base moved, so re-measure before Task 2 and check there is still room for a 142-byte line.

## Task 2: The skill and its producers

**Goal:** `progress-reporting` is a built-in skill whose body teaches the option, the record format and the producers, and whose embedded code is proven to work.

**Files:**
- `internal/skills/builtin/progress-reporting.md` (new), `internal/skills/builtin.go`;
- `internal/skills/builtin_test.go`, `internal/skills/progress_skill_test.go` (new);
- `internal/app/config/marshal_dir.go`, `internal/app/config/marshal_dir_test.go`.

**Steps:**

1. **Registration (`builtin.go`).** Add a `//go:embed builtin/progress-reporting.md` variable `progressReportingSkill`, and the map entry `"progress-reporting": progressReportingSkill`, as every other built-in does.
2. **The body.** Write it in the voice of the other built-ins: second person, short sections, rules as lists. It has these sections, in order:
   1. **`# Progress Reporting`,** then a two-sentence purpose. A measured card shows what a long command has really done. You turn it on with `shell.run`'s `progress` option.
   2. **When to use it.**
      - Use it for a command that will run long enough to watch (roughly more than half a minute) and has something real to count: bytes downloaded or sent, tests passed and failed, files processed, or the phases of a multi-step job.
      - Otherwise run the command without `progress`. The generic card already shows the elapsed time and the last output line.
      - Never write a producer for a command that finishes in seconds.
   3. **The `progress` option.**
      - The fields are `profile` (`generic`, `tests`, `download`, `transfer`, `custom`), `source` (`none`, `go-test-json`, `events-v1`) and `title` (at most 80 characters).
      - `go-test-json` needs profile `tests`. `events-v1` needs one of the four profiles other than `generic`. Source `none` gives the plain card whatever the profile. An invalid combination is rejected before launch.
      - Run long work with `"background": true` and follow it with `agent.await`.
      - The option never changes the command, and Marshal adds no flags.
      - Include one example of `shell.run` arguments as a JSON block: the download producer with `background`, profile `download`, source `events-v1` and a title.
   4. **Go tests.**
      - Run `go test -json` plus the packages, with profile `tests` and source `go-test-json`. No producer is needed.
      - The card counts passed, failed and skipped tests. It never shows a total, because the JSON does not declare one.
      - Other test runners can print records with profile `tests` and a `tests` metric.
   5. **The record format.** State each rule as one bullet:
      - The prefix is exactly `@@marshal-progress/v1 ` (with the trailing space), then one JSON object and a newline, on stdout. Stderr and every other stdout line are ordinary output.
      - Each record is a complete snapshot, never a patch. A field you leave out is cleared.
      - `version` is `1`. `sequence` starts at 1 and strictly increases, up to 2^53−1. An older or repeated sequence is ignored.
      - **Metrics:** at most 4, each with a unique `id` (up to 64 characters), a `label` (up to 80) and a `unit`.
        - The unit is `bytes`, `files`, `tests`, `packages` or `items`, whose values are integers, or any other unit of up to 16 characters.
        - `current` is 0 or more. `total` is optional and above 0. **Omit `total` when you do not know it, and never invent a total.**
        - `rate` is optional, in units per second. Without it the card works out a rate from successive records.
        - `primaryMetricId` names one of the metrics; without it the first is primary.
      - **Phase:** optional `{id, label, index, total}`, with `1 ≤ index ≤ total`. A phase count is not a percentage of the whole job.
      - **Rows:** at most 8, each `{id, label, state}`, with state `pending`, `running`, `done` or `error`.
      - **Limits:** `detail` is at most 1,024 bytes, and a whole record line at most 16 KiB.
      - **When to emit:** at most about five records a second, plus one at the start, at each phase change and at the end.
      - **What the card does with them** (the live box's behaviour at `e667a100`):
        - The row in the box shows the primary metric:
          - with a `total`, the phase as `1/2 <label>` when there is one, then a bar, a percentage, the count, the ETA and the rate;
          - without one, the count, the rate and, for bytes, "size unknown".
          - Bytes are shown in IEC units (KiB, MiB).
        - Expanding the row shows `detail` when it is set. Otherwise it shows the first four rows as a checklist. A producer that wants its checklist seen leaves `detail` empty.
        - A record more than 30 seconds old is stale. The bar holds, the rate and ETA hide, and the row warns "no update for …". Emit whenever the work moves, so a stale warning means the work really stalled.
        - An invalid record is dropped with one warning, and the last valid one stays on the card.
        - Only the process's exit finishes the card. A record at 100% shows the work as reported done while the process still runs.
        - When the process ends, its completion event shows the last record's measurement, and under it the last record's `detail`, else the last output line.
      - **The example.** End the section with one complete example record on a line of its own: the prefix followed by compact JSON with a phase, one metric with a total, two rows and a detail. Task 2's test decodes it.
   6. **Producers.**
      - Write them with `file.write` under `.marshal/progress/`. If a file is already there, read it and reuse it rather than writing it again.
      - Write `marshal_progress.py` first, then the producer you need.
      - Give the four blocks from the Contracts table, each with its path comment on the first line. Port each from the example it names, keeping its behaviour and measurements. Change only:
        - the import, which becomes `from marshal_progress import Progress`;
        - the first line, which is the path comment;
        - the docstrings, which state what each script measures.
      - Under each block, one line on how to run it, with its profile and source.
      - **One honesty note each:**
        - the download leaves a partial file on failure and refuses to overwrite;
        - the transfer counts bytes handed to SSH, not bytes verified on the remote, and may overwrite the remote file;
        - the custom job's percentage belongs to its phase.
      - **For a job none of these fit,** write a new producer that imports `marshal_progress` and reports only what it really measures.
   7. **Housekeeping.**
      - Tell the user where the scripts are.
      - If `git status` lists `.marshal/progress/`, the project's `.marshal/.gitignore` predates the entry. Say so, and do not edit the file unasked.
      - The producers need `python3`. If `python3 --version` fails, use the generic card.

   The body never names `docs/live-work/`, and it stays under 12 KiB.
3. **The ignore entry (`marshal_dir.go`).** Add `progress/` to `marshalDirIgnore` (Contracts).
4. **Tests.**
   - **`builtin_test.go`:**
     - Add `"progress-reporting"` to `TestBuiltInSkillsAllLoad`'s list, in alphabetical order, which makes 13 names.
     - Add `"docs/live-work"` to the banned strings in `TestBuiltInSkillsAreRepoNeutral`.
     - Add `TestLoadSkillsIncludesBuiltInProgressReportingSkill`, as `TestLoadSkillsIncludesBuiltInPostmortemSkill` does. The description equals the Contracts' text, `Risk` is `workspace_write`, and `len(Body)` is at most 12,288. The lowercased body contains each of:
       - `@@marshal-progress/v1 `, `events-v1`, `go-test-json`, `go test -json`;
       - `.marshal/progress/`, `file.write`, `marshal_progress`, `agent.await`;
       - `never invent a total`, `30 seconds`.
   - **`progress_skill_test.go` (new):**
     - **`progressSkillBody(t)`** loads the built-ins with `loadBuiltIns` and returns the skill's body.
     - **`producerBlocks(body)`** returns a map from path to source, for every fenced `python` block whose first line is `# .marshal/progress/<file>`.
     - **`TestProgressSkillExampleRecordsDecode`.** Every body line that starts with `progress.Prefix` decodes with `progress.DecodeEvent(line, 0, time.Now())`, and there is at least one such line.
     - **`TestProgressSkillHasEveryProducer`.** `producerBlocks` holds exactly `marshal_progress.py`, `download.py`, `transfer.py` and `custom.py`.
     - **`TestProgressSkillProducersRun`.** Skip when `exec.LookPath("python3")` fails. Write each block under `<tmp>/.marshal/progress/`, and run every command from `<tmp>`.
       - **Download.**
         - Make a 1 MiB file of non-repeating bytes.
         - Run `python3 .marshal/progress/download.py file://<abs source> out.bin`; it exits 0.
         - Decode every stdout line that starts with the prefix, passing the previous sequence each time. Each decodes, and there are at least 2.
         - The last record's `received` metric has unit `bytes` and `current == total ==` the source size.
         - `out.bin` equals the source.
         - The last stdout line starts with `Downloaded`.
         - Running the same command again exits non-zero and leaves `out.bin` unchanged.
       - **Custom.**
         - Make two files.
         - Run `python3 .marshal/progress/custom.py manifest.json a.bin b.bin`; it exits 0.
         - Every record decodes in sequence. Some record has phase `hash` with index 1 and total 2, and the last has phase `manifest` with index 2 and total 2.
         - The last record has metric `files` with `current == total == 2`, and row `manifest` in state `done`.
         - `manifest.json` is a JSON list of two entries whose `sha256` values match the files.
         - Running it again exits non-zero.
       - **Transfer.** `python3 .marshal/progress/transfer.py --help` exits 0, which proves it imports. It needs SSH, so it is not run further.
   - **`marshal_dir_test.go`:** add `"progress/"` to `TestEnsureMarshalDirIgnoredCreatesNestedIgnore`'s wanted entries.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pd-suite go test ./internal/skills/... ./internal/app/config/... -v -run 'Progress|BuiltIn|MarshalDir' 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)'
TMPDIR=/private/tmp/pd-suite go test ./internal/skills/... ./internal/app/config/... ./internal/agent/...
TMPDIR=/private/tmp/pd-suite go test ./internal/app/ -run TestDefaultPromptBudget -v 2>&1 | grep -E 'prompt_budget_test|^(--- |ok|FAIL)'
go vet ./internal/skills/... && gofmt -l internal/
```

Expected:
- `TestProgressSkillProducersRun` runs; it does not skip on this machine.
- skills, config and agent pass.
- `TestDefaultPromptBudget` passes, with a system prompt of about 8,987 bytes, under 9,000.
- vet is clean, and `gofmt -l` prints nothing.

## Task 3: The schema pointer

**Goal:** `shell.run` and `test.run` name the skill where the model reads the `progress` option, and the schemas stay within their ceiling.

**Files:** `internal/tools/native/command.go`, `internal/tools/native/schemas_test.go`.

**Steps:**

1. **`shellRunTool`.** Delete the sentence `Commands are subject to conservative guardrails. ` (with its trailing space) from the description. The sentences either side of it stay as they are.
2. **Both schemas.** Add the Contracts' `description` to the `progress` property of `shell.run` and of `test.run`, right after `"type":"object"`.
3. **Tests (`schemas_test.go`).** Extend `TestShellToolTextDocumentsWaitContinuationAndStop`:
   - add `"progress-reporting"` to the wanted strings of both `shell.run` and `test.run`;
   - after the existing `timeout_seconds` check, decode the `progress` property's `description` from the `shell.run` schema, as that check decodes `timeout_seconds`, and assert it equals the Contracts' text.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pd-suite go test ./internal/tools/native/...
TMPDIR=/private/tmp/pd-suite go test ./internal/app/ -run TestDefaultPromptBudget -v 2>&1 | grep -E 'prompt_budget_test|^(--- |ok|FAIL)'
go build ./... && gofmt -l internal/
```

Expected:
- native passes, including `TestShellToolTextDocumentsWaitContinuationAndStop` and the `native_test.go` check on `shell.run`'s wording, whose pinned phrases are untouched.
- `TestDefaultPromptBudget` passes, with tool schemas of about 14,991 bytes, under 15,000.
- The build is clean, and `gofmt -l` prints nothing.

## Task 4: Verification, docs and changelog

**Goal:** the phase is verified by the suite and in a real session, and the docs describe what shipped.

**Files:** `docs/tui-simplification/feature-spec.md`, `docs/live-work/examples/README.md`, `docs/tools/execution.md`, `CHANGELOG.md`.

**Steps:**

1. **Full verification.**

   ```bash
   cd /Users/alecpullen/projects/marshal
   TMPDIR=/private/tmp/pd-suite CGO_ENABLED=1 go test ./... 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
   go vet ./... && gofmt -l .
   ```

   Expected: every failure is in the Assumptions list, minus `TestDefaultPromptBudget`, which now passes. vet is clean, and `gofmt -l .` prints nothing.
2. **A real session.**
   - Build with `CGO_ENABLED=1 go build -o /private/tmp/pd-suite/marshal ./cmd/marshal`.
   - In a scratch git repository, serve a file of about 200 MiB with `python3 -m http.server 8765`.
   - Start the built binary and ask: "Download http://127.0.0.1:8765/<file> to data.bin and show me its progress."
   - Confirm:
     - the agent loads `progress-reporting` (one `skill.load` row);
     - it writes `.marshal/progress/marshal_progress.py` and `download.py` with `file.write`;
     - it runs the download with `background` and profile `download`, source `events-v1`;
     - the job's row in the live box shows a bar, a percentage, the byte count in IEC units and a rate;
     - expanding that row (click it, or press Esc to browse, move to it and press Enter) shows the full-width bar, and the destination path as its detail;
     - the job ends with a completion event whose measurement is the final byte count.
   - Record what was seen in the commit message. If the agent took another route, record the route and the prompt, and do not change the skill in this task.
   - Delete the binary afterwards.
3. **Spec.** Set the status line to say P-D landed, with this plan's commit range.
4. **`docs/live-work/examples/README.md`.** Add a closing paragraph. The built-in `progress-reporting` skill embeds self-contained copies of these producers, with the helper renamed `marshal_progress.py`, so the agent can write them into any project's `.marshal/progress/`. A change to one copy belongs in the other too.
5. **`docs/tools/execution.md`.** Add a `progress` row to `shell.run`'s argument table: not required; `{profile, source, title}` opts the run into a live progress card; the `progress-reporting` skill documents the record format.
6. **CHANGELOG,** under the unreleased section. Each group there is a bold title line followed by bullets; P-C's `**The live box**` group under Changed is an example. Add one group at the top of each section:
   - **Added**, group title `**Progress-reporting skill**`:
     - The agent can now show a measured progress card for long work. The built-in `progress-reporting` skill teaches `shell.run`'s `progress` option and the `@@marshal-progress/v1` record format.
     - It carries ready-to-write producers for downloads, SSH transfers and phased jobs, written to `.marshal/progress/`. Go tests need no producer: `go test -json` with profile `tests`.
   - **Changed**, group title `**Room for the progress-reporting skill**`:
     - The postmortem skill's description is shorter, and `shell.run`'s description no longer mentions its guardrails, which still apply. Together they make room for the new skill within the prompt budgets, and the default system prompt is back under its ceiling.
     - New projects' `.marshal/.gitignore` also ignores `progress/`.

7. **Commit** `CHANGELOG.md`, `docs/tui-simplification/`, `docs/live-work/examples/README.md` and `docs/tools/execution.md` as explicit paths. Merge `p-d-progress-skill` only when the user asks.

**Verify:** step 1's commands. `git status --short` shows only the unrelated untracked docs that were there before.

## Integration notes

- **P-C has landed** (merged as `e667a100`), so nothing runs alongside P-D. P-D changes no file P-C touched apart from `CHANGELOG.md` and the spec.
- **The budgets are close to full.** After P-D there are about 13 bytes left in the system prompt and 9 in the tool schemas. The next addition to either needs its own room.
- **The live box draws what these producers report.** The anchors are in `internal/app/tui/livebox/running.go`:
  - `fractionText` draws the bar, phase prefix, IEC bytes, ETA and rate;
  - `metricText` draws a count with no total;
  - `cardDetail` draws `detail`, else the checklist;
  - `goCounts` draws the go test JSON counts.

  The rate is the record's own `rate` when given (`progress.estimateRate`), and staleness is `progress.FreshFor` (30 s). The completion event's measurement and summary come from `viewmodel.MeasureSummary` and `jobSummary`. If any of these change before P-D runs, re-check body section 5 against them.

## Self-review

- **Cold-executor test.** Every string a test pins is in Contracts. The skill body's sections, rules and order are stated, and each producer's behaviour is a row in a table with the example it ports. The two judgment steps are the body's wording and the real session, and both have a checklist.
- **Contracts.** The postmortem description is used by Task 1. The skill frontmatter, the producers and the ignore entry are used by Task 2, and the pointer by Task 3.
- **Anchors**, all checked at `d7d9b39c` and again at `e667a100`, plus the live-box anchors in Integration notes:
  - `builtin.go`'s embed list;
  - `TestBuiltInSkillsAllLoad` (12 names) and `TestBuiltInSkillsAreRepoNeutral`'s banned list;
  - `marshalDirIgnore` and `TestEnsureMarshalDirIgnoredCreatesNestedIgnore`;
  - `shellRunTool` and `testRunTool`'s schemas, and `test.run`'s `Internal: true`;
  - `TestShellToolTextDocumentsWaitContinuationAndStop`, and `native_test.go:155`'s pinned phrases;
  - `TestDefaultPromptBudget`'s ceilings and how it counts;
  - the listing line format in `internal/agent/prompts.go` (`` - `name` — description ``);
  - `progress.Prefix`, `progress.DecodeEvent`, the frame limits in `internal/progress/progress.go`, and the four example scripts.
- **Scope.** Four small tasks. Nothing in the TUI, the progress parser or the system prompt text changes. The only behaviour change outside the new skill is two shortened descriptions.
- **Parallel safety.** No task is marked parallel-safe.
- **Placeholders.** None.
- **Agreement with the spec.** §6.4 records decisions 1 to 7, and §8's skill bullet lists the tests.
