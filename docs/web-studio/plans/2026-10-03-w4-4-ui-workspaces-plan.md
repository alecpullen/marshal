# W4.4 · UI: workspaces, builds, New agent — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w4-workspaces-design.md`](../specs/2026-10-03-w4-workspaces-design.md) §8.1–§8.2
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** UI. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W3.4](2026-10-03-w3-4-control-pages-plan.md) (previous UI plan) and [W4.3](2026-10-03-w4-3-bridge-secrets-egress-plan.md) (this phase's backend gate), with everything those depend on.
**Base:** a branch containing every plan listed under Runs after. Names from earlier phases are
as their plans define them.
**Plan slug:** `w4-4-ui-workspaces`. Commit each task as
`w4-4-ui-workspaces: task N — <title>`.

## Goal

The UI gets:

- `#workspaces`, a gallery with create actions;
- `#workspaces/<name>/edit`, the designer with Layers, Layers + source,
  and Source views, a side panel and version diff;
- `#workspaces/<name>/builds`, with a live log;
- the New agent workspace chip;
- the workspace shown on the Live wall and the session header.

## Non-goals

- The network inspector, project overview and secrets settings (W4.5).
- Test shell, which is disabled with "W5".

## Assumptions

These W4.2 and W4.3 routes exist:

| Area | Routes |
|---|---|
| Templates | `/api/workspaces` (GET, POST), `/api/workspaces/{name}` (GET with `?version`, DELETE), `…/draft` (PUT), `…/patch` (POST), `…/publish` (POST), `…/diff` (GET), `…/pool` (PUT), `…/ca/rotate` (POST) |
| Builds | `/api/workspaces/{name}/builds` (GET, POST), `…/builds/{n}/events` (SSE) |
| Secrets | `GET /api/secrets/status` |
| Network | `GET /api/network?workspace=&view=hosts` (per-host usage for the network card) |
| Agents | `AgentStatus.workspace`, and the spawn body field `workspace` |

`sse.ts` `connectSSE` (`sse.ts:77`) fetches `/api/events?…` and nothing
else. Task 1 adds an optional `url` to `SSEOptions` (`sse.ts:70`) for the
build stream.

---

## Task 1: API client and the build-log stream

**Goal:** typed functions for workspace routes, and a build-log
subscription.

**Files:**
- `web/ui/src/lib/api.ts`, `api.test.ts`
- `web/ui/src/lib/sse.ts`, `web/ui/src/lib/fleetsse.test.ts` (or a new `sse.test.ts` case)

**Steps:**

1. Add types mirroring W4.1 and W4.2: `WSDoc` (all sections),
   `WSSection`, `WSDiag`, `TemplateMeta`, `TemplateVersion`,
   `WorkspaceListItem` and `BuildsInfo`, which includes
   `starts {coldMs, warmMs}`.
2. Add one function per route in the Assumptions table.
3. Add `url?: string` to `SSEOptions`. When it's set, `connectSSE`
   fetches `${url}?lastEventId=${lastId}` instead of building the
   `/api/events` query. The stream key used for `getLastEventId` is the
   URL. Existing callers are unchanged.
4. Add `connectBuildLog(name, n, {onLine, onDone, signal})`, which calls
   `connectSSE` with `url: /api/workspaces/${name}/builds/${n}/events`.
5. Tests:
   - URLs and bodies;
   - `connectSSE` with a `url` fetches that URL;
   - the build-log parser delivers lines, then done.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/api.test.ts src/lib/fleetsse.test.ts
```

---

## Task 2: Gallery

**Goal:** `#workspaces` lists Studio and repo templates, with create
actions (spec §8.1).

**Files:**
- `web/ui/src/views/workspaces/Gallery.svelte` (new), `Gallery.test.ts` (new)
- `web/ui/src/lib/workspaces/CreateModal.svelte` (new)
- `web/ui/src/App.svelte`, `Rail.svelte`, `routes.ts`

**Steps:**

1. Enable Workspaces ▦ on the rail. `routes.ts` parses:
   - `#workspaces`;
   - `#workspaces/<name>/(edit|builds|network)`.
2. Gallery cards show:
   - the name and a source badge (Studio, or Repo · <project>);
   - the published version, plus a draft-changes badge when the draft
     differs;
   - content chips from the doc (base, toolchains, the package count);
   - usage (the agent count), and a pool badge ("2 warm");
   - the build status dot.

   A click opens the designer.
3. `CreateModal` creates from one of:
   - Blank;
   - a starter (five tiles);
   - "Import devcontainer.json" (pick a project);
   - "Snapshot a running agent" (pick a container agent).

   It posts `POST /api/workspaces {name, from}`, then opens the designer.
   Errors such as the devcontainer `build` refusal show inline.
4. Tests:
   - cards render from fixtures;
   - each create option posts the right `from`;
   - a refusal shows its reason.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/workspaces/Gallery.test.ts && npx svelte-check
```

---

## Task 3: Source editor component

**Goal:** a dependency-free source editor with line numbers, diagnostics,
range highlighting and cursor-to-line reporting.

**Files:**
- `web/ui/src/lib/workspaces/SourceEditor.svelte` (new), `sourceEditor.ts` (new), `sourceEditor.test.ts` (new)

**Steps:**

1. Structure: a `<textarea>` over a `<pre>` mirror. The textarea is
   transparent, with a caret; the mirror shows highlights and diagnostics.
   They share the Geist Mono font and line height, and scroll is synced.
   A line-number gutter sits on the left.
2. Props: `{value, diagnostics: WSDiag[], highlight?: {start, end}, onChange, onCursorLine}`.
   - `onChange` is debounced by 400ms.
   - `onCursorLine` fires on `selectionchange` with the 1-based line.
3. `sourceEditor.ts` holds the pure helpers:
   - `lineOfOffset(text, offset)`;
   - `rangeLines(text, start, end)`;
   - `diagByLine(diags)`.

   Test each.
4. Diagnostics: a gutter marker coloured by severity, with the message on
   hover. The line under the cursor shows its diagnostic text below the
   editor.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/workspaces/sourceEditor.test.ts && npx svelte-check
```

---

## Task 4: Designer — layers, sync and side panel

**Goal:** `#workspaces/<name>/edit`, with nine layer cards, a three-way
view switch, two-way sync with the source, and the side panel
(spec §8.1).

**Files:**
- `web/ui/src/views/workspaces/Designer.svelte` (new), `Designer.test.ts` (new)
- `web/ui/src/lib/workspaces/layers/*.svelte` (new: `BaseLayer`, `ToolchainsLayer`, `PackagesLayer`, `MountsLayer`, `FilesLayer`, `SecretsLayer`, `NetworkLayer`, `ResourcesLayer`, `SetupLayer`)
- `web/ui/src/lib/workspaces/SidePanel.svelte` (new), `designer.ts` (new), `designer.test.ts` (new)

**Steps:**

1. `designer.ts` holds a pure state reducer over
   `{source, doc, sections, diagnostics, selectedLayer, dirty}`:
   - `selectLayer(n)` sets the highlight to that layer's section range;
   - `cursorLine(line)` selects the layer whose range contains the line;
   - `applyServer({source, doc, sections, diagnostics})` replaces
     everything after a patch or parse.

   Tests cover the mapping both ways, and layers 1 and 2 sharing
   `[workspace]`.
2. `Designer.svelte`:
   - On load, `GET /api/workspaces/{name}`, reading the draft when it
     exists, otherwise the published version.
   - The view switch (`Segmented`) is Layers | Layers + source | Source,
     persisted under `marshal.ui.ws.view`.
   - **Layer edits** call `POST …/patch {layer, value}`. The response's
     source replaces the editor text, the changed lines (a diff of old
     against new line arrays) flash briefly, and the card is selected.
   - **Source edits** go through `PUT …/draft {source}` (debounced), and
     the response updates the doc, sections and diagnostics.
   - **Header actions:**
     - Save draft (implicit; shows "saved");
     - Publish, which confirms with the diff from the published version;
     - Build, which runs `POST …/builds` and goes to
       `#workspaces/<name>/builds`;
     - Test shell, disabled with "W5".
3. Layer cards show the section's fields as small forms:
   - **Base:** an image input.
   - **Toolchains:** chips, with an add select (`go`, `node`, `python`,
     `rust`) plus a version.
   - **Packages:** four chip inputs.
   - **Mounts:** rows of a repo or volume select, a target, and a
     read-only toggle. Repos come from `GET /api/repos` (W4.3).
   - **Files:** rows of source and target. Upload is out of scope; the
     note says to place files under the state store.
   - **Secrets:**
     - env rows (`NAME` → ref);
     - inject rows (host → ref, header, format);
     - a warning when `GET /api/secrets/status` reports `env`: "Injection
       needs the local or OpenBao backend".
   - **Network:**
     - a mode `Segmented` and the egress host list;
     - per-host "seen N×" from `GET /api/network?workspace=&view=hosts`;
     - a marked row for each host added from the inspector, when the
       draft differs from the published version.
   - **Resources:** CPU, memory, disk and timeout.
   - **Setup:** a textarea.
   - Each card shows the diagnostics that fall inside its section range.
4. `SidePanel.svelte` sections:
   - **Build:** status, size, cold and warm start (`starts`), and a pool
     size select (0–4) calling `PUT …/pool`.
   - **Verify gate:** "runnable" when the toolchains include the language
     of the project gate command. The command's first word maps through a
     small table (`go`→go, `npm`/`node`→node, `pytest`/`python`→python,
     `cargo`→rust). Otherwise it shows "may be skipped".
   - **Policy:** `[policy]` mode and allow list (editable, layer 0).
   - **History:** the versions list. Selecting two shows the
     `GET …/diff?a=&b=` output with W2.2's `DiffLines`.
   - **CA:** the injected host count and a Rotate button (confirm, then
     `POST …/ca/rotate`).
5. Tests:
   - a layer edit posts the patch and highlights the returned changed
     lines;
   - a cursor move in the source selects the right card;
   - diagnostics render on both the card and the gutter;
   - publish confirmation shows the diff;
   - the pool select posts.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/workspaces/Designer.test.ts src/lib/workspaces && npx svelte-check
```

---

## Task 5: Builds page

**Goal:** `#workspaces/<name>/builds` lists builds and streams the
selected build's log.

**Files:**
- `web/ui/src/views/workspaces/Builds.svelte` (new), `Builds.test.ts` (new)

**Steps:**

1. Left: versions with build status, duration, size and image tag.
   Right: a log viewer (mono font, auto-scroll that stops when the user
   scrolls up).
   - A running build opens with `connectBuildLog`.
   - A finished build reads its replay from the same SSE endpoint, which
     W4.2 serves from the log ring.
2. Cached layers show as dimmed `cached l<k>` lines. A failure highlights
   the last 20 lines and offers **Rebuild**.
3. Tests: list render; log lines append; done updates the status.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/workspaces/Builds.test.ts && npx svelte-check
```

---

## Task 6: New agent workspace chip, and workspace display

**Goal:** New agent offers workspaces, and the session header and Live
wall show them (spec §8.2).

**Files:**
- `web/ui/src/views/NewAgent.svelte`, `NewAgent.test.ts`, `web/ui/src/lib/newagent/newAgent.ts`
- `web/ui/src/views/Chat.svelte` (header), `web/ui/src/lib/live/Tile.svelte`

**Steps:**

1. **Workspace chip.** It lists `GET /api/workspaces`, Studio and repo,
   with the project default (`GET /api/projects/settings?root=`, W4.2)
   first and marked "default".
   - Each entry shows its contents chips, pool status, and the build
     status. An unbuilt entry is disabled with "Build first".
   - It warns "Verify gate may be skipped", using Task 4's
     gate-runnable check against the project's gate.
   - The choice is sent as `workspace` in the spawn body.
   - It is remembered with the other chips.
2. **Session header** (`Chat.svelte`): a `▦ <name>@v<n>` tag, linking to
   the designer.
3. **Live wall tile:** fill W3.3's empty workspace chip from
   `row.workspace`.
4. Tests:
   - the default is preselected;
   - an unbuilt workspace is disabled;
   - spawn sends `workspace`;
   - the header tag renders.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/NewAgent.test.ts src/lib/newagent && npx svelte-check
```

---

## Task 7: Rebuild static

**Goal:** the committed bundle includes W4.4.

**Steps:**

1. Run `cd web/ui && npm run build`.
2. Commit `web/bridge/static`.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../bridge && go test ./ -run 'TestAssets|TestWebIsStdlibOnly'
```

Check by hand:
- create a workspace from the `go-service` starter;
- edit packages in Layers and watch the source update;
- edit the source and watch the card update;
- publish and build, watching the log;
- spawn an agent on it from `#new`.

---

## Final verification

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
```

## Integration notes

- The designer never parses TOML in the browser. Every edit goes through
  the bridge, which calls the control agent. Latency is one round trip per
  debounced edit.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. |
| Anchors verified? | Pre-W1 `sse.ts` `SSEOptions`/`connectSSE` (`:70/:77`) were checked on `2ddc09e`. Everything else comes from W1–W4.3 as defined. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. Test shell is disabled, naming W5. |
| Matches the spec? | §8.1–§8.2. |
