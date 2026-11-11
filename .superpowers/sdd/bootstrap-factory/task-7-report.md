# Task 7 implementation report

## Result

Implemented the responsive factory list, new-draft form, and work-item detail on the existing Svelte UI. The detail keeps human decisions first, followed by CI failures, compact passing checks, reports, PR/agent links, and durable activity. Drafts require a separate approve-and-start action. Retry includes added allowance, optional model routes, and an explicit replacement plan/spec/reviewer-instructions section. Recovery attestation requires a named worker, explanation, and an unchecked confirmation. Stop remains unconfirmed until the backend reports it.

The routes are `#factory`, `#factory/new`, and `#factory/<encoded-id>`. Project links use `#factory/new?project=<encoded-root>` as an explicit query for form defaults. API types and payload keys follow `web/bridge/factory_types.go`. The client retains a request ID when retrying an identical failed mutation, generates a new one for changed input, and refreshes on a stale-version conflict. Factory polling is 2 seconds active, 15 seconds idle, suspended while hidden, refreshed on return, serialized, and backed off on errors.

## Red / green

- Red: the initial named test run failed because `factory.ts` and route parsing were missing. The first expanded poller test then caught an incorrect error-backoff cap; it was fixed.
- Green: `npm test -- src/lib/factory.test.ts src/lib/routes.test.ts src/lib/api.test.ts src/views/Factory.test.ts src/views/FactoryNew.test.ts src/views/FactoryItem.test.ts` passed: 6 files, 129 tests.
- `npm run build` passed and wrote the embedded assets under `web/bridge/static/`.
- `git diff --check` passed.
- `npx svelte-check --tsconfig ./tsconfig.json` reports six existing type errors in Chat, Run, and older tests plus one IssuePicker warning. It reports no factory file diagnostics; this is not a passing repository-wide typecheck.

## Browser proof

Flow: `#factory/demo` -> inspect exhausted decision and failed CI -> expand replacement plan; then load `#factory` and `#factory/new`. Local Vite at `http://127.0.0.1:5173/`, with all `/api/` responses mocked by Playwright. The Browser skill was read, but no callable Browser `js` tool was exposed, so the brief's allowed Playwright fallback was used. No live token, repository, or runtime was used.

- 390px: document width 390px, no horizontal overflow, no page errors, failure visible, replacement field visible after checking the control; 25 focusable controls. Log panel scrollHeight 2560px within 240px viewport.
- 1440px: document width 1440px, no horizontal overflow, no page errors, same decision interaction; 32 focusable controls. Log panel scrollHeight 1072px within 240px viewport.
- Factory list and create form rendered at both widths without page errors.
- Screenshots: `/tmp/factory-390.png`, `/tmp/factory-1440.png`. Mock script: `/tmp/factory-browser.cjs`.

## Self-review and limits

The UI uses the existing Card, Button, and Tag controls and the existing app shell. The command form disables empty answers, missing retry reasons/allowance, incomplete replacement plans, and unconfirmed recovery attestation. Long plan/spec and CI logs wrap or scroll inside their panels. The detail page refreshes after HTTP 409 before another command attempt.

The browser run uses mocked API data, so it does not establish real forge, worker, or credential integration. Tests cover the main component states and identity/visibility rules, but do not exhaustively render every decision kind or perform an end-to-end backend mutation. JavaScript numbers represent Go `uint64` versions and sequences; the backend's practical values are expected to stay below the safe-integer limit in this UI.

## Review fix round 1

Addressed the three Important findings in `task-7-review.md`:

1. Retry model profile changes now live inside `replacement.config`. The replacement copies the latest approval config and plan/spec by default, so changing only a route still creates a complete new approval revision. The form explains that this revision is recorded before work resumes. No top-level route fields are sent.
2. Recovery displays the exact last `agentId`. The attestation button requires that ID to be typed exactly, an independent confirmation explanation, and a deliberately checked confirmation. The command reason includes the validated ID required by the bridge.
3. The engineering report renders reviewer findings with severity, title, path, line and body, plus implementation/reviewer questions and reasons.

The bounded Minor fixes show environment, reviewer instructions and implementer auto permission in the draft preview; tests also cover ordinary agent drill-in and empty versus unavailable CI evidence.

Verification: `npm test -- src/lib/factory.test.ts src/lib/routes.test.ts src/lib/api.test.ts src/views/Factory.test.ts src/views/FactoryNew.test.ts src/views/FactoryItem.test.ts` passed (6 files, 134 tests). Covering behavioral tests in `FactoryItem.test.ts` assert the actual retry replacement payload, absence of ignored top-level route fields, exact recovery ID and reason, findings content, agent link, CI distinctions and preview settings. `npm run build` passed and wrote `web/bridge/static/assets/index-5jf3NPZa.js` with updated `index.html`. Browser smoke with mocked `/api/` data passed at 390px and 1440px: no horizontal overflow or page errors, the replacement form opened, findings appeared, long logs scrolled inside 240px panels, and list/new views loaded. Updated screenshots: `/tmp/factory-390.png`, `/tmp/factory-1440.png`. Browser plugin `js` remained unavailable, so the same Playwright fallback was used. No live credentials were used.

Follow-up typecheck after the fix: `npx svelte-check --tsconfig ./tsconfig.json` reports the same six preexisting errors and one warning outside the factory files; no factory diagnostics remain. The focused UI command was rerun after correcting test fixture typings: 6 files and 134 tests passed.
