# Browser checks

Playwright checks for the project and factory settings surfaces. They drive the
SPA the bridge actually serves, against a real bridge with a throwaway project,
so what they observe is the shipped bundle and the shipped API — not a jsdom
stand-in.

```bash
scripts/e2e/run_browser_checks.sh
```

The script builds the SPA, builds `webbridge`, writes a fixture config with
three agent profiles and no real provider, starts the bridge on port 7799 with
an isolated `HOME`/config/data dir, runs the suite at desktop and 390px, and
stops the bridge. `KEEP=1` leaves the fixture directory — the generated config,
the throwaway project and the bridge log — in `/tmp` and prints the path, so a
failure can be inspected after the fact; the bridge itself is always stopped.
`PW_PORT` moves it off 7799.

Prerequisites: `npm install` in `web/ui`, plus the browser:

```bash
cd web/ui && npx playwright install chromium
```

## Why these live beside vitest, not inside it

The specs are `*.pw.ts`, and `web/ui/playwright.config.ts` matches only that
name. vitest's default include picks up `*.spec.ts`, so a browser spec named
`*.spec.ts` would be collected by `npm test` and fail there for having no
browser. `npm test` stays jsdom-only; these run under `npm run test:browser`.

## What is covered

| Case | Surface |
|---|---|
| `role overrides follow the agent default independently and reset` | project settings, model card |
| `a stale save warns, keeps the edit, and offers reload` | `If-Match` / 412 path |
| `factory form shows inherited sources and keeps its input` | handoff review, settings link |
| `no horizontal overflow and readable labels` | both viewports |
| `the handoff form does not overflow either viewport` | handoff form, both viewports |
| `unsaved edits are confirmed before in-app navigation` | draft guard |

No external credentials are used: the fixture provider points at a closed
loopback port and is never called, because every case reads and writes settings
and drafts rather than starting work.
