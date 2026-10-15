# Context diet Tier 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut Marshal's fixed per-request prefix from about 18.5k to about 10.5k tokens by removing low-signal or misleading prompt content.

**Architecture:** Six deletion-led tasks, each self-contained and independently testable:
1. Skill hints.
2. Repo-specific prompt rules.
3. `config.*` setter tools.
4. Automatic memory extraction (injection becomes curated-only).
5. Repo and session context-pack seeds.
6. A prompt-budget guard test that stops the prefix growing back.

No new features. The one behaviour addition is that TUI exit always marks the session ended.

**Tech Stack:** Go 1.x with cgo (tree-sitter), SQLite, Bubble Tea. Tests use the `go test` standard library.

**Spec:** `docs/context-diet/design.md`

## Global Constraints

- Build and test with `CGO_ENABLED=1`. A C toolchain is required.
- Run `gofmt -w` on every changed Go file. `go vet` must stay clean for each changed package.
- Prefer the narrowest test scope while iterating (`go test ./internal/agent/ -run X`). Task 6 runs the full suite.
- Cache-stable prompt prefix: `TestPromptPrefixStableAcrossSteps` and `TestCrossTurnStableRegionIsAppendOnly` must pass after every task.
- `web/` is untouched by this plan (stdlib-only boundary, `TestWebIsStdlibOnly`).
- Known environmental failures on this machine (not regressions): `internal/snapshot` Lookup/Rooted/Service tests, `internal/app` `TestRuntimeCloseRunsNoGitGC` and `TestStartupSnapshotMaintenanceIsBoundedAndSkipsStorelessWorkspaces`, `internal/app/tui` `TestStatusLineUntrustedIsWarningColored`, `internal/tools/desktop/browser` `TestStandaloneBackendNavigateAndRead`, and `internal/worktree` `TestCLIGitOpsCommitAllWithoutARepoIdentity`. Confirm any other failure against a clean `git worktree add --detach` at the base commit before blaming the diff.
- Do not delete the DB `memories` table, the `/memory` panel, the web memory library, the `knowledge` routing role, or the `repo.map`/`repo.card` tools. Those are later tiers.
- One commit per task. Commit messages use the repo's `area: summary` style.

## Review Focus

1. **A session marked ended at exit is resumed later.** Ending a session must not hide it from the session list or block `WithExistingSession`. Task 4 adds `TestEndedSessionStillResumes`.
2. **A web-promoted workspace memory reaches an agent spawned with `MARSHAL_WORKSPACE`, while auto-extracted tentative project memories don't.** Task 4 rewrites `TestDBMemoryProviderReadsScopesForWorkspace` to pin both.
3. **A resumed session whose loaded-tool list still names a deleted `config.*.set` tool.** `tools.select` and the prompt must ignore or reject the stale name without panicking. Task 3 adds `TestToolsSelectRejectsRemovedConfigSetter` and `TestStaleLoadedConfigSetterIsIgnoredInPrompt`.
4. **A user config that still binds the `knowledge` role in `[agent_profiles]`.** The runtime must start normally. Task 4 adds `TestRuntimeStartsWithKnowledgeRoleBound`.
5. **Prompt prefix order after the hint goes.** The turn goal must immediately follow the cache anchor, on fresh turns and on rollover/compaction rebuilds. Task 1 rewrites the three anchor-before-hint tests to pin "goal follows anchor".

---

### Task 1: Remove per-turn skill hints

**Files:**
- Delete: `internal/agent/skillrank.go`, `internal/agent/skillrank_test.go`, `internal/agent/skillhint_test.go`
- Create: `internal/agent/skillbody_test.go` (keeps the three skill-body tests from `skillhint_test.go`)
- Modify: `internal/agent/runner.go` (fields around lines 277–280 and 430–435; `RunTask` lines 921–924, 962, 1022)
- Modify: `internal/agent/route.go:501-521` (delete `appendSkillHint`)
- Modify: `internal/agent/prompts.go:489-512` (delete `BuildSkillHintMessage`)
- Modify: `internal/agent/rollover.go:153`, `internal/agent/handoff.go:39`
- Modify: `internal/agent/cacheprefix_test.go`, `internal/agent/handoff_test.go`, `internal/agent/rollover_test.go`
- Modify: `internal/app/session/messages.go:97-104` (comment only)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `Runner` without `SkillEmbedder`, `skillRanker` or `skillHints`. No function named `appendSkillHint`, `computeSkillHints`, `BuildSkillHintMessage` or `classDefaultHints` exists afterwards. Test helper `skillFixture() []skills.Skill` lives in `cacheprefix_test.go`.

- [ ] **Step 1: Write the failing test.** Add this to `internal/agent/cacheprefix_test.go`, directly below `anchorIndex`:

```go
// skillFixture is a small installed-skill roster. It carries no embedder:
// skill hints are gone, so nothing ranks these against the goal.
func skillFixture() []skills.Skill {
	return []skills.Skill{
		{Name: "tdd", Description: "test-driven development workflow"},
		{Name: "cooking", Description: "recipes for dinner"},
		{Name: "lint", Description: "bulk lint fixing"},
	}
}

// No request may carry a per-turn skill suggestion message. Edit-class
// turns used to get TDD/verification suggestions from class defaults even
// without an embedder; this pins that the whole mechanism is gone and the
// goal directly follows the cache anchor.
func TestNoSkillHintMessageOnWire(t *testing.T) {
	idx := skills.NewIndex()
	for _, c := range skillFixture() {
		idx.Set(c.Name, c)
	}
	idx.Set("test-driven-development", skills.Skill{Name: "test-driven-development", Description: "tests first"})
	idx.Set("verification-before-completion", skills.Skill{Name: "verification-before-completion", Description: "verify"})
	p := &agenttest.ScriptedProvider{Responses: []string{"First answer.", "Second answer."}, FinishReasons: []string{"stop", "stop"}}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SkillIndex = idx
	r.SetForceClass(string(ClassEdit))

	const goal = "fix the flaky test"
	for i := 0; i < 2; i++ {
		if err := r.Run(context.Background(), goal); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	for k, req := range p.Requests {
		for _, m := range req.Messages {
			if strings.HasPrefix(m.Content, "Skill suggestions for this request") {
				t.Fatalf("request %d carried a skill hint message", k)
			}
		}
	}
	first := p.Requests[len(p.Requests)-1].Messages
	a := anchorIndex(t, first)
	if a+1 >= len(first) || first[a+1].Role != schema.RoleUser || first[a+1].Content != goal {
		t.Fatalf("goal must directly follow the anchor; got %#v", first[a:])
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails.**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run TestNoSkillHintMessageOnWire -count=1`
Expected: FAIL with `request N carried a skill hint message`. The edit-class defaults suggest the two installed skills.

- [ ] **Step 3: Delete the hint machinery.**
  1. `git rm internal/agent/skillrank.go internal/agent/skillrank_test.go`
  2. Create `internal/agent/skillbody_test.go` with this header, then paste `TestAppendSkillBodiesSendsFullBodyWhenYoung`, `TestAppendSkillBodiesSendsStubWhenAged` and `TestBodyFullTurnsZeroAlwaysSendsFullBody` verbatim from `skillhint_test.go` (currently around lines 350–405):

```go
package agent

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/skills"
)
```

  3. `git rm internal/agent/skillhint_test.go`
  4. In `internal/agent/runner.go`:
     - Delete the `SkillEmbedder` field and its three-line comment. It starts `// SkillEmbedder overrides embedder resolution for computeSkillHints.`
     - Delete the `skillRanker *skillRanker` and `skillHints []string` fields and their comments.
     - In `RunTask`, delete these four lines:

```go
	// Rank skills against the goal so the prompt can suggest them. This
	// only suggests — see computeSkillHints for why gating on similarity
	// does not work. Class defaults (no embedder needed) are merged in.
	r.computeSkillHints(ctx, goal, task.Class)
```

     - Delete both occurrences of `messages = r.appendSkillHint(messages)`. One follows `markCacheAnchor(messages)` near line 962, the other is in the plan-first rebuild near line 1022.
     - If `go build` reports `"marshal/internal/llm/embedding" imported and not used`, remove that import.
  5. In `internal/agent/route.go`, delete the whole `appendSkillHint` function and its four-line comment (lines 501–521). Remove the `skills` import only if the compiler reports it unused.
  6. In `internal/agent/prompts.go`, delete `BuildSkillHintMessage` and its doc comment (lines 489–512).
  7. Delete the `fresh = r.appendSkillHint(fresh)` line in `internal/agent/rollover.go` (line 153) and `internal/agent/handoff.go` (line 39).
  8. In `internal/app/session/messages.go`, change the `ContentTypeSkillAuto` comment's parenthetical from `(it emits hints instead — see internal/agent/skillrank.go)` to `(skill hints were removed in the context-diet Tier 1 work)`.

- [ ] **Step 4: Repair the tests that referenced hints.**
  1. `internal/agent/cacheprefix_test.go`:
     - Delete `TestSkillHintFollowsReplayedHistory` and the `hintIndex` helper. `TestNoSkillHintMessageOnWire` replaces the first.
     - In `TestCrossTurnStableRegionIsAppendOnly`, replace `e, candidates := rankFixture()` with `candidates := skillFixture()` and delete `r.SkillEmbedder = e`.
     - In the same test, replace the hint loop:

```go
	for k, req := range firsts {
		a := anchorIndex(t, req.Messages)
		if h := hintIndex(req.Messages); h >= 0 && h <= a {
			t.Fatalf("turn %d: skill hint (index %d) must come after the anchor (index %d)", k+1, h, a)
		}
	}
```

with:

```go
	for k, req := range firsts {
		a := anchorIndex(t, req.Messages)
		if a+1 >= len(req.Messages) || req.Messages[a+1].Content != goal {
			t.Fatalf("turn %d: the goal must directly follow the anchor (index %d)", k+1, a)
		}
	}
```

     - Rename `TestHandoffWireAnchorsBeforeHint` to `TestHandoffWireGoalFollowsAnchor`. Replace `_, candidates := rankFixture()` with `candidates := skillFixture()` and delete `r.skillHints = []string{"tdd"}`. Replace its last four lines (`h := hintIndex(fresh)` through the closing `}` of the if) with:

```go
	if a+1 >= len(fresh) || fresh[a+1].Content != "fix the bug" {
		t.Fatalf("goal must directly follow the anchor (index %d): %#v", a, fresh[a:])
	}
```

  2. `internal/agent/rollover_test.go`: rename `TestRolloverAndContinueAnchorsBeforeHint` to `TestRolloverAndContinueGoalFollowsAnchor`.
     - Replace `_, candidates := rankFixture()` with `candidates := skillFixture()`.
     - Delete the `skillHints: []string{"tdd"},` field.
     - Replace `h := hintIndex(fresh)` and its if-block with:

```go
	if a+1 >= len(fresh) || fresh[a+1].Content != "test goal" {
		t.Fatalf("goal must directly follow the anchor (index %d): %#v", a, fresh[a:])
	}
```

  3. `internal/agent/handoff_test.go`:
     - Delete the two lines `// Set a skill hint so we can verify it reappears after compaction.` and `runner.skillHints = []string{"tdd"}`.
     - Delete the block that starts `// The hint message must be present (after the skill bodies, before the goal).` and ends with `t.Fatal("fresh wire missing the skill hint message")` plus its closing `}`.

- [ ] **Step 5: Run the agent package and confirm it passes.**

Run: `CGO_ENABLED=1 go test ./internal/agent/... -count=1`
Expected: PASS, including `TestNoSkillHintMessageOnWire`, `TestCrossTurnStableRegionIsAppendOnly` and `TestPromptPrefixStableAcrossSteps`.
Then run: `CGO_ENABLED=1 go build ./... && go vet ./internal/agent/ ./internal/app/session/`
Expected: no output.

- [ ] **Step 6: Commit.**

```bash
gofmt -w internal/agent internal/app/session
git add -A internal/agent internal/app/session/messages.go
git commit -m "agent: remove per-turn skill hints and the embedding ranker"
```

---

### Task 2: Remove repo-specific rules from the shipped prompt

**Files:**
- Modify: `internal/agent/prompts.go:432-470` (rule constants and both rule compositions)
- Modify: `internal/agent/prompts_test.go:1040-1068`
- Modify: `internal/tools/native/workspace_finish.go:30-31`
- Modify: `internal/tools/native/file.go` (lines 790–794 and 991–993 call sites; lines 811–846 delete)
- Modify: `internal/tools/native/file_test.go:1335-1440` (delete the nudge tests)
- Modify: `internal/skills/builtin/brainstorming.md:63`, `internal/skills/builtin/marshal-writing-plans.md:9-10,17,40`, `internal/skills/builtin/using-skills.md:24-25`
- Modify: `AGENTS.md` (the "Specs and plans" section)
- Test: `internal/agent/prompts_test.go`, `internal/tools/native/workspace_finish_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `baseRules`/`baseRulesSystem` without `ruleDocsArchive`, `ruleWorkspaceFinish` or `ruleReviewerSubagent`, which no longer exist. `specOrPlanDocNudge`/`specOrPlanDocNudges` no longer exist. The `workspace.finish` description now carries the merge guidance.

- [ ] **Step 1: Write the failing tests.** Replace `TestBaseRulesSystemIncludeWorkflowConventions`, `TestWorkflowConventionConstants` and `TestBaseRulesReviewerRuleIsLast` in `internal/agent/prompts_test.go` with:

```go
// The shipped rules must not carry this repository's own workflow: where
// specs live, how to review, and how to merge are product-neutral or belong
// in the repo's AGENTS.md / the relevant tool description.
func TestBaseRulesCarryNoRepoSpecificWorkflow(t *testing.T) {
	for _, rules := range []string{baseRules, baseRulesSystem} {
		for _, banned := range []string{
			".docs-archive",
			"dispatch a reviewer subagent",
			"workspace.finish",
		} {
			if strings.Contains(rules, banned) {
				t.Errorf("base rules must not mention %q:\n%s", banned, rules)
			}
		}
		if !strings.Contains(rules, ">>>>>>> REPLACE") {
			t.Errorf("base rules lost the REPLACE terminator rule:\n%s", rules)
		}
	}
}
```

Add to `internal/tools/native/workspace_finish_test.go`:

```go
func TestWorkspaceFinishDescriptionCarriesMergeGuidance(t *testing.T) {
	tools, err := newToolSet(Options{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	desc := tools.workspaceFinishTool().Description
	for _, want := range []string{"instead of shell.run git merge", "stash"} {
		if !strings.Contains(desc, want) {
			t.Errorf("workspace.finish description missing %q: %s", want, desc)
		}
	}
}
```

(Add `"strings"` to that file's imports if it isn't there.)

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run TestBaseRulesCarryNoRepoSpecificWorkflow -count=1 && CGO_ENABLED=1 go test ./internal/tools/native/ -run TestWorkspaceFinishDescriptionCarriesMergeGuidance -count=1`
Expected: both FAIL. The rules mention `.docs-archive`, and the description lacks the guidance.

- [ ] **Step 3: Edit the prompt rules.** In `internal/agent/prompts.go`:
  - Delete the constants `ruleDocsArchive`, `ruleWorkspaceFinish` and `ruleReviewerSubagent`.
  - Because `ruleReviewerSubagent` was the last item and had no trailing newline, `ruleReplaceTerminator` becomes last; leave its text unchanged.
  - Make both compositions end like this:

```go
const baseRules = "Rules:\n" +
	rulePreferSmallChanges +
	ruleReadBeforeEdit +
	ruleFileToolsWrite +
	ruleUntrustedText +
	ruleTraceCodePath +
	ruleNarrowestValidation +
	ruleAskUser +
	ruleFactGathering +
	ruleFinalAnswer +
	ruleStopAfterValidation +
	ruleReplaceTerminator
```

  - Write `baseRulesSystem` identically, but with `ruleFileToolsWriteSystem` in place of `ruleFileToolsWrite`.
  - Change `ruleReplaceTerminator`'s value to drop the trailing `\n`, so the block still ends without a newline as before:

```go
	ruleReplaceTerminator    = "- REPLACE blocks end with >>>>>>> REPLACE; the closing ======= line is a diff-hunk separator the parser has to repair."
```

- [ ] **Step 4: Move the merge guidance into the tool.** In `internal/tools/native/workspace_finish.go`, replace the `Description` with:

```go
		Description: "Merge the session's worktree branch into the base branch (squash by default, squash=false for --no-ff), then remove the worktree and delete the branch. " +
			"Use this instead of shell.run git merge, and pop or drop any stash you created before calling it. " +
			"Guards: refuses when the project checkout is dirty or its HEAD moved. Commit any worktree changes first, or pass message to commit them.",
```

- [ ] **Step 5: Delete the spec/plan write note.** In `internal/tools/native/file.go`:
  - Delete the block at lines 790–794 (the comment `// The in-place edit path is the one the docs convention…` and its `if notes := specOrPlanDocNudges(paths)…{…}`).
  - Delete the equivalent `if notes := specOrPlanDocNudges([]string{args.Path}); len(notes) > 0 { … }` block near line 991.
  - Delete the functions `specOrPlanDocNudge` and `specOrPlanDocNudges` with their comments (lines 811–846).
  - In `internal/tools/native/file_test.go`, delete these tests (lines 1335 onward): `TestSpecOrPlanDocNudge`, `TestSpecOrPlanDocNudgesDedupesAndPrefixesPath`, `TestFileWriteDocNudge`, `TestFileWriteNewDocNudges`, `TestFileWritePatchDocNudges`, `TestFileWriteNonDocNoNudge`.
  - Then add one regression test in their place:

```go
// Writing a spec-shaped doc under docs/ must not append any location note:
// where specs live is the repository's decision, not the tool's.
func TestFileWriteDocsSpecHasNoLocationNote(t *testing.T) {
	root := t.TempDir()
	// file.write does not create parent directories.
	if err := os.MkdirAll(filepath.Join(root, "docs", "feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	res, err := invokeTool(t, reg, "file.write", `{"path":"docs/feature/design.md","content":"# design\n"}`)
	if err != nil {
		t.Fatalf("file.write: %v", err)
	}
	if strings.Contains(res.Content, ".docs-archive") {
		t.Fatalf("file.write appended a location note: %q", res.Content)
	}
}
```

  Before writing this, check how the deleted `TestFileWriteNewDocNudges` built its registry. If it used a helper other than `RegisterAll` + `invokeTool` (for example one that wires a file tracker), copy that setup instead, so `file.write` isn't rejected for an unrelated reason.

- [ ] **Step 6: Make the built-in skills defer to the repo.**
  - `internal/skills/builtin/brainstorming.md` line 63 becomes:
    `Write the approved design to the spec location named in the repo instructions (AGENTS.md or equivalent), defaulting to \`docs/specs/YYYY-MM-DD-<topic>-design.md\`, containing:`
  - `internal/skills/builtin/marshal-writing-plans.md`:
    - Lines 9–10 become `Use this skill after \`brainstorming\` has produced an approved spec (in the repo's spec location, default \`docs/specs/\`) and the work will be executed inline rather than through the`
    - Line 17 becomes `1. Read the approved spec first (repo's spec location, default \`docs/specs/\`). The design must be`
    - Line 40 becomes `8. Write the plan to the plan location named in the repo instructions, defaulting to \`docs/plans/YYYY-MM-DD-<topic>-plan.md\`. By default,`
  - `internal/skills/builtin/using-skills.md` lines 24–25 become:

```
  sections → spec in the repo's spec location (default `docs/specs/`) → user review) → marshal-writing-plans
  (consumes the approved spec, plan in the repo's plan location (default `docs/plans/`) with verbatim code for
```

  `internal/skills/builtin_test.go` still finds `docs/specs/` and `docs/plans/` in each body, so it needs no change. Run `CGO_ENABLED=1 go test ./internal/skills/ -count=1` to confirm.

- [ ] **Step 7: Keep the review behaviour for this repo.** In `AGENTS.md`, append this paragraph to the end of the `## Specs and plans` section:

```markdown
When asked to review code or completed work in this repository, dispatch a
reviewer subagent with `agent.run` instead of reviewing inline, unless the
change is trivially small.
```

- [ ] **Step 8: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go test ./internal/agent/ ./internal/tools/native/ ./internal/skills/ -count=1`
Expected: PASS.

- [ ] **Step 9: Commit.**

```bash
gofmt -w internal/agent internal/tools/native
git add -A internal/agent internal/tools/native internal/skills/builtin AGENTS.md
git commit -m "prompt: drop repo-specific workflow rules from the shipped prompt"
```

---

### Task 3: Delete the `config.*` setter tools and keep `config.read`

**Files:**
- Create: `internal/tools/native/config_read.go` (move `configReadTool` and `filteredConfigJSON` verbatim from `configtools.go`)
- Create: `internal/tools/native/config_read_test.go`
- Delete: `internal/tools/native/configtools.go`, `configtools_test.go`, `configtools_integration_test.go`, `configtools_stale_test.go`
- Modify: `internal/tools/native/native.go` (Options fields lines 98–109; toolSet fields 195–197; registration 286–289; constructor 433–435; comment 240–241)
- Modify: `internal/tools/native/tools_select.go:24`
- Modify: `internal/tools/native/native_test.go:261-345`
- Modify: `internal/tools/native/saferesolve.go:23` (comment only)
- Modify: `internal/app/app.go:766-768`
- Modify: `internal/app/tui/approval.go:99` and `internal/app/tui/approval_test.go:235-265`
- Modify: `internal/agent/chat.go:635`, `internal/agent/prompts.go:665,880` (comments only)
- Modify: `internal/app/app_test.go:1176` (comment only)

**Interfaces:**
- Consumes: nothing.
- Produces: `native.Options` without `ConfigPath`, `UserConfigPath` or `ConfigReloader`. `config.read` is registered (deferred) whenever `RegisterAll` runs. No `config.*.set`/`config.*.delete` tools exist. `internal/app/app.go`'s `buildAgentRunnerWithLock` keeps its `configReloader` parameter, which is now unused by native. Leave it for a later cleanup so this task stays a native-only change.

- [ ] **Step 1: Write the failing tests.** Replace `TestRegisterAllDefersConfigAndDataTools` in `internal/tools/native/native_test.go` with:

```go
func TestRegisterAllDefersOnlyLowUseTools(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: t.TempDir(),
		CommandRunner: &fakeRunner{},
		SessionState:  state,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	deferred := make(map[string]bool)
	for _, tool := range reg.ListDeferred() {
		deferred[tool.Name] = true
	}
	want := map[string]bool{"config.read": true, "csv.inspect": true, "json.query": true}
	if runtime.GOOS != "windows" {
		want["shell.sudo"] = true
	}
	if len(deferred) != len(want) {
		t.Fatalf("ListDeferred() = %v, want exactly %v", deferred, want)
	}
	for name := range want {
		if !deferred[name] {
			t.Fatalf("expected %s to be deferred", name)
		}
	}
	for _, tool := range reg.List() {
		if strings.HasPrefix(tool.Name, "config.") && tool.Name != "config.read" {
			t.Fatalf("config setter %s must not be registered", tool.Name)
		}
	}
	for _, name := range []string{"file.read", "file.write_patch", "shell.run", "test.run", "repo.search", "tools.select"} {
		if deferred[name] {
			t.Fatalf("expected %s to stay non-deferred", name)
		}
	}
}

// Review focus 3: a resumed session may still name a deleted setter.
func TestToolsSelectRejectsRemovedConfigSetter(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, SessionState: state}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := invokeTool(t, reg, "tools.select", `{"names":["config.agent.set"]}`)
	if err == nil || !strings.Contains(err.Error(), "unknown or non-deferred tools: config.agent.set") {
		t.Fatalf("tools.select error = %v, want unknown-tool rejection", err)
	}
}
```

In `TestToolsSelectLoadsDeferredNativeTool`, delete the line `ConfigReloader: func(config.Config) error { return nil },`. The test still selects and dispatches `config.read`.

Add to `internal/agent/prompts_test.go`. This one already passes today; it pins behaviour the deletion must keep:

```go
// Review focus 3: a loaded name that is no longer registered must neither
// appear as an available tool nor break the build of the prompt.
func TestStaleLoadedConfigSetterIsIgnoredInPrompt(t *testing.T) {
	read := registry.Tool{Name: "config.read", Description: "Read config.", Risk: registry.RiskReadOnly, Deferred: true}
	msg := BuildSystemPromptWithSystem(SystemPromptOptions{
		Role: RoleGeneral, Tools: []registry.Tool{read}, Deferred: []registry.Tool{read},
		Mode: policy.ModeEdit, LoadedNames: []string{"config.agent.set"},
	})
	if strings.Contains(msg.Content, "config.agent.set") {
		t.Fatalf("stale loaded name leaked into the prompt:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, "config.read") {
		t.Fatalf("config.read must still be announced:\n%s", msg.Content)
	}
}
```

Create `internal/tools/native/config_read_test.go`. Start from `TestConfigReadReturnsMaskedConfig` in `configtools_test.go` (lines 19–51), with two changes: replace `tools, err := newConfigToolSet(toolSet{config: cfg})` and its error check with `tools := toolSet{config: cfg}`, and drop the now-unused imports. Then add a section-filter case:

```go
func TestConfigReadFiltersSections(t *testing.T) {
	cfg := config.Default()
	tools := toolSet{config: cfg}
	res, err := tools.configReadTool().Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.read", Args: json.RawMessage(`{"sections":["Agent"]}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Content, `"Agent"`) || strings.Contains(res.Content, `"Providers"`) {
		t.Fatalf("filtered output must contain only Agent: %s", res.Content)
	}
}
```

Before committing to the key spelling, check it against the actual JSON keys: `config.Config` has no JSON tags, so top-level keys are the Go field names (`Agent`, `Providers`). Confirm by running the test once after Step 3.

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/tools/native/ -run 'TestRegisterAllDefersOnlyLowUseTools|TestToolsSelectRejectsRemovedConfigSetter' -count=1`
Expected: FAIL. `config.read` isn't registered without `ConfigReloader`.

- [ ] **Step 3: Move `config.read`, delete the rest.**
  1. Create `internal/tools/native/config_read.go` containing `package native`, imports (`context`, `encoding/json`, `fmt`, `marshal/internal/app/config`, `marshal/internal/tools/registry`), and the `configReadTool` and `filteredConfigJSON` functions copied verbatim from `configtools.go` (lines 1641–1701).
  2. In `configReadTool`, change the description's last sentence from `Use this before changing settings so you know the current values.` to `Change settings through /settings, /models or /connect.`.
  3. Mark the tool deferred: `tool.Deferred = true` before `return tool`.
  4. Run `git rm internal/tools/native/configtools.go internal/tools/native/configtools_test.go internal/tools/native/configtools_integration_test.go internal/tools/native/configtools_stale_test.go`
  5. In `internal/tools/native/native.go`:
     - Delete the `ConfigPath`, `UserConfigPath` and `ConfigReloader` fields from `Options` with their comments.
     - Delete the `configPath`, `userConfigPath` and `configReloader` fields from `toolSet`, and their three assignments in `newToolSet`.
     - Replace

```go
	if tools.configReloader != nil {
		all = append(all, tools.configTools()...)
	}
```

with

```go
	all = append(all, tools.configReadTool())
```

     - Change the comment above `csvTool` to: `// csv.inspect and json.query are low-use; defer them behind tools.select so they don't occupy the prompt each turn.`
  6. In `internal/tools/native/tools_select.go:24`, change `low-use native tools such as config.*, csv.inspect, json.query` to `low-use native tools such as config.read, csv.inspect, json.query`.
  7. In `internal/app/app.go`, delete the three lines `ConfigPath: …`, `UserConfigPath: …` and `ConfigReloader: configReloader,` from the `native.Options` literal (lines 766–768).
  8. In `internal/app/tui/approval.go:99`, delete `tc.Name == "config.write" ||` so the return starts `return tc.Name == "shell.sudo" ||`. In the comment above `forcedPromptClass`, nothing names `config.write`, so leave it.
  9. In `internal/app/tui/approval_test.go`:
     - Change the comment at line 235 to `// shell.sudo is deliberately non-bypassable: an always-allow rule would let a later command run as root with no authentication.`
     - Replace both `{Name: "config.write"}` / `&session.PendingToolCall{Name: "config.write"}` with `shell.sudo`.
  10. Comment-only updates:
      - `internal/agent/chat.go:635`: `(config.*, csv.inspect, json.query)` → `(config.read, csv.inspect, json.query, shell.sudo)`
      - `internal/agent/prompts.go:665`: `such as config.*` → `such as csv.inspect`
      - `internal/agent/prompts.go:880`: `the config.* family` → `a large MCP server`
      - `internal/tools/native/saferesolve.go:23`: keep it, `config.read` still exists.
      - `internal/app/app_test.go:1176`: `through config.mcp.set` → `the way /settings would`.
      - The comment in `internal/app/app.go` around line 1796 that mentions `config.*.set tools are loadable in the child`: change that clause to `and config writes from the TUI reach children through the shared ConfigReloader`.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/tools/native/ ./internal/agent/ ./internal/app/tui/ -count=1 && CGO_ENABLED=1 go test ./internal/app/ -run 'TestReload|TestStartRuntime' -count=1`
Expected: PASS. If `go vet ./internal/tools/native/` reports an unused helper that only the deleted tools used (for example in `helpers.go`), delete it.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal/tools/native internal/app internal/agent
git add -A internal/tools/native internal/app internal/agent
git commit -m "tools: delete config.* setter tools, keep config.read"
```

---

### Task 4: Curated-only memory injection; remove auto-extraction and `memory.write`

**Files:**
- Delete: `internal/knowledge/` (whole package), `internal/app/knowledge_schedule.go`, `internal/app/knowledge_schedule_test.go`, `internal/tools/native/memory.go`, `internal/tools/native/memory_test.go`, `docs/knowledge.md`
- Modify: `internal/app/app.go`:
  - Options field `knowledgeHook` (line 104) and `WithKnowledgeHook` (183–192)
  - `shutdownKnowledgeTimeout` (line 128)
  - `dbMemoryProvider.Memories` (414–429)
  - The scheduler wiring (919–941)
  - The shutdown block (2465–2489)
- Modify: `internal/tools/native/native.go` (`memory.write` registration)
- Modify: `internal/app/app_test.go`:
  - Delete: `TestRunUsesLiveConfigForShutdownKnowledgePass`, `TestRunBoundsShutdownKnowledgePass`, `TestShutdownKnowledgeFeedbackPrinted`, and the helpers `knowledgeEnabledConfig` and `newKnowledgeTestServer` once unused.
  - Rewrite: `TestRunReturnsProgramRunnerErrorAfterKnowledgeEndSession`, `TestRunQuiescesBeforeKnowledgeAndClosesAfter`, `TestDBMemoryProviderFiltersStaleMemories`, `TestDBMemoryProviderReadsScopesForWorkspace`.
- Modify: `README.md` (lines 30–32 and the `### Knowledge and memory` section at 124–130), `docs/ARCHITECTURE.md:87`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `dbMemoryProvider.Memories` returns only memories with `Confidence == db.MemoryConfidenceConfirmed`, or with `Scope` of `db.MemoryScopeWorkspace` or `db.MemoryScopeGlobal`, and never `stale`.
  - `WithShutdownHook(func(ctx context.Context, state *session.State, database *db.DB)) Option` replaces `WithKnowledgeHook`. It's invoked after quiesce and after the session is marked ended, before Close.
  - No `internal/knowledge` package exists.

- [ ] **Step 1: Write the failing tests.** In `internal/app/app_test.go`, replace `TestDBMemoryProviderFiltersStaleMemories` with:

```go
// Only memories a person curated reach the prompt: confirmed project
// memories, or anything promoted to workspace/global scope. Tentative
// auto-learned rows and stale rows never do.
func TestDBMemoryProviderInjectsOnlyCuratedMemories(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	now := time.Unix(100, 0)
	if err := database.CreateSession("sess_1", projectID, "", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, text := range []string{"tentative note", "confirmed note", "stale note"} {
		if err := database.SaveMemory(projectID, "fact", text, "sess_1", now); err != nil {
			t.Fatalf("SaveMemory %q: %v", text, err)
		}
	}
	stored, err := database.GetMemories(projectID)
	if err != nil || len(stored) != 3 {
		t.Fatalf("GetMemories = %v, %v; want 3 rows", stored, err)
	}
	if err := database.SetMemoryConfidence(stored[1].ID, db.MemoryConfidenceConfirmed, now.Add(time.Second)); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := database.SetMemoryConfidence(stored[2].ID, db.MemoryConfidenceStale, now.Add(time.Second)); err != nil {
		t.Fatalf("stale: %v", err)
	}

	memories, err := (&dbMemoryProvider{db: database}).Memories(projectID)
	if err != nil {
		t.Fatalf("Memories failed: %v", err)
	}
	if len(memories) != 1 || memories[0].Content != "confirmed note" {
		t.Fatalf("memories = %+v, want only the confirmed note", memories)
	}
}
```

In `TestDBMemoryProviderReadsScopesForWorkspace`, change the expected list from `[]string{"project note", "workspace note", "global note"}` to `[]string{"workspace note", "global note"}`, and add this comment above it: `// "project note" is tentative and project-scoped: not curated, not injected.`

Rewrite `TestRunReturnsProgramRunnerErrorAfterKnowledgeEndSession` as follows (no provider configured, which used to leave sessions un-ended):

```go
func TestRunMarksSessionEndedEvenWhenProgramRunnerFails(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	now := time.Unix(100, 0)
	wantErr := errors.New("program runner failed")
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return config.Default(), nil }),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			modelState(t, model).AddMessage(session.RoleUser, "keep session history", session.ContentTypePlain)
			return ProgramResult{Err: wantErr}
		}),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	database, dberr := db.Open(db.Path(dir))
	if dberr != nil {
		t.Fatalf("open db: %v", dberr)
	}
	defer database.Close()
	got, err := database.GetSession(fmt.Sprintf("sess_%d", now.UnixNano()))
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt == nil {
		t.Fatal("EndedAt is nil: exit must mark the session ended even with no provider")
	}
	if got.Summary != "" {
		t.Fatalf("Summary = %q, want empty (no knowledge pass)", got.Summary)
	}
}
```

Rewrite `TestRunQuiescesBeforeKnowledgeAndClosesAfter` as `TestRunQuiescesBeforeShutdownHookAndClosesAfter`:
- Replace `WithKnowledgeHook(knowledgeHook)` with `WithShutdownHook(shutdownHook)`.
- Rename the local `knowledgeHook`/`knowledgeRan` to `shutdownHook`/`hookRan`, and update the error strings from "during knowledge" to "during shutdown hook".
- Replace the two trailing `t.Logf` checks at the end (`got.Summary` / `got.EndedAt`) with:

```go
	if got.EndedAt == nil {
		t.Fatal("session must be marked ended before Close")
	}
```

Add Review Focus 1 and 4 tests at the end of `internal/app/app_test.go`:

```go
// Review focus 1: marking a session ended at exit must not stop it being
// resumed.
func TestEndedSessionStillResumes(t *testing.T) {
	dir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)
	now := time.Unix(100, 0)
	run := func(opts ...Option) {
		base := []Option{
			WithNow(func() time.Time { return now }),
			WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return config.Default(), nil }),
			WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
			WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
				modelState(t, model).AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
				return ProgramResult{}
			}),
		}
		if err := Run(context.Background(), bytes.NewBuffer(nil), append(base, opts...)...); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	run()
	id := fmt.Sprintf("sess_%d", now.UnixNano())
	run(WithExistingSession(id))

	database, err := db.Open(db.Path(dir))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	msgs, err := database.GetMessages(id)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	var users int
	for _, m := range msgs {
		if m.Role == string(session.RoleUser) {
			users++
		}
	}
	if users != 2 {
		t.Fatalf("resumed session has %d user messages, want 2 (one per run)", users)
	}
}

// Review focus 4: a config that still binds the retired knowledge role
// starts normally.
func TestRuntimeStartsWithKnowledgeRoleBound(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[profile]
default = "p"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "k"
[models.presets."mock/m"]
local_only = true
[agent_profiles.p]
implementer = "mock/m"
planner = "mock/m"
repo_scout = "mock/m"
tester = "mock/m"
reviewer = "mock/m"
knowledge = "mock/m"
`), 0644); err != nil {
		t.Fatal(err)
	}
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
}
```

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run 'TestDBMemoryProvider|TestRunMarksSessionEnded|TestRunQuiescesBeforeShutdownHook|TestEndedSessionStillResumes|TestRuntimeStartsWithKnowledgeRoleBound' -count=1`
Expected: compile failure (`WithShutdownHook` undefined). Once that's stubbed, `TestDBMemoryProviderInjectsOnlyCuratedMemories` and `TestRunMarksSessionEndedEvenWhenProgramRunnerFails` FAIL on behaviour.

- [ ] **Step 3: Curate injection.** Replace the body of `dbMemoryProvider.Memories` in `internal/app/app.go`, and update its doc comment:

```go
// Memories returns the curated notes visible to this project: confirmed
// project memories, then workspace and global memories (promoted by a
// person, see db.GetScopedMemories). Tentative project memories — what an
// agent learned on its own — and stale memories are never injected.
func (p *dbMemoryProvider) Memories(projectID int64) ([]contextpack.MemoryNote, error) {
	memories, err := p.db.GetScopedMemories(projectID, p.workspace)
	if err != nil {
		return nil, err
	}
	notes := make([]contextpack.MemoryNote, 0, len(memories))
	for _, m := range memories {
		if m.Confidence == db.MemoryConfidenceStale {
			continue
		}
		curated := m.Confidence == db.MemoryConfidenceConfirmed ||
			m.Scope == db.MemoryScopeWorkspace || m.Scope == db.MemoryScopeGlobal
		if !curated {
			continue
		}
		notes = append(notes, contextpack.MemoryNote{Kind: m.Kind, Content: m.Content, Confidence: m.Confidence, UpdatedAt: m.UpdatedAt})
	}
	return notes, nil
}
```

- [ ] **Step 4: Remove auto-extraction.**
  1. In `buildAgentRunnerWithLock` (`internal/app/app.go` around 919–941):
     - Delete the `scheduler := &knowledgeScheduler{…}` literal.
     - Change the `MetricsObserver` to `runner.MetricsObserver = recorder`.
     - Delete `runner.CompactionObserver = scheduler.OnCompaction`.
     - Leave `runner.MemoryProvider = newDBMemoryProvider(database)` in place.
  2. In the shutdown block (around 2465–2489), replace everything from `// Phase 2: knowledge — finalize the session…` through the closing `}` of `if runOpts.knowledgeHook != nil {…}` with:

```go
			// Phase 2: mark the session ended while the DB is still open.
			if err := database.EndSession(sessionID, runOpts.now(), ""); err != nil {
				logger.Warn("mark session ended failed", "error", err, "session_id", sessionID)
			}
			if runOpts.shutdownHook != nil {
				runOpts.shutdownHook(context.Background(), state, database)
			}
```

     Fix the Phase 1 comment `…without closing persistence so knowledge finalization can use the DB.` to read `…so the session can be marked ended.`
  3. Rename the options field `knowledgeHook` to `shutdownHook`. Replace `WithKnowledgeHook` with:

```go
// WithShutdownHook registers a callback invoked during TUI exit after the
// runtime has quiesced and the session is marked ended, but before the
// runtime closes. Tests use it to observe the shutdown window.
func WithShutdownHook(hook func(ctx context.Context, state *session.State, database *db.DB)) Option {
	return func(opts *options) {
		opts.shutdownHook = hook
	}
}
```

  4. Delete `shutdownKnowledgeTimeout` from the const/var block at line 128.
  5. Remove the `marshal/internal/knowledge` import.
  6. Delete the files: `git rm -r internal/knowledge internal/app/knowledge_schedule.go internal/app/knowledge_schedule_test.go docs/knowledge.md`
  7. Delete `TestRunUsesLiveConfigForShutdownKnowledgePass`, `TestRunBoundsShutdownKnowledgePass` and `TestShutdownKnowledgeFeedbackPrinted` from `internal/app/app_test.go`. Then delete `knowledgeEnabledConfig` and `newKnowledgeTestServer` if `grep -n 'knowledgeEnabledConfig\|newKnowledgeTestServer' internal/app/*_test.go` shows no remaining callers.
  8. Run `grep -rn 'knowledge\.' --include='*.go' internal cmd` and confirm it prints nothing.

- [ ] **Step 5: Remove `memory.write`.**
  - In `internal/tools/native/native.go`, delete:

```go
	if tools.db != nil {
		all = append(all, tools.memoryWriteTool())
	}
```

  - Run `git rm internal/tools/native/memory.go internal/tools/native/memory_test.go`.
  - If `memory.go` held helpers other code uses (check with `go build ./...`), keep only those in `helpers.go`.

- [ ] **Step 6: Update the docs.**
  - `README.md` lines 30–32, replace the bullet with:

```markdown
- **Project knowledge lives where you can read it.** Durable guidance goes in
  your repo's AGENTS.md. Memories you confirm or promote (in the web memory
  library) come back automatically in later sessions; nothing is learned
  behind your back.
```

  - In `README.md`, replace the `### Knowledge and memory` section body (lines 126–130) with:

```markdown
Marshal injects only curated memories: ones confirmed in `/memory` or the web
memory library, or promoted to workspace or global scope. It no longer runs a
background knowledge pass. Put standing project guidance in AGENTS.md.
```

  - In `docs/ARCHITECTURE.md:87`, change the row to: `| Knowledge | \`internal/contextpack/\`, \`internal/rollover/\` | Curated memory injection, context budget, long-session rollover. |`

- [ ] **Step 7: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/ ./internal/tools/native/ ./internal/agent/ ./internal/acp/ -count=1`
Expected: PASS, apart from the known environmental `internal/app` failures listed in Global Constraints.

- [ ] **Step 8: Commit.**

```bash
gofmt -w internal/app internal/tools/native
git add -A internal docs README.md
git commit -m "memory: inject curated memories only; remove the knowledge pass and memory.write"
```

---

### Task 5: Drop the repo card, directory map and recent-sessions seeds

**Files:**
- Delete: `internal/app/seed.go`, `internal/app/seed_test.go`
- Modify: `internal/app/runtime.go` (lines 794–797, 907–908, 976–991 delete `subscribeIndexReseed`, 1044–1045)
- Modify: `internal/app/index_worker_test.go:91-` (delete `TestSubscribeIndexReseedSeedsOnReport`)
- Modify: `docs/ARCHITECTURE.md:122` (prompt layout)

**Interfaces:**
- Consumes: nothing.
- Produces: no runtime path adds `contextpack.SectionRepoCard`, `SectionRepoMap` or `SectionSessionSummaries`. The section kinds and `contextpack.MergeRepoSections` stay; tests and `repo.map` don't depend on the seed.

- [ ] **Step 1: Write the failing test.** Add to `internal/app/runtime_test.go`:

```go
// The context pack starts empty of repo orientation: no repo card,
// directory map or recent-session summaries, even when the index and
// session history exist.
func TestRuntimeSeedsNoRepoOrientation(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	start := func() *Runtime {
		rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
		if err != nil {
			t.Fatalf("StartRuntime: %v", err)
		}
		return rt
	}
	// First start creates the project row under whatever root the runtime
	// resolves (temp dirs are symlinked on macOS, so never guess the path).
	// Record an ended session with a summary against that row, then restart.
	first := start()
	pid := first.ProjectID
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	if err := database.CreateSession("sess_old", pid, "old", now); err != nil {
		t.Fatal(err)
	}
	if err := database.EndSession("sess_old", now, "did some work"); err != nil {
		t.Fatal(err)
	}
	database.Close()

	rt := start()
	defer rt.Close(context.Background())
	for _, s := range rt.State.ContextPack().Sections {
		switch s.Kind {
		case contextpack.SectionRepoCard, contextpack.SectionRepoMap, contextpack.SectionSessionSummaries:
			t.Fatalf("context pack carries %s section %q", s.Kind, s.Title)
		}
	}
}
```

Add any missing imports (`contextpack`, `db`, `trust`) to `runtime_test.go`. The session-summary half fails on today's code even before the index scan lands. Check that `db.Path(tmp)` is the path the runtime itself opens (`grep -n 'db.Path(' internal/app/runtime.go`). If the runtime anchors the DB elsewhere, open that path instead.

- [ ] **Step 2: Run the test and confirm it fails.**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run TestRuntimeSeedsNoRepoOrientation -count=1`
Expected: FAIL with `context pack carries session_summaries section`.

- [ ] **Step 3: Delete the seeds.**
  - In `internal/app/runtime.go`:
    - Delete the comment `// AI-01: seed the context pack with repo orientation from the index.` / `// No-op on a fresh project…` and the two seed calls after it.
    - Delete `// AI-01: re-seed the pack's repo sections when an index pass completes.` and `rt.subscribeIndexReseed(indexBroker, database, projectID)`.
    - Delete the whole `subscribeIndexReseed` method and its comment.
    - Delete the two seed calls in `NewSession`.
  - Run `git rm internal/app/seed.go internal/app/seed_test.go`.
  - In `internal/app/index_worker_test.go`, delete `TestSubscribeIndexReseedSeedsOnReport` and any helper only it used.
  - If `indexBroker` becomes an unused parameter or variable in `startRuntime`, leave it when other subscribers or publishers use it. Only delete it if the compiler reports it unused.

- [ ] **Step 4: Update the prompt-layout doc.** In `docs/ARCHITECTURE.md`:
  - Change line 122 to `[1] context pack (stable part)  curated memories, plan, pins`.
  - Delete line 125 (`skill hint                  per-turn; directly before the goal`). Task 1 removed it.

- [ ] **Step 5: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/ ./internal/agent/ -count=1`
Expected: PASS, apart from the known environmental failures.

- [ ] **Step 6: Commit.**

```bash
gofmt -w internal/app
git add -A internal/app docs/ARCHITECTURE.md
git commit -m "contextpack: stop seeding repo card, directory map and recent sessions"
```

---

### Task 6: Prompt-budget guard, changelog, full verification

**Files:**
- Create: `internal/app/prompt_budget_test.go`
- Modify: `CHANGELOG.md` (add `### Removed` under `## [Unreleased]`, before `### Fixed` at line 57)
- Modify: `docs/context-diet/design.md` (append a measured-results section)

**Interfaces:**
- Consumes: everything from Tasks 1–5.
- Produces: `TestDefaultPromptBudget`, with constants `maxSystemPromptBytes = 11000` and `maxToolSchemaBytes = 31500`.

- [ ] **Step 1: Write the guard test.** Create `internal/app/prompt_budget_test.go`:

```go
package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/agent"
	"marshal/internal/trust"
)

// Ceilings for the fixed prefix of a default native-tools request, set by
// the context-diet Tier 1 work (docs/context-diet/design.md). Lower them as
// later tiers cut more; never raise them without a design note saying why.
const (
	maxSystemPromptBytes = 11000
	maxToolSchemaBytes   = 31500
)

func TestDefaultPromptBudget(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[profile]
default = "p"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "k"
[models.presets."mock/m"]
local_only = true
[agent_profiles.p]
implementer = "mock/m"
planner = "mock/m"
repo_scout = "mock/m"
tester = "mock/m"
reviewer = "mock/m"
`), 0644); err != nil {
		t.Fatal(err)
	}
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	r := rt.Runner

	sys := agent.BuildSystemPromptWithSystem(agent.SystemPromptOptions{
		Role:         agent.RoleGeneral,
		Tools:        r.Registry.List(),
		Deferred:     r.Registry.ListDeferred(),
		SkillIndex:   r.SkillIndex,
		ActiveSkills: rt.State.ActiveSkills(),
		NativeTools:  true,
		Mode:         r.Policy.ApprovalMode(),
		Addendum:     r.SystemPromptAddendum,
		WorkingDir:   rt.State.WorkingDir,
		Roster:       agent.RenderAgentRoster(rt.State.Config),
		Narration:    r.NarrationPrompt,
	}).Content

	var schemaBytes int
	for _, tl := range r.Registry.List() {
		if tl.Deferred {
			continue
		}
		b, err := json.Marshal(struct {
			Name        string
			Description string
			Parameters  json.RawMessage
		}{tl.Name, tl.Description, tl.Schema})
		if err != nil {
			t.Fatalf("marshal %s: %v", tl.Name, err)
		}
		schemaBytes += len(b)
		if tl.Name == "memory.write" || (strings.HasPrefix(tl.Name, "config.") && tl.Name != "config.read") {
			t.Errorf("removed tool %s is registered", tl.Name)
		}
	}
	t.Logf("system prompt %d bytes (ceiling %d), tool schemas %d bytes (ceiling %d)", len(sys), maxSystemPromptBytes, schemaBytes, maxToolSchemaBytes)

	if len(sys) > maxSystemPromptBytes {
		t.Errorf("system prompt is %d bytes, over the %d ceiling", len(sys), maxSystemPromptBytes)
	}
	if schemaBytes > maxToolSchemaBytes {
		t.Errorf("tool schemas are %d bytes, over the %d ceiling", schemaBytes, maxToolSchemaBytes)
	}
	for _, banned := range []string{".docs-archive", "dispatch a reviewer subagent", "config.agent.set", "Skill suggestions for this request"} {
		if strings.Contains(sys, banned) {
			t.Errorf("system prompt contains removed text %q", banned)
		}
	}
}
```

- [ ] **Step 2: Run it.**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run TestDefaultPromptBudget -count=1 -v`
Expected: PASS. The log line should show the system prompt near 10,500 bytes and the tool schemas near 31,000 bytes.

If either figure is over its ceiling, don't raise the ceiling. Find which content grew (compare against the design doc's estimate) and report it. Then confirm the test bites: temporarily set `maxSystemPromptBytes = 1000`, see it fail, and restore it.

- [ ] **Step 3: Record results and the changelog.**
  - Append to `docs/context-diet/design.md`:

```markdown
## Measured result

Recorded from `TestDefaultPromptBudget` on <date of run>: system prompt
<N> bytes, tool schemas <M> bytes. The context pack starts empty for a
project with no curated memories.
```

    Fill in the real numbers from the Step 2 log line before committing.
  - In `CHANGELOG.md`, insert before `### Fixed` under `## [Unreleased]`:

```markdown
### Removed

- The background knowledge pass (every 5 turns, at compaction and at exit)
  and the `memory.write` tool. Only curated memories — confirmed, or
  promoted to workspace/global scope — are injected into the prompt.
- Per-turn skill suggestions and the embedding skill ranker.
- The repo card, directory map and recent-session summaries from the
  context pack.
- The 29 `config.*` setter tools; `config.read` remains. Use `/settings`,
  `/models` or `/connect` to change configuration.
- Repository-specific workflow rules (spec location, reviewer subagent)
  from the shipped system prompt, and the spec-location note on file writes.

### Changed

- Exiting the TUI always marks the session ended, with or without a
  configured provider.
```

    If `## [Unreleased]` already has a `### Changed` heading (it does, at line 41), add the bullet there instead of creating a second heading.

- [ ] **Step 4: Full verification.**

Run:

```bash
gofmt -l . | grep -v '^\.home/' ; CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./... 2>&1 | tail -60
```

Expected: `gofmt -l` prints nothing, and `go vet` is clean. `go test` passes, apart from the environmental failures listed in Global Constraints. Confirm any other failure against a clean `git worktree add --detach <base>` before calling it pre-existing.

Also run the usability suite, which CI runs on every push:

```bash
CGO_ENABLED=1 go build -o marshal ./cmd/marshal && go test ./test/usability/... -run TestScripted -timeout 5m
```

Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal/app
git add internal/app/prompt_budget_test.go CHANGELOG.md docs/context-diet/design.md
git commit -m "test: guard the default prompt budget after the Tier 1 context diet"
```

## Integration notes

- Tasks 1, 2, 3 and 5 are independent and can land in any order. Task 4 touches `internal/tools/native/native.go` near the lines Task 3 edits, so run it after Task 3 to avoid a merge conflict there. Task 6 must be last.
- Existing databases keep their 404 tentative memories. They stay visible in `/memory` and the web library, and they'll only be injected once someone confirms or promotes them.
- Existing user configs that bind the `knowledge` role keep loading (Review Focus 4). The role just has no consumer now.
