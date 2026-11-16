# P6 plan 1: versions for source builds — implementation plan

Date: 2026-11-16. Status: ready for review.

Base: `origin/main` at `87c41ad9` with P5 plan 8's branch (`d57f178f`, PR
#100) merged in. Every anchor below was checked there. If plan 8 merges with
changes, re-check the anchors in `cmd/marshal/main.go`, the one file both
plans touch.

- **The baseline** is recorded in the spec's "Implementation record": 127
  packages ok, and only two latency budgets failing, which CI's command skips.
- **The spec is already written,** uncommitted in the main checkout:
  `docs/daemon/p6-daemon-lifecycle.md`, with decision 7 and B1, and its row in
  `docs/daemon/design.md`.
- **Plans 2, 3 and 6 run beside this one.** None of them touches this plan's
  files.

Approved design: [p6-daemon-lifecycle.md](../p6-daemon-lifecycle.md):
decision 7; B1; Migration order item 1; Tests L8.
Sibling plans: [plan 2, the reconnect](2026-11-16-p6-2-reconnect-plan.md),
[plan 3, staleness](2026-11-16-p6-3-staleness-plan.md),
[plan 6, a wake and two warnings](2026-11-16-p6-6-wake-warnings-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **A source build names its commit.** An unstamped `marshal` or `webbridge`
  reports `dev+<12 hex of the revision>`, with `+dirty` for a modified tree,
  wherever it shows or compares its version.
- **A plain `marshal`'s TUI has a version.** Today only `marshal acp` sets it.

## Non-goals

- **Deciding a restart by version.** Plans 3 and 4 decide by file identity.
- **Changing the skew rules.** A mismatch still only warns (P4 D11), and the
  bridge still reads plain `dev` as no version.

## Assumptions

Each was checked at the base.

- **`marshal`'s version** is the `version` variable, stamped by release builds
  and empty otherwise (`cmd/marshal/version.go:13`). `versionString` prints
  `dev` for an empty one and takes the commit from Go's build info
  (`version.go:19`, `buildStamps` at `:45`).
- **It is used in three other places, each its own way:** the provider
  User-Agent, only when stamped (`cmd/marshal/main.go:82`); the agent's
  version, set only in the `acp` branch (`main.go:105`); and the daemon
  commands' `daemonVersion`, `dev` when empty (`cmd/marshal/daemon.go:561`).
- **The TUI's own version** is `acp.AgentVersion` (`internal/launch/daemon.go:121`),
  which a plain `marshal` never sets. Embedded mode pins the in-process agent
  to it (`internal/launch/pipe.go:79`).
- **The agent reports `acp.AgentVersion`** as `agentInfo.version`
  (`internal/acp/status.go:368`).
- **`webbridge`'s version** is its own `version` variable
  (`cmd/webbridge/main.go:32`), reported as `dev` when empty by `daemonVersion`
  (`main.go:42`) on `GET /api/daemon` (`main.go:655`), and passed to the fleet
  as its build version (`main.go:549`).
- **The fleet uses its build version four ways:**
  - the skew check, which treats `dev` as no version (`web/bridge/fleet.go:1048`);
  - the default agent image's tag, `:latest` for `dev` and the version itself
    otherwise (`web/bridge/profile.go:29`). A `+` is not allowed in an image
    tag;
  - the egress image's tag, which already replaces every disallowed character
    (`web/bridge/egress_fleet.go:200`);
  - the derived image's tag, a hash of the version (`web/bridge/derive.go:36`).
- **Go's build info** carries `vcs.revision` and `vcs.modified` for a build
  made in a git checkout. `go test` binaries carry neither, so a test of the
  rule must feed it settings.
- **`web/` imports no `marshal/internal` package** (`TestWebIsStdlibOnly`).

## Decisions this plan makes beyond the spec

1. **`internal/buildinfo` holds the rule,** with the build info read through a
   function the tests replace. `IsSource` names a source build: `dev`, or
   `dev+` followed by anything.
2. **`marshal` computes its version once,** at the top of `run`, and uses that
   value for the banner's version word, the User-Agent, `acp.AgentVersion` (in
   every mode, so the TUI has it too) and `daemonVersion`.
3. **`web/bridge` repeats `IsSource` as `bridge.SourceBuild`,** since it cannot
   import `internal/buildinfo`, and a test in `cmd/webbridge`, which can
   import both, pins the two together over a table of versions. P4 pinned the
   bridge's copy of the data directory rule the same way.
4. **A source build's default agent image is `:latest`,** whether its version
   is `dev` or `dev+…`.
5. **The derived image's tag keeps hashing the version.** A source build of a
   new commit therefore derives its workspace images again. That is the cost
   of telling builds apart, and only a server profile with containers pays
   it.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P6 and its plans 1, 2, 3 and 6`:
  - `docs/daemon/p6-daemon-lifecycle.md` and `docs/daemon/design.md`;
  - this plan, `2026-11-16-p6-2-reconnect-plan.md`,
    `2026-11-16-p6-3-staleness-plan.md` and
    `2026-11-16-p6-6-wake-warnings-plan.md`.
- Then branch: `git switch -c p6-1-versions` from `main`, or create a worktree
  on that branch.
- Run each task's Verify and commit it as `p6-1-versions: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p6-1-suite`.

## Contracts

### The rule (`internal/buildinfo`, Task 1)

```go
// Version is a binary's version: stamped when not empty, else
// dev+<12 hex of vcs.revision>, with +dirty when vcs.modified is true, else
// dev (decision 7).
func Version(stamped string) string

// IsSource reports whether v names a source build: dev, or dev+….
func IsSource(v string) bool

// readBuildInfo is debug.ReadBuildInfo; tests replace it.
var readBuildInfo func() (*debug.BuildInfo, bool)
```

### The bridge (`web/bridge`, Task 3)

```go
// SourceBuild is buildinfo.IsSource, repeated because web/ imports no
// internal package (decision 3).
func SourceBuild(v string) bool
```

## Task 1 — `internal/buildinfo`

**Files:** `internal/buildinfo/buildinfo.go`, `buildinfo_test.go` (new).

1. `Version` returns the stamped version unchanged when there is one. Otherwise
   it reads the build info: with a revision, `dev+` and its first 12
   characters, then `+dirty` when `vcs.modified` is `true`; with no revision,
   or no build info, `dev`. A revision shorter than 12 characters is used
   whole.
2. `IsSource` follows decision 1.
3. Tests, each replacing `readBuildInfo`:
   - `TestAStampedVersionWins`.
   - `TestASourceBuildNamesItsCommit`: a 40-character revision gives
     `dev+` and its first 12.
   - `TestAModifiedTreeIsDirty`.
   - `TestNoVCSInfoIsDev`, and `TestNoBuildInfoIsDev`.
   - `TestIsSource`: `dev`, `dev+abc` and `dev+abc+dirty` are source builds;
     `0.9.0`, `v1.2.3-rc1` and the empty string are not.

**Verify:** `TMPDIR=/private/tmp/p6-1-suite go test ./internal/buildinfo/ -count=1`.

## Task 2 — `marshal` names itself one way

**Files:** `cmd/marshal/main.go`, `cmd/marshal/version.go`,
`cmd/marshal/daemon.go`, `cmd/marshal/main_test.go`.

1. `run` computes `buildinfo.Version(version)` once, before anything else
   uses it, and sets `acp.AgentVersion` to it before dispatching any mode. The
   `acp` branch no longer sets it itself.
2. The User-Agent is always `marshal/` and that version, so a source build
   names its commit there too.
3. `versionString`'s first word is that version. The commit and date it
   prints after it are unchanged.
4. `daemonVersion` returns that version.
5. Tests:
   - `TestVersionStringFallsBackToDev` keeps passing: a test binary has no VCS
     information, so the version is `dev`.
   - `TestEveryModeSetsTheAgentVersion`: with `version` stamped by the test,
     dispatching to the app runner, the attach runner and the fleet runner each
     sees `acp.AgentVersion` equal to it. Restore both afterwards.
   - `TestTheUserAgentCarriesTheVersion`, for a stamped version and for an
     unstamped one.

**Verify:** `TMPDIR=/private/tmp/p6-1-suite CGO_ENABLED=1 go test ./cmd/marshal/ -count=1 -timeout 10m`.

**Mutation check:** set `acp.AgentVersion` in the `acp` branch only, as today;
`TestEveryModeSetsTheAgentVersion` must fail. Restore.

## Task 3 — `webbridge` and the fleet

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/main_test.go`,
`web/bridge/profile.go`, `web/bridge/profile_test.go`,
`web/bridge/fleet_test.go`.

1. `webbridge`'s `daemonVersion` is `buildinfo.Version(version)`, and that
   value is the fleet's build version too.
2. `bridge.SourceBuild` follows decision 3. `defaultAgentImageFor` gives
   `:latest` for any source build (decision 4).
3. The skew check is unchanged: plain `dev` is no version, and two different
   `dev+…` versions warn.
4. Tests:
   - `TestTheBridgesSourceRuleMatchesBuildinfo` in `cmd/webbridge`: over a
     table of versions, `bridge.SourceBuild` and `buildinfo.IsSource` agree.
   - `TestDefaultImageTracksTheBuildVersion` gains a `dev+abc123def456` case
     giving `:latest`, and a `dev+abc123def456+dirty` one.
   - `TestTwoSourceBuildsWarnOfSkew` beside `TestVersionMismatchWarnsButDoesNotRefuse`:
     an agent reporting `dev+aaaaaaaaaaaa` under a bridge of
     `dev+bbbbbbbbbbbb` sets the runtime's version warning; plain `dev` on
     either side does not.
   - `TestWebbridgeReportsItsVersion` keeps passing.

**Verify:** `TMPDIR=/private/tmp/p6-1-suite CGO_ENABLED=1 go test ./cmd/webbridge/ ./web/bridge/ -run 'Version|Image|SourceRule|Skew' -count=1 -timeout 10m`,
then `TMPDIR=/private/tmp/p6-1-suite CGO_ENABLED=1 go test ./web/... -count=1 -timeout 20m`.

**Mutation check:** let `defaultAgentImageFor` treat only plain `dev` as a
source build; the `dev+…` cases of `TestDefaultImageTracksTheBuildVersion`
must fail. Restore.

## Task 4 — the docs, the record and full verification

**Files:** `docs/daemon/p6-daemon-lifecycle.md`, `AGENTS.md`.

1. Fill the "Plan 1" record, as P5's records are written: what was built,
   where the plan was short, the tests, the verification.
2. `AGENTS.md`'s map gains `internal/buildinfo/` under "App shell & config".
3. **The manual run.** Build both binaries from this branch, with a modified
   tree and without; record what `marshal --version`, `webbridge --version`
   and `marshal daemon status` print against a daemon started from them.
4. Full verification: `gofmt -l internal cmd test web` prints nothing; `go vet
   ./...` is clean; CI's suite command with `TMPDIR=/private/tmp/p6-1-suite`
   passes; `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`; the usability
   suite passes.

## Done when

- A source build of either binary reports `dev+<commit>`, and `+dirty` for a
  modified tree.
- A plain `marshal`'s TUI reports the same version as its `marshal acp`.
- A source build's default agent image is still `:latest`.
- L8 passes, every other test named above passes, each mutation check fails
  as described, and CI's suite command passes.
