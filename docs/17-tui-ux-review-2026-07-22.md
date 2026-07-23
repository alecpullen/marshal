# TUI UX Review — 2026-07-22

Scope: settings UX, provider connection flows, multi-model workflow (profiles /
SDD / swarm) configuration, setting naming friendliness, plus general TUI
findings. Reviewed against the tui-design guidance (progressive disclosure,
semantic color, contextual footers, discoverability).

Files reviewed: `internal/app/tui/settings/*`, `internal/app/tui/connect/*`,
`internal/app/tui/commands_dispatch.go`, `internal/app/tui/model.go` (dispatch,
pickers, status), `internal/app/tui/help/help.go`, `internal/app/tui/status.go`,
`internal/commands/*`, `internal/app/onboarding.go`, `internal/app/config/types.go`,
`internal/llm/routing/types.go`.

Finding IDs use `F-UX-2xx`.

---

## 1. Multi-model workflow configuration (the biggest gap)

### F-UX-201 — Agent profiles have no TUI editor at all  `P1` — implemented (Phase 1)
`AgentProfiles` is the heart of the multi-model story: a profile maps 14
`AgentRole`s (router, planner, implementer, reviewer, `sdd_implementer`,
`sdd_reviewer`, `sdd_branch_reviewer`, …) to model presets. The only TUI
surface is the read-only `agent.default_profile` enum
(`settings/frames_agent.go:57`) that picks among profiles that must already
exist in TOML. There is no way to create a profile, rename one, or assign a
preset to a role without hand-editing `config.toml`.

**Suggestion:** add a **Profiles** section to `sectionList()` mirroring the
presets collection pattern (`entriesDrillExt`): profiles list → drill into a
profile → one `kindPicker` row per role choosing a preset. Group the rows
visually (Core: router/planner/implementer/reviewer · Support:
knowledge/summarizer/repo_scout/tester/title/subtask · SDD: the three sdd
roles · Security: security_reviewer). Each row's summary should show the
*resolved* provider/model (`qwen-large → ollama/qwen3:32b`), with an
"(unset — falls back to implementer/default)" state instead of a blank.

### F-UX-202 — SDD settings are not in the settings UI  `P1` — implemented (Phase 1)
`SDDConfig` (`auto_worktree`, `max_fix_rounds`, `plans_dir`) has no frame in
`sectionList()` and therefore is also unreachable via `/set`. Swarm got a
frame; SDD didn't.

**Suggestion:** add an SDD frame (or fold SDD + Swarm into one "Workflows"
section) with the three fields plus a drill to the SDD role assignments from
F-UX-201.

### F-UX-203 — `/sdd` bare-invocation dead-ends instead of opening the plan picker  `P2` — implemented (Phase 2)
`/mode` → SDD opens `openSDDPlanPicker()`; bare `/sdd` prints
`Usage: /sdd <plan-file>` (`commands_dispatch.go:107`). Two entry points to
the same workflow behave differently; the one users will try first is the
worse one.

**Suggestion:** bare `/sdd` should open the same plan picker. Keep the arg
form for power users.

### F-UX-204 — No pre-flight "cast list" before an SDD/swarm run  `P2` — implemented (Phase 2)
A multi-model run silently resolves roles → presets → providers at start.
`/route` shows only the single active route. If a role resolves to a
misconfigured preset you find out mid-run.

**Suggestion:** before starting `/sdd` or `/swarm`, show a compact
confirmation panel: plan file, each participating role with resolved
provider/model, budget caps (fix rounds, tokens), worktree yes/no. Enter to
start, Esc to cancel. Also makes the multi-model behavior *visible*, which is
the product's differentiator.

### F-UX-205 — Agent section silently writes through to a preset  `P2` — implemented (Phase 2)
`agent.provider` / `agent.model` setters write into the default profile's
implementer preset when one exists, else into legacy `cfg.Agent`
(`frames_agent.go:11-31`). The read-only "Preset" row hints at this, but
nothing tells the user that editing "Provider" here is actually editing
preset `X` (also used by other roles/profiles).

**Suggestion:** when a preset is active, title the rows accordingly
("Provider (preset: qwen-large)") or replace provider/model rows with a
single drill into that preset's frame, so the edit happens where the data
lives.

---

## 2. Settings browser

### F-UX-206 — Raw dotted keys are the primary label  `P1` — implemented (Phase 1)
`browserField()` (`settings/browser.go:137`) deliberately swaps the human
title into the description and promotes the dotted id (`shell.guardrail_argv0`)
to the row title. The friendly name is only visible for the row under the
cursor. This is the direct cause of the "raw settings entry names" complaint.

**Suggestion:** invert (VS Code model): human title as the row label, dotted
key rendered dim after/below it and always searchable. In the unfiltered
view, prefix with the section ("Shell · Unrecognized-command policy"). Keep
`/set` addressing by dotted key unchanged.

### F-UX-207 — The default view is an unstructured wall of ~100 rows  `P2` — open
With no filter, every leaf field renders in one flat list sorted by id.
Sections exist only as search haystacks and collection drills; the sidebar
described by `sectionSpec` ("maps a sidebar entry…") was never rendered.

**Suggestion:** cheapest fix — insert non-selectable section header rows into
the flat list (Agent, Providers, Privacy, …) and let `g`/`G`/typing jump.
Better — a two-pane root: section list left, fields right (the paneStack and
frames already exist; this is a rendering change, not a data-model change).

### F-UX-208 — Missing descriptions and units  `P3` — open
Many fields have no `desc` (all of Indexing's toggles, most Sandbox ints,
Interface theme/mode, agent.plan_first). Byte/token/MB fields display raw
ints with mixed unit conventions ("Max file bytes", "Memory limit (MB)",
"Max output bytes"); durations render as Go strings ("30s").

**Suggestion:** every field gets a one-line desc (the settings row desc is
the only in-app documentation surface). Render sizes humanized ("2 MB") and
accept humanized input; standardize unit-in-title format ("Max output (bytes)").

### F-UX-209 — No modified-from-default indicator  `P3` — open
`configDiff` and the reset machinery already exist, but the list gives no cue
which values diverge from `config.Default()`.

**Suggestion:** a dim `●` marker (plus a "modified" search keyword) on
non-default rows; makes the existing per-section reset rows meaningful.

### F-UX-210 — Collection gestures are undiscoverable  `P3` — open
`fieldList` supports `a` add, `d` delete, `y`/`p` yank/paste, `shift+↑/↓`
reorder, `e` edit, `space` toggle, `←/→` enum cycle — but the panel hint line
is a static "↵ edit · Esc close" and the footer just says "N settings".

**Suggestion:** make the hint line contextual per cursor row kind (collection
row: "↵ open · a add · d delete · y/p duplicate"; enum: "←→ cycle · ↵ pick"),
exactly the progressive-disclosure pattern the chat footer already follows.

### F-UX-211 — Risky settings look like ordinary settings  `P3` — implemented
`sandbox.backend=passthrough`, `shell.auto_approve`,
`privacy.remote_providers`, `hooks` entries — all render identically to
`tui.theme`. Worse, `sandbox.unsafe_passthrough` (the opt-in required for
passthrough to even boot) is **not exposed in the sandbox frame at all**, so
the UI lets you select `passthrough` and then the app fails at startup with
no UI path to complete the opt-in.

**Suggestion:** add the missing `unsafe_passthrough` field; render
security-sensitive rows with `status.warning` styling and a one-line
consequence in desc; consider the two-press arm/confirm pattern already used
for reset rows.

### F-UX-212 — Project-scope-only saves  `P3` — open
The browser persists only to the project config (`SaveProjectConfig`).
`~/.config/marshal/config.toml` is writable only by onboarding or by hand,
yet things like theme and providers are naturally global.

**Suggestion:** a scope toggle in the browser (Project / Global, VS Code
User-vs-Workspace style), defaulting to project, with the effective merged
value shown either way.

### F-UX-213 — Miscategorized fields  `P3` — implemented
`project.name` and `project.languages` live in the **Commands** frame
(`frames_basic.go:55-58`). "Interface" holds only theme + mode.

**Suggestion:** a small "Project" section (name, languages, plus the
Commands test/format/vet fields could live there too as "Project commands").

---

## 3. Provider connection

### F-UX-214 — Three parallel provider-setup implementations  `P2` — implemented (Phase 2, settings-side)
First-run onboarding (`app/onboarding.go`, hardcoded to 3 providers, its own
key-mode logic), the `/connect` wizard (`tui/connect`, template → URL → key →
probe → model), and the settings Providers add-wizard + drill each implement
provider creation separately with different capabilities and visuals.

**Suggestion:** make the connect wizard the single flow. Onboarding becomes
"welcome + trust + embedded connect wizard"; the settings `a` wizard launches
the same stepper instead of its own template-pick-then-drill variant. One
place to fix bugs, one mental model for users.

### F-UX-215 — Connect wizard skips the privacy gate  `P2` — implemented (Phase 2)
The settings "Test connection" action refuses remote probes when
`privacy.remote_providers_allowed=false` ("✗ blocked (enable Remote providers
in Privacy)", `frames_collections.go:417`). The `/connect` wizard runs
`probe.Provider` with no such check — a network call to a remote endpoint in
a local-first product whose default forbids it.

**Suggestion:** apply the same gate in the wizard, but as an *interactive*
step: "Remote providers are disabled. [Enable and continue] [Cancel]".

### F-UX-216 — API keys are typed in cleartext  `P3` — implemented
The connect wizard's key input (`connect.go:246`) and onboarding's input use
default echo; the settings api_key field masks the *stored* value but the
inline edit input echoes typed characters.

**Suggestion:** `EchoPassword` (or bullet echo) for all secret inputs.

### F-UX-217 — No completion receipt; provider name is invisible  `P2` — implemented (Phase 2)
The wizard auto-generates the provider name (`uniqueName()`), never shows it,
and emits `DoneMsg` with no summary step. Users end up with `ollama-2` style
entries they didn't knowingly create, and don't learn where the config was
written.

**Suggestion:** a final "Connected ✓" step: provider name (editable), base
URL, key source (env var vs stored), chosen model, and destination config
file. Doubles as the teaching moment for the providers/presets vocabulary.

### F-UX-218 — Dead-end errors where an action should be  `P2` — implemented (Phase 2)
`providerPickerField`: picking "Add a provider…" returns the *error* "add a
provider first in the Providers section". `modelPickerField`: "test the
provider connection first to discover models". Both name the fix and refuse
to do it.

**Suggestion:** the add-provider item should launch the provider wizard; the
discover item should run the probe (the picker already supports async pending
state via `pickPending`).

### F-UX-219 — Probe errors truncated to ~48 chars  `P3` — implemented
`connect.go:345` truncates the error, which routinely amputates the useful
part ("connection refused" vs 401 vs TLS). No remediation hints.

**Suggestion:** wrap over 2-3 lines instead of truncating; map common
failures to hints ("connection refused → is Ollama running on this URL?",
"401 → key rejected; check $OPENROUTER_API_KEY").

### F-UX-220 — Provider list rows lead with the masked key  `P3` — implemented
Provider entries render as `name  (sk-…abc)` (`frames_collections.go:28`).
The key fragment is the least identifying attribute.

**Suggestion:** `name  base_url  [local|remote] [key: env|stored|none]` —
matches what users scan for and surfaces the "no key configured" state.

---

## 4. Discoverability & naming

### F-UX-221 — The flagship commands are hidden from /help  `P1` — implemented (Phase 1)
`/connect`, `/models`, `/model`, `/settings`, `/sdd`, `/swarm`, `/mode` are
all `Hidden: true`, and `Registry.List()` excludes hidden commands from the
`/help` output. Completion (`ListAll`) still offers them, but `/help` — the
document a new user reads — never mentions SDD, swarm, or how to connect a
provider. The product's headline features are undocumented in-app.

**Suggestion:** stop hiding them; group the `/help` output (Session ·
Models & providers · Workflows · History · Settings) so the list stays
scannable. Keep true aliases (`/quit`, `/ask`/`/edit`/`/auto`) hidden.

### F-UX-222 — Jargon labels  `P3` — open
Worst offenders: "Dynamic argv0 guardrail" (→ "Unrecognized-command policy"),
"Tool iters" (→ "Tool-call budget per role"), "Max turn context tokens"
(→ "Context budget per turn (tokens)"), "Subtask iterations" (→ "Subagent
tool-call limit"), "Summarise files" (needs desc explaining cost/benefit).
Only two fields in the whole registry define search `keywords`; add synonyms
("dark mode", "api key", "docker", "autonomy") so fuzzy search hits on the
words users actually type.

### F-UX-223 — Displayed setting ids diverge from TOML paths  `P3` — open
The browser shows `shell.timeout` for `[tools.shell] default_timeout_seconds`,
`swarm.max_fix_rounds` for `[swarm.budget]`, `snapshots.max_file_bytes` etc.
Users who edit `config.toml` (a local-first audience *will*) can't map one
to the other, and `/set` keys don't match the file.

**Suggestion:** if F-UX-206 demotes ids to secondary text anyway, show the
*real* TOML path there. Accept both spellings in `/set` during a transition.

---

## 5. Smaller observations (things that are working well noted at the end)

- **F-UX-224 `P3` — open** — Settings hint says "↵ edit" even when the cursor row is
  read-only (`agent.preset`) or an action; value cell glyphs (`›`, `▾`, `↵`)
  are the only kind cue. Contextual hints (F-UX-210) fix this too.
- **F-UX-225 `P3` — implemented** — `/trust` answers "use --trust or restart" — a command
  that exists only to say it can't do anything. Either implement re-prompt or
  drop it from the registry.
- **F-UX-226 `P3` — implemented** — Approval footer "Enter arm / Enter⏎ submit" reads as a
  riddle; consider "Enter select · Enter again confirm".

**Working well (keep):** semantic theme slots with NO_COLOR/mono fallbacks;
priority-based status-line collapse; contextual chat footer (the settings
panel should copy it); `/set` immediate-apply with receipts and blocked-save
retry semantics; picker badges (`● now`, `◉ discovered`, `local`); the
two-press reset pattern; SDD/swarm progress surfaced in the status line.

---

## Suggested sequencing

| Phase | Items | Theme |
|-------|-------|-------|
| 1 | F-UX-201, 202, 206, 221 | Make multi-model config possible and settings readable |
| 2 | F-UX-203, 204, 205, 214, 215, 217, 218 | One coherent connect flow; SDD ergonomics |
| 3 | F-UX-207–213, 216, 219, 220, 222–226 | Polish: grouping, units, hints, naming, warnings |
