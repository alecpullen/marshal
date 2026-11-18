# Studio shared agent conversation experience

Status: product direction approved 2026-11-17; Flutter port proposed for review 2026-11-18. See the linked implementation plan.

## Flutter port constraints

The target is Flutter `app/`, not the retiring Svelte Studio. Follow the [normative Flutter design system](../studio-flutter/design-system.md), existing kit and UI/model/client boundaries. The implementation plan identifies current screens and tests. Recheck in-flight changes before implementing and preserve already-working behavior; fixture evidence is not a live-service claim.

## Intent and scope

Make the shared Marshal session conversation readable and controllable across Fleet and Factory on desktop and phone. Build on the foundation's responsive header and the separate work-item and worker-subagent plans. This slice improves presentation and accessibility around existing session data and commands; it does not implement a new runtime, new child steering protocol or unsupported ACP capability.

## Conversation and context

Show a concise session identity, connection state, actual interaction authority and parent/work-item context where the existing route and state supply them. Unknown or unconfirmed access must say so; never guess a mode or imply a session is connected when it is reconnecting. Keep density controls, full/embedded navigation, session detail and existing child breadcrumbs consistent with the TUI's shared concepts. Long identities and paths remain inspectable without clipping essential controls.

Make the transcript easier to scan with consistent grouping and clear distinction among user messages, agent reasoning/steps, tool activity, delegated work, reports and errors. Preserve the existing underlying transcript tree, content visibility, density controls, event ordering and copy/detail behavior. The user can scroll back without incoming updates pulling them away, then jump to the latest message. A pending request or error remains visible and reachable without confusing it with a completed action. Existing TUI-style keyboard browsing remains available; text editing and ordinary copy shortcuts must continue to work.

Keep the composer reachable on a phone-sized screen and a constrained viewport representing an on-screen keyboard. Clearly distinguish submitting a new prompt, steering an active turn, requesting cancellation, and confirmed stopped/failed/unknown outcomes according to the existing session seam. Do not claim a sent request was consumed or a cancelled turn stopped until the underlying state says so. Preserve unsent text on connection or request failure and do not automatically resend an uncertain prompt.

## Pending interactions and children

Present pending questions, permission requests and connection/recovery states with a direct, accessible route to the relevant action, including from an embedded worker conversation. The current modal and dock interaction contracts remain authoritative. Do not duplicate a pending request or remove an action the user can already take. Preserve exact question/approval IDs and current authority when resolving requests.

The separate worker-subagent plan owns new child visibility, direct user steering, parent notifications and lifecycle behavior. When that work exists, use its shared views and capabilities; do not fabricate child status or provide controls an adapter does not support. If it is not yet merged, ship the conversation improvements against existing drill-down and leave a named integration point for that plan. External ACP sessions may have fewer capabilities; show unsupported or unknown rather than reporting an operation succeeded.

## Validation

Use deterministic session/stack and ACP fixtures for full and embedded conversations. Exercise dense transcripts, long output, subagent drill/back, multiple concurrent pending interactions, keyboard navigation/copy, preserved scroll, disconnected/reconnected/unknown state, user text on failed sends, confirmation of cancellation and mobile composer reachability. Verify existing request IDs and wire payloads, accessibility and dark/light themes, short viewport and reduced motion. Clearly distinguish fixture evidence from a live agent, real keyboard and external ACP-provider testing.
