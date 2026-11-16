# F8 plan: cutover — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: branch `f0-foundations` at `52221495`. Runs after every other plan in
this directory, and only when `docs/studio-flutter/route-parity.md` has no
unmapped route.

Approved design: [f8-cutover.md](../f8-cutover.md).

**How this plan is written.** No implementation code; steps are prose.
Tasks 1–3 ship in one release; Tasks 4–5 ship in the next, and only if no
defect that blocks work is open against the Flutter Studio.

## Goal

- The Flutter Studio is served at `/`; old links still open the right
  place.
- No Svelte, TypeScript or Node remains; a release builds the Studio with
  Flutter alone.

## Non-goals

- New features. Native mobile builds. The status page.

## Assumptions

- `web/bridge/assets.go` embeds `static` (Svelte) and, since F2,
  `static-next` (Flutter) served under `/next/`.
- The Svelte routes are declared in `web/ui/src/lib/routes.ts`.
- CI's web job runs `npm ci`, `npm test`, `npm run build` and an
  untracked-file check on `web/bridge/static`
  (`.github/workflows/go-checks.yml`); `scripts/e2e/run_browser_checks.sh`
  drives the Svelte Studio; `web/bridge/contract/tsgen` generates
  `web/ui/src/lib/contract.ts`.

## Decisions this plan makes beyond the spec

- The trial is one release.
- The ceiling in the spec is measured with the browser's network panel on
  the fixture bridge: compressed bytes on first load, Flutter against
  Svelte.

## Execution

- Branch `f8-1-swap` for Tasks 1–3 and `f8-2-removal` for Tasks 4–5.
- Commit each task as `<branch>: task N — <title>`, explicit paths only.

## Contracts

```dart
// model/route_translation.dart
/// The new place for a Svelte hash route, or null when it is unknown.
Place? translateLegacyHash(String hash);
```

## Task 1 — old links

**Files:** `app/lib/src/model/route_translation.dart`,
`app/lib/src/model/place.dart`, `app/lib/src/ui/shell/shell.dart`,
`app/test/model/route_translation_test.dart`.

1. `translateLegacyHash` maps every route `routes.ts` parses: `#` and
   `#fleet` to Overview; `#inbox`; `#chat/<id>` and `#sessions` to the
   thread and to Overview; `#factory…` and `#projects/<root>…` to the work
   item, the project page and its tabs; `#runs/<id>` to the run's thread;
   `#live` to Overview; `#workspaces…`, `#settings/…`, `#usage…`,
   `#watches`, `#library/…` to their sections; `#new` and `#factory/new`
   to the new-work dialog; `#roadmap/…` to the section where the planned
   row lives.
2. On start in the browser, a hash is translated and replaced with the new
   path. An unknown hash opens Overview with one line saying the link is
   out of date.
3. Tests: one case per route in `routes.ts`, generated from a table in the
   test; `an unknown hash opens overview with a notice`.

**Verify:** `cd app && flutter test test/model/route_translation_test.dart
&& flutter test --platform chrome test/model/route_translation_test.dart`.

## Task 2 — the swap

**Files:** `web/bridge/assets.go`, `assets_test.go`,
`.github/workflows/go-checks.yml`, `.github/workflows/release.yaml`,
`AGENTS.md`.

1. Serve the Flutter build at `/` and the Svelte build at `/classic/`, each
   with its index fallback; `/next/…` redirects permanently to `/…`. The
   Flutter build's base href becomes `/`.
2. Tests: `TestRootServesFlutter`, `TestClassicServesSvelte`,
   `TestNextRedirectsToRoot`, `TestNoAPIRouteIsShadowed`.
3. Record in the spec: `webbridge` binary size before and after, compressed
   first-load bytes for both Studios, and time to first usable screen on a
   phone. If Flutter's first load is more than three times Svelte's, stop
   after this task and raise it.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -count=1`.

## Task 3 — end-to-end coverage moves

**Files:** `scripts/e2e/`, `app/integration_test/`,
`docs/studio-flutter/f8-cutover.md`.

1. In the spec, table every check in `scripts/e2e/checks_spa.py` and the
   browser suite: re-expressed against the Flutter Studio (naming the new
   test), already covered by a phase's integration test (naming it), or
   dropped with a reason.
2. Write the re-expressed checks. The checks that drive the bridge's HTTP
   directly are untouched.

**Verify:** `python3 scripts/e2e/run_e2e.py` per `scripts/e2e/README.md`,
and `cd app && flutter test integration_test -d linux`.

## Task 4 — removal

**Files:** delete `web/ui/`, `web/bridge/contract/tsgen/`, the Svelte
contents of `web/bridge/static/`, `scripts/e2e/run_browser_checks.sh` and
its Playwright files; edit `web/bridge/assets.go`, `assets_test.go`, both
workflows, `.gitignore`, `scripts/test.sh` if it names the web UI.

1. `static` now holds the Flutter build; `static-next` and `/classic/` are
   removed. `/next/…` still redirects.
2. CI's web job becomes: build the Flutter web output into
   `web/bridge/static`, apply the same untracked-file check, then build
   `webbridge`. Remove every Node setup step.
3. Add a CI step that fails if any `package.json` or `*.svelte` file is
   tracked.
4. Tests: `TestClassicIsGone`; `TestRootServesFlutter` still passes.

**Verify:** `CGO_ENABLED=1 go build ./cmd/webbridge && CGO_ENABLED=1 go
test ./web/... -count=1`; `git ls-files '*.svelte' 'package.json'
'**/package.json'` prints nothing.

## Task 5 — documents and the mock

**Files:** `AGENTS.md`, `docs/ARCHITECTURE.md`,
`docs/web-studio/README.md`, `docs/web-studio/design.md`,
`docs/testing-ci-audit.md`, `app/README.md`,
`docs/studio-flutter/design.md`, `programme.md`; delete
`docs/studio-flutter/mock/`, `screenshots/`, `spikes/`.

1. `AGENTS.md`: commands, the map, the web control plane section (no
   `web/ui`, no `npm`, no `tsgen`), the native app entry.
2. The web-studio documents gain a header: the Studio's UI is described in
   `docs/studio-flutter/`; their page specifications are history; their
   backend and contract sections stay current.
3. `design.md` loses its mock section and gains screenshots of the real
   app at the three widths, light and dark.
4. Mark the programme complete, with the follow-on: native mobile builds.

**Verify:** `scripts/test.sh quick`; `cd app && flutter analyze && flutter
test`; every relative link in `docs/studio-flutter/` resolves.

## Done when

- A tagged release contains no Svelte and no Node step.
- A bookmark to any old hash route opens the matching place.
- The end-to-end table has no unaccounted check.
- The measurements are recorded and within the ceiling, or the exception
  was decided.

## Needs a Mac

- Build and launch the release app bundle; confirm the tray and the Studio
  window.
