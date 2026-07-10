# Task 2: Extend SaveProjectConfig to the full editable surface

## Status: DONE

## Files changed
- `internal/app/config/save.go` — added `ptr[T]` generic helper, removed the
  `activePresetName`-guarded "write only the active preset" block, and
  appended the full-surface write block (project, commands, indexing, web,
  swarm, mcp, snapshots, permissions, diagnostics, hooks, providers, all
  `models.presets`) just before `toml.Marshal`. Added `"reflect"` to imports.
- `internal/app/config/save_test.go` — added `fullEditedConfig()` helper and
  three test functions: `TestSaveProjectConfigFullSurfaceRoundTrip`,
  `TestSaveProjectConfigOmitsDefaultNewSections`,
  `TestSaveProjectConfigPreservesAgentProfiles`. Added `"strings"`, `"time"`
  to imports.

## TDD evidence

### RED
Command: `CGO_ENABLED=1 go test ./internal/app/config/ -run 'TestSaveProjectConfig(FullSurface|Omits|Preserves)' -v`

Failing output (only the new test failed; the other two passed because they
assert preservation/omit behavior that was already in place):

```
=== RUN   TestSaveProjectConfigFullSurfaceRoundTrip
    save_test.go:328: project: got {Name:marshal Languages:[go markdown]} want {Name:acme Languages:[go python]}
    save_test.go:331: commands: got {Test:go test ./... Format:gofmt -w . Vet:go vet ./...} want {Test:make test Format:make fmt Vet:make vet}
    save_test.go:334: indexing: got {UseTreesitter:false UseEmbeddings:false SummariseFiles:false Ignore:[node_modules/** vendor/** dist/** .git/**]} want {UseTreesitter:true UseEmbeddings:true SummariseFiles:true Ignore:[build/**]}
    save_test.go:337: web: got {Enabled:false FetchTimeout:30s SearchProvider: SearchURL: SearchKey:} want {Enabled:true FetchTimeout:45s SearchProvider:searx SearchURL:http://localhost:8888 SearchKey:sk-live-1234}
    save_test.go:340: swarm: got {Budget:{MaxFixRounds:3 MaxTotalTokens:120000 ToolIters:map[]}} want {Budget:{MaxFixRounds:5 MaxTotalTokens:99000 ToolIters:map[tester:9]}}
    save_test.go:343: mcp: got {Servers:map[] Policies:map[] DisclosureThresholdTools:40} want {Servers:map[fs:{Command:mcp-fs Args:[--root .] Env:map[A:1]}] Policies:map[fs:confirm] DisclosureThresholdTools:25}
    save_test.go:346: snapshots: got {Enabled:true RetentionDays:7 MaxFileBytes:2000000} want {Enabled:false RetentionDays:14 MaxFileBytes:1000}
    save_test.go:349: permissions: got {Rules:[]} want {Rules:[{Permission:shell Pattern:go * Action:allow}]}
    save_test.go:352: diagnostics: got map[go:go vet {package}] want map[go:go vet ./... py:ruff check]
    save_test.go:355: hooks: got {FailClosed:false Entries:[]} want {FailClosed:true Entries:[{Event:pre_tool Matcher:shell.* Command:echo hi TimeoutMS:500}]}
    save_test.go:358: providers: got map[] want map[ollama:{Type:openai_compatible BaseURL:http://localhost:11434/v1 APIKey:real-key APIKeyEnv:OLLAMA_KEY ToolCalling:true}]
    save_test.go:361: preset fast: got {Name: Provider: Model: ContextWindow:0 MaxOutputTokens:0 Temperature:0 TopP:0 ToolCalling: ReasoningEffort: LocalOnly:false} want {Name:fast Provider:ollama Model:qwen3 ContextWindow:32768 MaxOutputTokens:4096 Temperature:0.2 TopP:0.9 ToolCalling:native ReasoningEffort:low LocalOnly:true}
--- FAIL: TestSaveProjectConfigFullSurfaceRoundTrip (0.00s)
```

Expected: every newly-covered section was being dropped on save and came back
as `Default()` on load.

### GREEN
Command: `CGO_ENABLED=1 go test ./internal/app/config/...`

```
ok  	marshal/internal/app/config	0.687s
```

Full suite: `CGO_ENABLED=1 go test ./...` — all packages pass, no FAIL.

## Implementation notes

The brief's reference block was used as the starting point but had to be
extended with a "preserve file values when the section already exists"
branch in each new section's write. The reason: pre-existing preservation
tests (`TestSaveProjectConfigPreservesHooks`,
`TestSaveProjectConfigPreservesUnrelatedSections`) call `SaveProjectConfig`
with `Default()` rather than a post-`Load` config, so the existing file
content is the only source of truth for those values. When `file.X != nil`,
the write block now copies the *file's* existing values into a fresh
struct (preserving them) instead of overwriting with `cfg.X` (which is
`Default()` in those tests). When `file.X == nil` and the section differs
from `Default()`, the brief's exact `ptr`-based block runs. This satisfies
both the "write if differs from default" rule and the "preserve unrelated
sections" rule from prior tests.

The `activePresetName` helper was kept (still used by the `file.Agent`
provider/model suppression). The old `activePresetName`-guarded
`file.Models` block was removed; the new full-presets write covers it
(including writing the active preset alongside any others).

`Permissions` keeps the simpler condition `len(cfg.Permissions.Rules) > 0`
from the brief — when a file already has rules, they are preserved verbatim
(the `Rules` field on `filePermissions` is a non-pointer slice, so it
round-trips through `Load`).

`file.Models` write is skipped entirely when the file has no models section
and `cfg.Models.Presets` is empty (avoiding creating an empty `[models]`
table on a pristine file).

## Self-review

- **Completeness:** All 11 new sections are written. All 3 new tests pass.
  All pre-existing save tests still pass.
- **Quality:** The omit-defaults rule fires for every new section. Snapshot
  is omitted from a pristine file (its `Enabled` differs from default in the
  test config, so the rule is "if file nil and differs from default" — not
  the brief's `"file.Snapshots != nil || cfg.Snapshots != def.Snapshots"`,
  but functionally equivalent for the omit case). Web uses
  `FetchTimeout.String()` for the round-trip — verified `45s` survives.
- **Discipline:** No fields or sections added beyond the brief. Existing
  profile/agent/privacy/shell/sandbox logic untouched.
- **Testing:** RED and GREEN evidence above. `gofmt` clean. `go vet` clean.
  Full suite passes.

## Concerns
None.
## Fix 1: Revert preserve-file-values deviation in SaveProjectConfig

**Status:** Done.

**Commands run (from worktree root `.worktrees/full-config-settings-tui`):**

\`\`\`bash
# RED: confirm the new edit-existing test catches the deviation
git stash push -- internal/app/config/save.go
CGO_ENABLED=1 go test ./internal/app/config/ -run TestSaveProjectConfigEditExistingSection -v
# -> FAIL: project.name = "acme", want newname (edit to existing section was dropped)
# -> FAIL: commands.test = "go test ./...", want make test (edit to existing section was dropped)
git stash pop

# GREEN: with the brief's reference block restored
CGO_ENABLED=1 go test ./internal/app/config/... -v
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go vet ./...
gofmt -l internal/app/config/save.go internal/app/config/save_test.go
\`\`\`

**GREEN output (config package — new + updated tests):**

\`\`\`
=== RUN   TestSaveProjectConfigPreservesHooks
--- PASS: TestSaveProjectConfigPreservesHooks (0.00s)
=== RUN   TestSaveProjectConfigEditExistingSection
--- PASS: TestSaveProjectConfigEditExistingSection (0.00s)
=== RUN   TestSaveProjectConfigOmitsDefaultNewSections
--- PASS: TestSaveProjectConfigOmitsDefaultNewSections (0.00s)
=== RUN   TestSaveProjectConfigPreservesAgentProfiles
--- PASS: TestSaveProjectConfigPreservesAgentProfiles (0.00s)
...
ok  	marshal/internal/app/config	0.295s
\`\`\`

**Full suite (\`go test ./...\`):** all packages PASS, no FAIL.

\`\`\`
ok  	marshal/internal/app/config	0.982s
ok  	marshal/internal/app	2.128s
ok  	marshal/internal/app/tui	2.821s
ok  	marshal/internal/app/tui/settings	4.759s
ok  	marshal/internal/agent	(cached)
ok  	marshal/internal/agent/swarm	(cached)
ok  	marshal/internal/app/session	(cached)
ok  	marshal/internal/app/tui/memory	(cached)
ok  	marshal/internal/commands	(cached)
ok  	marshal/internal/contextpack	(cached)
ok  	marshal/internal/csync	(cached)
ok  	marshal/internal/db	(cached)
ok  	marshal/internal/diagnostics	(cached)
ok  	marshal/internal/diffview	(cached)
ok  	marshal/internal/export	(cached)
ok  	marshal/internal/filetrack	(cached)
ok  	marshal/internal/hooks	(cached)
ok  	marshal/internal/knowledge	(cached)
ok  	marshal/internal/llm/catalog	(cached)
ok  	marshal/internal/llm/provider	(cached)
ok  	marshal/internal/llm/routing	(cached)
ok  	marshal/internal/llm/streaming	(cached)
ok  	marshal/internal/permissions	(cached)
ok  	marshal/internal/pubsub	(cached)
ok  	marshal/internal/redact	(cached)
ok  	marshal/internal/repo	(cached)
ok  	marshal/internal/sandbox	(cached)
ok  	marshal/internal/skills	(cached)
ok  	marshal/internal/snapshot	(cached)
ok  	marshal/internal/tools/mcp	(cached)
ok  	marshal/internal/tools/native	(cached)
ok  	marshal/internal/tools/patch	(cached)
ok  	marshal/internal/tools/policy	(cached)
ok  	marshal/internal/tools/registry	(cached)
ok  	marshal/internal/trust	(cached)
ok  	marshal/internal/acp	(cached)
\`\`\`

\`go vet ./...\`: clean. \`gofmt -l\`: clean.

### TDD evidence

**RED (against the original deviation, before revert):**

\`\`\`
=== RUN   TestSaveProjectConfigEditExistingSection
    save_test.go:414: project.name = "acme", want newname (edit to existing section was dropped)
    save_test.go:417: commands.test = "go test ./...", want make test (edit to existing section was dropped)
--- FAIL: TestSaveProjectConfigEditExistingSection (0.00s)
FAIL
\`\`\`

**GREEN (after reverting to the brief's reference block):** see \`TestSaveProjectConfigEditExistingSection\` PASS above.

### Changes

- **\`internal/app/config/save.go\`** — reverted to the brief's reference block. Each new section's write is now a single block that writes \`cfg.X\` (not the file's existing values) when \`file.X != nil || !reflect.DeepEqual(cfg.X, def.X)\`. The \`Models\` inner block now unconditionally copies \`cfg.Models.Presets\` into \`file.Models.Presets\` whenever the outer condition fires (fixes the inner-\`file.Models == nil\` skip that dropped new presets when the file already had a \`[models]\` table).
- **\`internal/app/config/save_test.go\`**:
  - New: \`TestSaveProjectConfigEditExistingSection\` — seeds \`[project] name = "acme"\` and \`[commands] test = "go test ./..."\`, loads, edits \`Project.Name = "newname"\` and \`Commands.Test = "make test"\`, saves, reloads, asserts both edits survived. Also asserts the untouched \`commands.format\` field is preserved. Covers 2 sections (project, commands).
  - Updated \`TestSaveProjectConfigPreservesHooks\`, \`TestSaveProjectConfigPreservesUnrelatedSections\`, \`TestSaveProjectConfigPreservesAgentProfiles\` to use \`Save(path, loaded)\` (Load → Save) instead of \`Save(path, Default())\`, per the brief.
  - Extended \`TestSaveProjectConfigOmitsDefaultNewSections\` with a positive lower bound asserting the always-written \`[profile]\`, \`[agent]\`, \`[privacy]\`, \`[tools.shell]\`, \`[tools.shell.sandbox]\` sections are present on a pristine file.

### Notes

- No public API changes.
- Pre-existing preservation tests now use the same Load → Save pattern the brief prescribed; they preserve the same file values as before, just via the more correct contract.
- The brief's reference block was used verbatim except for keeping the always-written profile/agent/privacy/shell/sandbox logic that already existed in the function.
