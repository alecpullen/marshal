# Plan: Replace hand-coded TUI elements with huh v2

## Goal

Replace three hand-rolled TUI surfaces with `charm.land/huh/v2`:
1. Settings panel (`internal/app/tui/settings/`) — full rewrite as an embedded `*huh.Form`.
2. Tool-call approval chooser (`renderApprovalPanel` + the `enter`/`d`/`e`/`a`/`r` key handling in `model.go`).
3. Agent clarifying-question prompt (`renderQuestionPanel` + its `enter`/`esc` key handling).

Out of scope: the main inline input textarea (`m.input`), command suggestions, memory overlay, transcript rendering.

## Constraints & decisions

- **Inline rendering preserved.** Approval and question surfaces keep rendering *inside the existing input area* (transcript stays visible above). The huh form's `View()` is embedded into `renderInputArea`, not centered as a full-screen modal like settings.
- **Modal rendering preserved for settings.** Settings stays a centered overlay via `lipgloss.Place` (unchanged integration point in `model.go`); only the inner `settings.Model` is replaced by a wrapper around `*huh.Form`.
- **Edit flow is two-step.** The approval chooser is a `huh.Select` whose options include "Edit". Selecting Edit completes the form; the parent (`model.go`) then flips into the existing `editingCommand` textarea mode (unchanged). The edited command text is captured by the same textarea that exists today.
- **Rollback is conditional.** The "Rollback last change" option appears only when `m.state.HasBackup()` is true at the moment the form is built.
- **Esc behaviour preserved.** In approval/question contexts, Esc denies/skips (current behaviour), it does NOT quit the program. This requires a custom keymap on those forms (see below), since huh's default quit keymap uses Esc/Ctrl+C to abort.
- **Theming.** Use a custom `huh.Theme` derived from `huh.ThemeCharm` retuned to the Warm Sunset palette (coral `209`, gold `214`, teal `43`, dim `244`) so the new surfaces match the existing transcript styling. Shared helper in a new `internal/app/tui/huhtheme.go`.
- **Dependency.** Add `charm.land/huh/v2` to `go.mod`. It already depends on `charm.land/bubbles/v2`, `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2` — the same vanity-import stack the repo uses, so no version conflicts.

## Architecture

### Phase 1 — Settings panel

Replace `internal/app/tui/settings/` with a thin wrapper package that constructs and embeds a `*huh.Form`.

**New file: `internal/app/tui/settings/model.go` (rewrite)**

```go
type Model struct {
    form    *huh.Form
    cfg     config.Config   // mutated in place by field callbacks, same as today
    workingDir     string
    projectCfgPath string
    footer  string
    width    int
    height   int
    aborted  bool
    saved    bool
    loadedCfg config.Config // result of reload after save
}
```

- `New(cfg, workingDir, projectCfgPath)` builds a single `huh.NewGroup(...)` with these fields, mirroring the current field order and callbacks exactly:
  1. `huh.NewSelect[string]` — "Default profile", options = sorted profile names, value = `&cfg.Profile.Default`.
  2. `huh.NewInput` (read-only-ish display) — "Preset", value = presetName (non-editable; use a disabled/select with one option, or an Input with `Validate` rejecting change — simplest is a `huh.NewSelect[string]` with a single option).
  3. `huh.NewInput` — "Provider", value bound via `Value(&provider)` with an `OnChange`/callback that writes back to the active preset or `cfg.Agent.Provider` (replicate `m.activePresetName()` logic).
  4. `huh.NewInput` — "Model", same dual-target writeback.
  5. `huh.NewConfirm` — "Local only" (bound to a local `localOnly` bool, callback writes to preset).
  6. `huh.NewConfirm` — "Remote providers allowed" (`&cfg.Privacy.RemoteProvidersAllowed`).
  7. `huh.NewInput` with numeric `Validate` — "Max tool iterations" (bind to `&cfg.Agent.MaxToolIterations`, validate `>=1`).
  8. `huh.NewInput` numeric — "Max retries".
  9. `huh.NewInput` numeric — "Shell timeout".
  10. `huh.NewInput` numeric — "Max shell output".
  11. `huh.NewConfirm` — "Allow network" (`&cfg.Tools.Shell.AllowNetwork`).
  12. `huh.NewConfirm` — "Allow sudo".
  13. `huh.NewConfirm` — "Allow destructive".
  14. `huh.NewConfirm` — "Auto-approve shell".
- Form options: `huh.WithTheme(warmSunsetTheme())`, `huh.WithWidth(frameWidth)`, `huh.WithShowHelp(true)`.
- `SubmitCmd` set to a command returning `SavedMsg` after running `config.SaveProjectConfig` + `config.Load` (lift `saveCmd` logic). On save error, set `footer` and return nil (form stays open) — replicate current `saveCmd`.
- `CancelCmd` set to a command returning `CancelledMsg{}`.
- Keymap: override `huh.NewDefaultKeyMap()` so `keymap.Quit` does **not** match Esc/Ctrl+C (settings already gates Ctrl+C at the parent level and uses Esc for cancel via the form's own abort path). Wire Ctrl+S as the submit trigger by setting `keymap.Submit` appropriately, OR keep the current `ctrl+s` handling in the parent (`model.go` already special-cases nothing for settings — today settings handles ctrl+s internally via its own `Update`). Simplest: configure the form's keymap `Submit` binding to `ctrl+s` so Enter navigates fields and Ctrl+S submits, matching current UX.
- `SetSize(w,h)` calls `m.form.WithWidth(...)`/`WithHeight(...)` using the same `frameWidth = min(60, w-4)` clamped to `[30,60]` as today.
- `Update(msg)` delegates to `m.form.Update(msg)`; after each update, if `m.form.State == huh.StateCompleted` return `SavedMsg`; if `== huh.StateAborted` return `CancelledMsg`. The parent already handles both messages.
- `View()` returns `m.form.View()` (huh renders its own group box). Drop the manual box-drawing in `view.go` entirely.
- `Init()` returns `m.form.Init()`.

**Delete:** `field.go`, `view.go` box-drawing helpers (`frameTitle` etc.), the `field` interface and all field types. Keep `messages.go` (`SavedMsg`/`CancelledMsg`) unchanged so the parent integration is untouched.

**Parent integration (`internal/app/tui/model.go`):** unchanged. It already does `m.settingsModel, cmd = m.settingsModel.Update(msg)` and handles `settings.SavedMsg`/`CancelledMsg`. The `SetSize` call in `WindowSizeMsg` and the two `settings.New(...)` call sites stay. The `settingsModel` field type stays `settings.Model` (now a wrapper).

**Tests to update:**
- `settings/model_test.go`: `TestNewModelHasFields` (now check `m.form` non-nil), `TestSettingsExposeAgentAndToolFields`/`TestSettingsViewKeepsFrameBounded` — re-assert against huh's rendered output (labels still appear; width bound now depends on huh's box width — relax the `<=60` assertion to match huh's actual rendered width, verified at implementation time). `TestCancelReturnsCancelledMsg` — trigger via Esc producing `StateAborted`.
- `settings/field_test.go`: delete (field types gone).
- `settings/navigation_test.go`: delete or rewrite to drive the huh form (Tab still moves focus in huh; arrow keys still work on selects). The `> Field:` focus marker assertion changes to huh's focus rendering — verify the actual glyph huh uses and update assertions, or assert on field labels being present rather than the `>` prefix.
- `settings/ansi_test.go`: review and adjust/remove.

### Phase 2 — Approval chooser (inline)

**New file: `internal/app/tui/approval.go`**

```go
type approvalModel struct {
    form    *huh.Form
    tc      *session.PendingToolCall
    sb      session.SandboxInfo
    allowNet bool
    choice  string  // "approve" | "deny" | "edit" | "always" | "rollback"
    editValue string
    width   int
}
```

- `newApprovalModel(tc, sb, allowNet, hasBackup, width)` builds a `huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(renderApprovalSummary(...)).Options(opts...).Value(&choice)))` where:
  - `renderApprovalSummary` returns the current `renderApprovalPanel` body (command, risk, sandbox text) as the select's title/description (huh titles wrap).
  - Options: Approve, Deny, Edit, Always allow. Append "Rollback last change" when `hasBackup`.
- Keymap: override so Esc/Ctrl+C deny (map to the form's abort, which yields `StateAborted` → parent sends `UserApprovalDecision{Approved:false}`). Confirm/submit uses Enter.
- `Update` delegates to form; on `StateCompleted`, map `choice` → `UserApprovalDecision`:
  - approve → `{Approved:true}`
  - deny → `{Approved:false}`
  - edit → set `m.choice="edit"`, return a sentinel so the parent enters `editingCommand` mode (see parent changes below)
  - always → `{Approved:true}` + parent calls `m.state.AddSessionRule(tc.Command)`
  - rollback → parent applies rollback then returns (does not close approval; the chooser stays so the user can then approve/deny the original tool)
- `View()` returns `m.form.View()` for inline embedding.

**Parent changes (`model.go`):**
- Add `approvalModel *approvalModel` field (or reuse the existing `PendingApproval` check). When a `PendingToolCall` becomes pending and the model isn't already in `editingCommand` mode, lazily construct `m.approvalModel` on first keypress.
- In the `tc != nil` branch of `Update`: if `m.approvalModel != nil`, route keys to it. When it reports completion:
  - `choice == "edit"`: set `m.editingCommand = true`, prefill `m.input` with `tc.Command` (shell.run) or `tc.Args`, exactly as today. Leave `m.approvalModel` in place (the form is "done" but we keep the pending approval until Enter submits the edit).
  - otherwise send the decision, `SetPendingApproval(nil)`, clear `m.approvalModel`.
- Esc handling in approval context stays deny.
- The `renderApprovalPanel` call in `renderInputArea` (`view.go:96`) is replaced: when not editing, render `m.approvalModel.View()` inline instead.

**Tests to update (`model_test.go`):**
- `TestTUIApprovalBannerAndKeypresses`: rewrite to drive the select (arrow + Enter) instead of `enter`/`d`/`e` single keys. The edit sub-test now selects "Edit" then types into the textarea.
- `TestGlobalKeysDoNotLeakDuringApproval`: ensure Tab/Ctrl+keys still don't dismiss approval — they're consumed by the huh select.
- `TestEscDuringApprovalDenies`: Esc on the form aborts → deny.
- `TestApprovalBannerHasSingleBorder`/`TestApprovalRendersInlineInChat`: adjust the `⚠ Approval needed` assertion to the new rendered title; keep the inline (transcript-visible-above) assertion.
- `TestPolishedApprovalStateShowsCommandReasonRiskAndActions`: move the command/risk/sandbox text into the approval summary shown as the select title.

### Phase 3 — Question prompt (inline)

**New file: `internal/app/tui/question.go`**

```go
type questionModel struct {
    form   *huh.Form
    q      *session.PendingQuestion
    answer string
    width  int
}
```

- `newQuestionModel(q, width)` builds `huh.NewForm(huh.NewGroup(huh.NewInput().Title(q.Question).Value(&answer).Prompt("❯ ")))`.
- Keymap: Esc/Ctrl+C → abort (sends empty string, i.e. "declined"), Enter submits.
- `Update` delegates; on `StateCompleted` send `answer` to `q.ResponseChan`; on `StateAborted` send `""`.
- `View()` returns `m.form.View()`.

**Parent changes (`model.go`):**
- Add `questionModel *questionModel` field. In the `PendingQuestion` branch, construct it lazily on first keypress and route keys to it; on completion clear `m.state.SetPendingQuestion(nil)`, reset input placeholder, clear `m.questionModel`.
- `renderInputArea`: when `PendingQuestion()` non-nil and `m.questionModel` set, render `m.questionModel.View()` inline (replaces `renderQuestionPanel` + the inline `m.input.View()` prompt line).

**Tests to update:**
- `TestPendingQuestionEnterSubmitsAnswer` / `TestPendingQuestionEscDeclines`: drive the huh input (type + Enter / Esc).

## Shared theme

**New file: `internal/app/tui/huhtheme.go`**

```go
func warmSunsetTheme() *huh.Theme {
    t := huh.ThemeCharm()
    // retune t.Form/Field/... colours to coral/gold/teal/dim
    return t
}
```
Used by settings, approval, and question models.

## Validation plan

1. `go build ./cmd/marshal` succeeds (CGO_ENABLED=1).
2. `go test ./internal/app/tui/...` — all updated tests pass; deleted field tests are removed.
3. `go test ./...` — no regressions in `app_test.go` (which uses `settings.SavedMsg`) or session tests.
4. `go vet ./...` and `gofmt -w .` clean.
5. Manual smoke test: run `marshal`, open settings (`Ctrl+O`), edit fields, Ctrl+S saves and reloads; trigger a shell approval (type a request that runs a command), exercise approve/deny/edit/always/rollback; trigger an agent clarifying question.

## Risks

- **huh rendered width** may differ from the hand-rolled 60-cell frame; `TestSettingsViewKeepsFrameBounded`'s `<=60` cap may need relaxing. Verify actual width at implementation time.
- **Numeric Input** in huh has no native int field; numeric fields use string `Input` with `Validate`/`OnChange` parsing. Must replicate the current clamp behaviour (`intFieldWithBounds`) in the validate function.
- **Preset read-only display** — the current "Preset" label field is non-editable. huh has no read-only field; use a single-option `Select` or an `Input` with a `Validate` that rejects all changes (returning an error keeps the user stuck — better to use a one-option Select).
- **Edit-as-second-step** means the huh form completes (StateCompleted) but the parent keeps the pending approval open while the textarea captures the edit. Ensure `m.approvalModel` isn't re-built on subsequent keypresses while editing.
- **Esc semantics** — huh's default quit (Esc/Ctrl+C) aborts the form. Both approval and question want Esc to deny/skip (not quit the app) and Ctrl+C must still quit the whole app (handled at the parent level before routing to the form, as today). The parent already intercepts Ctrl+C at the top of `Update`; ensure that interception stays above the form routing.
- **Dynamic preset refresh** — the existing known issue (changing Default profile doesn't refresh the Provider/Model/Local-only fields) remains unless we adopt huh's `OptionsFunc`/`TitleFunc` dynamic-form machinery. Out of scope for this migration unless trivial; document as a follow-up.

## Open questions (none blocking)

- Whether to adopt huh's `OptionsFunc` to fix the preset-refresh bug is left as a follow-up, not part of this plan.