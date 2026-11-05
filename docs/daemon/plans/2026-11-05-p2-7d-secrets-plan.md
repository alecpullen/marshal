# P2 plan 7d: Secrets — implementation plan

Date: 2026-11-05. Status: ready for review.
Base: `main` at `d9db66e7` (plan 7b merged). Every anchor below was checked there.
- **This plan runs in parallel with plan 7c** (decided with the user, 2026-11-05). The two touch different TUI files. In the files both edit, each keeps to its own anchor:
  - `acpclient/acpclient.go`: this plan changes only `SaveUserProviders`' signature and the `Config` and `Layers` doc comments. 7c inserts after `NewSession` and `Changed`;
  - `acpclient/methods.go` and `inproc/inproc.go`'s `Deps`: this plan changes neither;
  - `app/tui/model.go`: this plan changes one call in `applyConnectDone`, and 7c touches trust and tools;
  - `docs/daemon/p2-client-seam.md`: both entries in its revision record are already written. Only the Implementation record can conflict, and the second plan to merge keeps both entries.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C3 (secrets), C10 (config), Migration order item 7d, its revision record entry "2026-11-05, against `d9db66e7` (plan 7b merged)", and tests T5 and T7.
Previous plan: [plan 7b, config](2026-11-05-p2-7b-config-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** no config read through the seam carries a literal secret. Each secret travels as the mask the settings browser already shows, and the agent puts the stored secret back wherever a config comes back.
- `strutil` gains `MaskKey`, moved from `listpanel`, and `IsMasked`.
- `config` gains `RedactSecrets`, `RedactLayers`, `RestoreSecrets` and `RestoreProviderKey`.
- The adapter masks `Config` and `Layers`, and restores every config it is handed: installs, project saves, user writes and probes.
- `SaveUserProviders` gains the provider renames the settings browser made, so a renamed provider keeps its key.

**Non-goals:**
- **No change to what users see.** The browser keeps showing `••••abcd` and "key stored", the connect wizard keeps "key stored in config", and `/doctor` keeps its diagnostics (decision 3).
- **MCP server `env` and `headers` stay in the clear.** The settings browser shows them (`settings/frames_mcp.go`), and they can hold tokens. They are not in `config.MaskSecrets`' set or C3's, so this plan records them as a follow-up.
- **No ACP method is added.** No changelog entry.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-7d-suite`. Create the directory first: `mkdir -p /private/tmp/p2-7d-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 7c uses `/private/tmp/p2-7c-suite`. Never share a directory between the two runs.
- **The base.** Plans 7b and P3 4a each ran the full suite before merging, and `d9db66e7` is the merge of both. This plan was written without rerunning it. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **Two suites may run at once.** Load makes the flakes listed under Task 4 likelier. Rerun a failure alone before treating it as new.
- **The secret set is `config.MaskSecrets`'s** (`config/mask.go:12`): every provider's `APIKey`, `Web.SearchKey`, and the userinfo of `Desktop.CDPURL`. The settings browser's receipts treat the same three fields as secret (`settings/configdiff.go:23`).
- **The mask is the browser's own.** `listpanel.MaskKey` (`listpanel/masked.go:8`) shows a key as `••••` plus its last four characters, `••••` alone for one shorter than four, and `(not set)` for none. The browser renders every masked field through it (`listpanel/fieldlist.go:551`), and the settings registry does for `/set` (`settings/registry.go:156`, `:216`).
- **Every TUI reader of a secret asks only whether it is set:**
  - the providers list's "key stored" label (`settings/frames_collections.go:39`);
  - the wizard's "key stored in config" (`connect/connect.go:686`);
  - `applyConnectDone`'s clearing of `api_key_env` next to a literal key (`model.go:6765`);
  - `config.Diagnose`'s three key checks (`config/diagnostics.go:64`, `:121`, `:162`).

  Nothing in the TUI sends a key anywhere except back through the seam: the probes go through the host, since plan 7a.
- **A masked field's edit starts empty.** Enter with nothing typed keeps the stored secret, a typed value replaces it, and `d` clears it (`listpanel/fieldlist.go:355`, `:418`). The field's value is never used to prefill an edit.
- **The browser renames a provider in its buffer** with `config.RenameProvider` (`settings/frames_collections.go:78`), then saves the providers section against its baseline (`settings/browser.go:693`). The baseline only decides which entries are deletions (`mergeMap`, `config/save.go:638`).
- **Every config the TUI hands back comes from `Session.Config`,** edited or not:
  - `ApplyConfig` and `ReloadConfig` from every save and reload path;
  - `SaveProjectConfig` from `persistAndReload` and the panels;
  - `SaveUserProviders` from the browser and `applyConnectDone`;
  - `SaveUserValue` with a value `config.LookupPath` read from such a config;
  - the probes, with a provider config from one.

**Decisions this plan makes beyond the spec** (all are recorded in the spec's revision record):

1. **A secret travels as its mask, in its own place** (decided with the user, 2026-11-05: the agent sends the masked hint, so nothing visible changes).
   - A read replaces a provider's key and the search key with `strutil.MaskKey`'s output, and the CDP URL with its userinfo masked, as `MaskSecrets` masks it. An empty secret stays empty.
   - This refines C3's "replaced by `""`, its source reported alongside": the mask takes the key's place, and the source is already in the config, as `api_key_env` or `auth`.
2. **No real secret starts with the mask's bullets.** `strutil.IsMasked` is true for a value that starts with `••••`. `MaskKey` returns such a value unchanged, so masking a mask is stable: the browser's display of a mask is the mask.
3. **Every reader keeps working unchanged.** A mask is empty exactly when the key is. So "key stored", the wizard's label, `applyConnectDone`'s env clearing, `config.Diagnose` and the browser's receipts all read the mask as today. `Diagnose` stays client side, as C10 says.
4. **The agent restores every config it is handed.** A masked secret is replaced by the stored secret it stands for:
   - first the secret at the same path in the session's live config, then in the session's last-loaded layers' merged config;
   - for a provider the call says was renamed, the key under its old name;
   - failing those, for a provider key, the one stored key whose mask it is, whatever its name.

   A literal value is kept as it is, and so is an empty one: an empty key means none, so a cleared key stays cleared. The live config is checked first because a typed key whose save failed lives only there; the layers second because they are fresher than the live config right after a write.
5. **A mask that matches nothing fails the call.** A write or an install that holds a mask with no stored secret behind it fails and changes nothing, so a mask is never written to disk or installed. A probe drops the key instead and probes without one, as an unkeyed provider would.
6. **`SaveUserProviders` takes the renames.**
   - The settings browser records each rename it makes as new name → name in the baseline, collapsing chains. A rename back to the original drops the entry.
   - It passes the map to `SaveUserProviders`, and clears it after a providers write succeeds.
   - Other callers pass nil.
7. **Layers are masked the same way:** their `Default`, `User` and `Merged` configs. A provider's provenance line compares masks, so it is unchanged unless two different keys share their last four characters.
8. **No ACP method changes name.** `SaveUserProviders` keeps `config/save_providers`, which gains the renames for P3.

**Accepted differences** (recorded in the spec):
- A rename the browser did not make, such as one through `/set`, reaches the agent without a name map. Two stored keys with the same last four characters then make the write fail instead of picking one.
- MCP `env` and `headers` stay in the clear (non-goals).

**Execution:**
- Branch first: `git switch -c p2-7d-secrets` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-7d-secrets: record the plan 7c and 7d revisions and plan 7d`. If plan 7c's run committed the spec already, commit only this plan. Plan 7c's plan file may also be uncommitted: leave it to plan 7c. The main checkout holds other untracked files (`scripts/lmstudio-bench/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-7d-secrets: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### `internal/strutil`

```go
// MaskKey renders a secret for display: •••• plus its last four characters,
// •••• alone for one shorter than four, and "(not set)" for none. A value
// that is already a mask is returned unchanged.
func MaskKey(key string) string

// IsMasked reports whether s is a mask MaskKey produced. No real secret
// begins with its bullets.
func IsMasked(s string) bool
```

`listpanel.MaskKey` becomes a variable bound to `strutil.MaskKey`, so its callers keep their spelling.

### `internal/app/config` (in `mask.go`, beside `MaskSecrets`)

```go
// RedactSecrets returns cfg with every literal secret replaced by its mask
// (P2 C3, plan 7d): each provider's API key and the web search key by
// strutil.MaskKey, and the desktop CDP URL's userinfo as MaskSecrets masks it.
// An empty secret stays empty. cfg and its maps are not changed.
func RedactSecrets(cfg Config) Config

// RedactLayers is RedactSecrets over the layers' Default, User and Merged
// configs. Every other field is kept.
func RedactLayers(l Layers) Layers

// RestoreSecrets returns client with every masked secret replaced by the
// stored secret it stands for, searching stored in order. renamed maps a
// provider's name in client to its name in stored. A literal or empty secret
// is kept. It fails, naming the secret's path, when a mask matches nothing.
// client and its maps are not changed.
func RestoreSecrets(client Config, renamed map[string]string, stored ...Config) (Config, error)

// RestoreProviderKey restores one provider's masked key as RestoreSecrets
// does. It reports false when the key is masked and matches nothing.
func RestoreProviderKey(name string, pc ProviderConfig, renamed map[string]string, stored ...Config) (ProviderConfig, bool)
```

The resolution order is decision 4's. A stored secret matches a mask when masking it gives the mask. A CDP URL is masked when its userinfo is the username `***` with no password. It is restored from the stored URL whose masked form it equals.

### Interface (`internal/acpclient`)

`SaveUserProviders` becomes:

```go
	// SaveUserProviders writes the user config's [providers] section,
	// credentials included. Entries on disk that are in neither providers nor
	// baseline survive: another process wrote them after baseline was taken.
	// A masked key is restored to the stored key it stands for; renamed maps a
	// provider renamed since baseline to its name there, so its key follows
	// it (plan 7d).
	//
	// ACP: config/save_providers (new in P3).
	SaveUserProviders(ctx context.Context, providers, baseline map[string]config.ProviderConfig, renamed map[string]string) error
```

`Config`'s doc comment (`acpclient.go:591`) says its secrets are masked (C3), and `Layers`' (`:597`) says the same.

### Adapter (`internal/acpclient/inproc`)

`(*Session).stored() []config.Config`: the state's `ConfigSnapshot()`, then its `Layers().Merged` when the layers are not the zero value. `(*Host).stored()` does the same over the current session's state.

### TUI

The settings `state` (`settings/state.go:18`) gains `renamed map[string]string`, made empty in `newState`.

### Pinned strings

- A mask with nothing behind it: `config: <path>: no stored secret matches its mask`, where `<path>` is the TOML path: `providers.<name>.api_key`, `web.search_key` or `desktop.cdp_url`.

## Task 1: The mask and its inverse

**Goal:** the pure helpers exist, and the browser's mask moves where both sides can use it.

**Files:**
- `internal/strutil/strutil.go` and its test;
- `internal/app/tui/listpanel/masked.go` and `masked_test.go`;
- `internal/app/config/mask.go`, `internal/app/config/mask_test.go`.

**Steps:**

1. **`strutil.MaskKey`.** Move `listpanel.MaskKey`'s body and comment, and add the rule that a mask is returned unchanged. Add `IsMasked`. `listpanel.MaskKey` becomes the variable, and `masked_test.go` keeps testing it there.
2. **`RedactSecrets`.**
   - Copy the providers map before changing any entry, as `MaskSecrets` does.
   - Mask each non-empty key, the search key when set, and the CDP URL's userinfo with `maskURLUserinfo`.
3. **`RedactLayers`** applies `RedactSecrets` to the three configs.
4. **`RestoreProviderKey`** follows decision 4 for one provider:
   - a key that is not masked returns at once;
   - otherwise it tries the same name in each stored config, then `renamed`'s old name in each, then every stored provider's key;
   - the last step accepts only one distinct matching key.
5. **`RestoreSecrets`.**
   - Copy the providers map, and restore each provider with `RestoreProviderKey`.
   - Restore a masked search key from the stored search keys in order, and a masked CDP URL from the stored URLs.
   - The first failure returns the pinned error, with the client unchanged.
6. **Tests:**
   - `TestMaskKeyIsStableOnMasks` (`strutil`): `MaskKey(MaskKey(k)) == MaskKey(k)` for a long key, a three-character key and a key of exactly four. `IsMasked` is true for each mask and false for each key.
   - `TestRedactSecretsMasksTheSecretSet` (`config`):
     - a config with two providers' keys, a search key and a CDP URL with a password comes back with masks only. Its JSON encoding contains no key and no password;
     - the original config's maps are unchanged;
     - empty secrets stay empty.
   - `TestRestoreSecretsInvertsTheMask`: `RestoreSecrets(RedactSecrets(c), nil, c)` equals `c` field for field.
   - `TestRestoreSecretsKeepsLiteralsAndEmpties`: a typed key and a cleared key in the client come back as they are.
   - `TestRestoreSecretsFollowsARename`: a provider renamed `old` → `new` restores from `old` with the map. Without the map it restores through the unique-match step, and fails when a second stored key has the same last four characters.
   - `TestRestoreSecretsPrefersTheFirstStoredConfig`: the same name in two stored configs with different keys takes the first one whose mask matches.
   - `TestRestoreSecretsFailsOnAnUnmatchedMask`: the pinned error, for a provider key and for the search key.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/strutil/ ./internal/app/config/ ./internal/app/tui/listpanel/
TMPDIR=/private/tmp/p2-7d-suite go test ./internal/strutil/ ./internal/app/config/ ./internal/app/tui/... -count=1
```

Expected: all pass. The TUI suite is unchanged: the browser renders the same masks.

## Task 2: The adapter masks and restores

**Goal:** no read through the seam returns a literal secret, and every config handed back gets its secrets restored.

**Files:**
- `internal/acpclient/acpclient.go`;
- `internal/acpclient/inproc/config.go`, `internal/acpclient/inproc/providers.go`, `internal/acpclient/inproc/secrets_test.go` (new), and the adapter tests that call `SaveUserProviders`;
- `internal/app/tui/settings/browser.go`, `internal/app/tui/model.go` (the two `SaveUserProviders` calls);
- the TUI tests that read a literal key back through the session.

**Steps:**

1. **The interface.** `SaveUserProviders` gains `renamed`, with the doc comment in Contracts, and `Config` and `Layers` say their secrets are masked. The browser (`browser.go:693`) and `applyConnectDone` (`model.go:6840`) pass nil for now; Task 3 gives the browser its map.
2. **Reads.** `Config` (`config.go:207`) returns `config.RedactSecrets` of the snapshot, and `Layers` (`:211`) returns `config.RedactLayers` of the layers.
3. **`stored`.** Add the two helpers in Contracts.
4. **Installs.**
   - `ApplyConfig` (`:216`) restores cfg with `RestoreSecrets(cfg, nil, s.stored()...)` before rebasing. On an error it returns it and changes nothing.
   - `ReloadConfig` (`:226`) restores before calling the reloader, only when there is one. On an error it returns true and the error, without reloading.
5. **Project writes.** `SaveProjectConfig` (`:271`) restores before saving, and returns an error without writing. `SaveVerifyCommands` carries no secret and is unchanged.
6. **User writes.**
   - `SaveUserValue` (`:73`): when `tomlPath` is a secret's path (`providers.<name>.api_key`, `web.search_key` or `desktop.cdp_url`) and the value is a masked string, restore it from the host's stored configs at that path. Use `RestoreProviderKey` for a provider, with no renames. A failure returns the pinned error without writing. Every other value is written as given.
   - `SaveUserProviders` (`:87`): restore each provider with `RestoreProviderKey(name, pc, renamed, h.stored()...)`, into a copy of the map. The first failure returns the pinned error without writing. The baseline is passed as it is.
7. **Probes.** `Host.client` (`providers.go:37`) restores pc's key with `RestoreProviderKey(name, pc, nil, h.stored()...)`. When that reports false, it clears the key and builds the client without one (decision 5). `ListModels` and `SaveModels` read the live config already, and are unchanged.
8. **Tests** (`secrets_test.go`), over an adapter whose state's config and layers carry `sk-test-SECRET-1234` for a provider, a search key and a CDP URL with a password:
   - `TestConfigReadsCarryNoSecret` (T7): `Config()` and `Layers()`, encoded with `json.Marshal`, contain neither `SECRET` nor the password, and do contain `••••1234`.
   - `TestInstallsRestoreTheStoredKey`: `ApplyConfig` of `Config()` leaves the state's key literal, and so does `ReloadConfig`, whose reloader receives the literal key.
   - `TestSaveProjectConfigRestoresTheSearchKey`: a project save of `Config()`, with the search key set in the user layer, writes no `search_key` line into the project file.
   - `TestSaveUserProvidersRestoresKeys`: a save of `Config().Providers` writes the literal key to the user file. A provider renamed in the client, with its rename passed, is saved under the new name with the old key. A cleared key is saved empty.
   - `TestSaveUserValueRestoresAMaskedKey`: writing a provider's `api_key` path with its mask writes the literal key, and with a typed value writes that.
   - `TestAnUnmatchedMaskChangesNothing`: a mask matching no stored key fails `ApplyConfig`, `SaveProjectConfig` and `SaveUserProviders` with the pinned error. The state and both files are unchanged.
   - `TestProbesUseTheStoredKey`: with an `httptest` server that records the `Authorization` header, `ProbeProvider` with the masked config sends the literal key, and with an unmatched mask sends none.
9. **TUI test migrations.**
   - A TUI test that read a literal key back through `m.sess.Config()` or `m.sess.Layers()` reads the state (`m.state.Config`) instead, or expects the mask.
   - A test that wrote a literal key into the state and checks a saved file keeps its check: the adapter writes the literal back.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/... ./internal/app/...
TMPDIR=/private/tmp/p2-7d-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p2-7d-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p2-7d-suite go test ./internal/app/ -count=1
```

Expected: all pass.

Mutation checks, restoring each:
- make `Config` return the snapshot unmasked, and `TestConfigReadsCarryNoSecret` fails;
- make `SaveUserProviders` skip the restore, and `TestSaveUserProvidersRestoresKeys` fails, because the file then holds the mask.

## Task 3: A renamed provider keeps its key

**Goal:** the settings browser tells the agent which providers it renamed, so their keys follow them exactly.

**Files:** `internal/app/tui/settings/state.go`, `internal/app/tui/settings/frames_collections.go`, `internal/app/tui/settings/browser.go`, `internal/app/tui/settings/browser_test.go`.

**Steps:**

1. **The map.** `state.renamed` (Contracts), made empty in `newState`.
2. **Recording.** The name field's setter (`frames_collections.go:78`), after `config.RenameProvider` and `RenameModels` succeed:
   - the original name is `renamed[k]` when present (deleting that entry), else `k`;
   - when the new name differs from the original, set `renamed[v]` to the original.
3. **Passing.** `flushChanges`' providers write (`browser.go:693`) passes `b.reg.st.renamed`. After it succeeds, replace the map with an empty one. A failed write keeps it for the retry.
4. **Tests** (`browser_test.go`):
   - `TestBrowserRenameKeepsTheProvidersKey`: over a `testSeam` whose state has provider `old` with a literal key and whose browser opens on `m.sess.Config()`'s masked copy, rename `old` → `new` and commit. The user file then holds `new` with the literal key, and no `old`.
   - `TestBrowserRenameChainsCollapse`: `a` → `b` → `c` sends `{c: a}`, and `a` → `b` → `a` sends an empty map.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/tui/settings/
TMPDIR=/private/tmp/p2-7d-suite go test ./internal/app/tui/settings/ -count=1
TMPDIR=/private/tmp/p2-7d-suite go test ./internal/app/tui/ -count=1
```

Expected: all pass. Mutation check: pass nil instead of the map, and give the state a second provider whose key shares `old`'s last four characters. `TestBrowserRenameKeepsTheProvidersKey` then fails with the pinned error. Restore it.

## Task 4: Verification and docs

**Goal:** the full suite is green, and the spec records plan 7d.

**Files:** `docs/daemon/p2-client-seam.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 7d entry to the Implementation record, after plan 7c's when it is there, otherwise after plan 7b's. It lists:
   - the helpers in `strutil` and `config`;
   - that `Config` and `Layers` carry masks, and that every install, project save, user write and probe restores them;
   - `SaveUserProviders`' renames and the browser's map;
   - the T7 test;
   - the accepted differences and the MCP follow-up.
2. **Main may have moved.** If plan 7c merged while this branch ran, merge `main` into the branch before the full verification. Expect conflicts only in the files this plan's header names. Keep both sides.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd ./web
go vet ./...
TMPDIR=/private/tmp/p2-7d-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-7d-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata internal/app/tui/livebox/testdata
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- the binary builds;
- the test line prints nothing, or only `web/bridge`'s known hang (Assumptions). Treat any other failure as new unless it also fails at `d9db66e7`, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing.

**Baseline.** Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, check by eye with `./marshal` and a provider that has a literal key in the user config:
- `/settings` → Providers shows "key stored", and the provider's API key row shows `••••` and its last four characters;
- changing the provider's base URL and back saves, and `/models` still lists its models, so the probe got the key;
- renaming the provider, then `/models`, still works, and the user config holds the key under the new name;
- pressing `d` on the API key row clears it, and the config file then has no `api_key` for it;
- `/doctor` shows the same diagnostics as before.

## Integration notes

- **Plan 7c** runs alongside. See the header for the shared files.
- **Plan 8** moves the skills panel. It holds no secret.
- **Follow-up:** MCP server `env` and `headers` can hold tokens and still travel in the clear. Masking them needs the same restore rule over two maps, and a decision on which entries count as secret.
- **P3:**
  - `config/save_providers` carries `renamed`;
  - the masks are what crosses the wire. R8's list of secret-carrying calls is unchanged: the provider-key write, the probes, whose config may hold a typed key, and `request_secret`'s answer. The user-value write joins it for a key's path (C3).

## Self-review

- **Every task verifiable alone.** Task 1 adds pure helpers and leaves every display unchanged. Task 2 masks the reads and restores everything handed back, with T7 and the restore tests, and keeps the TUI suite green because every reader treats a mask as a set key. Task 3 makes renames exact.
- **Cold executor.** The secret set, the mask, the resolution order, the failure rule and the rename map are spelled out, and each test names its setup and the behaviour it pins.
- **Contracts.** The helpers, the changed interface method, the adapter's helpers, the state's new field and the error string are declared there.
- **Anchors.** Every path, symbol and line was checked at `d9db66e7`.
- **Spec.** C3, C10 and the Migration order are updated, with the revision record "2026-11-05, against `d9db66e7` (plan 7b merged)".
- **No placeholders.**
