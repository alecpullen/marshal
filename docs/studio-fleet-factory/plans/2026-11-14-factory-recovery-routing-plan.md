# Recovery routing — change the implementation or review route when retrying — implementation plan

Date: 2026-11-14
Status: approved for autonomous execution by the user's implementation request.
Base: `84ccf315` (the reviewed stack head the specification was measured on). The
branch head that carries the approved specification is `f0737af7`, so the
implementation starts from there.
Specification: [factory-recovery-routing-spec.md](../factory-recovery-routing-spec.md).
The specification carries an uncommitted amendment in the worktree (D9 and D10
role-name the route messages and restate the plan-replacement path's
validation); this plan is written against that amended text, and the two
amended decisions are the ones Task 1 implements.

**Goal:** a retry of a stalled factory work item can change the implementer
route and/or the reviewer route without replacing the approved plan and
specification — the command carries the new route, the bridge validates it with
the same route rules the preflight uses, a new approval revision records the
change with the plan and specification carried over verbatim, and the workers
spawned after the retry run on the new route.

## Scope and boundaries

This slice adds:

- a shared route-rules helper, called by both `preflight` and the route-only
  retry path, so the two cannot drift;
- a route-only retry: a `retry` command that names a route and carries no
  replacement;
- a new approval revision for that retry whose plan, specification and plan
  hash are the previous approval's, and whose configuration differs only in
  the named route(s);
- a journal entry naming the old and the new profile for each role that
  changed;
- one narrow effect-derivation change (D15): the existing spawn-versus-resume
  rule is consulted in one more place, so a changed implementer route yields a
  fresh implementer;
- the studio surface that sends a route without replacing the plan, shows the
  routes in force, and states the consequence.

It does not add an HTTP route, a command kind, a field on `FactoryCommand`, an
effect kind, a state, a transition or a forge capability. It does not change
the factory loop, its state machine, its readiness gate, its draft-compensation
path, the handoff contract, the workspace format or the `Forge` interface.

All new Go code stays under `web/bridge/` and imports only the standard
library.

## Reconciled implementation decisions

The specification's twenty-three decisions are the contract; the points below
resolve only what it left to the plan, and record the discrepancies found
between the specification's prose and the code at the base commit.

1. **The route rules move into one helper, and the helper owns the model
   configuration.** The rules live in `factoryAPI.preflight`
   (`web/bridge/factory_http.go:183-220`). The helper
   `factoryValidateRoutes` owns the `config/get` call that serves them
   (`web/bridge/factory_http.go:161`), because the route-only retry path
   needs the rules without the rest of the preflight. `preflight` therefore
   calls the helper in place of the inline block; the order of the preflight's
   own checks is unchanged, because the route block already sat between the
   workspace check and the forge check.
2. **Every route message names its role.** The specification's D9 requires it,
   and §9 requires the refusal to name the role whose route was at fault.
   Two of the six messages are role-less today: "explicit routing profile
   required" (`web/bridge/factory_http.go:196`) and "unknown routing profile"
   (`web/bridge/factory_http.go:204`); the helper role-names both, exactly as
   the amended D9 states. No test asserts any of the six wordings, so the
   change touches no test — but the messages are pinned below.
3. **A JSON `null` route is invalid input, not an unsupported
   configuration.** D3 says a field present with JSON `null` is "rejected as
   invalid input", and D11's 422 covers a route that fails *validation*. A
   `null` route fails the command's shape, so it is refused with
   `errFactoryInput` and HTTP 400 before the route rules run. The two
   refusals are therefore distinguishable by status code as well as by
   message.
4. **Absent and present are distinguishable on the wire.** `json.RawMessage`
   decodes an absent field to `nil` and a `null` field to the four bytes
   `null`, because `json.RawMessage` implements `json.Unmarshaler` and the
   decoder calls it. The selection helper therefore treats `nil` as "absent"
   and compares the trimmed bytes against `null` for the rejected case. The
   pin is written against that behaviour; the implementer must add the test
   that proves it rather than trust this note.
5. **The candidate configuration has one source.** The configuration a retry
   is validated against is computed by one pure helper from the item and the
   command: the replacement's configuration when the command carries a
   replacement, otherwise the latest approval's. The HTTP layer validates the
   candidate; the controller applies the same candidate, computed from the
   same helper over the same item. There is no second place where a route
   could be composed differently.
6. **A route-only retry is validated by the route rules alone.** The full
   preflight re-checks the registered repository, the trusted project, the
   target branch, the native/workspace choice, the workspace image and the
   GitHub forge capability. None of those can be fixed from the retry
   surface, and the operator's one action must not be blocked by an unrelated
   failure. The plan-replacement path keeps the full preflight, because a
   replacement can change all of those.
7. **A plan-replacement retry that also names a route is validated in full,
   against the combined configuration.** The named route replaces the route in
   the replacement's configuration, and that combined configuration is what
   the preflight sees and what the approval records.
8. **The approval is appended only when a route actually differs.** The
   comparison is `bytes.Equal` over the raw route bytes, the same comparison
   `factoryImplementerRouteChanged` uses. A route-only retry that re-states
   the routes in force appends no approval and behaves exactly as today's
   retry: one grant, a cleared decision, the `repairing` state.
9. **The plan-replacement path always appends an approval**, as it does today.
   Rule 8 governs the route-only path only.
10. **The route change is journalled inside the same store mutation that
    appends the approval**, so the journal entry and the approval cannot
    disagree. The item's `ApprovedAt` is not touched: it is the plan's
    approval time and the queue order depends on it.
11. **D15's consultation is guarded by the resume effect's approval
    revision.** The branch that turns a completed `resume` effect into an
    `implement` effect (`web/bridge/factory_controller.go:241-243`) now
    consults `factoryImplementerRouteChanged` — but only for a resume that
    carries an *older* approval revision than the latest one. A resume that
    already carries the current revision was opened under the new route: it
    is the operator-attested fresh resume, which opens a new ACP session with
    the latest approval's routing
    (`web/bridge/factory_ownership.go:182-190`), and the specification says
    that path is already correct and is not changed. Without the revision
    guard the attested path would throw its fresh session away and spawn
    again, which the specification explicitly rules out. The guard mirrors
    the shape of the existing reviewer-park condition at
    `web/bridge/factory_controller.go:287`, which already compares the
    round's approval revision with the latest one.
12. **The D15 consultation is limited to the `resume` half of the branch.**
    The branch also handles a completed `create_pr`; that half is untouched,
    because the specification names only the resume transition.
13. **The studio's two route inputs are one source of truth.** They move out
    of the `{#if changePlan}` block and stay visible. When the plan checkbox
    is ticked they feed the replacement's configuration; when it is unticked
    they feed the command's top-level route fields. The inputs' labels are
    unchanged; the checkbox's label is not.
14. **The existing studio test that finds the checkbox by its label changes.**
    D21 relabels the checkbox, so the two tests that look it up by the old
    text are updated to the new text. The test that asserts the top-level
    route fields are undefined on a plan-replacement retry is **not** changed
    — D19 requires it to stay true.

### Discrepancies the planners found, and how they are resolved

| Discrepancy | Resolution |
|---|---|
| The specification's §5 error example showed "unknown routing profile", a role-less message, while D9 and §9 require the message to name the role | Closed in the contract, not in the plan: the specification was amended in the worktree during planning to role-name the §5 example, to state that the helper names the role in every message, and to name the two role-less messages with their line numbers. The amended D9 and D10 are what this plan implements; the messages are pinned below with the role in them |
| The specification's §7 file list does not mention a new Go file for the shared helper | The list describes the collision surface with the daemon track, not an exhaustive manifest. `web/bridge/factory_routing.go` and `web/bridge/factory_routing_test.go` are new files in the same package; `web/bridge/factory_workers.go` is **not** touched, so the production file set stays as §7 lists it plus the one new file |
| D3 calls a JSON `null` route "invalid input" while D11 maps a failed route to 422 `unsupported` | The `null` case is refused as invalid input (400) before the route rules run; every other route failure is the configuration error (422). Both are pinned |
| D15 says the rule must be consulted "where a retry's next implementer effect is chosen", which reads as an unqualified consultation | The unqualified form would break the operator-attested fresh-resume path that D15 says is already correct. The revision guard (decision 11) is the reading that satisfies both clauses |

## Contract pins

These are the details the specification left to the plan. They are fixed here,
and every test below depends on them.

| # | Pin | Value |
|---|---|---|
| P1 | Shared route-rules helper | `func (a *factoryAPI) factoryValidateRoutes(ctx context.Context, c FactoryConfig) error`, in the new file `web/bridge/factory_routing.go` |
| P2 | Helper call sites | exactly two: `factoryAPI.preflight` and the route-only retry branch of `factoryAPI.Command` |
| P3 | Route-rule messages | all six wrap `errFactoryConfig` and name the role (`implementer` or `reviewer`): `explicit %s routing required`, `explicit %s routing profile required`, `unknown %s routing profile`, `%s model preset unavailable`, `%s provider unavailable`, `%s provider credential unavailable`. The rendered forms are e.g. `factory configuration or capability unsupported: unknown implementer routing profile` |
| P4 | Model-configuration failure | `ErrFactoryUnavailable`, message `model configuration unavailable` / `invalid model configuration` — unchanged from today |
| P5 | Route selection reader | `func factoryRouteSelectionOf(c FactoryCommand) (factoryRouteSelection, error)` |
| P6 | Absent route | a `nil` `json.RawMessage` — the route is inherited and nothing is replaced |
| P7 | Null route | the trimmed raw bytes equal `null` — refused with `errFactoryInput`, message `invalid factory input: implementerRouting must be a routing object, not null` (and `reviewerRouting`), HTTP 400 `invalid_input` |
| P8 | Route application | `func factoryRouteApplied(conf FactoryConfig, sel factoryRouteSelection) FactoryConfig` — replaces the named route(s) wholesale; a route that is not named is left byte for byte alone |
| P9 | Candidate configuration | `func factoryRouteCandidate(item FactoryItem, cmd FactoryCommand) (FactoryConfig, bool, error)` — the second result is "the command names a route". The base is the replacement's configuration when the command carries a replacement, otherwise the latest approval's |
| P10 | Route-only approval | `func factoryRouteOnlyApproval(i FactoryItem, c FactoryCommand, now time.Time) (FactoryApproval, bool, error)` — the second result is "an approval is appended" |
| P11 | Approval revision | `uint64(len(i.Approvals) + 1)` — the existing rule |
| P12 | Plan, specification and plan hash | copied verbatim from the latest approval: `Plan`, `Spec` and `PlanHash` are the previous approval's bytes, so `PlanHash` still equals `factoryPlanHash(Plan, Spec)` and the store's re-validation at `web/bridge/factory_store.go:310` passes |
| P13 | Approval appended when | at least one of the two routes differs from the latest approval's, compared with `bytes.Equal` over the raw bytes |
| P14 | Approval not appended when | both routes are byte-identical — the retry then behaves exactly as today's retry |
| P15 | History event kind | `route_changed` |
| P16 | History event message | one clause per role that changed, joined with `"; "`: `<role> route <oldProfile> -> <newProfile>`, where `<role>` is `implementer` or `reviewer` and `<oldProfile>`/`<newProfile>` are the `profile` field of the routing object, or `-` when the routing object names no profile. Example: `implementer route frontier -> cheap-review` |
| P17 | `ApprovedAt` | unchanged by a route-only retry — it is the plan's approval time |
| P18 | Route-only validation | `factoryValidateRoutes` on the candidate configuration, and nothing else |
| P19 | Plan-replacement validation | the full `preflight`, on the replacement's configuration — unchanged |
| P20 | Combined retry | the named route(s) replace the route(s) in the replacement's configuration; the combined configuration is what the full preflight validates and what the approval records |
| P21 | D15 consultation site | the `case "create_pr", "resume":` branch of `AdvanceFactory`, `web/bridge/factory_controller.go:241-243` |
| P22 | D15 condition | `last.Kind == "resume" && last.ApprovalRevision != i.Approvals[len(i.Approvals)-1].Revision && factoryImplementerRouteChanged(i)` — satisfied, the next effect is `spawn` with role `implementer`; otherwise the branch is unchanged and the next effect is `implement` with role `implementer` |
| P23 | D15 outcome | a `spawn` effect for the implementer reads the latest approval's implementer routing (`web/bridge/factory_workers.go:23,36`), so the fresh implementer runs on the new route |
| P24 | Studio route-input labels | unchanged: `New implementer profile (optional)` and `New reviewer profile (optional)` |
| P25 | Studio plan-checkbox label | `Change approved plan or reviewer instructions` (was `Change approved plan, routes or reviewer instructions`) |
| P26 | Studio routes-in-force line | `Routes in force: implementer <profile> · reviewer <profile>`, where a profile the latest approval does not name is rendered `none` |
| P27 | Studio consequence line | `A route-only retry starts a fresh round. The interrupted round's review is not reused, and one granted round is spent.` |
| P28 | Studio command shape, plan unticked | the filled route(s) travel in the command's top-level `implementerRouting` / `reviewerRouting`; `replacement` is absent; an unfilled route travels in neither place |
| P29 | Studio command shape, plan ticked | the routes travel inside `replacement.config`; the command's top-level `implementerRouting` and `reviewerRouting` are absent |
| P30 | Studio retry enablement | unchanged: a reason is required, at least one additional round is required, and — only when the plan checkbox is ticked — a replacement plan is required. The route inputs are optional in every case |
| P31 | Studio route-input default | empty, and an empty input means "unchanged", never "clear the route" |
| P32 | Studio test conventions | flat top-level `it('lowercase sentence…')` cases in `web/ui/src/views/FactoryItem.test.ts`; no `describe` block |
| P33 | No new HTTP route | the change rides `POST /api/factory/items/{id}/commands`; no route table entry is added |
| P34 | No new field | `FactoryCommand` keeps exactly its current fields; the two routing fields already exist (`web/bridge/factory_types.go:267-268`) |
| P35 | Boundary | `web/bridge` stays Go standard library only; `TestWebIsStdlibOnly` (`web/bridge/boundary_test.go`) passes |

## Shared Go types

Task 1 owns the first declaration; Task 2 owns the rest. Later tasks consume
them without renaming.

```go
// factoryRouteSelection reports which of the two routes a retry command
// replaces. The routes are independent: one may change without the other.
//
// A route that is not named is inherited from the latest approval, byte for
// byte. A route that is named replaces the previous route wholesale, so any
// per-role override the previous routing object carried is dropped with it:
// the operator re-stated the route.
type factoryRouteSelection struct {
	implementer    json.RawMessage
	reviewer       json.RawMessage
	hasImplementer bool
	hasReviewer    bool
}
```

No exported type is added. `FactoryCommand`, `FactoryConfig`,
`FactoryApproval`, `FactoryGrant`, `FactoryEvent`, `FactoryItem` and every
other wire type are unchanged.

## File and dependency map

All paths are relative to the repository root.

| Task | Files | Depends on |
|---|---|---|
| 1 | `web/bridge/factory_routing.go` (new), `web/bridge/factory_http.go`, `web/bridge/factory_routing_test.go` (new) | — |
| 2 | `web/bridge/factory_routing.go`, `web/bridge/factory_controller.go`, `web/bridge/factory_routing_test.go`, `web/bridge/factory_controller_test.go` | 1 |
| 3 | `web/bridge/factory_http.go`, `web/bridge/factory_routing.go`, `web/bridge/factory_routing_test.go`, `web/bridge/factory_defaults_http_test.go` | 1, 2 |
| 4 | `web/bridge/factory_controller.go`, `web/bridge/factory_controller_test.go`, `web/bridge/factory_workers_test.go` | 2 |
| 5 | `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts` | — |
| 6 | `web/bridge/static/**` (rebuilt), `docs/studio-fleet-factory/factory-recovery-routing-spec.md`, this plan | 1–5 |

`web/bridge` is one Go package, so Tasks 1–4 are sequential under one
implementer: they compile together and must not overlap in flight. Task 5 is a
separate npm package (`web/ui`, Svelte 5 + TypeScript, vitest) with a disjoint
file set and depends only on the JSON contract, which is already frozen in
`FactoryCommand` (`web/bridge/factory_types.go:267-268`) and in its
TypeScript mirror (`web/ui/src/lib/factory.ts:29`); it may run concurrently
with Tasks 1–4. Task 6 runs last, because `web/ui/vite.config.ts` writes
`web/bridge/static/` (`outDir: '../bridge/static'`), which `web/bridge/assets.go:19`
embeds.

Task 3 touches `web/bridge/factory_defaults_http_test.go` only to widen the
shared fixture with the fake agent, so a test can replace the canned
`config/get`. That is additive and weakens no existing test.

## Task 1: The shared route rules

**Goal:** the route rules live in one helper that both `preflight` and the
route-only retry path call, and every rule's message names the role whose
route was at fault.

**Files:** create `web/bridge/factory_routing.go` and
`web/bridge/factory_routing_test.go`; modify `web/bridge/factory_http.go`.

- [ ] Write `TestFactoryValidateRoutesAcceptsAConfiguredProfile`: a
  configuration whose implementer and reviewer routes both name a known
  profile, whose profile binds a resolvable preset for each role, whose
  preset names a configured provider and whose provider carries a credential,
  passes with no error.
- [ ] Write `TestFactoryValidateRoutesNamesTheFailingRole`: a table over the
  six rules and the two roles. For each role the table asserts the exact
  message of P3 and that the error is `errFactoryConfig`. The cases are: no
  route at all; a route that is not a routing object (`5`); a route with no
  `profile` (`{}`); a route naming a profile the model configuration does not
  know; a profile whose role preset the model configuration does not resolve;
  and a preset whose provider is not configured or whose credential is
  missing. The credential case needs a provider whose type is `anthropic`
  with `hasKey` false, because the credential rule
  (`web/bridge/factory_http.go:218`) exempts `openai_codex` and a provider
  with no key environment variable.
- [ ] Write `TestFactoryValidateRoutesRejectsANilFleet`: a `factoryAPI` with
  no fleet is refused with the unavailable error rather than panicking.
- [ ] Write `TestFactoryPreflightUsesTheSharedRouteRules`: a configuration
  whose implementer route names an unknown profile is refused by `preflight`
  with the role-named message, which proves the preflight now calls the
  helper rather than keeping its own copy.
- [ ] Run
  `CGO_ENABLED=1 go test ./web/bridge -run '^TestFactoryValidateRoutes|^TestFactoryPreflightUsesTheSharedRouteRules' -count=1 -v`;
  confirm the new tests fail because the helper does not exist.
- [ ] Implement, in the new file `web/bridge/factory_routing.go`:

```go
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
)

// factoryValidateRoutes applies the route rules to both routes of a
// configuration: an explicit routing profile, a known profile, a resolvable
// preset, a configured provider and its credential.
//
// The preflight and the route-only retry both call this, so "the same rules
// apply" is a structural property rather than a convention. Every message
// names the role whose route was at fault, because the helper checks both
// routes of a configuration.
func (a *factoryAPI) factoryValidateRoutes(ctx context.Context, c FactoryConfig) error {
	if a.fleet == nil {
		return fmt.Errorf("%w: fleet unavailable", ErrFactoryUnavailable)
	}
	raw, err := a.fleet.controlCall(ctx, "config/get", nil, false)
	if err != nil {
		return fmt.Errorf("%w: model configuration unavailable", ErrFactoryUnavailable)
	}
	var modelConfig struct {
		Providers map[string]struct {
			HasKey    bool   `json:"hasKey"`
			Type      string `json:"type"`
			APIKeyEnv string `json:"apiKeyEnv"`
		} `json:"providers"`
		Presets map[string]struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"presets"`
		Profiles map[string]map[string]struct {
			Preset string `json:"preset"`
		} `json:"profiles"`
		DefaultProfile string `json:"defaultProfile"`
	}
	if json.Unmarshal(raw, &modelConfig) != nil {
		return fmt.Errorf("%w: invalid model configuration", ErrFactoryUnavailable)
	}
	for n, route := range []json.RawMessage{c.ImplementerRouting, c.ReviewerRouting} {
		role := "implementer"
		if n == 1 {
			role = "reviewer"
		}
		var selection struct {
			Profile   string            `json:"profile"`
			Overrides map[string]string `json:"overrides"`
		}
		if len(route) == 0 || json.Unmarshal(route, &selection) != nil {
			return fmt.Errorf("%w: explicit %s routing required", errFactoryConfig, role)
		}
		if selection.Profile == "" {
			return fmt.Errorf("%w: explicit %s routing profile required", errFactoryConfig, role)
		}
		profile := selection.Profile
		if profile == "" {
			profile = modelConfig.DefaultProfile
		}
		bindings, ok := modelConfig.Profiles[profile]
		if !ok {
			return fmt.Errorf("%w: unknown %s routing profile", errFactoryConfig, role)
		}
		preset := selection.Overrides[role]
		if preset == "" {
			preset = bindings[role].Preset
		}
		p, ok := modelConfig.Presets[preset]
		if !ok || p.Model == "" || p.Provider == "" {
			return fmt.Errorf("%w: %s model preset unavailable", errFactoryConfig, role)
		}
		provider, ok := modelConfig.Providers[p.Provider]
		if !ok || provider.Type == "" {
			return fmt.Errorf("%w: %s provider unavailable", errFactoryConfig, role)
		}
		if provider.Type != "openai_codex" && (provider.Type == "anthropic" || provider.APIKeyEnv != "") && !provider.HasKey {
			return fmt.Errorf("%w: %s provider credential unavailable", errFactoryConfig, role)
		}
	}
	return nil
}
```

  The block is the one at `web/bridge/factory_http.go:161-220` moved
  verbatim; the only changes are the three role-less messages, which now name
  the role, and the `a.fleet == nil` guard, which the helper needs because
  the route-only retry path does not go through the preflight's own fleet
  check. The `profile == ""` fallback to the default profile is dead code
  today — the empty profile already returned — and is carried over so the
  extraction stays mechanical and changes no behaviour.

- [ ] In `web/bridge/factory_http.go`, replace the block from
  `raw, err := a.fleet.controlCall(ctx, "config/get", nil, false)`
  (`web/bridge/factory_http.go:161`) through the end of the route loop
  (`web/bridge/factory_http.go:220`) with:

```go
	if err := a.factoryValidateRoutes(ctx, c); err != nil {
		return err
	}
```

  The preflight's remaining checks — the repository, the target branch, the
  trusted project, the native/workspace choice, the workspace image, and the
  forge capability — are untouched, and so is their order relative to the
  route rules: the route block already sat between the workspace check and
  the forge check.
- [ ] Run `gofmt -w web/bridge/factory_routing.go web/bridge/factory_http.go web/bridge/factory_routing_test.go`;
  run the named tests; require PASS. Run `gofmt -l` on the three files;
  require no output.
- [ ] Run `CGO_ENABLED=1 go test ./web/bridge -run 'Factory' -count=1`; the
  pre-existing factory tests must pass unmodified.
- [ ] Commit only this task's files:
  `feat(factory): share the routing rules between preflight and retry`.

## Task 2: The route-only retry appends an approval

**Goal:** a retry that names a route and carries no replacement appends one
approval whose plan, specification and plan hash are the previous approval's
and whose configuration differs only in the named route(s), and journals the
change; a retry whose route is byte-identical to the current one appends no
approval and behaves exactly as today's retry.

**Files:** modify `web/bridge/factory_routing.go`,
`web/bridge/factory_controller.go`, `web/bridge/factory_routing_test.go` and
`web/bridge/factory_controller_test.go`.

- [ ] Write `TestFactoryRouteSelectionRejectsANullRoute`: a command whose
  `implementerRouting` is the JSON value `null` is refused with
  `errFactoryInput` and the message of P7; the same for `reviewerRouting`.
  The test must build the command by decoding `{"implementerRouting":null}`
  into a `FactoryCommand`, so it proves the wire behaviour of P6 and P7
  rather than asserting it in prose.
- [ ] Write `TestFactoryRouteSelectionReadsAbsentAndPresentRoutes`: a command
  with neither field reports no route; a command with one field reports that
  route alone; the reported route is the exact bytes the command carried.
- [ ] Write `TestFactoryRouteOnlyRetryAppendsOneApprovalWithTheSamePlan`: an
  item with one approval and a decision, and a retry carrying an implementer
  route and no replacement, appends exactly one approval. The new approval's
  `Plan`, `Spec` and `PlanHash` equal the previous approval's byte for byte;
  its `Config.ImplementerRouting` equals the supplied route; its
  `Config.ReviewerRouting` equals the previous approval's; every other
  configuration field is unchanged; the previous approval is unchanged; the
  item's `ApprovedAt` is unchanged.
- [ ] Write `TestFactoryRouteOnlyRetryChangesTheReviewerRouteOnly`: a retry
  carrying only a reviewer route appends one approval whose
  `Config.ReviewerRouting` equals the supplied route and whose
  `Config.ImplementerRouting` equals the previous approval's.
- [ ] Write `TestFactoryRouteOnlyRetryChangesBothRoutesAndNothingElse`: a
  retry carrying both routes appends one approval carrying both, and the
  configuration differs from the previous approval's in those two fields and
  in no other.
- [ ] Write `TestFactoryRouteOnlyRetryWithAnUnchangedRouteAppendsNoApproval`:
  a retry carrying a route byte-identical to the current one appends no
  approval, appends the same grant today's retry appends, clears the
  decision and leaves the item in the `repairing` state — the same result as
  today's retry with no replacement. A retry carrying no routing at all
  behaves the same way.
- [ ] Write `TestFactoryRouteOnlyRetryAppendsTheGrantAndJournalsTheRouteChange`:
  the appended grant carries the command's `additionalRounds`, its `reason`
  and its request id; the earlier grants are unchanged; the item's total
  allowance — the sum of every grant's `additionalRounds` — grows by exactly
  the granted amount; the journal's last event has the kind `route_changed`
  and the message of P16; a retry that changes both routes produces one event
  naming both clauses.
- [ ] Write `TestFactoryRouteOnlyRetryRevokesTheInterruptedRoundsReadiness`:
  an item at the reviewer-park position, whose round carries a clean review
  and passed CI at the current approval revision, is advanced to `set_ready`
  — proving the position would otherwise be ready. A route-only retry then
  advances the approval revision, and the next advance must not produce
  `set_ready`. The round, its CI evidence and its review report are still
  present in the item.
- [ ] Run
  `CGO_ENABLED=1 go test ./web/bridge -run '^TestFactoryRoute' -count=1 -v`;
  confirm the new tests fail because the contract does not exist.
- [ ] Implement, in `web/bridge/factory_routing.go` — add the imports
  `bytes`, `strings` and `time`, and these declarations. Declare
  `factoryRouteSelection` first, exactly as the "Shared Go types" section
  gives it:

```go
// factoryRouteSelection reports which of the two routes a retry command
// replaces. The routes are independent: one may change without the other.
//
// A route that is not named is inherited from the latest approval, byte for
// byte. A route that is named replaces the previous route wholesale, so any
// per-role override the previous routing object carried is dropped with it:
// the operator re-stated the route.
type factoryRouteSelection struct {
	implementer    json.RawMessage
	reviewer       json.RawMessage
	hasImplementer bool
	hasReviewer    bool
}
```

  Then add these declarations:

```go
// factoryRouteSelectionOf reads the two routing fields a retry command
// carries.
//
// A field that is absent leaves its route alone. A field that is present
// with the JSON value null is invalid input: null is not a route, and
// reading it as "unchanged" would make the client's intent ambiguous.
func factoryRouteSelectionOf(c FactoryCommand) (factoryRouteSelection, error) {
	sel := factoryRouteSelection{}
	if c.ImplementerRouting != nil {
		if bytes.Equal(bytes.TrimSpace(c.ImplementerRouting), []byte("null")) {
			return sel, fmt.Errorf("%w: implementerRouting must be a routing object, not null", errFactoryInput)
		}
		sel.implementer, sel.hasImplementer = c.ImplementerRouting, true
	}
	if c.ReviewerRouting != nil {
		if bytes.Equal(bytes.TrimSpace(c.ReviewerRouting), []byte("null")) {
			return sel, fmt.Errorf("%w: reviewerRouting must be a routing object, not null", errFactoryInput)
		}
		sel.reviewer, sel.hasReviewer = c.ReviewerRouting, true
	}
	return sel, nil
}

// factoryRouteApplied replaces the named routes in a configuration. A route
// the selection does not name is left byte for byte alone.
func factoryRouteApplied(conf FactoryConfig, sel factoryRouteSelection) FactoryConfig {
	if sel.hasImplementer {
		conf.ImplementerRouting = sel.implementer
	}
	if sel.hasReviewer {
		conf.ReviewerRouting = sel.reviewer
	}
	return conf
}

// factoryRouteCandidate returns the configuration a retry command is
// validated and applied against, and whether the command names a route at
// all.
//
// A command that carries a replacement is validated against the
// replacement's configuration; a command that does not is validated against
// the latest approval's, which is what a route-only retry inherits from.
func factoryRouteCandidate(item FactoryItem, cmd FactoryCommand) (FactoryConfig, bool, error) {
	sel, err := factoryRouteSelectionOf(cmd)
	if err != nil {
		return FactoryConfig{}, false, err
	}
	if !sel.hasImplementer && !sel.hasReviewer {
		return FactoryConfig{}, false, nil
	}
	base := FactoryConfig{}
	switch {
	case cmd.Replacement != nil:
		base = cmd.Replacement.Config
	case len(item.Approvals) > 0:
		base = item.Approvals[len(item.Approvals)-1].Config
	default:
		return FactoryConfig{}, false, fmt.Errorf("%w: no approved configuration to change", errFactoryConfig)
	}
	return factoryRouteApplied(base, sel), true, nil
}

// factoryRouteOnlyApproval returns the approval a route-only retry appends,
// and whether one is appended at all.
//
// The plan, the specification and the plan hash are carried over verbatim: a
// route change moves the approval revision, not the approved work. An
// approval is appended only when at least one route actually differs from
// the latest approval's, so a retry that re-states the routes in force
// behaves exactly as a plain retry.
func factoryRouteOnlyApproval(i FactoryItem, c FactoryCommand, now time.Time) (FactoryApproval, bool, error) {
	sel, err := factoryRouteSelectionOf(c)
	if err != nil {
		return FactoryApproval{}, false, err
	}
	if !sel.hasImplementer && !sel.hasReviewer {
		return FactoryApproval{}, false, nil
	}
	if len(i.Approvals) == 0 {
		return FactoryApproval{}, false, fmt.Errorf("%w: no approved configuration to change", errFactoryConfig)
	}
	last := i.Approvals[len(i.Approvals)-1]
	candidate := factoryRouteApplied(last.Config, sel)
	if bytes.Equal(candidate.ImplementerRouting, last.Config.ImplementerRouting) && bytes.Equal(candidate.ReviewerRouting, last.Config.ReviewerRouting) {
		return FactoryApproval{}, false, nil
	}
	return FactoryApproval{
		Revision:   uint64(len(i.Approvals) + 1),
		Plan:       last.Plan,
		Spec:       last.Spec,
		PlanHash:   last.PlanHash,
		Config:     candidate,
		ApprovedAt: now,
	}, true, nil
}

// factoryRouteChangeMessage names the old and the new profile of every role
// whose route changed.
func factoryRouteChangeMessage(before, after FactoryConfig) string {
	parts := []string{}
	for _, r := range []struct {
		role      string
		old, next json.RawMessage
	}{
		{"implementer", before.ImplementerRouting, after.ImplementerRouting},
		{"reviewer", before.ReviewerRouting, after.ReviewerRouting},
	} {
		if bytes.Equal(r.old, r.next) {
			continue
		}
		parts = append(parts, r.role+" route "+factoryRouteProfile(r.old)+" -> "+factoryRouteProfile(r.next))
	}
	return strings.Join(parts, "; ")
}

// factoryRouteProfile names the routing profile a route selects, or "-" when
// the route names none.
func factoryRouteProfile(route json.RawMessage) string {
	var selection struct {
		Profile string `json:"profile"`
	}
	if json.Unmarshal(route, &selection) != nil || selection.Profile == "" {
		return "-"
	}
	return selection.Profile
}
```

- [ ] In `web/bridge/factory_controller.go`, in the `"retry"` case of
  `factoryApplyCommand` (the case begins at
  `web/bridge/factory_controller.go:1001`), replace the block

```go
		if c.Replacement != nil {
			r := *c.Replacement
			if err := factoryNormalize(&r); err != nil {
				return err
			}
			a := FactoryApproval{Revision: uint64(len(i.Approvals) + 1), Plan: r.Plan, Spec: r.Spec, PlanHash: FactoryPlanHash(r.Plan, r.Spec), Config: r.Config, ApprovedAt: now}
			i.Approvals = append(i.Approvals, a)
		}
```

  with

```go
		if c.Replacement != nil {
			r := *c.Replacement
			if err := factoryNormalize(&r); err != nil {
				return err
			}
			a := FactoryApproval{Revision: uint64(len(i.Approvals) + 1), Plan: r.Plan, Spec: r.Spec, PlanHash: FactoryPlanHash(r.Plan, r.Spec), Config: r.Config, ApprovedAt: now}
			i.Approvals = append(i.Approvals, a)
		} else {
			// A route-only retry: the plan and the specification are carried
			// over verbatim and only the named route(s) change. The approval
			// is appended only when a route actually differs, so a retry
			// that re-states the routes in force behaves as a plain retry.
			approval, appended, err := factoryRouteOnlyApproval(*i, c, now)
			if err != nil {
				return err
			}
			if appended {
				previous := i.Approvals[len(i.Approvals)-1]
				i.Approvals = append(i.Approvals, approval)
				i.History = append(i.History, FactoryEvent{At: now, Kind: "route_changed", Message: factoryRouteChangeMessage(previous.Config, approval.Config)})
			}
		}
```

  Nothing else in the `"retry"` case changes: the grant is still appended
  first, the decision is still cleared, the state is still set to
  `repairing`, and the operator-attested path still opens a `resume` effect.
  The item's `ApprovedAt` is not touched, so the queue order does not move.
- [ ] Run `gofmt -w` on the four task files; run the named tests; require
  PASS. Run `gofmt -l` on them; require no output.
- [ ] Run `CGO_ENABLED=1 go test ./web/bridge -run 'Factory' -count=1`; the
  pre-existing factory tests must pass unmodified. In particular
  `TestFactoryLoopReplacementSupersedesReview`
  (`web/bridge/factory_controller_test.go:761`) exercises the
  plan-replacement path, which this task does not change.
- [ ] Commit only this task's files:
  `feat(factory): append an approval for a route-only retry`.

## Task 3: The route-only retry is validated before the controller

**Goal:** a route-only retry is validated by the shared route rules against
the candidate configuration and refused with the configuration error and HTTP
422 before anything is written; a retry that also carries a replacement is
validated in full against the combined configuration.

**Files:** modify `web/bridge/factory_http.go`,
`web/bridge/factory_routing.go`, `web/bridge/factory_routing_test.go` and
`web/bridge/factory_defaults_http_test.go`.

- [ ] Widen the shared fixture in `web/bridge/factory_defaults_http_test.go`:
  add an `agent *fakeAgent` field to `factoryDefaultsFixture`
  (`web/bridge/factory_defaults_http_test.go:23`) and set it in
  `newFactoryDefaultsFixture` from the local `agent := newFakeAgent()`
  (`web/bridge/factory_defaults_http_test.go:41`). This is additive: it lets
  a test replace the canned `config/get` result, and it weakens no existing
  test.
- [ ] Write `TestFactoryRouteOnlyRetryAcceptsAValidRoute`: through
  `fixture.api.Command`, a retry carrying an implementer route and no
  replacement succeeds; the item carries one more approval whose plan and
  specification equal the previous approval's and whose implementer routing
  equals the supplied route; the item's grants carry the command's
  `additionalRounds` and reason.
- [ ] Write `TestFactoryRouteOnlyRetryRefusesAnInvalidRoute`: a table over
  the six route rules. Each case is refused with HTTP 422 and the code
  `unsupported`, the message names the role, and the item is unchanged — the
  same approvals, the same grants and the same version. The table drives the
  invalid route through the supplied field; the "unchanged route became
  invalid" case is a separate test below.
- [ ] Write `TestFactoryRouteOnlyRetryRefusesAnInvalidInheritedRoute`: the
  item's approval names a profile the model configuration has since lost; a
  retry that changes only the reviewer route is refused, and the message
  names the implementer — the role whose route was at fault, even though the
  operator did not touch it.
- [ ] Write `TestFactoryRouteOnlyRetryRefusesANullRoute`: a command whose
  `implementerRouting` is the JSON value `null` is refused with HTTP 400 and
  the code `invalid_input`, and the item is unchanged.
- [ ] Write `TestFactoryRouteOnlyRetryIsNotBlockedByAnUnrelatedPreflightCondition`:
  the item's approval names a workspace the fleet cannot resolve. A
  route-only retry succeeds, and a plan-replacement retry carrying the same
  workspace is refused with HTTP 422. The asymmetry is asserted, not
  accidental.
- [ ] Write `TestFactoryRouteOnlyRetryWithAReplacementValidatesTheCombinedConfig`:
  a retry carrying both a replacement and an implementer route appends one
  approval whose plan and specification are the replacement's and whose
  implementer routing is the supplied route; the replacement's own reviewer
  routing is inherited from the replacement, not from the previous approval.
- [ ] Write `TestFactoryPlanReplacementRetryWithoutRoutesIsUnchanged`: a
  retry carrying a replacement and no route appends one approval whose plan
  and specification are the replacement's and whose configuration is the
  replacement's — byte for byte what today's path produces.
- [ ] Write `TestFactoryRouteOnlyRetryReplaysToTheOriginalItem`: the same
  request id and content returns the original item and appends no second
  approval; the same request id with different content is a conflict.
- [ ] Run
  `CGO_ENABLED=1 go test ./web/bridge -run '^TestFactoryRouteOnlyRetry|^TestFactoryPlanReplacementRetry' -count=1 -v`;
  confirm the new tests fail because the retry path does not validate yet.
- [ ] Implement, in `web/bridge/factory_http.go`: in `factoryAPI.Command`
  (the method begins at `web/bridge/factory_http.go:77`), replace the block

```go
	if cmd.Kind == "retry" && cmd.Replacement != nil {
		replacement := *cmd.Replacement
		replacement.RequestID = cmd.RequestID
		if err := factoryNormalize(&replacement); err != nil {
			return FactoryItem{}, fmt.Errorf("%w: %v", errFactoryInput, err)
		}
		if err := a.preflight(ctx, replacement.Config, true); err != nil {
			return FactoryItem{}, err
		}
		cmd.Replacement = &replacement
	}
```

  with

```go
	if cmd.Kind == "retry" {
		if cmd.Replacement != nil {
			replacement := *cmd.Replacement
			replacement.RequestID = cmd.RequestID
			if err := factoryNormalize(&replacement); err != nil {
				return FactoryItem{}, fmt.Errorf("%w: %v", errFactoryInput, err)
			}
			cmd.Replacement = &replacement
		}
		// The candidate is the configuration the command would be applied
		// against: the replacement's when the command carries one, the
		// latest approval's otherwise.
		candidate, named, err := factoryRouteCandidate(item, cmd)
		if err != nil {
			return FactoryItem{}, err
		}
		switch {
		case cmd.Replacement != nil:
			// A plan replacement can change the repository, the project root,
			// the target branch, the workspace and the expected CI checks,
			// so it is validated in full — against the combined
			// configuration, in which the named routes are the ones the
			// command supplied.
			if named {
				cmd.Replacement.Config = candidate
			}
			if err := a.preflight(ctx, cmd.Replacement.Config, true); err != nil {
				return FactoryItem{}, err
			}
		case named:
			// A route-only retry changes exactly two fields, so it runs the
			// route rules alone: an unrelated condition the operator cannot
			// fix from this surface must not block the one action they need.
			if err := a.factoryValidateRoutes(ctx, candidate); err != nil {
				return FactoryItem{}, err
			}
		}
	}
```

  The `item` the candidate is computed from is the one already fetched at
  `web/bridge/factory_http.go:96` (`item, err := a.store.Get(id)`); do not
  fetch it twice. The refusal happens before `a.controller.Command` is
  called, so an invalid route never reaches the store: no grant, no approval,
  no state change, no version bump, and the request id is not recorded, so
  the retry can be corrected and sent again. The `errFactoryConfig` refusal
  maps to HTTP 422 `unsupported` through the existing `factoryWriteError`
  (`web/bridge/factory_http.go:309-310`); the `errFactoryInput` refusal maps
  to HTTP 400 `invalid_input`.
- [ ] Run `gofmt -w` on the four task files; run the named tests; require
  PASS. Run `gofmt -l` on them; require no output.
- [ ] Inspect `web/bridge/factory_http.go` and confirm the route rules are
  called from **exactly two** places — `factoryAPI.preflight` and the
  route-only branch of `factoryAPI.Command` — and that no copy of the rule
  block survives inline. P2 pins this, and no test can prove it.
- [ ] Run `CGO_ENABLED=1 go test ./web/bridge -run 'Factory' -count=1`; the
  pre-existing factory tests must pass unmodified. In particular
  `TestFactoryRetryReplacementIsNotReResolved`
  (`web/bridge/factory_defaults_http_test.go`) drives the plan-replacement
  path and must still pass, and
  `TestFactoryCreateReplayReturnsTheOriginalItem` must still pass.
- [ ] Commit only this task's files:
  `feat(factory): validate the routes a retry supplies`.

## Task 4: A changed implementer route yields a fresh implementer

**Goal:** when the implementer route changed, the next implementer effect is a
fresh spawn on the new route rather than a continuation of the previous
implementer on its previous route.

**Files:** modify `web/bridge/factory_controller.go`,
`web/bridge/factory_controller_test.go` and `web/bridge/factory_workers_test.go`.

- [ ] Write `TestFactoryRouteChangeSpawnsAFreshImplementerAfterAResume`: an
  item whose last effect is a completed `resume` carrying an approval
  revision older than the latest one, and whose two approvals differ in
  their implementer routing, advances to a `spawn` effect with role
  `implementer` — not an `implement` effect. The effect's `ApprovalRevision`
  is the latest approval's revision.
- [ ] Write `TestFactoryUnchangedRouteResumesTheImplementer`: the same item
  with the same implementer routing in both approvals advances to an
  `implement` effect with role `implementer`, exactly as before this task.
- [ ] Write `TestFactoryFreshResumeContinuesUnderTheCurrentApproval`: an item
  whose last effect is a completed `resume` carrying the *current* approval
  revision — the operator-attested fresh resume, which already opened a
  session on the latest approval's routing — advances to an `implement`
  effect, not a second spawn. This is the guard that keeps the attested path
  unchanged.
- [ ] Write `TestFactoryRouteOnlyRetryGivesTheNextReviewerTheNewRouting`: after
  a route-only retry that changes the reviewer route, the next review round's
  `spawn` effect carries the new approval's revision and the latest
  approval's `Config.ReviewerRouting` is the supplied route.
- [ ] Write `TestFactorySpawnFactoryWorkerUsesTheLatestApprovalRouting`: drive
  `Fleet.SpawnFactoryWorker` (`web/bridge/factory_workers.go:16`) for the
  reviewer role on an item whose latest approval names a reviewer routing,
  and assert that the `session/new` frame carries that routing. The routing
  reaches the session verbatim: `web/bridge/factory_workers.go:23` reads the
  latest approval's configuration, `:40` selects the reviewer route, and
  `web/bridge/fleet.go:1389-1390` puts `opts.Routing` into the `session/new`
  parameters. Use the `captureTransport` seam
  (`web/bridge/registry_test.go:1043`), which records every request as a
  `capturedFrame{method, params}`; the pattern for asserting on a recorded
  `session/new` frame is at `web/bridge/agentpath_test.go:303`. The fleet
  fixture is the one `parkedFixture` builds in
  `web/bridge/factory_workers_test.go:11`, extended with a registered
  project and a runtime.
- [ ] Run
  `CGO_ENABLED=1 go test ./web/bridge -run '^TestFactoryRouteChange|^TestFactoryUnchangedRoute|^TestFactoryFreshResume|^TestFactoryRouteOnlyRetryGivesTheNextReviewer|^TestFactorySpawnFactoryWorker' -count=1 -v`;
  confirm the new tests fail.
- [ ] Implement, in `web/bridge/factory_controller.go`: in `AdvanceFactory`,
  replace the branch

```go
		case "create_pr", "resume":
			kind = "implement"
			role = "implementer"
```

  (at `web/bridge/factory_controller.go:241-243`) with

```go
		case "create_pr", "resume":
			// A changed implementer route must not continue the previous
			// implementer on its previous route, so the rule that chooses
			// between a fresh spawn and a continuation is consulted here
			// too. A resume that already carries the current approval
			// revision was opened under the new route — the
			// operator-attested fresh resume — so it continues.
			if last.Kind == "resume" && last.ApprovalRevision != i.Approvals[len(i.Approvals)-1].Revision && factoryImplementerRouteChanged(i) {
				kind = "spawn"
				role = "implementer"
			} else {
				kind = "implement"
				role = "implementer"
			}
```

  The rule itself is not changed: `factoryImplementerRouteChanged`
  (`web/bridge/factory_controller.go:40-47`) still compares the last two
  approvals' implementer routing, native flag and workspace. The `create_pr`
  half of the branch is untouched, the reviewer-park branch
  (`web/bridge/factory_controller.go:287`) is untouched, and the loop, its
  effect kinds, its state machine and its readiness gate are untouched.
- [ ] Run `gofmt -w` on the three task files; run the named tests; require
  PASS. Run `gofmt -l` on them; require no output.
- [ ] Run `CGO_ENABLED=1 go test ./web/bridge -run 'Factory' -count=1`; the
  pre-existing factory tests must pass unmodified. In particular
  `TestFactoryLoopHappyAndRepair` walks the effect sequence
  `spawn … resume, implement, …` and must still produce `implement` after its
  `resume`: its item carries a single approval, so the route rule is false.
- [ ] Commit only this task's files:
  `fix(factory): spawn a fresh implementer after a route change`.

## Task 5: The studio sends a route-only retry

**Goal:** the studio can change the implementer route and/or the reviewer
route without replacing the plan, shows the routes in force, and states the
consequence of a route-only retry.

**Files:** modify `web/ui/src/views/FactoryItem.svelte` and
`web/ui/src/views/FactoryItem.test.ts`.

- [ ] Update the existing test
  `it('requires an explicit replacement plan and retry grant')` in
  `web/ui/src/views/FactoryItem.test.ts`: the checkbox it finds by
  `getByLabelText('Change approved plan, routes or reviewer instructions')`
  becomes `getByLabelText('Change approved plan or reviewer instructions')`.
  The test's expectation is unchanged — the button is disabled because the
  reason is empty. Do not weaken it.
- [ ] Update the existing test
  `it('sends route changes in the replacement approval config')`: the same
  label change, and nothing else. Its assertion that the command's
  `implementerRouting` and `reviewerRouting` are undefined on a
  plan-replacement retry stays true (D19), and the route inputs it fills are
  now visible before the checkbox is ticked, so the test's sequence still
  works.
- [ ] Write the vitest case
  `it('sends a route-only retry without the plan-replacement checkbox')`: the
  plan checkbox is left unticked, an implementer profile is typed, the retry
  is submitted, and the command carries `implementerRouting` equal to
  `{"profile":"…"}`, carries no `reviewerRouting` and carries no
  `replacement`.
- [ ] Write the vitest case
  `it('sends both routes in a route-only retry')`: both profiles are typed
  with the plan checkbox unticked, and the command carries both top-level
  route fields and no `replacement`.
- [ ] Write the vitest case
  `it('shows the routes currently in force and leaves the new-route inputs empty')`:
  the block shows the routes of the latest approval, and both route inputs
  are empty.
- [ ] Write the vitest case
  `it('states that a route-only retry starts a fresh round')`: the block
  states the consequence of P27.
- [ ] Run `cd web/ui && npm run test`; confirm the new tests fail.
- [ ] Implement, in `web/ui/src/views/FactoryItem.svelte`:
  - in the `<script>` block, add the derived values for the routes in force.
    Place them next to the other `$derived` values (the `granted` and
    `lastWorkerId` derivations, `web/ui/src/views/FactoryItem.svelte:51-52`):

```ts
  const currentConfig = $derived(item?.approvals?.at(-1)?.config ?? item?.draft.config ?? null)
  const currentImplementerProfile = $derived((currentConfig?.implementerRouting as { profile?: string } | undefined)?.profile || 'none')
  const currentReviewerProfile = $derived((currentConfig?.reviewerRouting as { profile?: string } | undefined)?.profile || 'none')
```

  - in the `if (kind === 'retry')` block
    (`web/ui/src/views/FactoryItem.svelte:23-31`), replace the body with

```ts
        if (kind === 'retry') {
          c.reason = reason; c.additionalRounds = additionalRounds
          const latest = item.approvals?.at(-1)?.config ?? item.draft.config
          if (changePlan) {
            const draft: FactoryCreate = { ...item.draft, requestId: crypto.randomUUID(), plan: replacementPlan, spec: replacementSpec,
              config: { ...latest, reviewerInstructions: replacementInstructions,
                implementerRouting: replaceImplementerProfile.trim() ? { profile: replaceImplementerProfile.trim() } : latest.implementerRouting,
                reviewerRouting: replaceReviewerProfile.trim() ? { profile: replaceReviewerProfile.trim() } : latest.reviewerRouting } }
            c.replacement = draft
          } else {
            if (replaceImplementerProfile.trim()) c.implementerRouting = { profile: replaceImplementerProfile.trim() }
            if (replaceReviewerProfile.trim()) c.reviewerRouting = { profile: replaceReviewerProfile.trim() }
          }
        }
```

    The plan-replacement branch keeps its existing transport exactly: the
    routes ride inside the replacement's configuration and the top-level
    fields are not sent.
  - in the markup, move the two route inputs out of the `{#if changePlan}`
    block (`web/ui/src/views/FactoryItem.svelte:106`) so they are available
    without the plan checkbox being ticked, and add the two new lines. The
    block becomes, in order: the reason input, the additional-rounds input,
    the routes-in-force line, the implementer route input, the reviewer route
    input, the consequence line, the plan checkbox, the `{#if changePlan}`
    details block (which keeps the replacement plan, the replacement
    specification and the reviewer instructions, and loses the two route
    inputs), and the retry button. The exact lines to add are:

```svelte
          <p class="text-sm">Routes in force: implementer {currentImplementerProfile} · reviewer {currentReviewerProfile}</p>
          <label class="grid gap-1 text-sm">New implementer profile (optional) <input class="rounded border border-border bg-bg p-2" bind:value={replaceImplementerProfile} /></label>
          <label class="grid gap-1 text-sm">New reviewer profile (optional) <input class="rounded border border-border bg-bg p-2" bind:value={replaceReviewerProfile} /></label>
          <p class="text-sm">A route-only retry starts a fresh round. The interrupted round's review is not reused, and one granted round is spent.</p>
```

  - relabel the checkbox
    (`web/ui/src/views/FactoryItem.svelte:105`): `Change approved plan, routes
    or reviewer instructions` becomes `Change approved plan or reviewer
    instructions`. The route inputs' labels are unchanged (P24).
  - the summary of the replacement details block becomes `Replacement plan and
    reviewer instructions`, since the routes are no longer inside it.
  - the retry button's enablement is unchanged (P30):
    `disabled={busy || !reason.trim() || additionalRounds < 1 || (changePlan && !replacementPlan.trim())}`.
- [ ] Run `cd web/ui && npm run test`; require PASS.
- [ ] Commit only these two files:
  `feat(web): send a route-only retry from the studio`.

## Task 6: Rebuild the assets, prove acceptance, and record the outcome

**Goal:** the embedded assets carry the studio change, every acceptance
criterion has a passing test, and the specification records the outcome.

**Files:** rebuild `web/bridge/static/**`; update
`docs/studio-fleet-factory/factory-recovery-routing-spec.md` and this plan.

- [ ] Run `cd web/ui && npm install && npm run build`; confirm
  `web/bridge/static/` changed. The build writes `../bridge/static`
  (`web/ui/vite.config.ts:8`), which `web/bridge/assets.go:19` embeds, so it
  runs only now that the studio source is final.
- [ ] Run the feature suite:
  `CGO_ENABLED=1 go test ./web/bridge -run '^TestFactoryValidateRoutes|^TestFactoryPreflightUsesTheSharedRouteRules|^TestFactoryRoute|^TestFactoryUnchangedRoute|^TestFactoryFreshResume|^TestFactoryPlanReplacementRetry|^TestFactorySpawnFactoryWorker' -count=1 -v`.
- [ ] Run the pre-existing contract gates:
  `CGO_ENABLED=1 go test ./web/bridge -run 'Factory' -count=1` and
  `CGO_ENABLED=1 go test ./web/bridge -run '^TestWebIsStdlibOnly$' -count=1`.
- [ ] Run the package gate: `CGO_ENABLED=1 go test ./web/bridge -count=1`.
- [ ] Run the format, vet and build gates: `gofmt -l web/bridge`,
  `go vet ./web/...`, `CGO_ENABLED=1 go build ./...`, and
  `cd web/ui && npm run test`.
- [ ] Run the repository suite:
  `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 20m`. Record unrelated
  baseline failures separately; do not claim an overall pass when any
  required gate remains red.
- [ ] Add an "Implementation record" to the specification: the branch, the
  task commits, the files touched, the gates and their results, and the
  open questions from §8 that remain open.
- [ ] Record the plan's outcome here: the task/commit table and the gates
  actually run.
- [ ] Commit: `docs(factory): record implemented recovery routing`.

## Acceptance matrix

| Specification criterion | Owning tests |
|---|---|
| A retry carrying `implementerRouting` and no replacement appends exactly one approval whose `Plan`, `Spec` and `PlanHash` equal the previous approval's byte for byte, whose `Config.ImplementerRouting` equals the supplied route, and leaves the previous approval unchanged | Task 2 `TestFactoryRouteOnlyRetryAppendsOneApprovalWithTheSamePlan`; Task 3 `TestFactoryRouteOnlyRetryAcceptsAValidRoute` |
| A retry carrying `reviewerRouting` and no replacement appends exactly one approval whose `Config.ReviewerRouting` equals the supplied route and whose implementer routing equals the previous approval's | Task 2 `TestFactoryRouteOnlyRetryChangesTheReviewerRouteOnly` |
| A retry carrying both routes appends exactly one approval carrying both and changes nothing else in the configuration | Task 2 `TestFactoryRouteOnlyRetryChangesBothRoutesAndNothingElse` |
| A retry carrying a byte-identical route appends no approval, appends today's grant, clears the decision and leaves the item `repairing` | Task 2 `TestFactoryRouteOnlyRetryWithAnUnchangedRouteAppendsNoApproval` |
| A retry carrying no routing and no replacement is byte-identical in effect to today's retry: one grant, no approval, the same state | Task 2 `TestFactoryRouteOnlyRetryWithAnUnchangedRouteAppendsNoApproval` |
| A route-only retry appends a grant with the command's `additionalRounds` and `reason`, the allowance grows by exactly that amount, and the earlier grants are unchanged | Task 2 `TestFactoryRouteOnlyRetryAppendsTheGrantAndJournalsTheRouteChange`; Task 3 `TestFactoryRouteOnlyRetryAcceptsAValidRoute` |
| A route-only retry leaves the plan and the specification unchanged, the interrupted round's review no longer satisfies the readiness gate, and the round, its CI evidence and its review report remain in the journal | Task 2 `TestFactoryRouteOnlyRetryAppendsOneApprovalWithTheSamePlan`, `TestFactoryRouteOnlyRetryRevokesTheInterruptedRoundsReadiness` |
| A route whose profile is missing, unknown or unresolvable, or whose provider is unconfigured or uncredentialed, is refused with the configuration error and the 422 status, and the item is unchanged | Task 1 `TestFactoryValidateRoutesNamesTheFailingRole`; Task 3 `TestFactoryRouteOnlyRetryRefusesAnInvalidRoute` |
| The refusal message names the role whose route was at fault | Task 1 `TestFactoryValidateRoutesNamesTheFailingRole`; Task 3 `TestFactoryRouteOnlyRetryRefusesAnInvalidRoute`, `TestFactoryRouteOnlyRetryRefusesAnInvalidInheritedRoute` |
| The unchanged route is re-validated, so a route that has become invalid since the approval refuses the retry | Task 3 `TestFactoryRouteOnlyRetryRefusesAnInvalidInheritedRoute` |
| A route-only retry is not blocked by an unrelated preflight condition, while a plan-replacement retry still is | Task 3 `TestFactoryRouteOnlyRetryIsNotBlockedByAnUnrelatedPreflightCondition` |
| The route change is journalled as a history event naming the old and the new profile for each role that changed, and the item's `ApprovedAt` is unchanged | Task 2 `TestFactoryRouteOnlyRetryAppendsTheGrantAndJournalsTheRouteChange` |
| A retry carrying both a replacement and routing fields validates the combined configuration and appends one approval whose plan and specification are the replacement's and whose named route is the supplied one | Task 3 `TestFactoryRouteOnlyRetryWithAReplacementValidatesTheCombinedConfig` |
| A plan-replacement retry carrying no routing fields validates the replacement's configuration and appends one approval whose plan and specification are the replacement's, exactly as today | Task 3 `TestFactoryPlanReplacementRetryWithoutRoutesIsUnchanged`; the unmodified `TestFactoryLoopReplacementSupersedesReview` and `TestFactoryRetryReplacementIsNotReResolved` |
| The next implementer worker uses the new approval's implementer routing when the route changed, and the existing implementer is resumed when it did not | Task 4 `TestFactoryRouteChangeSpawnsAFreshImplementerAfterAResume`, `TestFactoryUnchangedRouteResumesTheImplementer`, `TestFactorySpawnFactoryWorkerUsesTheLatestApprovalRouting` |
| The next reviewer worker uses the new approval's reviewer routing | Task 4 `TestFactoryRouteOnlyRetryGivesTheNextReviewerTheNewRouting`, `TestFactorySpawnFactoryWorkerUsesTheLatestApprovalRouting` |
| A retry is refused while an effect is dispatched or uncertain, and while a worker is active unless the previous worker is attested stopped, exactly as today | The unmodified `TestFactoryLoopRetryRequiresIdle` (`web/bridge/factory_controller_test.go:512`) and the retry guards at `web/bridge/factory_controller.go:917-946`, which this slice does not touch |
| A replayed route-only retry returns the original item and appends no second approval, and the same request id with different content is a conflict | Task 3 `TestFactoryRouteOnlyRetryReplaysToTheOriginalItem` |
| The studio sends the route in the top-level `implementerRouting` field and sends no replacement when only a route is changed, and the retry works without the plan-replacement checkbox being ticked | Task 5 `it('sends a route-only retry without the plan-replacement checkbox')`, `it('sends both routes in a route-only retry')` |
| The studio sends the routes inside the replacement's configuration and sends no top-level route fields when the plan is replaced, as today | Task 5 the unmodified assertion in `it('sends route changes in the replacement approval config')` |
| The studio shows the routes currently in force and leaves the new-route inputs empty by default | Task 5 `it('shows the routes currently in force and leaves the new-route inputs empty')` |
| The studio states that a route-only retry starts a fresh round, that the interrupted round's review is not reused, and that one granted round is spent | Task 5 `it('states that a route-only retry starts a fresh round')` |
| The studio test file keeps its vitest conventions: flat top-level `it('lowercase sentence…')` cases and no `describe` block | Task 5, by inspection of `web/ui/src/views/FactoryItem.test.ts` after the edit |
| `TestWebIsStdlibOnly` passes and no new `marshal/internal` import appears under `web/` | The existing boundary test, run in Task 6; the new file `web/bridge/factory_routing.go` imports only `bytes`, `context`, `encoding/json`, `fmt`, `strings` and `time` |
| No new HTTP route is added, and the factory loop, its effect kinds, its state machine, its readiness gate, its draft-compensation path and the `Forge` interface are unchanged | The unmodified pre-existing factory tests, run in Task 6; `factory_routes` (`web/bridge/factory_http.go:232`) is untouched |

Every test is offline and deterministic: no live forge, no real git
repository, no container runtime, no model provider and no developer
credentials. The registered repository uses the RFC 2606 reserved
`https://example.invalid/...` URL, which the preflight only constructs a
client for and never calls.

## Execution, commits, and PR policy

- Execute Tasks 1–4 sequentially with one implementer: they are one Go
  package and share the frozen contract. Task 5 runs in parallel in the npm
  package, because `web/ui` is a separate package with a disjoint file set
  and depends only on the JSON contract, which is already frozen. Task 6
  runs last.
- Each implementer follows test-driven development: add the named failing
  test, capture the expected failure, implement the smallest owning change,
  run the named verification, and commit only that task's files.
- The implementation PR targets this branch and contains Tasks 1–6.
- Every PR is created as draft. Before it can become ready, dispatch an
  independent reviewer to review the branch against its base. Address every
  substantive finding, rerun the affected gates, and have the reviewer verify
  the fixes before changing the PR from draft to ready.
- Do not merge automatically. The user retains the merge decision.

## Plan self-review

- Coverage: the specification's §4 (D1–D23) and §9 map to Tasks 1–6; §8's
  six open questions stay open and are recorded in the implementation record;
  §10's non-goals are respected.
- Contract consistency: the helper, the selection reader, the candidate, the
  approval builder, the history event and the studio labels are fixed in the
  contract pins and consumed unchanged by Tasks 2–5.
- Scope discipline: no HTTP route, no command kind, no field on
  `FactoryCommand`, no effect kind, no state, no transition, no forge
  capability. The one production change outside the retry path is D15's
  consultation, and it is confined to the resume transition the
  specification names. The factory loop, its state machine, its readiness
  gate, its draft-compensation path, the handoff contract, the workspace
  format and the `Forge` interface are untouched.
- Decision fidelity: D15's two clauses — "the rule must also be consulted
  where a retry's next implementer effect is chosen" and "the
  operator-attested fresh-resume path … is not changed" — are both honoured
  by the revision guard in P22. An unqualified consultation would have
  contradicted the second clause.
- Existing tests: two studio tests change, and only because D21 relabels the
  checkbox; the assertion D19 protects is left intact. No Go test is
  weakened; the pre-existing factory suite is the regression net for the
  untouched contract.
- Testability: every acceptance criterion has an offline deterministic test,
  and each task's own gate passes at its own commit, so the tree stays green
  at every commit.
- Collision control: the Go tasks are sequential because they compile as one
  package; the studio task is separate and may run concurrently; the asset
  rebuild runs last.
- Known cost: `TestFactorySpawnFactoryWorkerUsesTheLatestApprovalRouting` is
  the one test that must extend an offline fleet fixture (registering a
  project and a runtime) rather than reuse an existing one. The anchors for
  that fixture are given in Task 4. If it proves impractical offline, the
  criterion is still covered by
  `TestFactoryRouteOnlyRetryGivesTheNextReviewerTheNewRouting` together with
  the unmodified `web/bridge/factory_workers.go`, and the implementer must
  record the substitution in the implementation record rather than drop the
  criterion.

## Plan outcome record

_To be completed by the implementer: the task/commit table, the gates
actually run and their results, and any substitution recorded above._
