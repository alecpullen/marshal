# Regular Subagent Model Selection, Live Drill-Down, and TUI Question Answers

## Status

Approved design; implementation plan to follow after written-spec review.

## Goals

Improve regular Marshal-mode subagents in three connected areas:

1. Show the selected provider/model in the `agent.run` transcript entry and
   subagent card while the child is running and after it finishes.
2. Permit `agent.run` to request any configured provider/model pair, defaulting
   to the parent/base model when no model is supplied.
3. Make regular-mode subagent cards clickable while running or completed,
   displaying the live child transcript with the existing drill-down and
   return behavior.
4. Fix terminal TUI question handling so listed choices are submitted as the
   selected choices; only the explicit “Other” choice requests free text.

## Non-goals

- No change to ACP/HTTP question behavior.
- No web UI redesign or web-only question behavior change. Shared code may be
  changed only when required by the TUI fix and covered by existing behavior.
- No new authorization policy beyond configured provider/model validation.
- No nested subagents; the existing depth and concurrency limits remain.

## Design

### `agent.run` request and routing

The tool accepts an optional `model` string in `provider/model` format:

```json
{
  "prompt": "...",
  "description": "...",
  "model": "provider/model"
}
```

When omitted, the child uses the current base model. When supplied, the
factory resolves the requested pair against configured providers and models.
Any configured pair is permitted; an unknown provider or model returns a
clear `agent.run` error before starting the child. Named custom agents remain
supported. If both `agent` and `model` are supplied, the explicit model takes
precedence while the named agent's other settings remain in effect.

The factory input will carry the optional model request rather than encoding
it as a synthetic custom agent. The resolved route metadata will be retained
on `SubagentView` so rendering can display the actual provider/model selected,
including fallback information where applicable.

### Transcript rendering and live drill-down

Regular-mode subagent registration will use the same child `session.State`,
broker events, and view metadata already used by SDD/pipeline subagents. The
transcript tool line/card will include the resolved provider/model while the
child is running and after completion.

Subagent click regions and the existing view stack will be mode-independent:

- A regular subagent card with a child state is clickable in either status.
- Clicking enters the child transcript immediately, including while running.
- Broker/state refreshes continue to update the drilled-in transcript live.
- Escape and the existing “up” navigation return to the parent transcript.
- Existing Ctrl+F behavior for entering the latest running child remains.

The existing SDD behavior must remain unchanged.

### Terminal TUI question answers

The TUI answer model will preserve the semantic distinction between listed
choices and free text:

- A single-choice selection submits the selected option value directly.
- A multi-choice selection submits the selected option values.
- Selecting the displayed “Other” option opens/uses the custom input and
  submits its trimmed text.
- The “Other” sentinel is never submitted as the answer for a predefined
  option.

The fix is confined to terminal TUI behavior unless the faulty path is shared
and changing it is necessary to preserve the same semantics elsewhere.

## Error handling

Invalid `provider/model` values fail validation before child registration or
execution and include the requested value plus the reason it is unavailable.
Existing child-construction, execution, depth, concurrency, and completion
errors retain their current propagation behavior.

## Testing

Add focused regression coverage for:

- `agent.run` schema/argument parsing with and without `model`.
- Base-model fallback, valid explicit routing, and invalid-pair errors.
- Provider/model metadata on running and completed regular subagents.
- Regular-mode click-to-drill while running, live child updates, and Escape/up
  return to the parent.
- TUI single-choice, multi-choice, and “Other” answer finalization.
- Existing SDD drill-down and question behavior remaining intact.

Validation will run focused package tests first, followed by the relevant
broader Go test suite.