# Marshal Studio in Flutter — reimagined UI

Status: **draft for discussion**, 2026-11-17. Nothing here is approved. The
mock in [`mock/`](mock/) is a clickable Flutter app on illustrative data; it
talks to no daemon.

This replaces the Svelte Studio (`web/ui`) with one Flutter app that runs as
the native macOS app and as the web Studio a bridge serves. It is a redesign,
not a port: every capability in [`../web-studio/`](../web-studio/design.md) and
[`../studio-fleet-factory/`](../studio-fleet-factory/README.md) keeps a home,
but the homes are different.

## 1. What was wrong

The current Studio is organised by object type. Its sidebar lists Fleet,
Inbox, each project, Projects, Live, Runs, Workspaces, Watches, Library,
Usage, Settings and three roadmap pages. Each was added by a phase that was
correct on its own, and together they ask the user to know which page a thing
lives on before they can find it. One piece of work is spread across a chat
page, a review page, a run page, a factory item page and a project page.

The Fleet-first alignment of 2026-11-15 reordered those pages. It did not
reduce them, which is why it did not fix the feeling.

## 2. The model

**Everything you do is a thread, and every thread lives under a project.**

A thread is one piece of work with a conversation. There are three kinds, and
they share one screen:

- **Agent** — one agent, steered in conversation.
- **Work item** — a factory item: a plan, several agents, pull requests,
  reviews and repairs, up to a ready PR.
- **Plan run** — an `/sdd` plan or a swarm executing task by task.

Navigation is then six fixed places and the project tree:

| Place | Answers |
|---|---|
| **Overview** | What needs me, what is running, what is ready, is the fleet healthy |
| **Inbox** | Everything waiting on me, across every project |
| **Pull requests** | Every open PR Marshal produced, with its CI: ready, failing, in progress |
| *Projects › threads* | The work itself |
| **Automations** | What starts work without me |
| **Library** | What agents know and can use |
| **Settings** | Where work runs and what it may touch |

The host switcher at the top of the sidebar scopes all of it to one daemon or
to all of them. A local daemon and a remote server are both just hosts.

### The thread screen

Three columns: sidebar, conversation, inspector.

- The **conversation** is the transcript for an agent, the activity timeline
  for a work item, and the task lanes plus the live step for a plan run. The
  composer is always at its foot and always means "steer this".
- The **inspector** holds everything that used to be a separate page about
  that work. Its tabs depend on the kind:
  - agent: Changes, Files, Terminal, Preview, Inspect;
  - work item: Plan, Agents, Checks, Changes, Pull requests;
  - plan run: Plan, Roles, Changes, Terminal, Inspect.
- The inspector **expands** to take most of the window for review; the
  conversation narrows to an outline beside it. Review & ship is that state,
  not a different route.
- A **ready** thread opens with its pull requests at the head of the
  conversation: CI checks first, then reviews, then the engineer's report,
  then Review, Merge, Open on forge and Send back. The pull request, not the
  thread, is the unit of review; the same PR card with its check strip is on
  the Overview (directly under Needs you), in the Inbox and on the Pull
  requests page.
- A **failing** pull request shows the failing check and the tail of its log
  on the card, with one primary button: **Fix with a worker**. One click
  sends it; there is no form. A factory worker reproduces the failure, fixes
  it, runs the gate and pushes to the same PR, and the card becomes "A worker
  is on it" with Watch and Stop. It returns to the user only if it cannot fix
  it, as an ask. The caret beside the button holds the few variations:
  investigate only, fix with a note, choose the worker, and always fix
  failures automatically in this project. The same button sits beside a
  failing check in a work item's Checks tab. Inside a factory's repair
  allowance this happens without the click; the button is for everything
  else — an independent agent's PR, an exhausted allowance, auto-repair off.
- A thing that needs the user appears as the **same card** in the Overview, in
  the Inbox and pinned in its thread. Answering it anywhere is one action.

### Starting work

One composer (`⌘N`), prompt first. Project, branch, environment, model,
permission mode, workspace and host are chips defaulted from the project.
Under the prompt the user picks **Agent**, **Plan first** or **Work item**;
issues, recent prompts and recipes sit below as starting points, and the
microphone starts voice planning. New agent, new work item, the planning
workspace and handoff creation stop being four entry points.

"Plan first" opens a planning thread. Handing it to the factory is a step
inside that thread — the handoff review — rather than a form on another page.

## 3. Where every existing capability goes

Nothing is dropped. This is the map from today's destinations and from the
two design sets to the new structure.

| Today / specified | New home |
|---|---|
| Fleet home, Home | Overview |
| Ready for review, ready to ship | Pull requests; ready and failing PRs on Overview; the review head of a ready thread |
| Inbox, pending list, attention list | Inbox; top three on Overview |
| Approvals, questions, factory decisions, network decisions, review-bot drafts, MCP intake | The one ask card: Inbox, Overview, and pinned in the thread |
| Live wall | Overview › Running now (tiles with the last three headlines) |
| Sessions list, agent sidebar groups | Project tree; "Earlier" under each project |
| Chat / agent session, the four dock states | Thread screen; inspector docked, collapsed or expanded |
| Dock tabs Inspect, Changes, Files, Terminal, Preview | Agent inspector tabs, unchanged in meaning |
| Review & ship, gate checklist, override, by-step diff, line comments as steering | Changes tab, expanded |
| Exit path: commit, gate, push, PR, patch export | Changes tab footer |
| Runs page: lanes, graph, timeline | Plan-run thread centre, as three views |
| Factory list, project factory dashboard | Project page › Work (a board by stage) |
| Factory item: approval, activity, review, PRs, documents | Work-item thread: stage strip, activity centre, inspector tabs Plan, Agents, Checks, Changes, Pull requests |
| Handoff creation and approval summary | Composer › Work item, then handoff review in the thread |
| Plan revisions, working drafts, conflict resolution | Plan tab › Propose a change; drafts autosave in the composer and Plan tab |
| Decisions, recovery, completion panels | Ask cards in the thread; recovery is an ask with its settings inline |
| Worker subagents, agent drill-in | Agents tab; opening one swaps the centre to its conversation |
| CI evidence, narrated engineering report, preview environment links | Checks tab and Pull requests tab |
| Project overview, defaults, factory settings, health, discovery report | Project page tabs Defaults and Health |
| PR review bot, CI fixer, issue poller | Automations › Bots; also Project page › Automations |
| Schedules | Automations › Schedules |
| Watches | Automations › Watches; tripped ones on Overview |
| Recipes | Automations › Recipes; offered in the composer |
| Library: skills, plugins, MCP, memory with scopes and promotion | Library |
| Workspaces gallery and designer, environments, devcontainer import | Settings › Environments |
| Network inspector, egress rules | Settings › Network; a blocked request is an ask |
| Secrets, credentials | Settings › Secrets and Settings › Connections |
| Hosts, host onboarding, disconnection | Host switcher and Settings › Hosts; queued steering shows in the thread |
| Connections: forges and providers | Settings › Connections |
| Models & routing, budgets | Settings › Models & routing |
| Usage, audit, disk and prune | Overview cards; detail in Settings › Usage & audit |
| Tokens, MCP clients, status links | Settings › Access; Share on the thread header creates a status link |
| Notifications | Settings › Notifications |
| Mode switcher, permission modes | A chip in the composer and on the thread's composer |
| Command palette | `⌘K`, the search field at the top of the sidebar |
| Roadmap pages | Removed from navigation (decided 2026-11-17). A planned capability is a disabled row where it will live, with one line saying so |
| Public status page `/s/<token>` | Stays plain HTML served by the bridge; it is not part of the app |

Environments stay a Settings section (decided 2026-11-17).

## 4. Visual direction

**Sunset, with the Ember palette** (decided 2026-11-17). The earlier Warm
Sunset palette clashed, most in dark mode: six saturated hues on a near-black
ground, with coral for running and amber for needs-you almost the same colour
doing opposite jobs. Ember keeps the warm neutrals and simplifies the rest:

- Coral is the one accent: primary buttons, selection, the current tab.
- Status is three colours: amber needs you, green passed, red failed.
- Running is neutral. The pulse shows it, not a hue.
- Roles are named, not coloured.

Rules:

- Borders group content. No background tint on chrome or panels.
- Fill marks state only: the selected row, a removed diff line.
- Colour carries status, never decoration.
- The running dot pulses; nothing else moves. Reduced motion stops that too.
- Real icons, not text glyphs. **Every icon, dot, chip and strip has a
  tooltip**; an icon may stand alone only because its tooltip names it.
- Monospace only for code, paths, counts and durations.

The mock uses the system font. Geist is not bundled; bundling it is part of
the real work. Ember should be offered back to the TUI so the two clients
stay one identity.

## 5. Layout at other sizes

- Below about 1080 px the inspector hides; its tabs become a sheet.
- Below about 760 px the sidebar becomes a drawer and the app bar carries the
  Inbox and New buttons. The thread screen is the conversation alone.

The same code is the phone layout, which gives the mobile app the factory
documents asked for without a second client.

## 6. Building it

The order of work, the decisions that cross phases and the risks are in
[programme.md](programme.md).

## 7. The mock

`mock/` is a self-contained Flutter project for macOS and web.

- `lib/theme.dart` — the tokens for both directions.
- `lib/data.dart` — illustrative projects, threads and asks.
- `lib/main.dart` — shell and sidebar.
- `lib/overview.dart`, `inbox.dart`, `project.dart`, `places.dart` — pages.
- `lib/thread.dart`, `inspector.dart`, `composer.dart` — the thread screen
  and the new-work dialog.
- `lib/pulls.dart` — the PR card, the Pull requests page and the review head
  of a ready thread.

Run it with `flutter run -d chrome` or `flutter run -d macos` from `mock/`.
On the web, a screen can be linked directly, for example
`?place=thread&id=recovery&dark=0`; `new=1` opens the composer.

Known limits of the mock: every agent thread shows the same sample
transcript; buttons other than navigation do nothing; `⌘K` is drawn but not
wired; the inspector sheet for narrow widths is not built.

Screenshots of each screen are in [`screenshots/`](screenshots/).
