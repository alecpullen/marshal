# Context diet: archive swarm

Status: design approved 2026-10-15; spec awaiting review. Lands after Tier 2a,
in either order with Tier 2b.

## Intent

Swarm (`/swarm <goal>`: a planner, parallel repo scouts, an implementer and
tester loop, then a reviewer) has been typed zero times in 1,453 recorded
prompts. It costs no prompt tokens, but it's spread across:
- the agent layer
- the TUI
- session state and config
- the ACP contract (`session/swarm_start`)
- the web UI's run kinds and recipes

The user wants it out of main but kept for possible reuse, the same way the
conversation-inspector work lives on `archive/conversation-inspector-c1`.

## Decisions

1. **Archive and delete.**
   - Create the branch `archive/swarm` at the last main commit that still has
     swarm, and push it to origin. The push needs the user's confirmation at
     execution time.
   - Then delete swarm from main.
   - Rejected: a build tag or config flag. Every surface would still carry the
     code and its tests.
2. **Move the pieces the SDD pipeline and `agent.run` share:**
   - `swarm.WriteLock` → `agent.WriteLock`. It is the single write gate for the
     parent runner and every `agent.run` child.
   - `swarm.RunnerFactory` and `swarm.RegistryScope` → `pipeline.RunnerFactory`,
     typed over the pipeline's existing `pipeline.RegistryScope`.
     `pipelineScope` and the tester scope go.
3. **Keep:**
   - the castlist preflight panel (`/sdd` uses it)
   - `repo_scout` as a live routing role. Question-class turns choose their
     model through it (`routing.roleForTaskClass`), and it's in
     `routing.FastRoles`.
   - the `planner` and `tester` role names in routing and `[agent_profiles]`,
     for config compatibility. They become unused, like Tier 1's `knowledge`.
   - In all three cases, the swarm-only prompt text in `roleAddenda` goes. A
     runner whose `Role` is one of them falls back to the general addendum.
     The main agent's own `Role` is `general` regardless of which role picked
     its model.
4. **Delete:**
   - `internal/agent/swarm`: orchestrator, state, meter, prompts, verdict and
     tests.
   - `buildSwarmRunner` and the swarm override factory in `app.go`.
   - `routing.SwarmCastRoles`. The preflight panel keeps `SDDCastRoles`.
   - The swarm runner and override factory in `Runtime`, `NewSession` and the
     return values of `buildAgentRunner*`.
   - Tools: `registry.TesterView`, `testerTestRunTool`, the internal `test.run`
     and the tester registry plumbing.
   - TUI: `/swarm`, `SwarmOverrideFactory`, `RoleOverrideRunner`, the swarm
     branch of run preflight, swarm progress in the live strip, now bar and
     status, the session-sheet swarm section, and the swarm budget fields in
     settings and its reset.
   - Session: `SwarmProgress` and the swarm role statuses.
   - Config: `SwarmConfig` / `[swarm]`. Unknown keys are ignored on load, so old
     files still parse.
   - ACP: `session/swarm_start`, `session/swarm_status`, the advertised
     `swarmDispatch` capability, `SwarmStartParams`, `SwarmTurnResult`,
     `SwarmCastEntry`, the `SwarmRun` run detail (`kind: "swarm"`) and
     `swarmBudget` in the agents-roster response.
   - Web: the `swarm` run and recipe kind in the bridge (`RunSwarm`, its
     validation and titles) and in the UI (types, new-run modal, recipe editor,
     run view, live tile, timeline). No compatibility path is needed: the user
     has no stored swarm recipes.
   - Docs: AGENTS.md map entries, README sections, ARCHITECTURE row.

## Verification

Tests that pin the result:
- `/swarm` is not a registered command.
- `session/swarm_start` is not a registered ACP method.
- The bridge rejects `kind: "swarm"` for runs and recipes.
- `test.run` is not registered.
- `agent.WriteLock` serializes concurrent writes from two `agent.run` children.
- The pipeline dispatcher builds runners with each of `ScopeFull`,
  `ScopeReadOnly`, `ScopeArtifactWriter` and `ScopeFallback`.

Final run:
- full Go suite
- usability suite
- `web/ui` build and vitest
- web bridge tests, including the stdlib-only boundary test
