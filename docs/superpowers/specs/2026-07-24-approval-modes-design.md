# Approval Modes Design

**Goal:** Give the user control over how much autonomy the agent has, via a unified cycle of interaction modes that bundle turn-classification and approval-gating into one concept, toggled by a single keybind and slash commands.

**Status:** Approved design, pending implementation plan.

---

## 1. Background & motivation

Today Marshal has two orthogonal, disconnected controls:

- **Turn-classification modes** (`/ask`, `/edit`, `/auto`): control whether the agent is read-only (`question` class) or plans-and-edits (`edit` class). These are toggled via Tab/Shift+Tab and slash commands. They do **not** gate approval.
- **Approval gate**: per-tool, in `execute.go`. The policy engine (`PolicyEngine.Evaluate`) returns `DecisionConfirm` for risky tools, and the runner blocks on `requestApproval` until the TUI responds. There is no global "approve everything" lever.
- **Shell-only auto-approve**: `Tools.Shell.AutoApprove` (a boolean) auto-approves shell commands only. No cross-tool equivalent exists.

The user wants a single, unified control that sets the agent's autonomy level — from fully interactive (confirm each write) to fully autonomous (auto-approve everything, no questions) — riding the same cycling UX as the existing modes. A new neutral `default` mode (Claude-Code-style) replaces the implicit read-only behavior, and the agent can request elevation to an editing mode when it needs to make changes.

## 2. Mode model

Five modes in one Tab cycle, replacing the current `ask`/`edit`/`auto` trio. Each mode is a named value of a new extensible `ApprovalMode` enum and bundles turn-classification + approval-gating:

| Mode | Turn class | Writes | Approval | May ask user? | Framing |
|------|-----------|--------|----------|---------------|---------|
| `plan` | `question` (read-only) | denied | n/a | yes | Forced numbered-plan output, then stops |
| `default` | `question` (read-only) | denied until elevated | n/a | yes | Neutral; requests elevation via `mode.request` |
| `edit` | `edit` | allowed | confirm each | yes | Today's behavior |
| `copilot` | `edit` | allowed | auto-approve except floor | yes | Pair-programming partner; checks in on real decisions |
| `auto` | `edit` | allowed | auto-approve except floor | no | Fully autonomous, hands-off |

**Cycle order:** `plan → default → edit → copilot → auto` (wrapping).

**Default mode:** `default` (the new neutral mode). Existing users with no config get the new neutral default, not the old confirm-each behavior. This is an intentional behavior change: out of the box, the agent is read-only until the user elevates it.

**`plan` vs `default`:** both are read-only (`ForceClass = "question"`) and may ask the user questions. `plan` forces a numbered-plan output directive then stops; `default` is free-form and requests elevation when it wants to edit.

**`copilot` vs `auto`:** identical auto-approve behavior in the policy engine. The only difference is the question-asking gate in the runner (`copilot` allows `ask_user`/`question.ask`; `auto` disables them with a correction message).

### The floor (non-bypassable in every mode)

The existing `guardrailPatterns` (sudo, rm -rf, git reset --hard, git clean -fd, mkfs, shutdown, reboot) **plus `git push`** (all variants: `git push`, `git push --force`, `git push -f`, `git push --tags`, etc.). In `edit`/`copilot`/`auto`, a floor hit returns `DecisionConfirm` — the user is asked, never silently auto-approved. Guardrails that already return `DecisionDeny` stay `Deny`. The floor is non-bypassable: no mode downgrades a floor `Confirm` to `Allow`.

Local commits (`git commit`) are **not** on the floor — the agent may stage and commit freely in `copilot`/`auto`. Only pushing to a remote is gated. This matches Marshal's local-first principle: local commits are reversible, pushes aren't.

## 3. Elevation mechanism (`mode.request`)

The agent requests elevation from `default` to an editing mode via a new native tool.

**Tool shape:**
```
name: mode.request
args: { "mode": "edit" }   // the agent requests editing intent generically
risk: RiskReadOnly          // requesting a mode change is itself side-effect-free
```

The agent calls `mode.request` with `mode: "edit"` (generic editing intent — it does not pick the autonomy level). The tool handler does not execute the mode change itself; it posts a request to the TUI (same channel pattern as `ask_user`/`question.ask`) and blocks on the user's response.

**User prompt:** the TUI shows a picker offering the three editing variants — `edit` (confirm each), `copilot` (auto-approve, may ask), `auto` (fully autonomous) — plus a deny/keep-in-default option. The user picks the autonomy level, not just yes/no.

**Elevation scope:** persistent until the user manually switches back (Tab or slash command). The agent stays in the chosen editing mode for subsequent turns. The elevation is a real mode switch and is persisted to config.

**Tool result back to the agent:** `mode.request` returns the user's choice to the agent — e.g. `mode.request result: approved — switched to copilot mode` or `mode.request result: denied — staying in default mode; describe your proposed changes instead`. This lets the agent know whether to proceed with edits or fall back to describing them.

**Availability:** `mode.request` is advertised in the available-tools section only when the active mode is `default`, and only for the general/interactive runner. In other modes it's omitted (nothing to elevate to). Swarm/SDD orchestrators fix their own task classes and do not drive TUI elevation, so `mode.request` is omitted from their tool sets (same way `ask_user` is omitted from swarm roles today).

**Prompting for elevation:** in `default` mode, write tools are denied by the policy engine with a reason that directs the agent to `mode.request`. The `default` mode role addendum tells the agent: "You are in default mode and cannot modify files. If you need to make changes, call mode.request to ask the user to switch to an editing mode. Do not attempt write tools directly."

## 4. Policy-engine integration (Approach A)

The approval mode is a first-class field on `PolicyEngine`, set via a new `SetApprovalMode` method (mirroring the existing `SetSessionRules`/`SetRules` setters that the TUI already calls).

**New `ApprovalMode` type** (in `internal/tools/policy`):
```go
type ApprovalMode string
const (
    ModePlan    ApprovalMode = "plan"
    ModeDefault ApprovalMode = "default"
    ModeEdit    ApprovalMode = "edit"
    ModeCopilot ApprovalMode = "copilot"
    ModeAuto    ApprovalMode = "auto"
)
```

**`Evaluate` change:** after computing the normal decision (today's logic), apply the mode as a final transform. The mode-aware logic runs last, after guardrails and F4 rules, so:

1. **Guardrails always win** — they return `DecisionDeny` before the mode logic runs (unchanged). The floor can never be downgraded.
2. **`git push` floor** — a new check in `Evaluate` (after guardrails, before the mode transform): if the command is a `git push` variant, it returns `DecisionConfirm` regardless of mode. This is the non-bypassable floor in `copilot`/`auto`.
3. **Mode transform** — when the engine has computed a decision and the active mode is set:
   - `plan` / `default`: non-read tools (`RiskWorkspaceWrite`, `RiskCommand`, `RiskNetwork`, `RiskDestructive`) and shell writes → `DecisionDeny` with reason `"denied: in <mode> mode, cannot modify files; call mode.request to switch to an editing mode"`. Read-only tools (`RiskReadOnly`) and read shell commands → `DecisionAllow` as today. Shell commands that would mutate (e.g. `rm`, `git commit`, `echo > file`) are denied the same way as write tools; the distinction is the tool's registered `Risk` level for native tools, and for `shell.run` the existing command classification (a command matching an allow/confirm rule that is inherently read-only passes; anything the existing logic would `Confirm` or `Deny` is denied in these modes). In practice this means `shell.run` in `plan`/`default` returns `DecisionDeny` unless the command matches an explicit allow rule for a read-only command — mirroring how the runner already classifies commands, just with the confirm fallback replaced by deny.
   - `edit`: no transform — today's behavior (confirm each write).
   - `copilot` / `auto`: a computed `DecisionConfirm` → `DecisionAllow` (auto-approve), *unless* the call hit the floor (git push) or a guardrail `Deny` (already returned above). The reason becomes `"auto-approved in <mode> mode"`.

**Floor-vs-transform ordering:** the git-push floor produces a `DecisionConfirm` that must survive the `copilot`/`auto` downgrade. Implementation: the floor check runs before the mode transform and, when it fires, returns the decision early — the mode transform never runs for a floor hit. This is the cleanest structure: the floor is a separate `if isGitPushFloor(cmd) { return DecisionConfirm, "git push requires approval (non-bypassable floor)" }` branch in `Evaluate`, placed after guardrails and before the mode transform, so a floor `Confirm` is returned to the caller directly and the mode transform is skipped entirely. No flag or reason-prefix is needed; the early return is the mechanism.

**Existing tests stay green:** `PolicyEngine` constructed without a mode (today's `NewEngine`) defaults to `ModeEdit` (confirm-each), which is today's behavior. The mode transform is a no-op for `edit`. All existing policy tests construct engines without setting a mode, so they land on `edit` and behave as before.

**Runner is unchanged:** `handlePolicyDecision` in `execute.go` still sees `DecisionAllow`/`DecisionConfirm`/`DecisionDeny` and reacts exactly as today. No new code path in the hot loop — the policy engine just returns different decisions based on the mode.

## 5. Question-asking gating (`auto`)

The `auto` mode disables the agent's ability to ask the user questions. Today, `ask_user`/`question.ask` availability is gated at `runner.go:700`/`732` by `r.role() != RoleGeneral`. The mode adds a second gate on the same path.

**Change in `runner.go`** (the `ActionAskUser` and `ActionQuestionAsk` cases): add a check that the active approval mode is not `auto`. If it is, emit the same correction message shape used for role-gating today:

> `ask_user is not available in auto mode; proceed with your best judgment and state the assumption you made.`

This mirrors the existing role-availability correction exactly (`runner.go:701`), swapping "the X role" for "auto mode". The agent gets a correction and continues — it does not stall.

**How the runner learns the mode:** the runner already holds a reference to the `PolicyEngine` (via `r.Policy`). The mode lives on the engine, so the runner reads `r.Policy.ApprovalMode()` (a new read accessor, mirroring `Logger()`). No new plumbing through the TUI — the engine is already shared.

**`copilot` (may ask) vs `auto` (no asking):** the only runtime difference is this gate. Both auto-approve identically via the policy engine. `copilot` leaves the question path open; `auto` closes it.

## 6. Config, persistence & TUI wiring

**Config schema** — a new field on `AgentConfig`:
```toml
[agent]
approval_mode = "default"   # plan | default | edit | copilot | auto
```
Default value: `default`. The old `Tools.Shell.AutoApprove` boolean stays as-is (shell-only legacy) and is unaffected.

**Persistence of runtime changes:** when the user switches mode via Tab or slash command, the new mode is written back to config (`.marshal/config.toml` project-local, or the user config) via the existing `config.Save` path — same mechanism `save.go` already uses for other settings. This survives restart. The elevation choice from `mode.request` is also persisted (it is a real mode switch, not a transient override).

**TUI wiring:**
- `app.go:430` constructs the `PolicyEngine`; after construction, call `pol.SetApprovalMode(cfg.Agent.ApprovalMode)` to seed from config.
- The TUI `Model` holds the current mode (replacing `forceMode`). On mode switch (Tab/Shift+Tab cycle, slash command, or `mode.request` approval), it calls `m.runner.SetApprovalMode(mode)` — a new method on the runner that delegates to `r.Policy.SetApprovalMode(mode)` and updates the TUI's local state for rendering.
- The cycle order: `plan → default → edit → copilot → auto` (wrapping). `modeOrder` in `model.go` is updated.
- Status line (`status.go`) renders the active mode with a badge, as it does today for `auto`.

**Slash commands:** `/plan`, `/default`, `/edit`, `/copilot`, `/auto`, and `/mode [name]` (the existing picker). The current `/ask`/`/edit`/`/auto` are replaced; `/mode`'s picker lists the five modes. All are `TUIOnly` as today.

**`mode.request` dispatch:** the TUI receives the tool call, shows the editing-variant picker, sends the user's choice back via the tool's response channel (same channel pattern as `ask_user`), and applies the chosen mode via `SetApprovalMode` + config save.

## 7. Prompt changes

Each mode shapes the agent's behavior via a mode-specific directive prepended to the system prompt (in `renderRoleAddendum` or a new sibling).

**Per-mode directives:**
- `plan`: "You are in plan mode. You are read-only and cannot modify files. Produce a numbered plan as your final answer, then stop. You may ask the user clarifying questions about requirements before planning."
- `default`: "You are in default mode. You are read-only and cannot modify files. If you need to make changes, call the mode.request tool to ask the user to switch to an editing mode. Do not attempt write tools directly."
- `edit`: "You are in edit mode. Each file change requires user approval before it is applied."
- `copilot`: "You are in copilot mode. File changes are auto-approved except for destructive guardrails and git push. You may ask the user a question if you hit a genuine ambiguity that would materially change the outcome."
- `auto`: "You are in auto mode. File changes are auto-approved except for destructive guardrails and git push. You cannot ask the user questions — proceed with your best judgment and state the assumptions you make."

**`mode.request` in the tool list:** the tool is advertised in the available-tools section only when the active mode is `default` (and only for the general/interactive runner). In other modes it is omitted.

**No new JSON envelope action:** the mode is conveyed purely through the system prompt directive + the `mode.request` native tool. The envelope (`answer`/`tool_call`/`patch`/`final`/`ask_user`) is unchanged.

## 8. ACP transport

The ACP headless transport (`marshal acp`) has no TUI. It already handles two of the three interactive surfaces the TUI owns — approvals and questions — by delegating to the connected editor client over JSON-RPC. The modes design needs to account for how each mode behaves when there is no in-process picker.

**Approval flow (already present):** when the runner hits `DecisionConfirm`, the `PermissionBridge` (`internal/acp/permissions.go`) sends a `session/request_permission` JSON-RPC request to the connected editor, which renders its own approval UI and returns the decision. This works unchanged for `edit` mode. For `copilot`/`auto`, the policy engine downgrades `Confirm → Allow` before the bridge is ever invoked, so no permission request is sent — the agent just proceeds. The floor (guardrails + git push) still returns `Confirm`, which the bridge forwards to the editor as before. No ACP change needed for approvals.

**Mode selection (config-only):** ACP has no runtime mode toggle (no Tab cycle, no slash commands — those are TUI-only). The active mode is seeded entirely from config at runtime construction (`app.StartRuntime` constructs the `PolicyEngine` and calls `SetApprovalMode(cfg.Agent.ApprovalMode)`). An ACP user sets their mode in `config.toml` and restarts. This is the same config path the TUI uses for its default; ACP simply has no runtime override. The spec does not add an ACP method to switch modes mid-session — that can be a future extension if needed.

**Elevation (`mode.request`) over ACP:** the `mode.request` tool posts a request to the TUI and blocks on the user's choice. Over ACP there is no in-process picker. Two options, and the spec picks the simpler one:

- **Chosen: treat `mode.request` like a permission request.** The `mode.request` tool handler, when running under ACP, posts a `session/request_permission`-style request to the editor client (reuse the existing `PermissionBridge`/`PermissionRequest` wire shape, with a new `Reason` prefix identifying it as a mode-elevation request). The editor renders its own picker (the three editing variants + deny) and returns the choice. The tool result relays the editor's decision back to the agent. This reuses the existing permission transport rather than inventing a new ACP method, and an editor that already renders `request_permission` can render elevation with minimal additional logic. If the editor returns `approved: false`, the agent stays in `default` and is told to describe its changes instead.
- **Rejected: disable `mode.request` over ACP.** Would force ACP users to pre-config an editing mode, making `default` useless over ACP. Rejected because `default` should work everywhere.

Implementation detail: the `mode.request` handler needs to detect whether it's running under ACP vs the TUI. The cleanest seam is the same one `ask_user`/`question.ask` already use — the runner's `requestApproval`/`requestAnswer`/`requestQuestions` methods, which the TUI overrides via the `session.State` pending-call channels and ACP overrides via the `PermissionBridge`. The `mode.request` handler posts its request through the same pending-call mechanism (`session.State.SetPendingApproval` with a mode-elevation reason), and the active transport (TUI picker or ACP bridge) responds. The TUI shows its in-process picker; ACP forwards to the editor. One handler, two transports — matching how approvals already work.

**Question-asking over ACP (already handled):** ACP v1 does not support interactive questions. Today, `ask_user`/`question.ask` are auto-answered with `UnansweredAnswers` (`turn.go:268-282`), so the agent always gets "the user declined to answer; proceed with your best judgment." This is unchanged by the modes design:

- `copilot` over ACP: the agent may call `ask_user`/`question.ask`, which are auto-answered as unanswered. The agent proceeds with its best judgment. This degrades gracefully — `copilot`'s "may ask" becomes "asks, gets no answer, proceeds" over ACP, which is the same as today's behavior for the general role.
- `auto` over ACP: the runner's `auto` question-gate (Section 5) fires *before* the ACP auto-answer path, emitting the correction message directly. The agent never reaches the pending-question event. So `auto` over ACP is identical to `auto` over the TUI — no questions, proceed with best judgment. No ACP change needed.

**Summary of ACP changes:**
- `internal/acp/permissions.go` — the `PermissionBridge` already handles `request_permission`; the `mode.request` elevation request reuses this wire shape. No new ACP method is added. The bridge may need to recognize a mode-elevation `Reason` prefix to relay the editing-variant choices to the editor, but the transport is unchanged.
- `internal/acp/turn.go` — no change. The `auto` question-gate is in the runner, not the ACP layer; ACP's existing auto-answer path handles `copilot`'s unanswered questions as today.
- `internal/app/runtime.go` / `app.StartRuntime` — seed `pol.SetApprovalMode(cfg.Agent.ApprovalMode)` during runtime construction (same call the TUI path makes). This is the only ACP-side wiring change.

## 9. File map

Files created or modified:

- `internal/tools/policy/policy.go` — add `ApprovalMode` type, `SetApprovalMode`/`ApprovalMode()` methods, the git-push floor check, and the mode transform in `Evaluate`.
- `internal/tools/policy/policy_test.go` — tests for each mode's decision transform, the git-push floor, and floor-vs-auto-approve ordering.
- `internal/app/config/types.go` — add `ApprovalMode` field to `AgentConfig`.
- `internal/app/config/file_types.go` — add `ApprovalMode *string` to `fileAgent`.
- `internal/app/config/merge.go` — merge `approval_mode`.
- `internal/app/config/defaults.go` — default `approval_mode = "default"`.
- `internal/app/config/save.go` — persist `approval_mode`.
- `internal/app/config/config_test.go` — round-trip and merge tests for `approval_mode`.
- `internal/agent/runner.go` — `SetApprovalMode` method (delegates to `r.Policy`); `auto` question-gating in `ActionAskUser`/`ActionQuestionAsk`.
- `internal/agent/runner_misc_test.go` or a new `runner_mode_test.go` — tests for the `auto` question gate.
- `internal/agent/prompts.go` — per-mode directives; advertise `mode.request` only in `default`.
- `internal/agent/prompts_test.go` — assertions for each mode directive and `mode.request` visibility.
- `internal/tools/native/mode_request.go` (new) — the `mode.request` tool handler.
- `internal/tools/native/mode_request_test.go` (new) — tool handler tests.
- `internal/tools/native/native.go` (or wherever tools are registered) — register `mode.request`.
- `internal/app/tui/model.go` — replace `forceMode` with the five-mode cycle; `SetApprovalMode` on the runner; `modeOrder` update.
- `internal/app/tui/commands_dispatch.go` — `/plan`, `/default`, `/edit`, `/copilot`, `/auto`, `/mode` handlers.
- `internal/app/tui/status.go` — render the active mode badge.
- `internal/app/tui/model_test.go` — cycle and mode-switch tests.
- `internal/commands/commands.go` — replace `/ask`/`/edit`/`/auto` command registrations with the five new modes.
- `internal/commands/commands_test.go` — update command-registration tests.
- `internal/app/app.go` — seed `pol.SetApprovalMode(cfg.Agent.ApprovalMode)` after engine construction; wire `mode.request` dispatch in the TUI.

## 10. Out of scope

- Per-tool auto-approve overrides within a mode (e.g. "auto-approve everything except shell"). The floor handles the always-confirm set; finer control remains via existing F4 permission rules.
- Time-limited elevation (e.g. "auto for the next 3 turns"). Elevation is persistent until manually switched.
- Mode-specific model routing (e.g. a different model for `auto` vs `edit`). Out of scope; routing is orthogonal.
- A headless/CLI mode-switch flag (`marshal --mode auto`). Config + slash commands cover the use cases; a CLI flag can be added later.