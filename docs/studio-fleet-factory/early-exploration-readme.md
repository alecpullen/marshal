# Marshal web design exploration

The primary conversation and decision record is [design-notes.md](design-notes.md). It consolidates the scope, accepted directions, proposals, open questions, source research, and mockups.

These standalone mockups are a design study, not production implementation. They are separate from the Marshal repository and daemon work.

Open `voice-planning.html` directly in a browser, or serve this directory. Voice, transcription, planner questions and submission are simulated. No microphone, model, repository, or external service is connected. Draft edits and the selected preference use browser localStorage only.

## Concepts to compare

- A: transcript first; optional questions beneath the transcript.
- B: questions first; latest speech excerpt and expandable transcript.
- C: quiet capture; questions behind a persistent entry point.
- All three: pause/resume, defer/dismiss questions, finish into an editable structured draft, read original transcript, simulated submission.

## Established direction from discussion

- Collaborative planning precedes factory handoff; autonomous coordinator roles belong to later work.
- A durable feature/work item connects planning, specifications, plans, decisions, execution and delivery. Naming remains open.
- Specifications/plans/PRs have flexible relationships. Planner proposes PR breakdown; implementers can adjust within authority.
- Draft PRs begin implementation, with plan/spec documents as a possible initial commit.
- Incremental handoff is supported. Multi-repository work is low priority; single-repository flows come first.
- Handoff config selects model/provider, specialist reviewer prompts, delegation limits and repair iteration limit. Project defaults are optional.
- Planning agents may answer implementation questions and approve minor changes when authorised. Consequential changes return to the human.
- Build/review/fix runs automatically, default limit five iterations. Clean required CI and reviews mark PR ready. Stagnation or exhausted iterations stop and surface evidence to the human.
- Forge priorities: CI failures prominent, successful checks compact, narrated engineering explanations with optional code, and factory repair integration.
- Gitea integration or a fork remains under exploration, not selected.
- Mobile supports main forge, review, CI, factory and planning workflows; desktop and keyboard use remain first class.
- External planning handoff uses MCP; Claude Code terminal first, web second. Two-way Channels and resumable CLI sessions are documented possibilities, not tested integrations.
- Enough planning context must transfer for an internal model to take over. Planner transfer requires human approval by default; automatic fallback is configurable.
- Voice primarily supports substantial planning explanations. Optional questions may arrive while speaking, without interrupting or requiring an answer.

## Voice layout decision

User chose B (questions alongside) as the default, with C as an optional quieter mode. Both are views of the same recording and question state.

## Change review exploration

User approved the combined B/C PR inspection layout shown in `change-review-v2.html` and `screenshots/review-v2-combined.png`. Tapping a PR opens this view directly: expanded failing CI above the engineering narration, related reviewer findings and factory repair status grouped with the failure, successful checks collapsed, and optional code within the walkthrough. The screenshot represents the full scrolling page. This approves the design direction, not production implementation, and does not decide the fleet overview.

`change-review.html` compares A (readiness overview), B (narrated review), and C (repair workspace), using the same fictional approval-reconnect change. High-resolution captures are under `screenshots/review-*.png`. The markup is static; CI, PR and factory data are illustrative. No real work was started or modified.

## Fleet and project factory decisions

The fleet manager is the main UI. Users may create optional factories within it and continue using independent agents. A factory belongs to one project; cross-project factory operation is not an initial goal. Factories begin with a working plan → implement → review → fix process that users can customise. Configuration can supply defaults and individual handoffs can override them.

`fleet-factory.html` and `screenshots/fleet-dashboard.png`, `factory-dashboard.png`, and `fleet-factory-comparison.png` show the accepted fleet and project-factory dashboard grouping. Fleet groups factories and exposes independent agents; the project factory focuses on work items. Attention entries link directly to decisions or ready changes. The user requested removing the purple workflow overview strip from the factory screen; no revised mockup is needed. Existing screenshots are superseded on that point. Agent/model names, counts, features and statuses are sample data, not live project status.

## Proposals not yet approved

- Editable structured interpretation plus original transcript before submission.
- Detailed behaviour of the chosen voice and PR inspection layouts; fleet and feature overview layouts.
- Voice/transcription provider, audio retention, offline/background behaviour.
- Exact definition of repair iteration and automated stagnation detection.

## Validation

Checked the preview in Chromium at 1360px and 390px. Pause/resume, injected question, defer, draft editing, simulated submission and mobile concept switching exercised without page errors. No real voice behaviour has been implemented or tested.
