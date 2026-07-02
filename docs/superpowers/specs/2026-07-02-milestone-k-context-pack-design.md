# Milestone K: Context Pack v1 Design

## Goal

Milestone K adds a deterministic context-pack system that gives Marshal a compact, visible project context bundle for each agent turn. The v1 pack combines a repo card, selected file snippets, recent tool output, and the current plan while tracking approximate token usage.

## Scope

In scope:

- Build context packs from explicit inputs.
- Render context packs as bounded sectioned text for model prompts.
- Store the current context pack on session state.
- Inject the current pack into the agent runner prompt flow when present.
- Show context-pack section titles and token usage in the TUI.
- Keep the implementation compatible with Milestone J by treating repo card and file index output as upstream inputs.

Out of scope:

- Tree-sitter symbols.
- Embeddings or semantic ranking.
- Automatic raw-file discovery.
- Role-specific context budgets beyond exposing token usage for Milestone L.
- Interactive TUI context browsing.

## Architecture

Add `internal/contextpack` as the owner of context-pack data structures, token estimation, budget handling, and rendering. This package is pure and deterministic: it does not import the agent runner, TUI, native tools, database, or filesystem APIs.

The agent runner consumes the current pack from `session.State` and inserts it as a bounded user-context message before planning or tool actions. The TUI reads the same state and renders a simple context browser panel. Milestone J integration stays narrow: J owns repo scanning, persisted file index records, and repo card generation; K accepts repo-card text and selected snippets as inputs.

## Core Types

`internal/contextpack` defines:

```go
type SectionKind string

const (
	SectionRepoCard   SectionKind = "repo_card"
	SectionPlan       SectionKind = "plan"
	SectionFileSnippet SectionKind = "file_snippet"
	SectionToolOutput SectionKind = "tool_output"
)

type Pack struct {
	Sections   []Section
	TokenUsage TokenUsage
	GeneratedAt time.Time
}

type Section struct {
	Kind            SectionKind
	Title           string
	Content         string
	Source          string
	Priority        int
	EstimatedTokens int
}

type TokenUsage struct {
	MaxTokens       int
	EstimatedTokens int
	Truncated       bool
}

type BuildInput struct {
	RepoCard         string
	FileSnippets     []FileSnippet
	RecentToolOutput []ToolOutput
	Plan             []string
	MaxTokens        int
	Now              func() time.Time
}
```

`FileSnippet` contains a path, optional start/end lines, and content. `ToolOutput` contains the tool name, summary, and optional content. The builder orders sections by fixed priority: repo card first, plan second, selected file snippets third, and recent tool output last.

## Data Flow

1. A caller gathers explicit context inputs from existing state and upstream repo intelligence.
2. `contextpack.Builder.Build(input)` creates a `Pack`.
3. The builder estimates tokens with a local approximation: `ceil(len([]rune(text)) / 4)`.
4. The builder applies the max-token budget in priority order.
5. The session stores the pack through `State.SetContextPack(pack)`.
6. The runner adds the rendered pack as a user-context message when `State.ContextPack()` is non-empty.
7. The TUI displays estimated token usage and section titles from the stored pack.

The runner message should be rendered with a stable prefix:

```text
Project context pack:

## Repo Card
Project: marshal
Languages: Go, Markdown
```

This keeps prompt tests simple and makes the pack visible in provider request traces.

## Budget And Truncation

The default max context budget is `12000` estimated tokens. The builder keeps whole sections when possible. If a section exceeds the remaining budget and it is a content-heavy section, the builder truncates content to fit and appends:

```text

...[truncated]
```

If a section cannot fit even after truncation, it is skipped. The pack records whether any truncation or skipping occurred through `TokenUsage.Truncated`.

The builder does not perform filesystem IO. Missing or unreadable snippets are represented only if the caller supplies a `FileSnippet` or `ToolOutput` describing the omission.

## TUI Context Browser

The v1 TUI change is intentionally small. Add a `Context` panel that displays:

- `No context pack built yet.` when absent.
- `Context Pack: <estimated>/<max> tokens` when present.
- One line per section with kind, title, source when present, and estimated tokens.

The panel is read-only. Keyboard navigation and opening full section contents are deferred.

## Runner Integration

The runner should inject the rendered context pack before the user goal is sent to the provider. If there is no pack, behavior remains unchanged.

For non-question tasks, the current plan is known only after the planning call. The runner may update the stored context pack after planning so subsequent action calls include the plan section. The initial planning call can use whatever pack exists before the turn starts.

## Error Handling

Context-pack building is best-effort. Empty inputs produce an empty pack without failing the agent turn. Invalid budgets fall back to the default. Rendering an empty pack returns an empty string, and runner injection is skipped.

The context-pack package should return errors only for impossible internal states. v1 builder inputs are plain values, so normal missing-context cases should not be errors.

## Testing

Add focused tests for:

- Token estimation.
- Section ordering.
- Budget truncation and skipped sections.
- Rendered section format.
- Empty-pack behavior.
- Copy-safe `session.State` storage.
- Runner prompt injection when a pack exists.
- No runner prompt change when no pack exists.
- TUI context panel display.

The tests should use fakes for repo-card and snippet inputs. They should not import Milestone J scanner or repo-map internals.

## Acceptance Criteria

- `go test ./...` passes.
- `internal/contextpack` has deterministic unit tests.
- Context pack state is visible in the TUI.
- Runner provider requests include the rendered pack when one is stored.
- Existing agent behavior is unchanged when no context pack is stored.
- The branch avoids editing Milestone J-owned scanner/indexing files unless J has already merged.
