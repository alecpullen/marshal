# Preview and test-environment links in the PR experience — feature spec

Date: 2026-11-15
Status: spec for the sixth implementation slice of the studio-fleet-factory
archive — the presentation slice the fifth slice deferred. No product code is
changed by this document. This specification received an independent
`glm-5.3-flash` review through `ollama-cloud` on 2026-11-15; see the branch
review record.

## 1. Provenance

This is the **later slice** the discovery spec reserved for itself. Its §1
table separates two things and gives this slice exactly the second:

| | Status | Owner |
|---|---|---|
| The **discovery mechanism** | Agreed | Delivered by the fifth slice ([preview-environment-discovery-spec.md](preview-environment-discovery-spec.md)) |
| The **refined presentation** — showing those links, with their provider-reported status and associated revision, in the pull-request experience | Proposal, then | **This slice** |

The proposal being delivered is from
[design-notes §6](design-notes.md#6-forge-experience-and-narrated-engineering-reports):

> Show existing preview/test environment links with their provider-reported
> status and associated revision when available, so users can tell which change
> they are trying.

The user directed this slice on 2026-11-15: *"Show discovered deployment and
test-environment links in the PR experience, including provider-reported status
and revision."* Four presentation decisions were taken in the design
conversation and are fixed here:

| # | Question | Decision |
|---|---|---|
| 1 | Where the links appear | **Both**: a snapshot section in the factory-written PR description, and a live card in the studio's work-item view |
| 2 | What "associated revision" means | An **equality badge** — "matches the PR head" / "not the PR head" — by SHA string comparison only |
| 3 | Gitea/Forgejo commit-status `target_url`s | **Clearly-labelled provider links** under the preview heading, never called previews |
| 4 | How the description's snapshot is sourced | **Fetch at render** (`set_ready`), nothing persisted; the studio fetches live |

Everything shown is a value the forge or the round's CI evidence reported.
Marshal adds no inference beyond the single SHA-string comparison the badge
states outright.

## 2. Summary

When a factory round marks its pull request ready, the description it writes
gains a **Preview and test environments** section: the preview-environment
report's deployment rows (provider status, preview link, recorded revision,
equality badge), the configured-environment links, and — where the forge serves
no deployment records — clearly-labelled links from commit statuses. The same
content appears as a **live card** on the studio's work-item view, fetched on
view and refreshable, with the full gap vocabulary.

The frozen preview report contract is read, never changed. The factory's
persisted state is never changed. Nothing is persisted, cached, or polled.

## 3. What exists today

Measured on this tree, 2026-11-15.

| Piece | Where | State |
|---|---|---|
| Preview-environment report | `web/bridge/preview_environment.go:18`, `preview_environment_report.go:48` (`Fleet.PreviewEnvironments(ctx, repo, PreviewFilter)`) | Implemented, read-only, contract frozen by `previewCapabilityOrder` (`preview_environment.go:99`) |
| Preview route | `GET /api/repos/{id}/preview-environments?ref=&sha=` (`preview_environment_http.go`, registered in `web/bridge/http.go`) | Implemented; **no studio consumer yet** |
| PR description composer | `composeFactoryPRBody(round FactoryRound)` (`web/bridge/factory_prbody.go:11`) | Renders `## CI` + the narration sections; `factoryCIBody` (`:58`) "states CI evidence rather than omitting it" |
| The `set_ready` effect | `web/bridge/factory_effects.go:242-283` | Pre-checks the PR (`forge.GetFactoryPR`, `:250`), confirms `before.HeadSHA == e.ExpectedHead` (`:258`), composes the body (`:276`), writes it before clearing draft (`:280`) |
| The pre-checked PR record | `FactoryPR` (`web/bridge/factory_types.go:80`) via `FactoryForge.GetFactoryPR` (`factory_forge.go:12`) | Carries `HeadRef`, `HeadSHA`, `BaseSHA`, `Draft` |
| Round CI evidence | `FactoryCI`/`FactoryCheck` (`factory_types.go:98-115`); built in `forge_github_factory.go:108-174` | `FactoryCheck.URL` for `Key.Kind == "commit_status"` is the provider's `target_url` (`forge_github_factory.go:135-140`, `:173`); newest status per context kept |
| Factory item wire shape | `FactoryItem` (`factory_types.go:233`); newest PR is the **last** `PRs` element (`factory_controller.go:431`) | Carries `RepoID`, `Branch`, `PRs` |
| Studio work-item view | `web/ui/src/views/FactoryItem.svelte`; "Pull requests" card at `:156` | No preview content |
| Studio wire mirrors | `web/ui/src/lib/factory.ts` (hand-written, mirroring `web/bridge` types) | The established precedent for bridge types outside `web/bridge/contract` |
| Studio API wrappers | `web/ui/src/lib/api.ts:1342-1354` | `request()`-based; no preview function |

There is **no** preview content anywhere in the PR experience today — the
fifth slice was bridge-only by design (its D23).

## 4. Design decisions

**Decided — D1. Two surfaces.** The links appear in the factory-written PR
description and in a studio work-item card. The description reaches forge-side
and external readers as a self-contained snapshot; the studio card is live.
Neither surface infers; both render the same report fields.

**Decided — D2. Two data sources, zero new forge calls.** Deployments and
environments come from the existing read-only report
(`Fleet.PreviewEnvironments`). The labelled provider links come from the
round's own CI evidence (`FactoryCheck` rows with `Key.Kind == "commit_status"`
and a non-empty `URL`) — same revision scope the `## CI` section already
trusts. The status links are read from `round.CI` only when `round.CI != nil`
and `round.CI.HeadSHA == round.HeadSHA` — the same validity guard
`factoryCIBody` uses — and the whole subsection is absent otherwise. The
frozen preview contract, `Forge`, `DiscoveryForge` and `PreviewForge` are
untouched; no route is added.

**Decided — D3. Fetch at render; nothing persisted.** The `set_ready` effect
fetches one report immediately before composing the body. The studio fetches
the same endpoint on view and on manual refresh. No report is stored on the
round, the item, or anywhere else: the written description *is* the
written-time record, and the factory's persisted contract
(`FactoryRound`/`FactoryItem`/`contract.ts`) does not grow. The effect's
order is: PR pre-check → fetch → compose → write description, so the badge
compares against the pre-confirmed head. The composer becomes
`composeFactoryPRBody(round FactoryRound, preview *PreviewReport, headSHA
string) (string, error)`: `preview == nil` or `preview.Status ==
"unavailable"` renders the §5.6 unreadable line; status links are derived
from `round` inside the composer. The composer's existing call sites in
`factory_prbody_test.go` and `factory_effects_test.go` update with the
signature.

**Decided — D4. The `previewSource` seam.** `factoryEffects` gains one field,
`previewSource func(context.Context, Repo, PreviewFilter) (*PreviewReport,
error)`. When nil, the effect uses `f.fleet.PreviewEnvironments`. Tests stub
the seam; production wiring is one defaulting line. The effect calls it with
`PreviewFilter{Ref: before.HeadRef}` — **never `SHA`**: filtering by SHA would
hide exactly the older-revision previews whose "not the PR head" badge is the
point (discovery D7: `?ref=` is the primary filter and each deployment carries
its own SHA).

**Decided — D5. Equality badge.** Each deployment row compares the
provider-reported `sha` with the confirmed PR head (`before.HeadSHA` in the
description; the newest factory-recorded PR's `headSha` in the studio) by
**case-insensitive exact string equality**. No prefix matching, no ancestry,
no "N commits behind". The description uses these strings:

| Condition | Badge text |
|---|---|
| Both present, equal ignoring case | `matches the PR head` |
| Both present, different | `not the PR head` |
| Provider reported no revision | `revision not reported by the provider` |

The description's scope line names the head it was written against ("…PR head
`abc1234` at write time"), so the snapshot stays truthful as the head moves
later. In the studio the recorded head can be stale after an external push:
label the comparison `matches the last factory-recorded PR head` or `differs
from the last factory-recorded PR head`, and show the short recorded SHA. Never
claim that this is the current forge head. When no PR or recorded head exists,
show revisions without an equality badge.

**Decided — D6. Labelled provider links.** Where commit-status checks carry
`target_url`s, they render under a `Links reported by commit statuses:` line —
`- \`deploy/preview\` (\`success\`): https://…` — never under the word "preview",
never called environments. Check-run rows (`Key.Kind == "check_run"`) are
excluded: their URLs are CI pages, which `## CI` already presents (a flagged
assumption — §11). The subsection header text is exactly `Links reported by
commit statuses:`.

**Decided — D7. The section always states, never omits.** Mirroring
`factoryCIBody`'s rule ("states CI evidence rather than omitting it"), the
**Preview and test environments** section is always present in a `set_ready`
body, and states one of: the rows it has, the gaps it found, the empty case, or
the unreadable case. Absence and inaccessibility stay textually distinct.

**Decided — D8. Placement.** The section goes directly after the `## CI`
block and before `## Issue`, matching the approved ready-review mockups (checks
and previews above the walkthrough). Heading level `##`, title exactly
`Preview and test environments`.

**Decided — D9. Bounds.** The description renders at most **10** deployment
rows, **10** environment rows and **10** status links, each truncated list
followed by `- …and {N} more` (N = omitted count). Rationale: the forge's
65536-char body limit and the narration slice's bounded-body discipline; the
report itself already caps deployments at 20 and environments by its page
bound. The studio card renders the full report.

**Decided — D10. Rendering safety.** Provider strings (environment, state,
name, description) render in protected inline code spans: newlines become
spaces, and the wrapping backtick run is one longer than the longest run inside
the value, padded with single spaces when the value begins or ends with a
backtick (the run-length rule matches `codeFence`; the padding is the
inline-span addition, which `codeFence` does not need). Apply this protection
to all provider-controlled text, including ref, SHA, gap detail and URL values;
cap each displayed provider text at 512 Unicode code points with an ellipsis,
after replacing control characters with spaces. Preserve full underlying data.

A URL is clickable only if it parses as an absolute HTTP or HTTPS URL with a
nonempty host, contains no whitespace, control characters, angle brackets or
backticks, and is at most 2048 bytes. Do not trim or transform an invalid URL
into a valid one. For valid URLs in the PR description, render the URL as a
Markdown autolink `<https://…>`; render rejected nonempty values as bounded
protected inline code prefixed `Untrusted URL:` (never an href or autolink).
In Studio, use the same validation, render valid URLs as links, and render
invalid URLs as text. `noopener noreferrer` remains required on links opened in
a new tab. Apply this rule to environment, deployment, log and status URLs.

**Decided — D11. Best-effort fetch.** Limit preview discovery to a five-second
child context of the effect context; cancel it immediately after discovery. A
child timeout or fetch error must not prevent the description write or ready
transition when the parent remains live. Parent cancellation still cancels the
effect normally. A fetch error, or a report with `status == "unavailable"`,
produces one fixed line (§5) and the description is written normally. Partial
failure is the report's own vocabulary (its gaps). `set_draft` still writes no
body; the next round's `set_ready` is the only refresh — the narration spec's
excluded regeneration proposal stays excluded.

**Decided — D12. Studio typing.** `web/ui/src/lib/preview.ts` hand-writes TS
mirrors of `PreviewReport`, `PreviewCapability`, `PreviewGap`,
`PreviewEnvironment`, `PreviewDeployment` — the `factory.ts` precedent for
bridge wire types that live outside `web/bridge/contract`. The mirror fields
are exactly the JSON fields the report serialises: `PreviewReport`,
`PreviewEnvironment` and `PreviewDeployment` as frozen at
`preview_environment.go:18-77`; the capability as `key`/`state`/`detail` and
the gap as `key`/`section`/`state`/`detail`, matching the discovery
contract's `DiscoveryCapability`/`DiscoveryGap` JSON. `contract.ts` is
untouched and `TestTypeScriptContractIsCurrent` passes unmodified. `api.ts`
gains `getPreviewEnvironments(repoId, ref)` building
`GET /api/repos/{id}/preview-environments?ref=…` (the query parameter omitted
when `ref` is empty).

**Decided — D13. Scope resolution.** The description fetches with
`Ref: before.HeadRef`; the studio with `?ref={item.branch}`. When the report
itself resolved no scope (discovery D6's terminal case: `ref` and `sha` both
empty), the scope line uses the "every revision" wording in §5.

## 5. The description section — rendering contract

Insertion: after the `## CI` block, before `## Issue`. The rendered body is
never empty (D7).

### 5.1 Scope line

```text
Deployment records for `{report.Ref}`, PR head `{head[0:7]}` at write time.
```

When `report.Ref` is empty:

```text
Deployment records for every revision the forge reports, PR head `{head[0:7]}` at write time.
```

`{head[0:7]}` is the confirmed PR head SHA truncated to at most seven
characters (verbatim when it is already seven or fewer).

### 5.2 Gap lines

Immediately after the scope line. Gaps are grouped **by identical `detail`**;
each group renders one line with its keys joined `", "` in frozen capability
order (`preview_environments`, `preview_deployments`, `preview_statuses`):

```text
Not fully read — `preview_environments`, `preview_deployments`: {detail}
```

A single-key group reads `Not fully read — \`preview_environments\`: {detail}`.
Three gaps with three distinct details therefore render three lines; three
gaps with one identical detail render one. Multiple group lines render in the
frozen capability order of each group's first key — equivalently, stable
grouping over the report's already-ordered `gaps` array. The "Not fully read"
prefix is Marshal's framing around the report's own gap detail (§11). Protect
the detail as provider text under D10.

### 5.3 Deployment rows

```text
- `{environment}` — `{state}` — `{sha[0:7]}` {badge} — {environmentUrl} (logs: {logUrl}) — {description}
```

- `{state}` in a protected span when present; when empty, the literal unquoted
  text `(no status reported by the provider)` replaces the whole `` `{state}` `` slot.
- `{sha[0:7]}` is the first seven characters of the provider-reported `sha`
  (verbatim when the provider reported seven or fewer); `{badge}` is D5's text.
  When `sha` is empty, the `— \`{sha[0:7]}\` {badge}` part is replaced by
  `— revision not reported by the provider`.
- `— {environmentUrl}` is dropped when `environmentUrl` is empty, and
  `(logs: {logUrl})` is dropped with it — a logs segment never renders
  unattached to a link. `targetUrl`, `task`, `transient`, `production`,
  `originalEnvironment`, `id`, `createdAt` and `updatedAt` are never rendered
  in either surface — the report remains the full-fidelity source. Each
  present segment joins with ` — `; `(logs: {logUrl})` follows the link after
  a single space inside the link segment, and `{description}` is the final
  segment in a protected span (D10).
- Order: the report's own deterministic order (deployments, newest per
  environment, id descending — discovery D12/D14).

### 5.4 Environment rows

Preceded by the line `Configured environments:`, then one row per reported
environment:

```text
- `{name}` — {htmlUrl}
```

` — {htmlUrl}` is omitted when the provider reported no URL; any nonempty URL
is rendered under D10. Order: the report's own order.

### 5.5 Status-link rows

Preceded by the line `Links reported by commit statuses:`, then one row per
eligible check (D6), **sorted by check name, then by the provider id string
lexicographically** (the collector builds these from a map; the sort keeps
bodies deterministic):

```text
- `{name}` (`{state}`): {url}
```

`{state}` is `factoryCheckConclusion`'s wording — conclusion, else status,
else `pending` — rendered in the same protected span (D10). The `url` is the
provider's `target_url`, rendered according to D10.

### 5.6 Empty and unreadable lines

- When the section has no deployment rows, no environment rows and no gap
  lines — with or without status links — `No preview deployments or test
  environments are configured for this revision.` renders in that place, so
  the section always states what exists (D7). In the hybrid case (status
  links present) the line reports only the absence of deployment and
  environment records; the links below it are never described as previews.
- When the fetch failed or the report's `status` is `unavailable`, the section
  is exactly the heading (no scope line) plus `Preview and test environments
  could not be read when this description was written.` No internal error text
  enters the forge-side body.

### 5.7 Full example

```markdown
## Preview and test environments

Deployment records for `feature/x`, PR head `8e4c1b2` at write time.

- `production` — `success` — `8e4c1b2` matches the PR head — <https://preview.example.internal>
- `staging` — `pending` — `7a3f9c1` not the PR head — <https://staging.example.internal> (logs: <https://ci.example/run/42/log>)

Configured environments:

- `production` — https://github.com/o/r/deployments/activity_log?environments_filter=production

Links reported by commit statuses:

- `deploy/preview` (`success`): <https://preview.example.internal>
```

## 6. The studio card

- **Placement:** a new card titled `Preview and test environments` directly
  after the existing "Pull requests" card in `FactoryItem.svelte`. Renders
  nothing when `item.repoId` is empty.
- **Fetch:** `getPreviewEnvironments(item.repoId, item.branch)` on mount and on
  a `Refresh` button press. Trigger the initial load once after the item first
  becomes available, separately from the item refresh poller. No preview
  polling — polling policy remains open in the design record.
- **Loading:** `Reading preview environments…`. **Fetch error:** `Preview and
  test environments could not be read.` with the underlying error as muted
  text. **Report `unavailable`:** the same line with the report's `error` as
  muted text. **Empty:** `No preview deployments or test environments are
  configured for this revision.`
- **Rows:** the same deployment, environment and status-link content as §5
  (links as `<a target="_blank" rel="noopener noreferrer">`), **untruncated**.
  The badge comparator is the newest recorded PR — the last element of
  `item.prs` — labeled as the **last factory-recorded PR head**, with the
  recorded short SHA shown. Rows carry no equality badge when it is missing.
- **Gaps:** the full gap list, one line per gap (ungrouped), each showing the
  five-state vocabulary: `{key} — {state} — {detail}` with `key` in `<code>`.

## 7. Missing access and partial failure

- A gap degrades presentation only where the report says so; rows the report
  returned still render beside gap lines.
- The gap lines carry the report's own cause+fix detail (the preview surface's
  fix rules, discovery spec §5.6) — no second fix vocabulary is written here.
- A Git-only repository (no forge declared) reports `unsupported` with the
  "declare github or gitea" fix; those gap lines render as-is. The description
  path always has a forge (a PR exists), but the section must not assume the
  report is forge-backed.
- The empty line (§5.6) renders **only** when there are no rows, no links and
  no gaps — so "nothing configured" and "could not read" never blur.

## 8. Open questions

None. The discovery spec's §8 questions resolve here as:

| Discovery §8 | Resolution |
|---|---|
| 1. Where the links appear | Both surfaces (D1), decided with the user 2026-11-15 |
| 2. The associated revision, precisely | Equality badge (D5), decided with the user 2026-11-15 |
| 3. Gitea/Forgejo commit-status target URLs | Labelled provider links (D6), decided with the user 2026-11-15 |
| 4. GitLab | Deferred — non-goal (§10) |
| 5. `forgeCapabilities` reconciliation | Deferred — non-goal (§10) |

## 9. Acceptance criteria

- A `set_ready` effect whose stubbed `previewSource` returns a report with two
  deployments writes a body containing the section after `## CI` and before
  `## Issue`, with `matches the PR head` / `not the PR head` computed against
  `before.HeadSHA` — including a case-only SHA difference counting as a match.
- A deployment with an empty `sha` renders `revision not reported by the
  provider` and no equality claim.
- A preview source that waits for its child context to expire is bounded at
  five seconds; while the parent remains live, the ready write and transition
  succeed with the unreadable line. Parent cancellation still stops the effect.
- A failing `previewSource` (and a report with `status == "unavailable"`) still
  yields a successful `set_ready` with a body containing exactly the §5.6
  unreadable line and no internal error text.
- A report that is `ok` with no deployment rows, no environment rows and no
  gaps yields exactly the §5.6 empty line even when status links render below
  it; a report with gaps and nothing readable yields gap lines and **no**
  empty line.
- Three gaps sharing one `detail` render as one `Not fully read —` line
  listing all three keys; three distinct details render three lines.
- Commit-status links render under `Links reported by commit statuses:` sorted
  by name then id, with `factoryCheckConclusion` wording; `check_run` URLs
  never appear there; a check with an empty URL appears nowhere.
- Truncation at 10 rows/list renders `…and {N} more` with the correct count.
- Provider strings, including scope and gap details, cannot inject Markdown or
  exceed the display bound. Unsafe and malformed URLs are non-clickable text in
  both surfaces; valid HTTP(S) links, including logs and status links, work.
- The studio card fetches with `?ref={item.branch}`, refreshes on demand, badges
  against the last recorded `item.prs` element using the explicitly recorded-
  head labels, renders untruncated rows and the full gap list with states, and
  shows the loading/error/unavailable/empty states. Repeated item polling must
  not issue more preview requests; only initial load and manual refresh do.
- `set_draft` writes no body; `FactoryRound`/`FactoryItem`/`contract.ts` are
  byte-unchanged; `TestPreviewCapabilityOrderIsStable`,
  `TestPreviewContractJSONUsesStableShape`,
  `TestDiscoveryCapabilityOrderIsStable` and `TestTypeScriptContractIsCurrent`
  pass unmodified.
- `TestWebIsStdlibOnly` passes; no `marshal/internal` import appears under
  `web/`; this slice adds no new forge request paths beyond the reads
  `Fleet.PreviewEnvironments` already makes — the `set_ready` fetch is exactly
  one such read (D2, D3).

## 10. Non-goals

- Building, running, or managing any preview or test environment; any new
  forge write (the description write is the existing `set_ready` effect).
- Persisting, caching, or polling the report; freshness policy.
- Regenerating the description after statuses change (the narration spec's
  excluded proposal stays excluded).
- A studio pull-request page beyond this card.
- GitLab; changing the set of forge families.
- Reconciling the studio's `forgeCapabilities` guess with instance-reported
  capabilities (discovery §8.5).
- Ancestry wording of any kind ("N commits behind").
- Extending the frozen preview or discovery contracts, or widening `Forge`,
  `DiscoveryForge`, or `PreviewForge`.
- Gitea/Forgejo factory CI collection: round CI is GitHub-first today
  (`FactoryCI` is built only in `forge_github_factory.go`), so on
  Gitea/Forgejo rounds the labelled-links subsection is simply absent — honest,
  since there is no provider-reported link to show. Wiring Gitea commit-status
  collection into factory CI is a separate slice.

## 11. Assumptions

Choices made without the user's input, each reversible:

1. Section placement after `## CI` (the approved ready-review mockups order
   checks and previews above the walkthrough).
2. The section always states rather than omits (D7), including the one-line
   empty case — `factoryCIBody`'s precedent.
3. The 10-row description bounds and the `…and {N} more` wording.
4. Revision display truncated to seven characters in rows and scope line
   (the badge states the comparison; the report keeps full values).
5. Badge comparison is case-insensitive exact equality (the user chose string
   comparison; case-insensitivity is added because providers differ in SHA
   case). The third badge text `revision not reported by the provider` is this
   spec's fixed wording for the otherwise-undefined no-revision case.
6. Status-link rows sorted by name then id for deterministic bodies (the
   collector's map iteration is unordered).
7. Gap grouping by identical `detail` in the description; full ungrouped list
   in the studio. The `Not fully read —` prefix on gap lines is Marshal's
   framing around the report's own gap detail.
8. The `previewSource` seam shape (D4) and `preview.ts` as the mirror's
   location and name (D12).
9. Studio fetch ref is `item.branch`; the description fetch ref is
   `before.HeadRef` (D13).
10. The fixed unreadable line carries no error text forge-side (D11); the
    studio shows it muted.
11. Check-run rows are excluded from the labelled links (D6): their URLs are
    CI pages the `## CI` section already presents. Only `commit_status`
    `target_url`s render; including check-run links later is a presentation
    choice, not a contract change.

## Branch review record

This specification received an independent `glm-5.3-flash` review through
`ollama-cloud` on 2026-11-15, against two questions: fidelity to the recorded
approved decisions, and implementability against the real code. The reviewer
verified every cited anchor against the tree and stated the clean areas
explicitly: all four §1 user decisions are represented faithfully and
identically on both surfaces; the design-notes §6 proposal is quoted verbatim
and the adapter rules honored; no frozen contract (the preview report's
capability order, the discovery vocabulary and JSON shapes, the factory
persistence shapes, `contract.ts`) is weakened or extended; and the composer
insertion point, the `set_ready` ordering, the `previewSource` seam against
`Fleet.PreviewEnvironments`'s exact signature, the commit-status collection
with its newest-per-context guard, and the studio insertion point all match
the code. Ten findings were raised; all ten are resolved.

| Severity | Finding | Resolution |
|---|---|---|
| Minor | D6's `check_run` exclusion was presented as decided but is neither a recorded user decision nor a §11 assumption | Flagged as §11 assumption 11, explicitly reversible |
| Minor | §5.2 fixed what "grouped by identical detail" means but never the order of multiple group lines | Groups now render in the frozen capability order of each group's first key — stable grouping over the report's already-ordered `gaps` array |
| Minor | §5.3 left `logUrl`-without-`environmentUrl` undefined (drop, standalone, or attach?) | The logs segment now drops with the link segment; a logs segment never renders unattached to a link |
| Minor | D7's "always states" was contradicted by the status-links-only case (no deployments, no environments, no gaps) | §5.6's empty line now renders whenever no deployment rows, no environment rows and no gaps exist — with or without status links — and the acceptance criterion says so |
| Minor | §9's "the probe side issues no new forge requests at all" contradicted D3's `set_ready` fetch | Reworded: no new forge request paths beyond the reads `Fleet.PreviewEnvironments` already makes |
| Minor | Five cited anchors had drifted | All refreshed: `factoryCIBody` :58, `PreviewReport` :18, `previewCapabilityOrder` :99, the D12 span :18-77, the `type status` block :135-140 |
| Nit | D5's third badge text is invented wording, unflagged | Flagged in §11 assumption 5 |
| Nit | The `Not read —` gap prefix understates truncation gaps and is invented vocabulary | Changed to `Not fully read —` and flagged in §11 assumption 7 |
| Nit | D10's `codeFence` analogy conflated block-fence and inline-span rules | Reworded: the run-length rule matches `codeFence`; the padding is the inline-span addition |
| Nit | §5.6's "scope-less heading" phrasing was garbled; D3's test-call-site blast radius unstated | Reworded ("exactly the heading (no scope line) plus …"); D3 now names the `factory_prbody_test.go` / `factory_effects_test.go` call sites |

## 13. Review corrections — 2026-11-15

The follow-up review bounded the best-effort fetch, restricted provider links,
separated card loading from item polling, and clarified that the Studio badge
uses the last factory-recorded head, which may be stale. The implementation
plan also keeps each task compilable. These refinements supersede conflicting
wording in the earlier review record above.

## Implementation record

Delivered on branch `preview-environment-links` as Tasks 1–4 of the
[plan](plans/2026-11-15-preview-environment-links-plan.md), test-first, with one
commit per task, then rebased onto `main` after PRs #76 and #78 landed.

| Task | Deliverable |
|---|---|
| 1 | The description section: the scope line, gap groups, deployment, environment and status-link rows, the empty and unreadable lines, the 10-row bounds and the four pure helpers |
| 2 | The `previewSource` seam on `factoryEffects` and the best-effort fetch before composing at `set_ready` |
| 3 | `web/ui/src/lib/preview.ts` wire mirrors and `getPreviewEnvironments` |
| 4 | The live preview card on the studio work-item view |
| 5 | Specification record, README entry and the rebuilt embedded bundle |

Files: `web/bridge/factory_prbody.go`, `web/bridge/factory_effects.go`, their
tests, `web/ui/src/lib/preview.ts`, `web/ui/src/lib/api.ts`,
`web/ui/src/views/FactoryItem.svelte` and its test, and the rebuilt
`web/bridge/static`. The frozen preview contract (`preview_environment.go`,
`previewCapabilityOrder`, the report's JSON shape), `contract.ts`,
`FactoryRound`/`FactoryItem`, and the `Forge`, `DiscoveryForge` and
`PreviewForge` interfaces are byte-unchanged; `web/bridge` stays standard
library only.

Acceptance gates, run from the repository root on this branch:

| Gate | Result |
|---|---|
| `go test ./web/bridge -count=1` | PASS — `ok marshal/web/bridge 102.639s` |
| `go test ./web/bridge -run '^(TestFactoryPreviewBody\|TestFactoryPRBody)' -count=1` | PASS |
| `go test ./web/bridge -run '^TestFactoryEffect' -count=1` | PASS |
| `go test ./web/bridge -run '^(TestPreviewCapabilityOrderIsStable\|TestPreviewContractJSONUsesStableShape\|TestDiscoveryCapabilityOrderIsStable\|TestWebIsStdlibOnly)$' -count=1` | PASS, unmodified |
| `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent -count=1` | PASS |
| `gofmt -l web/bridge` and `go vet ./web/bridge/...` | no output, exit 0 |
| `CGO_ENABLED=1 go build ./...` | exit 0 |
| `CGO_ENABLED=1 go test ./...` | PASS — every package ok, run alone on an unloaded machine |
| `cd web/ui && npm test` | PASS — 88 files, 985 tests |
| `cd web/ui && npm run build` | PASS — the bundle in `web/bridge/static` is rebuilt from this source |
| `cd web/ui && npm run check` | 6 errors, 1 warning in 6 files — **all pre-existing at the base commit** (`src/views/Chat.svelte:697`, `src/views/Run.svelte:336`, `src/lib/Sidebar.test.ts:16`, `src/lib/fleet.test.ts:78`, `src/views/Review.test.ts:49`, `src/views/Runs.test.ts:15`: `AccessState`/`AgentRow` fixture drift in files this slice does not touch). No diagnostic names this slice's files. |

Two choices the plan left open are worth recording. The plan's Task 1 prose
lists the environment rows before the deployment rows where §5's subsection
order and §5.7's full example list deployments first; the plan defers every
ordering rule to §5, so the description renders deployments, then
`Configured environments:`, then the status links. And D10 says URLs render with
newlines "stripped": the implementation replaces each newline with a space
instead of deleting it, so a provider URL cannot fuse two tokens across a line
break while no newline can reach the body.

One plan inconsistency was resolved from the plan's own integration note: the
composer's signature change made `factory_effects.go` and
`factory_effects_test.go` uncompilable until their call sites moved, so Task 1
carried both as `(round, nil, "")` and Task 2 replaced the production one with
the real fetch.

## 14. Implementation branch review — 2026-11-15

The implemented branch received an independent `glm-5.3-flash` review through
`ollama-cloud` against `origin/main`, and a focused re-review verified the
fixes. Six findings were raised: all six are fixed, and so are the two minor
issues the re-review surfaced. No finding required a contract or spec change;
every one was the code diverging from a decision already recorded here.

| Severity | Finding | Resolution |
|---|---|---|
| Major | The five-second best-effort bound (D11) was absent, so a hanging forge probe could block `set_ready` and the ready transition | The fetch runs under `context.WithTimeout(ctx, 5*time.Second)`, cancelled as soon as it returns. A child deadline still writes the §5.6 unreadable line and completes `set_ready`; parent cancellation stops the effect. `TestFactoryEffectReadyPreviewTimeout` pins both paths |
| Major | D10's URL rule was unimplemented: values were emitted bare, so a provider URL carrying angle brackets or control characters reached the body | `factoryURLClickable` validates — absolute http/https, nonempty host, no whitespace, control character, angle bracket or backtick, at most 2048 bytes, no repair. Valid values autolink as `<…>`; rejected nonempty values render as bounded `Untrusted URL:` inline code |
| Major | The gap detail was interpolated raw, and no displayed provider text was capped | `factoryDisplayText` replaces control characters and caps at 512 code points with an ellipsis, inside `factoryInlineCode`. Gap details and every other provider string take that path, so none can break its span |
| Major | The studio badge used the description's wording, so a stale recorded head was still described as "the PR head" | The card renders `matches`/`differs from the last factory-recorded PR head` with the recorded short SHA, and a test pins that an external push cannot make it claim currency |
| Major | Studio URLs became links without validation, so a provider-controlled `javascript:` or `data:` value stayed clickable | One `isHttpUrl` validator guards every link; a rejected nonempty value renders as text |
| Minor | The initial preview load was chained to the item poller and had no stale-response guard | It fires once from the mount path, poll ticks never refetch it, and a request sequence plus an unmount flag discard superseded responses |
| Minor | The mount path fetched the factory item twice | The poller's immediate tick is the single mount fetch |
| Minor | The two D10 validators disagreed on C1 control characters and opaque `https:foo` values | Both reject them: the studio pattern covers `\u007f-\u009f` and requires the literal `http(s)://` form, matching `factoryURLClickable` |
