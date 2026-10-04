# W4.1 · Engine: workspace files, trust, policy — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w4-workspaces-design.md`](../specs/2026-10-03-w4-workspaces-design.md) §4
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** backend. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W3.2](2026-10-03-w3-2-bridge-control-plan.md) (previous backend plan), with everything it depends on. Backend plans never depend on UI plans.
**Base:** a branch containing every plan listed under Runs after. Anchors into code from before
W1 were checked on `2ddc09e`. W3's `config/*` methods (`internal/acp/config.go`)
are as W3.1 defines them.
**Plan slug:** `w4-1-engine-workspace`. Commit each task as
`w4-1-engine-workspace: task N — <title>`.

## Goal

The engine:

- parses, validates, patches and formats workspace files;
- serves that through `workspace/*` on the control agent;
- covers repo workspace files in the trust hash;
- applies a workspace policy on `session/new`.

## Non-goals

- Bridge and UI (W4.2–W4.5).

## Assumptions

- `github.com/pelletier/go-toml/v2` is already in `go.mod` (v2.4.2).
- The W3.1 control-agent methods are registered in `registerHandlers`
  without a session. This plan adds `workspace/*` the same way.

---

## Task 1: `internal/workspacecfg` — types and `Parse`

**Goal:** parse a workspace file into a typed doc, line-ranged sections
and diagnostics (spec §4.1–§4.2).

**Files:**
- `internal/workspacecfg/doc.go`, `parse.go`, `parse_test.go` (new)

**Steps:**

1. Define `Doc` (JSON tags in camelCase, TOML tags as in spec §4.1).

   **`Workspace`** holds `Name`, `Base`, `Toolchains []string`, and
   `Extends string`.

   **Sections:**

   | Field | Type |
   |---|---|
   | `Packages` | `{Apt, Go, Npm, Pip []string}` |
   | `Mounts` | `[]Mount{Repo, Volume, Target string; Readonly bool}` |
   | `Files` | `map[string]FileMount{Target string; Readonly bool}` |
   | `SecretsEnv` | `map[string]string` |
   | `Inject` | `map[string]Inject{Ref, Header, Format string}` |
   | `Network` | `{Mode string; Egress []string}` |
   | `Resources` | `{CPU float64; Memory, Disk, Timeout string}` |
   | `Policy` | `{Mode string; Allow []string}` |
   | `Setup` | `{Run string}` |

2. `Parse(src []byte) (Doc, []Section, []Diagnostic)`:
   - Decode with `toml.NewDecoder(bytes.NewReader(src)).DisallowUnknownFields()`
     into a private raw struct where `Secrets` is `map[string]any`.
   - On `*toml.StrictMissingError`, add one warning diagnostic per unknown
     key, using each `DecodeError.Position()` row. The rest still
     decodes.
   - On a `*toml.DecodeError`, return a single error diagnostic at its
     row, with an empty `Doc`.
   - Split `Secrets`: string values go to `SecretsEnv`. A map under
     `inject` becomes `Inject`. Anything else is an error diagnostic on
     the `[secrets]` header line.
3. Validation diagnostics (errors), each at the line of its section
   header:
   - a toolchain that doesn't match `^(go|node|python|rust)@[0-9][0-9A-Za-z.\-]*$`;
   - a network mode outside `open|allowlist|off`;
   - a secret ref without the `vault:` prefix;
   - a mount with both or neither of `repo`/`volume`, or with an empty
     `target`;
   - a policy mode outside the five ACP modes;
   - `resources.memory` or `disk` not matching `^\d+[kmg]$`;
   - `resources.timeout` that `time.ParseDuration` rejects.

   `Extends` is allowed by the parser. The bridge decides whether the
   template is a repo template.
4. Sections: scan the lines for table headers with
   `^\s*\[\[?\s*([A-Za-z0-9_."-]+)\s*\]\]?\s*(#.*)?$`. A section runs from
   its header to the line before the next header, with trailing blank
   lines trimmed.
   - **Layer map:** `workspace` → layers 1 and 2 (two `Section` entries
     with the same range), `packages` → 3, `mounts` → 4, `files` → 5,
     `secrets` and `secrets.inject` → 6, `network` → 7, `resources` → 8,
     `setup` → 9, `policy` → 0 (side panel).
   - `[[mounts]]` repeats are merged into one range, from the first header
     to the end of the last block.
5. Tests:
   - parse the full example from spec §4.1, with `setup` as a table;
   - every validation error;
   - an unknown key gives a warning with the right line;
   - section ranges, including repeated `[[mounts]]`;
   - a syntax error gives one diagnostic.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/workspacecfg/ -v && go vet ./internal/workspacecfg/
```

---

## Task 2: `Patch` and `Format`

**Goal:** re-render one layer's section into the source without touching
the rest, and format a whole doc canonically (spec §4.2).

**Files:**
- `internal/workspacecfg/render.go`, `render_test.go` (new)

**Steps:**

1. **`renderSection(key string, d Doc) []byte`.** It `toml.Marshal`s a
   wrapper struct holding only that section's field, then trims.
   - `workspace` renders `name`, `base`, `toolchains` and `extends`.
   - `secrets` renders the string map, then `[secrets.inject]`.
   - Use explicit wrapper types per key so the field order is stable.
2. **`Format(d Doc) []byte`.** It writes every non-empty section in the
   canonical order (workspace, packages, mounts, files, secrets, network,
   resources, policy, setup), separated by blank lines.
3. **`Patch(src []byte, layer int, value json.RawMessage) ([]byte, error)`:**
   1. `Parse` the source. Error diagnostics make it fail.
   2. Find the layer's section key or keys from the layer map.
   3. Unmarshal `value` (JSON) into that part of a copy of the doc. For
      layers 1 and 2, `value` is the `workspace` object.
   4. Render the section, then splice it: replace lines
      `StartLine..EndLine`, or append the section in canonical position
      when it's missing (after the nearest preceding canonical section
      that exists).
   5. Re-parse the result and return an error if it no longer parses.
4. Tests:
   - patching packages keeps comments in other sections byte for byte;
   - patching a missing `[network]` inserts it after `[secrets]`, or
     after the last earlier section;
   - patching layer 2 (toolchains) re-renders `[workspace]` with the base
     intact;
   - `Format` then `Parse` round-trips to an equal doc.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/workspacecfg/ -v
```

---

## Task 3: ACP `workspace/*` methods

**Goal:** the control agent exposes `workspace/parse`, `workspace/patch`
and `workspace/format` (spec §4.3).

**Files:**
- `internal/acp/workspacefiles.go` (new), `workspacefiles_test.go` (new)
- `internal/acp/host.go`

**Steps:**

1. Handlers on a small `WorkspaceFiles` struct with no state:
   - `Parse({source})` returns `{doc, sections, diagnostics}`;
   - `Patch({source, layer, value})` returns `{source, doc, sections, diagnostics}`.
     A patch error returns `invalidParamsError(err)`.
   - `Format({doc})` returns `{source}`.

   `source` is a JSON string.
2. Register them in `registerHandlers` next to W3.1's `config/*`. Add
   `"workspaceFiles": {}` to `agentCapabilities`.
3. Tests: one call per method through the handler functions. The
   initialize test asserts the capability.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestWorkspaceFiles|TestRunInitializeCapabilities' -v
```

---

## Task 4: Trust coverage of repo workspace files

**Goal:** `.marshal/workspaces/*.toml` is covered by the trust hash, and
by "has project config" (spec §4.4).

**Files:**
- `internal/trust/trust.go` (`ConfigHashFor` `:153`, `HasProjectConfig` `:256`)
- `internal/trust/*_test.go`

**Steps:**

1. In `ConfigHashFor`:
   - Glob `.marshal/workspaces/*.toml` under the working dir.
   - With no matches, keep the current result exactly: the sha256 of
     `config.toml` alone. Existing trust records then stay valid.
   - With matches, hash `"config.toml\0" + content + "\0"` (empty content
     when `config.toml` is absent), then each workspace file sorted by
     base name as `name + "\0" + content + "\0"`.
2. `HasProjectConfig` returns true when `config.toml` or any workspace
   file exists.
3. Tests:
   - the hash is unchanged for a config-only project (pin it against the
     current function's output on a fixture);
   - adding a workspace file changes it;
   - editing one changes it;
   - a workspace file without `config.toml` counts as project config.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/trust/ -v && CGO_ENABLED=1 go test ./internal/app/... 2>&1 | tail -5
```

The `internal/app` run is a regression check for trust prompts. The only
expected failure there is the known `TestSDDPlanPickerFlagsUnreadableLedger`.

---

## Task 5: `session/new` policy

**Goal:** `session/new` accepts `policy: {mode?, allow?}` (spec §4.5).

**Files:**
- `internal/acp/session.go` (`sessionParams` `:121`, the `Create` path)
- `internal/acp/session_test.go`

**Steps:**

1. Add `Policy *PolicyParams "policy,omitempty"` with
   `PolicyParams{Mode string; Allow []string}`.
   - Validate the mode against the ACP set (`plan`, `default`, `edit`,
     `copilot`, `auto`, as `SetMode` does, `turn.go:1349`).
   - Validate that each allow pattern is non-empty after trimming.
2. After the session runtime is created:
   - apply the mode the same way `TurnManager.SetMode` does, through the
     runtime's `SetMode` function;
   - for each pattern, call `rt.State.AddSessionRule(strings.TrimSuffix(strings.TrimSpace(p), " *"))`
     (`internal/app/session/session.go:1530`).
3. Tests:
   - after `session/new` with `policy`, `State.SessionRules()` (`:1536`)
     holds the prefixes and the mode is set;
   - an invalid mode is rejected;
   - no policy leaves everything unchanged.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'Policy|TestSessionNew' -v && CGO_ENABLED=1 go test ./internal/acp/
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./... ; go vet ./... ; gofmt -l .
```

Expected: only the five known failures, and no `gofmt` output.

## Integration notes

- Projects that already have `.marshal/workspaces/*.toml` (none should,
  before W4) will re-prompt for trust once.
- `workspace/*` is stateless and cheap. The bridge caches results by
  source hash (W4.2).

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. |
| Anchors verified? | Checked on `2ddc09e`: `go-toml/v2` in `go.mod`, `ConfigHashFor`/`HasProjectConfig` (`trust.go:153/256`), `sessionParams` (`session.go:121`), `AddSessionRule`/`SessionRules` (`session.go:1530/1536`), the `SetMode` mode list (`turn.go:1349`). go-toml v2's `Decoder.DisallowUnknownFields`, `StrictMissingError` and `DecodeError.Position` are its documented public API. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §4. |
