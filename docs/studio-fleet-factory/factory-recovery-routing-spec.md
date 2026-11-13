# Recovery routing — change the implementation or review route when retrying — feature spec

Date: 2026-11-14
Status: spec for an approved-behaviour slice of the studio-fleet-factory
archive. No product code is changed by this document. This specification
awaits an independent `glm-5.3-flash` review; see the branch review record.

## 1. Provenance

This is the design output for the approved behaviour in
[design-notes §5](design-notes.md#5-factory-review-and-repair-loop) ("Factory
review and repair loop"), under **Agreed**:

> "For recovery from a stalled run, the useful adjustments depend on the
> nature of the stall. The user wants to be able to change the models and/or
> providers used by implementation and review agents before retrying.
> Recovery should not be limited to rerunning the same configuration. Other
> recovery controls and how configuration changes apply to existing agent
> sessions remain to be designed."

The clause this slice delivers is the first two sentences: **the models and/or
providers used by implementation and review agents can be changed before
retrying.** The third sentence — "how configuration changes apply to existing
agent sessions" — is recorded in §8 as an open question, because the design
record leaves it open and it needs the user.

The adjacent §5 paragraph is explicitly **not** implemented here:

> "When switching models during recovery, users should be able to choose
> between continuing the existing conversation and starting fresh with a
> handoff summary. The user confirmed both options would be useful. How
> conversation context is carried across providers/harnesses, including any
> limitations, remains to be investigated; this does not assume native session
> portability between providers."

The choice between continuing and starting fresh, and the handoff summary that
a fresh start would carry, are open questions (§8.1, §8.2). This slice does not
decide them and does not build either.

The approved visual direction for this area is recorded in
[design-notes §10](design-notes.md#stalled-run-recovery--approved) ("Stalled-run
recovery — approved"), whose retry controls "expose provider and model choices
for implementation and review agents". This slice makes those choices
effective in the factory, not only in the prototype.

The two earlier slices are implemented and their contracts are recorded in
[the discovery spec](existing-project-discovery-spec.md) and
[the project factory defaults spec](project-factory-defaults-spec.md); the
narrated engineering report is recorded in
[the narration spec](factory-narration-spec.md).

## 2. Summary

A retry of a stalled factory work item can change the **implementer route**
and/or the **reviewer route** without replacing the approved plan and
specification.

Today the only way to change a route is to also replace the plan and the
specification, because the studio gates the route inputs behind the "Change
approved plan, routes or reviewer instructions" checkbox and sends them only
inside the plan replacement's config. This slice adds a route-only retry: the
command carries new routing, the bridge validates it with the existing route
preflight, appends a new approval revision whose plan and specification are
unchanged, and the new route is used by the workers spawned after the change.

The approval revision and the round are coupled in the factory, so a new
approval revision has one further consequence, and it is the correct one: the
interrupted round's review no longer satisfies the readiness gate. A new model
does not inherit the previous model's clean review.

## 3. What exists today

Measured on the reviewed stack head `84ccf315`.

| Piece | Where | State |
|---|---|---|
| Command wire type | `web/bridge/factory_types.go:258` | `FactoryCommand` carries `ImplementerRouting` (`:267`) and `ReviewerRouting` (`:268`) as `json.RawMessage`. **No production code reads either field.** The retry case of `factoryApplyCommand` reads only `c.Replacement` |
| Approval config | `web/bridge/factory_types.go:30` | `FactoryConfig` carries `ImplementerRouting` (`:37`) and `ReviewerRouting` (`:38`). These are the only production reads of the names: `factory_controller.go:46`, `factory_ownership.go:183,185`, `factory_workers.go:36,40` |
| Worker routing source | `web/bridge/factory_workers.go:23` | `conf := item.Approvals[len(item.Approvals)-1].Config` — the **latest approval's** config. The implementer uses `conf.ImplementerRouting` (`:36`), the reviewer `conf.ReviewerRouting` (`:40`) |
| Route-change rule | `web/bridge/factory_controller.go:40` | `factoryImplementerRouteChanged` compares the **last two approvals'** `ImplementerRouting`, `Native` and `Workspace`. It does **not** compare `ReviewerRouting` |
| Where the rule is consulted | `web/bridge/factory_controller.go:287` | After a reviewer parks, a changed implementer route yields a fresh `spawn` (`:288`) instead of a `resume` (`:291`) |
| Retry command | `web/bridge/factory_controller.go:1001` | `factoryApplyCommand`'s `"retry"` case appends a `FactoryGrant` always, and appends a new `FactoryApproval` **only** when `c.Replacement != nil` |
| Approval revision and rounds | `web/bridge/factory_controller.go:62`, `:83` | `factoryNext` stamps `e.ApprovalRevision` from the latest approval; the round records it. The readiness gate requires `r.ApprovalRevision == i.Approvals[last].Revision` (`:284`) and `last.ApprovalRevision == i.Approvals[last].Revision` (`:303`) |
| Route validation | `web/bridge/factory_http.go:121` | `factoryAPI.preflight` requires an explicit routing profile (`:193`, `:196`), a known profile (`:204`), a resolvable preset (`:212`), a configured provider (`:216`) and its credential (`:219`). A `retry` with a `Replacement` preflights the replacement's config (`:108-115`) |
| Retry guards | `web/bridge/factory_controller.go:917-946` | `Command` refuses a retry while an effect is dispatched or uncertain, and while a worker is active unless the previous worker is attested stopped |
| Studio retry UI | `web/ui/src/views/FactoryItem.svelte:100-115` | The route inputs (`:108`, `:109`) live inside the `{#if changePlan}` block (`:106`), gated by the checkbox at `:105`. The command only carries a replacement `if (changePlan)` (`:25-31`) |
| Studio command mirror | `web/ui/src/lib/factory.ts:29` | The TypeScript `FactoryCommand` already declares `implementerRouting?` and `reviewerRouting?` — the fields exist on the wire in both languages and are never sent |

There is **no route-only retry**. The gap is that the fields are on the wire,
the approval is the only place a route can live, and the only command that
appends an approval is the one that replaces the plan and specification.

## 4. Design

Every decision below is marked **Decided** and can be challenged by a
reviewer. Where the design record leaves a choice open and this slice does not
need to settle it, §8 records it rather than inventing an answer.

### 4.1 The command shape

**Decided — D1.** A retry command may carry new routing in the two fields the
wire type already has: `implementerRouting` and `reviewerRouting`
(`web/bridge/factory_types.go:267-268`). No new field is added to
`FactoryCommand`, no new HTTP route is added, and the existing
`POST /api/factory/items/{id}/commands` route carries the change. The
TypeScript mirror already declares both fields (`web/ui/src/lib/factory.ts:29`);
this slice makes the studio send them.

**Decided — D2.** A retry that carries routing and **no** `replacement` is a
**route-only retry**. A retry that carries a `replacement` is a
**plan-replacement retry** and keeps its existing meaning. The two may be
combined; D12 states what the combination means.

**Decided — D3.** The routing fields are read as *replacements for the named
route*, not as merges:

- the field **absent** — the route is inherited from the latest approval
  unchanged;
- the field present with a JSON value other than `null` — the route is
  **replaced wholesale** by that value. Any per-role overrides the previous
  routing object carried are dropped, because the operator re-stated the
  route;
- the field present with JSON `null` — **rejected as invalid input**. `null`
  is not a route, and silently reading it as "unchanged" would make a client's
  intent ambiguous. The studio never sends `null`.

**Decided — D4.** Each route is independent. A retry may change the
implementer route only, the reviewer route only, or both. The unchanged route
is inherited from the latest approval and is not rewritten.

**Decided — D5.** A route-only retry changes **nothing** in the configuration
except the named route(s). The reviewer instructions, the native/workspace
choice, the expected CI checks, the CI-acknowledgement flag, the round and CI
limits, the repository, the project root, the target branch and the host are
all inherited from the latest approval unchanged. In particular, **reviewer
instructions are not part of this slice**: they continue to ride the
plan-replacement path, which is where the studio offers them today.

### 4.2 What a route-only retry does to the approval history

**Decided — D6.** A route-only retry appends a **new approval revision** to
`item.Approvals`, exactly as a plan-replacement retry does. The new approval
is:

| Field | Value |
|---|---|
| `Revision` | `len(Approvals) + 1` — the existing rule (`factory_controller.go:1011`) |
| `Plan` | copied verbatim from the latest approval |
| `Spec` | copied verbatim from the latest approval |
| `PlanHash` | copied verbatim from the latest approval (equal to `factoryPlanHash(Plan, Spec)`, which the store re-validates at `factory_store.go:310`) |
| `Config` | the latest approval's config with the named route(s) replaced |
| `ApprovedAt` | now |

The previous approval is untouched. The store's append-only approval history
(`factory_store.go:419-431`) is unchanged, and the new approval satisfies the
existing per-approval validation (`factory_store.go:299-311`): a non-empty
plan, a matching plan hash, a normalizable config, and either expected CI
checks or the explicit no-required-CI acknowledgement.

The approved plan and the approved specification are therefore **unchanged**,
and the plan hash is unchanged, so the plan identity of the work item does not
move.

**Decided — D7.** An approval is appended **only when at least one route
actually differs** from the latest approval's config. Routes are compared as
raw bytes, the same comparison `factoryImplementerRouteChanged` uses
(`bytes.Equal`, `factory_controller.go:46`). A route-only retry whose routes
are byte-identical to the current ones appends **no** approval and therefore
behaves **exactly as today's retry** — a grant, a cleared decision, and the
`repairing` state, with no revision bump, no round invalidation and no change
to the plan, the specification or the routes. A retry that carries no routing
at all is likewise unchanged.

**Decided — D8.** A route-only retry appends a `FactoryGrant` exactly as
today's retry does: the same `additionalRounds`, the same `reason`, and the
command's `requestId` as the grant id (`factory_controller.go:1005`). The
allowance accounting, the exhaustion decision and the "previous attempts are
preserved" rule are unchanged — the earlier grants stay in the journal and the
total allowance grows by exactly the granted amount.

The route change is also **journalled**, as a history event of kind
`route_changed` whose message names the old and the new profile of each role
that changed: one clause per changed role, joined with `"; "`, each clause
`<role> route <oldProfile> -> <newProfile>`, where `<role>` is `implementer`
or `reviewer` and the profiles are the `profile` field of the routing object,
or `-` when the routing object names no profile. For example
`implementer route frontier -> cheap-review`. The approval history already
shows revision N → N+1 with an identical plan hash, which is the evidence;
the history event makes the *intent* legible in the studio's activity list.
The item's `ApprovedAt` does **not** change: it is the plan's approval time,
and the queue order depends on it.

### 4.3 Validation

**Decided — D9.** The route rules are extracted from `factoryAPI.preflight`
into a **shared helper** that validates the routes of a config, and both
`preflight` and the route-only retry path call it. The rules are the ones
preflight already applies (`web/bridge/factory_http.go:183-219`):

- the route is present and is a routing object — "explicit implementer/reviewer
  routing required";
- the route names a non-empty profile — "explicit implementer/reviewer routing
  profile required";
- the profile is known — "unknown implementer/reviewer routing profile";
- the profile's preset for that role resolves — "implementer/reviewer model
  preset unavailable";
- the preset's provider is configured — "implementer/reviewer provider
  unavailable";
- the provider's credential is available — "implementer/reviewer provider
  credential unavailable".

Sharing the helper is what makes "the same rules apply" a structural property
rather than a convention: the two call sites cannot drift.

The helper **names the role in every message**. Two of the six messages are
role-less today — "explicit routing profile required"
(`factory_http.go:196`) and "unknown routing profile" (`:204`) — and the
helper role-names them, because it checks **both** routes of a config and a
role-less message would not say which route was at fault. This changes the
error text of the plan-replacement path for those two cases; it changes no
rule, no status code and no outcome.

**Decided — D10.** A route-only retry is validated by that shared helper, run
against the **candidate config** — the latest approval's config with the named
route(s) replaced. The helper checks **both** routes of the candidate config,
so the unchanged route is re-validated too: a route that has become invalid
since the approval — a profile that was removed, a provider whose credential
was withdrawn — refuses the retry rather than producing an item that cannot
spawn a worker.

The route-only retry does **not** run the rest of `preflight` — the registered
repository, the trusted project, the target branch, the native/workspace
choice, the workspace image and the GitHub forge capability. A route-only
retry changes exactly two fields; the other conditions were validated at
approval time and are re-observed by the loop itself, and re-checking them
would let an unrelated, unfixable-from-this-surface failure block the one
action the operator needs. This is deliberately more validation than today's
plain retry receives — a plain retry is not preflighted at all — and
deliberately less than the plan-replacement retry receives, because a
replacement can change the repository, the project root, the target branch,
the workspace and the expected CI checks, all of which need re-validation. The
plan-replacement path keeps the full `preflight`, including the route rules it
already ran; only the two role-less messages become role-named (D9).

**Decided — D11.** A route that fails validation **refuses the command**. The
error is the existing configuration error (`errFactoryConfig`), which
`factoryWriteError` maps to **422 `unsupported`** (`factory_http.go:309-310`).
Nothing is mutated: no grant, no approval, no state change, no version bump.
The refusal happens before the controller is called, exactly as the
plan-replacement retry's preflight does (`factory_http.go:108-115`), so an
invalid route can never half-apply.

**Decided — D12.** When a retry carries **both** a `replacement` and routing
fields, the routing fields replace the named route(s) **in the replacement's
config**, and the resulting candidate config is what the plan-replacement path
validates. A plan-replacement retry that carries no routing fields is
byte-for-byte unchanged: its replacement's config is validated as today and
its approval is appended with the replacement's plan and specification. There
is one source of truth for a given route in any one command.

### 4.4 The interrupted round

**Decided — D13.** Because the approval revision advances, the interrupted
round's review evidence **no longer satisfies the readiness gate**. The
readiness gate compares the round's `ApprovalRevision` with the latest
approval's revision (`factory_controller.go:284`, `:303` and `:325`); a new
revision means the previous round's clean review cannot mark the pull request
ready.

That is the intended consequence, and it is the same one a plan replacement
already produces: **a new model does not inherit the previous model's clean
review.** The round itself, its CI evidence, its implementation report and its
review report stay in the journal and remain visible in the studio; they are
not deleted, invalidated in place, or rewritten. Only their standing as
*current* readiness evidence changes.

### 4.5 Making the new route effective

Worker routing is read from the latest approval's config at spawn time
(`factory_workers.go:23,36,40`), so the new route is in force for every
worker spawned after the retry.

**Decided — D14.** The **reviewer** needs no further machinery. The factory
spawns a fresh reviewer for every review round
(`factory_controller.go:262`, `factory_effects.go:144`), and the spawn reads
`conf.ReviewerRouting` from the latest approval. A new reviewer route is
therefore used by the next review.

**Decided — D15.** The **implementer** must not run the next round on the
previous route. The factory already distinguishes "spawn a fresh implementer"
from "resume the existing implementer": `factoryImplementerRouteChanged`
compares the last two approvals' implementer routing, native flag and
workspace (`factory_controller.go:40-47`), and the controller consults it
where it chooses between the two (`factory_controller.go:287-292`). **This
slice does not change that rule or its comparison.** It requires that the rule
is also consulted where a retry's next implementer effect is chosen, so that a
changed implementer route yields a fresh implementer — a newly spawned worker
using the new approval's implementer routing — rather than a resumed
implementer on the previous route.

The one place where that consultation is currently missing is the branch that
turns a completed `resume` effect into an `implement` effect
(`factory_controller.go:240-244`): a retry that lands there — the exhausted
and no-progress positions, where the implementer has already been resumed —
would otherwise continue the previous implementer on its previous route, and
the new implementer route would never take effect. Consulting the existing
rule there is what closes that.

Two positions are already correct and are not changed: the reviewer-park
branch (`factory_controller.go:287`) already consults the rule, and the
operator-attested fresh-resume path already opens a new ACP session with the
latest approval's routing (`factory_ownership.go:182-190`), so the new route
is in force there and no further fresh spawn is required.

**Decided — D16.** The route change means a **fresh implementer**, not a
continued conversation. The fresh implementer starts from the last round's
head (`factory_workers.go:29-32`), so the work continues from where it was;
the **conversation does not**. That is the factory's existing behaviour for a
changed implementer route, and this slice does not change it. Whether the
operator should instead be *offered* the choice to continue the existing
conversation on the new route, and what a fresh start should carry, are open
questions (§8.1, §8.2) that need the user.

**Decided — D17.** The retry guards are unchanged. A retry is refused while
any effect is dispatched or uncertain, while the item is in the `recovering`
state (`factory_controller.go:922`), and while a worker is active unless the
previous worker is attested stopped
(`factory_controller.go:917-946`). A route-only retry does not get a weaker
guard than today's retry. What happens to the **previous** implementer's
session and workspace after a route change replaces it is an open question
(§8.2).

### 4.6 The studio

**Decided — D18.** The studio exposes the route change **independently of the
plan-replacement checkbox**. The two route inputs — "New implementer profile
(optional)" and "New reviewer profile (optional)" — become available in the
recovery decision block without the plan checkbox being ticked, and the
command carries them in the top-level `implementerRouting` and
`reviewerRouting` fields.

**Decided — D19.** The plan-replacement path keeps its existing transport. When
the operator ticks the plan-replacement checkbox, the routes continue to ride
inside the replacement's config, and the top-level route fields are **not**
sent. The existing studio test that asserts `implementerRouting` and
`reviewerRouting` are undefined on a plan-replacement retry therefore stays
true.

**Decided — D20.** The two route inputs are the single source of truth for
the new route in the studio, and the transport depends on the plan checkbox:

| Plan checkbox | Route inputs | What the command carries |
|---|---|---|
| unticked | both empty | `replacement` absent; no top-level routing fields — today's retry |
| unticked | one or both filled | `replacement` absent; the filled route(s) in the top-level fields — a route-only retry |
| ticked | either | `replacement` present, carrying the plan, the specification, the reviewer instructions and the routes in its config; no top-level route fields — today's plan-replacement retry |

**Decided — D21.** The plan-replacement checkbox is relabelled, because the
routes are no longer gated by it: "Change approved plan, routes or reviewer
instructions" becomes **"Change approved plan or reviewer instructions"**. The
route input labels are unchanged. The existing studio test that finds the
checkbox by its label is updated to the new text.

**Decided — D22.** The recovery block shows the routes currently in force,
taken from the latest approval, so the operator can see what they are
changing. The route inputs start **empty**, and an empty input means
"unchanged" — not "clear the route".

The block also states the consequence the operator must know: **a route-only
retry starts a fresh round.** The interrupted round's review is not reused, and
one granted round is spent. The reason is D13 — the approval revision advances,
so the previous round's review no longer satisfies the readiness gate, and the
loop has no branch that re-reviews a round in place.

**Decided — D23.** The retry button's enablement rule is unchanged: a reason
is required, at least one additional round is required, and — only when the
plan checkbox is ticked — a replacement plan is required. The route inputs are
optional in every case.

### 4.7 What this does not change

- The factory loop, its effect kinds, its state machine, its readiness gate,
  its draft-compensation path, the handoff contract, the workspace format and
  the `Forge` interface are unchanged. The one effect-derivation requirement
  is D15, and it is the existing spawn-versus-resume rule consulted in one
  more place; the rule's comparison, its two outcomes and every other
  derivation are unchanged.
- The `Forge` interface, the persisted workspace structures and the workspace
  version are unchanged.
- The reviewer role is unchanged: a reviewer is still spawned fresh for each
  review round, and a reviewer report still carries findings and needs no
  narration.
- The approval and grant history rules, the five-round default, the
  CI-acknowledgement rule and the PR description contract are unchanged.

## 5. Contract sketch

Proposed wire shape. The route is the existing
`POST /api/factory/items/{id}/commands` route; no new route is added.

```http
POST /api/factory/items/{id}/commands
Content-Type: application/json

{
  "requestId": "1f0c…",
  "kind": "retry",
  "decisionId": "decision-item-7-9",
  "reason": "the reviewer model stalls on this repository",
  "additionalRounds": 2,
  "expectedVersion": 7,
  "implementerRouting": { "profile": "frontier" },
  "reviewerRouting":   { "profile": "cheap-review" }
}
```

→ `200` with the updated item. The approvals array has one more entry:

```json
{
  "revision": 3,
  "plan": "<the previous approval's plan, byte for byte>",
  "spec": "<the previous approval's specification, byte for byte>",
  "planHash": "<the previous approval's plan hash>",
  "config": {
    "repoId": "repo-1",
    "projectRoot": "/Users/x/repo",
    "targetBranch": "main",
    "implementerRouting": { "profile": "frontier" },
    "reviewerRouting":   { "profile": "cheap-review" },
    "reviewerInstructions": "<unchanged>",
    "maxRounds": 5
  },
  "approvedAt": "2026-11-14T…"
}
```

The grants array has one more entry carrying the additional rounds, and the
plan and specification in the item are the previous approval's.

An invalid route is refused before anything is written:

```json
{ "error": "factory configuration or capability unsupported: unknown implementer/reviewer routing profile",
  "code": "unsupported" }
```

→ **422**, with the item unchanged: the same approvals, the same grants, the
same version.

## 6. Missing access and partial failure

- An invalid route refuses the whole command. There is no partial application:
  a retry that changes one valid route and one invalid route applies neither.
- The unchanged route is re-validated with the changed one, so a route that
  has become invalid since the approval — a profile that was removed, a
  provider whose credential was withdrawn — refuses the retry rather than
  producing an item that cannot spawn a worker.
- The route change refuses the retry while a worker is still active, exactly
  as today's retry does. The operator's existing named-worker attestation
  path is the way past it.
- An invalid route never reaches the store: the refusal happens before the
  controller is called, so the request id is not recorded and the retry can be
  corrected and sent again.
- A replayed command with the same request id returns the original item
  (`factory_store.go:325-328`); a route-only retry therefore cannot append a
  second approval. The same request id with different content is a conflict.
- A stale `expectedVersion` is a conflict, as today.

## 7. Parallel-safety against the daemon track

This slice adds no HTTP route and touches no `internal/...` package, so it
does not collide with the daemon track's route-table or child-process work.
The files it touches are `web/bridge/factory_types.go`,
`web/bridge/factory_controller.go`, `web/bridge/factory_http.go`,
`web/bridge/factory_controller_test.go`, `web/bridge/factory_http_test.go`,
`web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`
and the rebuilt `web/bridge/static/**` assets. The landing-time re-check
against the daemon track's then-current diff is retained.

`web/bridge` remains Go standard library only: no `marshal/internal` import
and no third-party module, as `TestWebIsStdlibOnly`
(`web/bridge/boundary_test.go`) enforces.

## 8. Open questions

Not decided here; they need the user, not an implementer's judgement.

1. **The fresh-start handoff summary.** When the implementer route changes,
   the factory starts a fresh implementer (D16). What should that fresh start
   carry? The design record proposes "the approved scope, current code
   revision, prior attempts and findings, and unresolved blockers", but that
   is a proposal, and the record states that "how conversation context is
   carried across providers/harnesses, including any limitations, remains to
   be investigated". The mechanism, the exact content and the provider
   limitations are all unresolved. This slice does not build a handoff
   summary, and the fresh implementer therefore starts with the same prompt
   the factory gives any implementer.
2. **Continuing the existing conversation on the new route.** The design
   record confirms that both options — continue the existing conversation,
   and start fresh with a handoff summary — "would be useful", and the
   question of which the operator picks is left to the user. The
   implementation side is unproven: the record does not assume native session
   portability between providers. Until it is settled, D16 stands: a route
   change starts a fresh implementer, and the operator is not offered the
   choice.
3. **The previous implementer's session and workspace.** When a route change
   replaces the implementer, what happens to the previous implementer's
   session, its agent record and its retained workspace — stopped, parked,
   retained for inspection, or offered to the operator? The retry guard
   already refuses while a worker is active
   (`factory_controller.go:917-946`), and the operator-attested recovery path
   already exists, but the disposition of the superseded implementer after a
   *successful* route change is not decided anywhere.
4. **Whether the operator should be warned that a route change discards the
   conversation.** If the answer to question 2 is "both", the studio must say
   what continuing would cost before the operator commits.
5. **Whether the `no_progress` guard survives a route-only retry.** The guard
   (`factory_controller.go:329`) requires the previous round's approval
   revision to equal the latest, so after any new revision the first round is
   exempt from "repair did not change the tracked tree". That exemption is
   arguably wrong for a *route* change, because the plan did not change and the
   tree expectation stands. Fixing it is a state-machine condition change, so
   this slice leaves it unchanged and records the consequence here.
6. **Whether a route-only retry should also be able to change the reviewer
   instructions or the expected CI checks.** This slice is routes only; those
   remain reachable only through the plan-replacement path (D5). The operator
   may want them, and that is a scope decision.

## 9. Acceptance criteria

For this slice:

- a retry carrying `implementerRouting` and no replacement appends exactly
  one approval, whose `Plan`, `Spec` and `PlanHash` equal the previous
  approval's byte for byte, and whose `Config.ImplementerRouting` equals the
  supplied route; the previous approval is unchanged;
- a retry carrying `reviewerRouting` and no replacement appends exactly one
  approval whose `Config.ReviewerRouting` equals the supplied route and whose
  implementer routing equals the previous approval's;
- a retry carrying both routes appends exactly one approval carrying both,
  and changes nothing else in the config;
- a retry carrying a route that is byte-identical to the current one appends
  **no** approval, appends the same grant today's retry appends, clears the
  decision and leaves the item in the `repairing` state — the same result as
  today's retry with no replacement;
- a retry carrying no routing and no replacement is byte-identical in effect
  to today's retry: one grant, no approval, the same state;
- a route-only retry appends a grant with the command's `additionalRounds` and
  `reason`, the item's total allowance grows by exactly that amount, and the
  earlier grants are unchanged;
- a route-only retry leaves the plan and the specification unchanged, and the
  interrupted round's review no longer satisfies the readiness gate — the
  item cannot reach `ready` on the previous round's clean review — while the
  round, its CI evidence and its review report remain in the journal;
- a route whose profile is missing, unknown, unresolvable, or whose provider
  is unconfigured or uncredentialed is refused with the configuration error
  and the 422 status, and the item is unchanged: the same approvals, the same
  grants and the same version;
- the refusal message names the role whose route was at fault;
- the unchanged route is re-validated, so a route that has become invalid
  since the approval refuses the retry;
- a route-only retry is **not** blocked by an unrelated preflight condition —
  a workspace whose image is gone, say — while a plan-replacement retry is
  still blocked by it; the asymmetry is asserted, not accidental;
- the route change is journalled as a history event naming the old and the new
  profile for each role that changed, and the item's `ApprovedAt` is
  unchanged;
- a retry carrying both a replacement and routing fields validates the
  combined config and appends one approval whose plan and specification are
  the replacement's and whose named route is the supplied one;
- a plan-replacement retry carrying no routing fields validates the
  replacement's config and appends one approval whose plan and specification
  are the replacement's, exactly as today;
- the next implementer worker uses the new approval's implementer routing
  when the implementer route changed — the previous implementer's route is
  not used for the next implementer round — and the existing implementer is
  resumed when it did not;
- the next reviewer worker uses the new approval's reviewer routing;
- a retry is refused while an effect is dispatched or uncertain, and while a
  worker is active unless the previous worker is attested stopped, exactly as
  today;
- a replayed route-only retry returns the original item and does not append a
  second approval, and the same request id with different content is a
  conflict;
- the studio sends the route in the top-level `implementerRouting` field and
  sends no replacement when only a route is changed, and the retry works
  without the plan-replacement checkbox being ticked;
- the studio sends the routes inside the replacement's config and sends no
  top-level route fields when the plan is replaced, as today;
- the studio renders the plan-replacement checkbox with the label "Change
  approved plan or reviewer instructions", and the studio test finds the
  checkbox by that label;
- the studio shows the routes currently in force and leaves the new-route
  inputs empty by default;
- the studio states that a route-only retry starts a fresh round, that the
  interrupted round's review is not reused, and that one granted round is
  spent;
- the studio test file keeps its vitest conventions: flat top-level
  `it('lowercase sentence…')` cases and no `describe` block;
- `TestWebIsStdlibOnly` passes, and no new `marshal/internal` import appears
  under `web/`;
- no new HTTP route is added, and the factory loop, its effect kinds, its
  state machine, its readiness gate, its draft-compensation path and the
  `Forge` interface are unchanged.

## 10. Non-goals

- the §5 "continue the existing conversation versus start fresh with a
  handoff summary" choice, and the handoff summary itself. The design record
  leaves the mechanism open; this slice decides only that the route change is
  effective, and that it means a fresh implementer. The spawn-versus-resume
  machinery already distinguishes the two cases once a route changes, and the
  fresh-start handoff summary remains open (§8.1, §8.2);
- the disposition of the superseded implementer's session and workspace
  (§8.3);
- reviewer instructions as a route-only change: they remain part of the
  plan-replacement path (D5);
- automatic merging, GitLab, specialist review focuses, notifications, and
  workflow extensibility — each recorded with its own decision status;
- changing the factory loop, its effect kinds, its state machine, its
  readiness gate, its draft-compensation path, the handoff contract, the
  workspace format, or the `Forge` interface. D15 is the one narrow
  requirement in this area, and it is the existing spawn-versus-resume rule
  consulted in one more place, not a new rule, state, effect kind or
  transition;
- a new HTTP route, a new forge, a new effect kind, a new command kind, or a
  new field on `FactoryCommand`;
- changing the plan, the specification, the reviewer instructions, the CI
  expectations or the round limits through a route-only retry;
- per-role routing overrides in the studio. The route inputs name a routing
  **profile**, exactly as today's plan-replacement inputs do; a route that
  carries per-role overrides is still accepted on the wire and still replaces
  the named route wholesale (D3), but the studio does not offer an editor
  for overrides.

## Branch review record

This specification received an independent `glm-5.3-flash` review through
`ollama-cloud` on 2026-11-13, and the findings were addressed before the
implementation began.

| Review finding | Resolution |
|---|---|
| D9 quoted two preflight messages that do not exist in the code — "explicit implementer/reviewer routing profile required" and "unknown implementer/reviewer routing profile" — while the code produces the role-less "explicit routing profile required" and "unknown routing profile", and the claim that the messages name the role was false for them | D9 now quotes the real strings, names the two role-less messages with their line numbers, and states that the shared helper role-names every message because it checks both routes of a config. D10 now says the plan-replacement path keeps the full `preflight` and that only those two messages change. The §5 sketch shows the role-named message |
| The decision label D12 was used twice | The §4.4 decision was folded into D6, which already carries the plan, specification and plan hash verbatim, and §4.4 now opens with D13. The labels D1–D23 are unique |
| D21 relabels the studio's plan-replacement checkbox but had no acceptance criterion | §9 now requires the studio to render the checkbox with the label "Change approved plan or reviewer instructions" and the studio test to find it by that label |
| D17 listed the retry guards but omitted the `recovering`-state refusal | D17 now names it, with its line number |
| D8 said the route change is journalled without pinning the event's shape | D8 now pins the kind `route_changed` and the message format — one clause per changed role, joined with "; ", each clause `<role> route <oldProfile> -> <newProfile>` |
| D13 cited two of the three revision-keyed readiness conversions | D13 now cites `:284`, `:303` and `:325` |

### Independent design proposal

Before the review, a second planner produced an independent design proposal
for the same slice, without seeing this specification. Its mechanism — a new
approval revision carrying the previous plan, specification and plan hash
verbatim, with only the routing substituted — matches D6–D8, and its
conclusion that the route change must yield a fresh implementer matches D15.
Three points it raised were adopted here:

- the route rules are extracted into a **shared helper** called by both
  `preflight` and the route-only retry path, so "the same rules apply" is
  structural rather than a convention, and the messages name the role (D9);
- the route-only retry runs **route validation only**, not the whole
  `preflight`, so an unrelated condition the operator cannot fix from this
  surface does not block the one action they need — while the
  plan-replacement path keeps the full `preflight` (D10);
- the route change is **journalled** as a history event naming the old and the
  new profile, and the item's `ApprovedAt` is left alone because the queue
  order depends on it (D8).

Two points it raised were recorded as open questions rather than decided: the
`no_progress` guard's exemption after a new revision (§8.5), and whether the
route-only retry should also carry the reviewer instructions or the expected CI
checks (§8.6).

## Implementation record

Delivered on branch `studio/factory-recovery-routing-implementation` as Tasks
1–6 of the [plan](plans/2026-11-14-factory-recovery-routing-plan.md),
test-first, with one commit per task. The studio task ran concurrently with
the Go tasks in a separate worktree, because `web/ui` is a separate npm
package with a disjoint file set.

| Task | Deliverable | Commit |
|---|---|---|
| 1 | The shared route rules: `factoryValidateRoutes` extracted from `preflight` into `web/bridge/factory_routing.go`, with every message naming its role | `3cc82111` |
| 2 | The route-only retry appends an approval: the selection reader, the candidate configuration, the approval builder, the journal message, and the `"retry"` case of `factoryApplyCommand` | `5a2a9126` |
| 3 | The route-only retry is validated before the controller: the `retry` branch of `factoryAPI.Command`, with route-only running the route rules only and plan-replacement keeping the full preflight | `e892a9cb` |
| 4 | A changed implementer route yields a fresh implementer: D15's one consultation site, guarded by the approval revision | `b6669403` |
| 5 | The studio sends a route-only retry: the route inputs ungated from the plan-replacement checkbox, the checkbox relabelled, the routes in force shown, and the consequence stated | `4a4163bb` |
| 6 | The rebuilt embedded assets, the gates below, and this record | this commit |

Files: `web/bridge/factory_routing.go` and `factory_routing_test.go` are new;
`web/bridge/factory_http.go`, `factory_controller.go`,
`factory_controller_test.go`, `factory_defaults_http_test.go` and
`factory_workers_test.go` are modified; `web/ui/src/views/FactoryItem.svelte`
and `FactoryItem.test.ts` are modified; `web/bridge/static/**` is rebuilt.
Everything in `web/bridge` is standard library only, no route was added, the
`Forge` interface is unchanged, and no production file outside `web/bridge` and
`web/ui` is touched.

Acceptance gates, run from the repository root on this branch:

| Gate | Result |
|---|---|
| `go test ./web/bridge -run 'TestFactoryRoute\|TestFactoryValidateRoutes\|TestFactoryPreflightUsesTheSharedRouteRules\|TestFactoryPlanReplacementRetry\|TestFactoryRouteChange\|TestFactoryUnchangedRoute\|TestFactoryFreshResume\|TestFactorySpawnFactoryWorker\|TestWebIsStdlibOnly' -count=1` | PASS — `ok marshal/web/bridge 2.191s` |
| `go test ./web/bridge -run 'Factory' -count=1` | PASS — `ok marshal/web/bridge 33.317s`; the pre-existing factory tests are unmodified |
| `go test ./web/bridge -run '^TestWebIsStdlibOnly$' -count=1` | PASS — `ok marshal/web/bridge 0.405s` |
| `go test ./web/bridge -count=1` | PASS — `ok marshal/web/bridge 124.946s` |
| `gofmt -l web/bridge` | no output |
| `go vet ./web/...` | no output, exit 0 |
| `CGO_ENABLED=1 go build ./...` | exit 0 |
| `cd web/ui && npm run test` | PASS — 88 test files, 941 tests, 0 failures |
| `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 20m` | PASS — exit 0, no `FAIL` line |

Every test is offline and deterministic: no live forge, no network, no container
runtime, no model provider and no developer credentials.

**One substitution was recorded.** The plan named
`TestFactorySpawnFactoryWorkerUsesTheLatestApprovalRouting` as the test that
must extend an offline fleet fixture, and warned that the fixture might be
expensive to build. It was: `parkedFixture` has no registered repository and
no runtime for the new agent, and `SpawnFactoryWorker` forces a git source
because it requires a non-empty `RepoID`. The test instead uses the
repository's existing offline pair — `newBareRepoFixture` as the registered
repository's URL (a real `git clone --mirror` against a local bare repository,
no network) plus `captureTransport` for `session/new`. The criterion is
exercised, not weakened: the real `Fleet.SpawnFactoryWorker` runs, and the
assertion is on the captured `session/new` routing, whose profile is the
reviewer's `cheap-review` and distinct from the implementer's `frontier`.

**One branch-review finding was examined and does not reproduce.** The review
reported that a replayed route-only retry re-validates against the current
model configuration, so that withdrawing the profile after the original retry
would make the replay answer 422 instead of the original item. It does not:
`factoryAPI.Command` answers a recorded request id from the store before it
reaches the route validation (`web/bridge/factory_http.go:89` precedes the
retry block at `:110`), so the replay is answered from the recorded request
and is never re-validated. `TestFactoryRouteOnlyRetryReplaysAfterTheProfileIsWithdrawn`
pins that: it withdraws the profile the retry named and requires the replay
to return the original item. The finding was left as a test rather than a
change, because the behaviour it describes is the behaviour §6 promises.

The open questions in §8 remain open: the fresh-start handoff summary (§8.1),
continuing the existing conversation on the new route (§8.2), the superseded
implementer's session and workspace (§8.3), warning the operator that a route
change discards the conversation (§8.4), the `no_progress` guard's exemption
after a new revision (§8.5), and whether a route-only retry should also carry
the reviewer instructions or the expected CI checks (§8.6). None of them is
decided by this slice.
