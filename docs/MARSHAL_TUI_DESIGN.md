# Marshal — TUI Design

**Version** 0.3 — Continuous scroll workflow  
**Date** April 2026  
**Palette** Forge  

> **Changes in v0.3**
>
> Complete workflow redesign. Replaced the discrete screen model (launch → prompt bar → loop running → loop complete → session browser) with a single continuous-scroll main panel modelled on Claude Code's interaction style. The sidebar becomes a slim 16-char task timeline showing queue and history state. The session browser is removed as a separate screen — history is always visible in the scroll. Prompt bar is permanently anchored at the bottom of the main panel. Task composer becomes a modal overlay.

---

# 1. Visual Design Language

## 1.1 Palette — Forge

The Marshal TUI uses a palette called Forge, derived from GitHub's dark theme. All terminal colours reference these tokens.

| Token | Hex | Usage |
| --- | --- | --- |
| `--bg` | `#0D1117` | Primary background — main panel, sidebar |
| `--bg2` | `#161B22` | Secondary — task block headers, statusbar, prompt bar background |
| `--bg3` | `#21262D` | Tertiary — progress bar track, inactive round dots, think-block background |
| `--br` | `#30363D` | Primary border — panel divider, task block borders |
| `--br2` | `#21262D` | Secondary border — round separators, minor dividers |
| `--br3` | `#484F58` | Emphasis border — focused prompt input outline |
| `--tx` | `#E6EDF3` | Primary text — task headers, active sidebar items, verdicts |
| `--tx2` | `#8B949E` | Secondary text — log output, sidebar inactive items |
| `--tx3` | `#484F58` | Tertiary — prefixes, timestamps, hints, metadata |
| `--bl` | `#388BFD` | Blue — executor agent, prompt prefix, focused state |
| `--gr` | `#3FB950` | Green — pass verdict, commit confirmation |
| `--rd` | `#F85149` | Red — fail verdict, revert confirmation |
| `--am` | `#D29922` | Amber — running state, warnings, queued indicator |
| `--pu` | `#A78BFA` | Purple — critic agent, think-blocks |

## 1.2 Typography

All text uses the system monospace font. Marshal is a coding tool — monospace creates visual consistency between UI chrome and code/diff output.

| Size | Usage |
| --- | --- |
| 13px / 500 | Prompt input text, task block header descriptions |
| 12px / 400 | Log output lines, sidebar items |
| 11px / 400 | Statusbar, keyboard hints, metadata, log prefixes |
| 10px / 400 | Section labels (uppercase), round separators |

## 1.3 Layout constants

| Element | Value | Rationale |
| --- | --- | --- |
| Sidebar width | 28 chars | Enough for truncated task name + status dot plus file tree. Maximises main panel space while remaining functional. |
| Sidebar active accent | 2px left border in `--bl` | Single accent mechanism across the whole UI. |
| Border thickness | 1 char (box-drawing) | Standard terminal border. |
| Statusbar | Always visible, 4 segments max | Persistent orientation. Never scrolls. |
| Prompt bar | Always visible at bottom of main panel | No navigation required to submit a task. |
| Task block | Bordered section in scroll | Each task is visually self-contained within the continuous scroll. |

---

# 2. Overall Layout

## 2.1 ASCII wireframe — active session

```
┌────────────────┬─────────────────────────────────────────────┐
│ ● add venue c… │                                             │
│   pass  r1     │  ╔ add venue column to events table ══════╗ │
│                │  ║                                         ║ │
│ ● fix auth b…  │  ║ exec  writing migration file...         ║ │
│   fail  r3     │  ║       ALTER TABLE events ADD COLUMN ... ║ │
│                │  ║       ─────────────────────────────     ║ │
│ ─────────────  │  ║ critic reviewing diff...                ║ │
│                │  ║       PASS — migration correct,         ║ │
│ ◎ add staff p… │  ║       index on foreign key present      ║ │
│   running  r1  │  ╚═══════════════════════════ pass ════════╝ │
│                │                                             │
│ ○ add input v… │  ╔ add staff portal with timesheets ══════╗ │
│   queued       │  ║                                         ║ │
│                │  ║ exec  scaffolding components...         ║ │
│                │  ║       creating StaffLayout.tsx          ║ │
│                │  ║       creating TimesheetView.tsx        ║ │
│                │  ║       █ (streaming)                     ║ │
│                │  ║                                         ║ │
│                │  › add input validation to the event fo_   │
└────────────────┴─────────────────────────────────────────────┘
│ round 1 · exec · marshal/task-def456 · 3 files changed      │
└─────────────────────────────────────────────────────────────-┘
```

## 2.2 ASCII wireframe — composer overlay

```
┌────────────────┬─────────────────────────────────────────────┐
│ ● add venue c… │                                        ░░░░ │
│   pass  r1     │  ╔ add venue column to events table ══╗░░░░ │
│                │  ║ ...previous output...               ║░░░░ │
│ ◎ add staff p… │  ╚═════════════════════════ pass ══════╝░░░░ │
│   running  r1  │  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │
│                │  ░ ┌─────────────────────────────────┐ ░░░░ │
│                │  ░ │ task                            │ ░░░░ │
│                │  ░ │ add input validation to the     │ ░░░░ │
│                │  ░ │ event form fields, ensure all   │ ░░░░ │
│                │  ░ │ required fields show errors_    │ ░░░░ │
│                │  ░ ├─────────────────────────────────┤ ░░░░ │
│                │  ░ │ 📎 src/components/EventForm.tsx │ ░░░░ │
│                │  ░ ├─────────────────────────────────┤ ░░░░ │
│                │  ░ │ rounds 3  branch ✓  commit ✓    │ ░░░░ │
│                │  ░ │               [run task  ↵]     │ ░░░░ │
│                │  ░ └─────────────────────────────────┘ ░░░░ │
│                │  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │
└────────────────┴─────────────────────────────────────────────┘
│ 1 file pinned · ~2.1k tok context · marshal/task-def456      │
└──────────────────────────────────────────────────────────────┘
```

## 2.3 Panel responsibilities

| Panel | Width | Scrolls | Purpose |
| --- | --- | --- | --- |
| Sidebar | 28 chars + 1 border | Vertically if many tasks | Task timeline: history, running, queued plus file tree |
| Main panel | Remaining width | Vertically (continuous) | All task output, streaming log, prompt bar |
| Statusbar | Full width | Never | Persistent orientation — round, agent, branch, files |

---

# 3. Sidebar — Task Timeline

The sidebar is a read-only vertical timeline. It does not require interaction for normal use. Tasks appear in submission order, oldest at top. The currently running task is always visible (sidebar auto-scrolls to keep it in view).

## 3.1 Task row anatomy

Each task occupies two lines in the sidebar:

```
● add venue column to events table…    ← line 1: dot + truncated description (26 chars max)
  pass  r1                             ← line 2: status word + round count
```

| State | Dot | Dot colour | Status word | Line 2 colour |
| --- | --- | --- | --- | --- |
| Queued | ○ | `--tx3` | queued | `--tx3` |
| Running | ◎ | `--am` (pulse) | running  rN | `--am` |
| Pass | ● | `--gr` | pass  rN | `--tx3` |
| Fail | ● | `--rd` | fail  rN | `--rd` |

The active sidebar accent (2px left border in `--bl`) marks whichever task is currently visible/active in the main panel scroll position — not necessarily the running task.

A thin horizontal rule (`──────────────`) separates the history block (completed tasks) from the live block (running + queued tasks).

## 3.2 Queue representation

When a task is queued, it appears immediately in the sidebar as a ○ row below the running task. Multiple queued tasks stack in submission order. When the running task completes, the next queued task transitions to ◎ running and Marshal starts its loop automatically.

```
◎ add staff p…     ← currently running
  running  r2

○ add input v…     ← queued, will start next
  queued

○ write tests f…   ← queued, will start after
  queued
```

## 3.3 Sidebar keyboard interaction

| Key | Action |
| --- | --- |
| `↑` / `↓` | Move sidebar focus (highlights a task row) |
| `↵` | Scroll main panel to that task's block |
| `d` | Open diff viewer for focused completed task |
| `esc` | Return focus to main panel / prompt bar |

Sidebar focus is opt-in — the default state is main panel focus. The sidebar never steals input from the prompt bar.

---

# 4. Main Panel — Continuous Scroll

The main panel is a single vertically scrolling surface. Task blocks are appended to the bottom as tasks are submitted. The view auto-scrolls to follow streaming output. The user can scroll up freely to review history; auto-scroll resumes on the next keystroke that is not a scroll key.

## 4.1 Task block structure

Each task is enclosed in a bordered block that becomes part of the permanent scroll history.

```
╔ <task description> ══════════════════════════════╗
║                                                   ║
║  <round 1 output>                                 ║
║  ─────────────────────────────────────────────    ║ ← round separator
║  <round 2 output if retry>                        ║
║                                                   ║
╚══════════════════════════════ <verdict badge> ════╝
```

- **Block header**: Task description in `--tx` on `--bg2` background. Left-padded. Double-line box top border.
- **Block body**: Log output lines (see Section 4.2). Background `--bg`.
- **Block footer**: Double-line box bottom border. Verdict badge right-aligned: green "pass" or red "fail" badge, or amber "running" if active.
- **Active block**: The currently running task block has no footer yet — it grows downward as output streams in.

## 4.2 Log line rendering

| Prefix (6 chars) | Colour | Content |
| --- | --- | --- |
| `exec  ` | `--bl` | First line of executor response for this chunk |
| `critic` | `--pu` | First line of critic response |
| (blank) | `--tx3` | Continuation lines, code, diff context |
| (blank) | `--gr` | Lines starting with ✓ — success confirmations |
| (blank) | `--rd` | Lines starting with ✗ — errors, test failures |
| (blank) | `--am` | Warnings, token budget warnings |

Additional conventions:

- Round separators are a dim horizontal rule (`──────────` in `--br2`) spanning the log width, with a centred label: `round 2` in `--tx3`.
- Think-blocks are rendered inline in italic `--pu` on `--bg3` background, prefixed with a dim `think` label. They appear before the verdict in the critic section.
- The streaming cursor is a blinking block █ in `--bl` on the last token of the active line. It disappears when streaming ends.
- Diff lines use `--tx3` to visually subordinate them — they are context, not the primary message.

## 4.3 Prompt bar

The prompt bar is permanently anchored to the bottom of the main panel, above the statusbar. It is always visible and always accepts input regardless of scroll position.

```
  › add input validation to the event form fields_
    ↵ run · tab expand · ↑↓ history · esc clear
```

| Element | Detail |
| --- | --- |
| Prefix `›` | `--bl`, always shown |
| Input text | `--tx`, 13px. Single line. |
| Border | `--br3` outline when focused (always focused unless composer is open) |
| Hint row | `--tx3`, 11px. Shown when input is empty. Hidden when typing. |
| History | `↑` / `↓` cycles previous task descriptions into the input. |

When a task is running, submitting a new task via `↵` immediately queues it (adds it to sidebar as ○ queued) and appends a dim "queued: <description>" line to the main panel scroll so the user has confirmation. The prompt bar then clears and is ready for another submission.

## 4.4 Scroll behaviour

| Condition | Scroll behaviour |
| --- | --- |
| Task streaming | Auto-scroll to bottom, following output |
| User presses `↑` | Auto-scroll pauses. Scroll indicator appears in `--tx3`: "↑ scrolled — any key to resume" |
| User presses non-scroll key | Auto-scroll resumes |
| Sidebar `↵` on a task | Main panel jumps to that task's block header |
| Task completes | If user was at bottom: stays at bottom. If scrolled up: a notification dot appears in statusbar. |

---

# 5. Task Composer — Modal Overlay

The task composer is an optional expansion of the prompt bar, opened with `tab`. It overlays the main panel (blurs background content with `░` fill) and closes back to the prompt bar with `esc` — text is preserved.

## 5.1 Composer elements

| Element | Detail |
| --- | --- |
| Task text area | Multi-line editor. `--br3` border when focused. Free-form task description. |
| File chips row | Pinned context files. Language tag + truncated path. `×` to remove. `+ add` opens fuzzy picker. |
| Options row | max rounds · branch isolation · auto commit · dry run. Toggles with `space`. |
| Run button | `[run task  ↵]` right-aligned. `--bl` background when active. |

## 5.2 Composer statusbar

While the composer is open the statusbar shows context information rather than session state:

```
│ 1 file pinned · ~2.1k tok context · marshal/task-def456     │
```

## 5.3 Composer keyboard

| Key | Action |
| --- | --- |
| `tab` from prompt bar | Open composer with current prompt text pre-filled |
| `esc` | Close composer, return text to prompt bar |
| `ctrl+↵` | Submit task from composer |
| `ctrl+f` | Open fuzzy file picker to pin context |
| `space` on option | Toggle option |

---

# 6. Diff Viewer

The diff viewer is a full-screen overlay, accessible from the sidebar with `d` on any completed task.

```
┌─ diff: add venue column to events table ─────────────────────┐
│                                                              │
│  prisma/schema.prisma                                        │
│  ─────────────────────────────────────────────────────────  │
│   12   model Event {                                         │
│   13     id        String   @id                             │
│ + 14     venueId   String?                                  │  ← green
│ + 15     venue     Venue?   @relation(...)                  │  ← green
│   16     name      String                                   │
│                                                              │
│  prisma/migrations/20260409_add_venue.sql                    │
│  ─────────────────────────────────────────────────────────  │
│ + 1    ALTER TABLE "Event" ADD COLUMN "venueId" TEXT;       │  ← green
│ + 2    CREATE INDEX ...                                      │  ← green
│                                                              │
│  q / esc  close · ↑↓  scroll · tab  next file              │
└──────────────────────────────────────────────────────────────┘
```

| Element | Detail |
| --- | --- |
| Title bar | Task description. `--bg2` background. |
| File headers | Filename in `--tx`. Thin rule below. |
| Addition lines | `--gr` foreground. `+` prefix. |
| Deletion lines | `--rd` foreground. `−` prefix. |
| Context lines | `--tx3` foreground. Line numbers in `--tx3`. |
| Hint bar | `--tx3`, fixed at bottom. |

`q` or `esc` closes and returns to main panel at the same scroll position.

---

# 7. Config View

Config view is a full-screen overlay, opened with `c`. Read-only.

| Element | Detail |
| --- | --- |
| Section headers | `--bg2` background rows. Section name in `--tx3` uppercase. |
| Key column | 14 chars. Key names in `--tx3`. |
| Value column | Model names in `--bl` (executor) or `--pu` (critic) · `true` in `--gr` · thresholds in `--am` · resolved env vars as `$VAR ✓` in `--tx3` |
| Statusbar | "config valid" · "both endpoints reachable" · filename right-aligned |

---

# 8. Statusbar

The statusbar is always visible at the very bottom of the terminal. It never scrolls and never hides. Maximum 4 segments separated by `·`.

| Screen state | Segments |
| --- | --- |
| Idle (no running task) | "ready" · branch · session count |
| Task running | round N · agent (coloured) · branch · files changed |
| Task complete (pass) | "pass · committed SHA7" · branch |
| Task complete (fail) | "fail · reverted" · branch |
| Composer open | files pinned · token estimate · branch |
| Config open | "config valid" · "N endpoints reachable" · filename |
| Scrolled up during run | round N · agent · "↑ scrolled" · branch |

New activity indicator: when the user is scrolled up and a task completes or a new streaming line arrives, a `●` dot in `--am` appears in the rightmost statusbar segment as a non-intrusive nudge.

---

# 9. Keyboard Map

| Key | Context | Action |
| --- | --- | --- |
| `↵` | Prompt bar | Submit task (or queue if one is running) |
| `tab` | Prompt bar | Open task composer |
| `↑` / `↓` | Prompt bar | Cycle input history |
| `esc` | Prompt bar | Clear input |
| `ctrl+↵` | Composer | Submit task |
| `esc` | Composer | Close, return text to prompt bar |
| `ctrl+f` | Composer | Open fuzzy file picker |
| `c` | Any (not running) | Open config overlay |
| `d` | Sidebar focused | Open diff viewer for focused task |
| `q` / `esc` | Diff / config overlay | Close overlay |
| `↑` / `↓` | Main panel | Scroll (pauses auto-scroll) |
| `↑` / `↓` | Sidebar focused | Move sidebar focus |
| `↵` | Sidebar focused | Jump main panel to that task block |
| `T` | Any | Phase 2: toggle think-block visibility |
| `ctrl+c` | Any | Quit (prompts if task is running) |
| `?` | Any | Help overlay |

---

# 10. State Machine

```
                    ┌─────────────────────────────────┐
                    │         main panel               │
                    │  (continuous scroll, always on)  │
                    └───────────┬─────────────────────┘
                                │
              ┌─────────────────┼──────────────────┐
              │                 │                  │
              ▼                 ▼                  ▼
         prompt bar          tab key            ↵ submit
         (default)            │                   │
              │               ▼                   ▼
              │          composer             task queued
              │          overlay              or started
              │               │                   │
              │          ctrl+↵                   │
              │               │                   ▼
              └───────────────┘            loop running
                                                  │
                                    ┌─────────────┴──────────────┐
                                    │                            │
                                    ▼                            ▼
                               PASS verdict               FAIL verdict
                               commit + done              revert + done
                                    │                            │
                                    └──────────┬─────────────────┘
                                               │
                                               ▼
                                     next queued task starts
                                     (or idle if queue empty)
```

Overlays (composer, diff viewer, config) sit on top of this state machine and do not interrupt it. A task can be running in the background while the composer overlay is open.

---

# 11. Resolved Design Decisions

| Decision | Resolution | Rationale |
| --- | --- | --- |
| Single scroll vs screen-per-state | Single continuous scroll | Matches developer mental model of a coding session. History is always visible without navigation. |
| Prompt bar position | Permanently anchored at bottom of main panel | No navigation required to submit. Always ready. |
| Queue behaviour | New tasks queue immediately, confirmed in sidebar and scroll | User gets instant feedback. Marshal never blocks input. |
| Sidebar width | 28 chars | Enough for truncated description + dot + status plus file tree. Balances main panel space with usability. |
| Sidebar role | Read-only timeline + jump target | Sidebar does not steal focus. Interaction is opt-in. |
| Task composer | Modal overlay, not a separate screen | Composer does not interrupt a running task. Tab in, esc out. |
| Diff viewer | Full-screen overlay | Diffs need space. Overlay keeps context of which task you're reviewing. |
| Config view | Full-screen overlay | Same rationale. Read-only, so no risk of accidental edits. |
| Auto-scroll pause | Any scroll key pauses; any non-scroll key resumes | Standard terminal convention. Non-intrusive. |
| Activity indicator | Amber dot in statusbar when scrolled up and new output arrives | Visible without being modal or disruptive. |
| Session browser | Removed as separate screen | The continuous scroll is the session browser. Diff viewer covers the detailed per-task view. |
| Launch screen | Removed | Empty main panel with prompt bar is sufficient. No ceremony on startup. |
| Think-blocks | Inline in log, italic purple, collapsible Phase 2 | Visible but subordinate. Reasoning is context, not primary output. |
