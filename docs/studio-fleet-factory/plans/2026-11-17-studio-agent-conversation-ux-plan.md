# Studio shared agent conversation UX implementation plan

> **For agentic workers:** Use the repository's `marshal-executing-plans` workflow to execute this approved plan.

**Status:** approved by the user on 2026-11-17.

**Goal:** Make Marshal's shared full and embedded agent conversations easier to follow and control on desktop and phone.

**Architecture:** Refine the existing `AgentConversation`, transcript components, composer and pending-request surfaces in place. Reuse the existing session store, ACP stack, interaction authority and child drill-down; integrate new subagent controls only when their separate plan has delivered them.

**Tech stack:** Svelte 5, TypeScript, Tailwind, Vitest, Playwright/axe and existing isolated session fixtures.

**Spec:** [shared agent conversation UX](../studio-agent-conversation-ux-spec.md); dependencies: [visual foundation](../studio-visual-foundation-spec.md), [work-item experience](../studio-work-item-experience-spec.md), and the separately planned [worker subagents](2026-11-16-studio-worker-subagents-plan.md).

**Base:** `origin/main` at `47fa29d7`; execute after the visual foundation and work-item plans. Reconcile current daemon reconnect and subagent work before editing. If the subagent plan is not implemented, preserve current drill-down and leave child steering to that plan; do not fabricate it or block this work.

**Non-goals:** new agent backend, new ACP child-control protocol, replacing the transcript data model, changing authority, enabling unsupported provider capabilities or auto-resending uncertain prompts.

**Global constraints:** Keep existing event ordering, stack/legacy fallbacks, keyboard browsing and copy, density, accessible controls, route/back behavior, request IDs and permission semantics. Unconfirmed, unknown and failed operations must not be presented as completed. No developer credentials or live services in tests.

## Contracts

Add these pure view helpers to new `web/ui/src/lib/session/presentation.ts`. Import `AccessState` from `../interaction`.

```ts
import type { AccessState } from '../interaction'
export interface SessionPresentation {
  connectionLabel: string
  accessLabel: string
  tone: 'neutral' | 'info' | 'success' | 'warn' | 'error'
}
export function presentSession(connected: boolean, access: AccessState | null): SessionPresentation
export type SendOutcome = 'accepted' | 'failed' | 'unknown'
export function describeSendOutcome(outcome: SendOutcome): string
```

`presentSession` reports disconnected as reconnecting and null access as unavailable, never defaulting to Ask/Act or treating requested permission as confirmed. `SendOutcome` is a presentation classification only: accepted means acknowledged by the existing seam, not consumed or completed; an ambiguous transport loss is unknown. The underlying owner remains responsible for determining the actual result.

## Task 1: Session context and connection clarity

**Goal:** Both Fleet and factory users can identify the session, its parent/context, connection and reported authority.

**Files:** new `web/ui/src/lib/session/presentation.ts`, new `web/ui/src/lib/session/presentation.test.ts`, `web/ui/src/lib/session/AgentConversation.svelte`, `web/ui/src/lib/session/AgentConversation.test.ts`, `web/ui/src/views/Chat.test.ts`.

**Steps:**

1. Implement/test `presentSession` for connected/disconnected and every existing access state, including null. Reuse the existing access label helper from `interaction.ts` rather than duplicating permission semantics.
2. Refine `AgentConversation.svelte`'s header using the foundation's responsive layout: show name, project/branch/workspace where reported, parent/work-item link in embedded context, readable connection and actual access. Keep existing `ModeSwitcher`, density, `StatusShare`, review/network and open-full actions, and `onBack`/`onOpenFull` callbacks. Long names may truncate visually only if the full text remains accessible.
3. Test full and embedded modes, unknown access, disconnected state, long names, route/Back and keyboard focus. Assert no action posts merely from rendering status.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/session/presentation.test.ts src/lib/session/AgentConversation.test.ts src/views/Chat.test.ts` passes.

## Task 2: Transcript hierarchy and preserved position

**Goal:** Transcript rows are easier to scan without losing information, order, density or browsing behavior.

**Files:** `web/ui/src/lib/transcript/Transcript.svelte`, `web/ui/src/lib/transcript/MessageNode.svelte`, `web/ui/src/lib/transcript/ToolRow.svelte`, `web/ui/src/lib/transcript/StepHeader.svelte`, `web/ui/src/lib/session/AgentConversation.svelte`, `web/ui/src/lib/transcript/Transcript.test.ts`, `web/ui/src/lib/session/AgentConversation.test.ts`, `web/ui/src/views/Chat.test.ts`.

**Steps:**

1. Add fixture tests for user/agent/tool/error/completion rows, long output, density levels, child drill and legacy fallback. Assert the same ordered node IDs, full text and copy/detail access before adjusting presentation.
2. Apply foundation typography, spacing and subtle grouping to the existing transcript components; give user, agent, tool and error rows visible labels or accessible names without relying only on color. Do not hide reasoning, reports or detail already available at the selected density; preserve `flattenVisible`, `browseKey`, overrides and node identity.
3. Preserve `pinned`, `onScroll`, `scrollToLatest` and `jumpToLive` semantics in `AgentConversation`. Add tests that new events while scrolled up don't move the reader, show Jump to latest, and a child drill/back returns to its prior parent cursor and scroll. Keyboard browsing should not intercept typing or ordinary selection/copy.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/transcript/Transcript.test.ts src/lib/transcript/browse.test.ts src/lib/session/AgentConversation.test.ts src/views/Chat.test.ts` passes.

## Task 3: Composer outcome and cancellation clarity

**Goal:** Prompt, steering, cancellation and failed/uncertain sends are explained while preserving the user's text.

**Files:** `web/ui/src/lib/Composer.svelte`, `web/ui/src/lib/session/AgentConversation.svelte`, `web/ui/src/lib/store.ts`, new `web/ui/src/lib/Composer.test.ts`, `web/ui/src/lib/store.test.ts`, `web/ui/src/lib/session/presentation.ts`, `web/ui/src/lib/session/presentation.test.ts`.

**Steps:**

1. Add tests for successful prompt and active-turn steer, budget refusal, explicit rejection, transport loss after a possible dispatch, cancellation requested versus terminal stop, and nonempty text after every failure. `describeSendOutcome` must label accepted as acknowledged only and unknown as requiring inspection rather than automatic retry.
2. In `Composer.svelte`, catch and display non-budget errors without clearing text or throwing an unhandled promise rejection. Keep `onSend`/`onCancel` and Enter/Shift+Enter semantics. Label an ambiguous send `Outcome unknown — check the conversation before retrying` and require a deliberate user action to send again; do not automatically resubmit.
3. In `store.ts`'s `actions.prompt`, do not leave an unqualified optimistic success when `api.promptSession` fails: retain the text visibly as failed/unknown according to the actual response and reconcile the authoritative session on reconnect through existing reads. Do not assume transport failure proves the server did not receive it. Preserve the existing wire payload; no new idempotency promise. In `AgentConversation`, show a stop request separately from a confirmed terminal event using existing session state.
4. Verify the composer remains reachable within 390 × 600 and no keyboard shortcut stops a turn while the user is copying selected text. Preserve subagent steering as a separate capability owned by its dedicated plan.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/Composer.test.ts src/lib/store.test.ts src/lib/session/presentation.test.ts src/lib/session/AgentConversation.test.ts` passes.

## Task 4: Pending requests and capability-aware drill-down

**Goal:** Existing pending approvals/questions remain reachable and attributable in full and embedded sessions.

**Files:** `web/ui/src/lib/PermissionModal.svelte`, `web/ui/src/lib/QuestionModal.svelte`, `web/ui/src/lib/session/AgentConversation.svelte`, `web/ui/src/lib/store.ts`, `web/ui/src/lib/PermissionModal.test.ts`, `web/ui/src/lib/session/AgentConversation.test.ts`, `web/ui/src/views/Chat.test.ts`.

**Steps:**

1. Add tests for simultaneous or sequential requests, switching into a child, unsupported child transcript, and failed/uncertain answer delivery. Assert exact existing tool-call/question IDs and that one request never resolves another.
2. Improve the existing modals' visible context and focus/return path; keep the existing backend payloads and permissions. Do not clear a pending request on a known failed submission; on ambiguous delivery refresh the current pending snapshot and report unknown until reconciled, without inventing a successful resolution or automatically sending a second answer. Preserve any user-entered answer where the current request ID is unchanged.
3. Retain `drillInto`, `popDrill`, per-child stack stores and unavailable-transcript behavior. If separately implemented subagent capabilities exist on the execution base, use their documented components and explicitly label unsupported controls; otherwise do not introduce mock controls. Do not treat an external ACP backend as equivalent to Marshal without capability evidence.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/PermissionModal.test.ts src/lib/session/AgentConversation.test.ts src/views/Chat.test.ts src/lib/store.test.ts` passes.

## Task 5: Full and embedded workflow proof

**Goal:** The same conversation experience works from Fleet and Factory at phone and desktop sizes.

**Files:** new `web/ui/e2e/agent-conversation-ux.pw.ts`, `web/ui/e2e/fixtures.ts`, `docs/studio-fleet-factory/studio-agent-conversation-ux-spec.md`.

**Steps:**

1. Add deterministic session/stack fixtures for a full Fleet session and an embedded factory worker, with user/agent/tool rows, a child drill, pending request and reconnect. Assert context, order, scroll retention, accessible Back, exact request target and no duplicate mutation.
2. Exercise failed and ambiguous sends, cancellation acknowledgement and later terminal state, keyboard browse/copy and child unavailable fallback. Assert typed content remains recoverable and no unknown send is automatically retried.
3. Run at desktop/tablet/390 × 844/390 × 600 in both themes with axe, reduced motion and 200% zoom. Capture and inspect representative screenshots; append actual fixture, real-provider and physical-keyboard limitations to the spec.

**Verify:** `scripts/e2e/run_browser_checks.sh e2e/agent-conversation-ux.pw.ts e2e/factory-item.pw.ts` passes; `cd web/ui && npm run check && npm test -- --run && npm run build` passes. From the root run `go build ./cmd/webbridge` and `go test ./web/bridge -run TestWebIsStdlibOnly`.

## Final verification

Run `scripts/e2e/run_browser_checks.sh`, `go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`, `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent` and `git diff --check`. Report live-agent, external ACP and actual mobile-keyboard coverage separately. Commit each verified task as `studio-agent-conversation-ux: task N — <title>`.
