# Marshal — TUI Design

**Version** 0.2  
**Date** March 2026  
**Palette** Forge  


---


# 1. Visual Design Language


## 1.1 Palette — Forge


The Marshal TUI uses a palette called Forge, derived from GitHub's dark theme. All terminal colours reference these tokens.


| Token | Hex | Usage |
| --- | --- | --- |
| `--bg` | `#0D1117` | Primary background — main content areas, sidebar |
| `--bg2` | `#161B22` | Secondary — titlebar, topbar, statusbar, active sidebar item |
| `--bg3` | `#21262D` | Tertiary — progress bar track, inactive round dots, code blocks |
| `--br` | `#30363D` | Primary border — screen edges, sidebar divider, panel separators |
| `--br2` | `#21262D` | Secondary border — row separators, minor dividers |
| `--br3` | `#484F58` | Emphasis border — focused input outlines |
| `--tx` | `#E6EDF3` | Primary text — headings, active items, important values |
| `--tx2` | `#8B949E` | Secondary text — body content, sidebar inactive items, log lines |
| `--tx3` | `#484F58` | Tertiary — labels, metadata, timestamps, keyboard hints, dimmed output |
| `--bl` | `#388BFD` | Blue — executor agent, active/focused state, info |
| `--gr` | `#3FB950` | Green — pass verdict, test success, committed |
| `--rd` | `#F85149` | Red — fail verdict, error, revert |
| `--am` | `#D29922` | Amber — running/in-progress, warnings |
| `--pu` | `#A78BFA` | Purple — critic agent, think-blocks, R1 content |


## 1.2 Typography


All text uses the system monospace font throughout. Marshal is a coding tool — monospace creates visual consistency with diffs, file paths, and code output.


| Size | Usage |
| --- | --- |
| 13px / 500 | Task description in topbar, prompt input, launch logo |
| 12px / 400 | Sidebar items, log output, config values, session table |
| 11px / 400 | Topbar metadata, statusbar, file chips, option labels, keyboard hints |
| 10px / 400 | Section headers (uppercase + tracked), column headers, log prefixes |


## 1.3 Layout constants


| Element | Value | Rationale |
| --- | --- | --- |
| Sidebar width | 196px | Wide enough for session IDs + status dots without truncation. |
| Info panel width | 192px | Mirrors sidebar for visual balance. Fixed so streaming log gets remaining space. |
| Active sidebar accent | 2px left border in `--bl` | The only 2px border in the system. Sole accent mechanism. |
| Border thickness | 0.5px | Thin borders throughout. 2px only for the active sidebar accent. |
| Statusbar | Always visible, 4 segments max | Persistent orientation layer. Never scrolls. Never hides. |


---


# 2. Screen Inventory


| Screen | Trigger | Sidebar item |
| --- | --- | --- |
| Launch / cold start | `marshal` with no active session | run task (active) |
| Prompt bar | Any keypress from launch screen | run task (active) |
| Task composer | Tab from prompt bar | run task (active) |
| Loop running | Task submitted | session ID (active, amber dot) |
| Loop complete | Pass or fail verdict | session ID (active, green/red dot) |
| Session browser | `s` key or sidebar | sessions (active) |
| Config view | `c` key or sidebar | config (active) |


---


# 3. Input Screens


## 3.1 Launch screen


| Element | Content |
| --- | --- |
| "marshal" logo | `--tx`, 28px. Small and unobtrusive. |
| Subtitle | Current directory in `--tx3`. |
| Input bar | Full-width, focused (`--br3` border), placeholder in `--tx3`. Blinking block cursor (`--bl`). |
| Status tiles | Executor model, critic model, max rounds, branch setting. Executor in `--bl`, critic in `--pu`. |
| Recent tasks | Last 4 tasks: numbered shortcuts, pass/fail badge, age. Fail sessions in `--rd`. |
| Sidebar | Endpoint health instead of session list. Green dot = reachable. Red dot = unreachable (blocking). |
| Statusbar | "both endpoints reachable" · branch · session count |


## 3.2 Prompt bar


| Element | Content |
| --- | --- |
| Prompt line | Single-line input with `›` prefix in `--bl`. Block cursor. |
| Hint row | `↵ run · ↑↓ history · tab expand · esc cancel` |
| Recent tasks | Inline below prompt. `↑↓` navigates and fills input. `↑` from empty cycles history. |


## 3.3 Task composer (tab expansion)


| Element | Content |
| --- | --- |
| Task area | Multi-line editor, `--br3` border when focused. Free-form task description. |
| File context | Chips row. Language tag + path. `×` to remove. `+ add file` opens fuzzy picker. |
| Options row | max rounds · branch isolation · auto commit · dry run. `ctrl+↵` submits. |
| Run button | "run task ↵" right-aligned. `--bl` background. |
| Statusbar | "N files pinned · ~N tok context" · branch |


> **Composer is opt-in**
>
> Tab from prompt bar to open. Text is preserved across transitions. `esc` from composer returns to prompt bar with text intact. `esc` from empty prompt bar returns to launch screen.


## 3.4 Input screen state machine


```
launch screen
    │
    │  any character keypress
    ▼
prompt bar ◄──────────────── esc (composer was open)
    │                              │
    │  tab                         │
    ▼                              │
task composer ────────────────────►│
    │
    │  ↵ (prompt) or ctrl+↵ (composer)
    ▼
loop running
```


---


# 4. Loop Screens


## 4.1 Loop running


| Element | Content |
| --- | --- |
| Topbar | Agent badge (`--bl`=executor, `--pu`=critic) · "round N / N" · task description · token count right-aligned |
| Log panel | Scrolling chronological output. See Section 4.3. |
| Info panel | Fixed 192px right panel. See Section 4.4. |
| Statusbar | Current round · active agent · right: branch name · files changed |


## 4.2 Loop complete


| Element | Content |
| --- | --- |
| Topbar badge | Green "pass" or red "fail". Round count. Task description. |
| Log panel | Verdict → merge/revert narration → commit SHA (pass) or revert confirmation (fail). Last line: "done. press r to run a new task, s to browse sessions." |
| Info panel | Result · rounds dots · commit SHA or "reverted" · files changed · total tokens · cache % · duration |
| Statusbar | Pass: "pass · committed SHA" · `main`. Fail: "fail · reverted" · `main`. |


## 4.3 Log rendering rules


| Prefix | Colour | Content type |
| --- | --- | --- |
| `exec` | `--bl` (#388BFD) | First line of each executor response |
| `critic` | `--pu` (#A78BFA) | First line of each critic response |
| (blank) | `--tx3` (#484F58) | Continuation lines, code snippets, diff lines |
| (blank) | `--gr` (#3FB950) | Test pass lines, success confirmations (✓ prefix) |
| (blank) | `--rd` (#F85149) | Error lines, test failures |
| (blank) | `--am` (#D29922) | Warnings, token budget warnings |


Additional log conventions:


- A thin separator (0.5px) is inserted between phases: before executor call, between executor and critic, before verdict.
- Previous round verdict shown at top of log in dimmed colour when a retry begins.
- Think-blocks from R1: italic purple, 11px, prefixed "think". Phase 1: always visible. Phase 2: collapsible with T.
- Cursor: blinking block on the last streaming token. Disappears when streaming ends.
- Diff lines are dimmed to subordinate them to model output — they are context, not the primary message.


## 4.4 Info panel by state


| Field | Running | Complete |
| --- | --- | --- |
| Status | Amber ● running | Green ● pass  or  Red ● fail |
| Round | Dots: filled=done, pulsing=active, empty=remaining | Dots: filled=done, empty=unused |
| Executor | Model name in `--bl` | Model name in `--bl` |
| Critic | Model name in `--pu` | Model name in `--pu` |
| Tokens | Running count + progress bar + "N% of Nk · N cached" | Total + "N% cached" |
| Commit | (not shown) | SHA in `--bl` (pass) or "reverted" in `--rd` (fail) |
| Branch | `marshal/task-ID` in `--tx2` | "merged + deleted" in `--tx3` (pass) |
| Elapsed | Live MM:SS | Final MM:SS |


---


# 5. Session Browser


## 5.1 Table layout


| Column | Width | Content |
| --- | --- | --- |
| id | 68px | Last 4 chars of session ID. Blue=selected, red=failed, `--tx3`=others. |
| task | 1fr | Task description. Truncated with ellipsis. |
| result | 52px | Pass/fail/running badge. Green/red/amber background. |
| rounds | 48px | Round count (not N/max). `--tx3`. |
| tokens | 72px | Total token count, right-aligned. `--tx3`. |
| age | 56px | Human-readable age (2h, 1d). Right-aligned. `--tx3`. |


## 5.2 Expanded session view


Enter on a selected row expands it inline. Each round shows:


- Round header: number · verdict badge · token count · cached tokens
- Executor block: `--bg2` background, `--tx2` text
- Critic block: `--bg2` background. Left border: `--rd` 2px (fail) or `--gr` 2px (pass)


## 5.3 Diff viewer


- Accessed with D from session browser
- Additions: green tint + `+` prefix
- Deletions: red tint + `−` prefix
- Context lines: neutral, line numbers in `--tx3`
- Q or Escape exits, returning to session browser at same scroll position


---


# 6. Config View


| Element | Content |
| --- | --- |
| Section headers | `--bg2` background rows. Section name in `--tx3` uppercase. |
| Key column | 140px. Key names in `--tx3`. |
| Value column | Model names in `--bl` (executor) or `--pu` (critic) · `true` in `--gr` · thresholds in `--am` · resolved env vars as "$VAR ✓" in `--tx3` |
| Statusbar | "config valid" · "both endpoints reachable" · filename right-aligned |


> **Read-only by design**
>
> The config view shows what is loaded, including resolved env vars, without leaving the terminal. Editing is done in `marshal.toml` directly — Marshal is not a TOML editor.


---


# 7. Keyboard Map


| Key | Action |
| --- | --- |
| r | Go to run task screen (prompt bar) |
| s | Go to session browser |
| c | Go to config view |
| q | Quit marshal |
| ? | Help overlay |
| ↑↓ | Navigate lists |
| ↵ | Confirm / run / expand |
| esc | Cancel / go back one level |
| tab | From prompt bar: expand to composer |
| d | From session browser: diff viewer for selected session |
| T | Phase 2: toggle think-block panel |


---


# 8. Resolved TUI Design Decisions


| Decision | Resolution | Rationale |
| --- | --- | --- |
| Palette | Forge (GitHub dark) | Minimises eye strain. Status colours carry unambiguous meaning. |
| Typography | Monospace throughout | Relay is a coding tool. Consistency between UI text and code output. |
| Input flow | Launch → prompt bar → composer (tab) | Default path is minimal. Composer is opt-in. |
| Sidebar active marker | 2px left border in `--bl` | Single accent mechanism. No heavy background change. |
| Think-block rendering | Italic `--pu`, dimmed, above verdict | Visible but subordinate. Reasoning is context, not primary output. |
| Session expansion | Inline (rows shift down) | Keeps table context visible. No navigation required. |
| Config view | Read-only | Marshal is not a TOML editor. |
| Endpoint health | Shown on launch screen | Discovers connectivity problems before a branch is created. |
| Diff viewer | Full-screen, Q to exit | Diffs need space. |
| Statusbar | Always visible, 4 segments max | Persistent orientation. Never scrolls. |
