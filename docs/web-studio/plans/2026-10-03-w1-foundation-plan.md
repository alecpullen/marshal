# W1 · Foundation — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w1-foundation-design.md`](../specs/2026-10-03-w1-foundation-design.md)
**Parent design:** [`docs/web-studio/design.md`](../design.md) (§9, row W1)
**Execution:** run inline, task by task, with `marshal-executing-plans` (not `/sdd`).
**Base:** `origin/main` at `2ddc09e` ("Merge branch 'feat/request-inspection'").
This plan was authored on branch `web-studio/w1-foundation`, which is stacked on
`ccr-a651f5e7-6xpauj` (the design-doc PR, #23). That branch adds docs only,
so every code anchor below is as on `2ddc09e`.
**Plan slug:** `w1-foundation`. Commit each task as
`w1-foundation: task N — <task title>`.

## Goal

The web UI renders a session's transcript from the same Go view model as the
TUI, streamed as a snapshot plus patches. It does so inside a new Warm Sunset
shell with a Home inbox. Agents carry owner and origin end to end.

## Non-goals

- The session dock, the inspector (`i`), open (`o`) and drill-in (`f`): W2.
- Live wall, the New agent redesign, Review & ship: W2–W3. The old
  Dashboard stays at `#fleet`.
- Path-based routing. Hash routing stays.
- Flushing the stack while no turn is running: W2's idle ticker.
- Inbox budgets, warm pools, watches and automations.
- Accounts and per-user enforcement.

## Assumptions

- Go builds need `CGO_ENABLED=1` and a C toolchain (AGENTS.md).
- These tests already fail on `2ddc09e` in this container, which runs as
  root and has a proxied network:
  - `internal/acp` `TestValidateWorkingPathsRejectsEtc`
  - `internal/app/tui` `TestSDDPlanPickerFlagsUnreadableLedger`
  - `internal/app/tui/connect` `TestOAuthLoginShowsAuthorizationURL`
  - `internal/llm/provider/limits` `TestFetchReturnsData`
  - `internal/sddplans` `TestDiscoverSurfacesUnreadableLedger`

  A task's Verify step passes if the only failures are these. A new failure
  blocks the task.
- `web/ui` has its dependencies installed (`npm ci` in `web/ui`).
- The built SPA under `web/bridge/static` is committed (`assets.go` embeds
  it). Only Task 16 rebuilds it, so the UI diffs in tasks 9–15 stay
  reviewable.
- `web/` stays standard library only. `TestWebIsStdlibOnly` in
  `web/bridge/boundary_test.go` enforces this.

## Task order

| # | Task | Layer |
|---|---|---|
| 1 | Move `internal/app/tui/stack` to `internal/viewmodel` | Go |
| 2 | Move the node text helpers into `viewmodel` | Go |
| 3 | Wire projection | Go |
| 4 | ACP stack projector and `session/stack` | Go |
| 5 | Flush stack patches during and at the end of a turn | Go |
| 6 | Bridge: broadcast `stack_patch` without storing it | Go |
| 7 | Bridge: `GET /api/sessions/{id}/stack` | Go |
| 8 | Bridge: owner and origin on `AgentStatus` | Go |
| 9 | UI: fix the legacy `session/update` unwrap | TS |
| 10 | UI: Warm Sunset tokens and Geist fonts | CSS |
| 11 | UI: base components and glyph map | Svelte |
| 12 | UI: shell (rail, grouped sidebar, palette, Home route) | Svelte |
| 13 | UI: Home inbox | Svelte |
| 14 | UI: stack store | TS |
| 15 | UI: transcript renderers and density | Svelte |
| 16 | UI: browse keys, follow, now bar, Chat integration, rebuild static | Svelte |
| 17 | Docs: AGENTS.md tree and design §5.2 | Docs |

Tasks 1–3 are pure refactor and addition: the TUI's behaviour does not
change. Tasks 4–8 add the stream. Tasks 9–16 are the UI.

---

## Task 1: Move `internal/app/tui/stack` to `internal/viewmodel`

**Goal:** the transcript view model lives in `internal/viewmodel`, the TUI
imports it from there, and nothing else changes.

**Files:**
- `internal/app/tui/stack/{stack,stack_test,tasks,tasks_test}.go` → `internal/viewmodel/` (moved)
- every `internal/app/tui/*.go` that imports `marshal/internal/app/tui/stack`
  (today 25 files, including `viewport_render.go`, `browse.go`, `density.go`,
  `steps.go` and `tasks_render.go`)

**Steps:**

1. Move the package and rename it:

   ```bash
   git mv internal/app/tui/stack internal/viewmodel
   sed -i 's/^package stack$/package viewmodel/' internal/viewmodel/*.go
   grep -l '"marshal/internal/app/tui/stack"' internal/app/tui/*.go \
     | xargs sed -i -e 's#"marshal/internal/app/tui/stack"#"marshal/internal/viewmodel"#' \
                    -e 's/\bstack\.\([A-Z]\)/viewmodel.\1/g'
   gofmt -w internal/app/tui internal/viewmodel
   ```

   The second `sed` expression rewrites only `stack.` followed by an
   upper-case letter, which is an exported identifier. A local variable named
   `stack` would need a lower-case member to match, so it is never touched.
2. Update the package doc comment at the top of `internal/viewmodel/stack.go`:
   - change `// Package stack turns the session transcript into a tree: turn → step → row.`
     to `// Package viewmodel turns the session transcript into a tree: turn → step → row.`;
   - change the sentence ending "The tui package renders the tree; keeping the
     two apart lets the grouping be tested without a terminal." to "The TUI
     renders the tree and internal/acp projects it for web clients; keeping
     the grouping here lets it be tested without either."
3. Confirm nothing still references the old path:
   `grep -rn 'internal/app/tui/stack' --include=*.go .` prints nothing.

**Verify:**

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./internal/viewmodel/ ./internal/app/tui/...
gofmt -l internal/viewmodel internal/app/tui   # prints nothing
go vet ./internal/viewmodel/ ./internal/app/tui/
```

Expect the build to succeed and `internal/viewmodel` to report `ok`. In
`internal/app/tui/...`, the only failures are the two listed under
Assumptions. This was confirmed on a scratch checkout of `2ddc09e`.

---

## Task 2: Move the node text helpers into `viewmodel`

**Goal:** step headlines, tool targets, owners and tool display names are
computed by exported `viewmodel` functions. The TUI's unexported names
become one-line wrappers, so the TUI's call sites and tests are unchanged.

**Files:**
- `internal/viewmodel/describe.go` (new)
- `internal/app/tui/steps.go`, `toolgroup.go`, `symbolrow.go`, `toolnames.go`

**Steps:**

1. Create `internal/viewmodel/describe.go` with exactly this content. The
   bodies are the TUI functions' bodies, with only the names exported, and
   `dimSeparator` (a plain `" · "` at `internal/app/tui/model.go`) inlined:

   ```go
   package viewmodel

   import (
   	"encoding/json"
   	"fmt"
   	"path/filepath"
   	"strings"
   	"unicode"

   	"marshal/internal/app/session"
   	"marshal/internal/tools/registry"
   )

   // The functions in this file turn tree payloads into the short phrases every
   // client shows: a step's headline, a tool call's subject, an owner's name.
   // They are plain text with no styling, so the TUI and the web wire projection
   // say the same thing about the same node.

   // maxSubjectSymbols is how many symbol names a row names before collapsing
   // the rest into a "+N" count. Two is enough to see the shape of a change
   // without the row becoming a list.
   const maxSubjectSymbols = 2

   // StepOwner is the actor a step ran as: its label, else its role with the
   // sdd_ prefix dropped. Empty for the orchestrator.
   func StepOwner(st session.Step) string {
   	if st.Actor.Role == "" && st.Actor.Label == "" {
   		return ""
   	}
   	if st.Actor.Label != "" {
   		return st.Actor.Label
   	}
   	return strings.ReplaceAll(strings.TrimPrefix(st.Actor.Role, "sdd_"), "_", " ")
   }

   // StepRoleWord is the owner shortened to its role word ("reviewer #1" →
   // "reviewer"), the form that survives a narrow header.
   func StepRoleWord(st session.Step) string {
   	o := StepOwner(st)
   	if i := strings.IndexAny(o, " #"); i > 0 {
   		o = o[:i]
   	}
   	if st.Actor.Role != "" && strings.Contains(st.Actor.Role, "branch") {
   		return "branch reviewer"
   	}
   	return o
   }

   // StepHeadline picks the header text. A narrated step uses the first sentence
   // of its first narration; the remainder (and any later narration) becomes the
   // continuation. Without narration the headline is inferred from the tool rows
   // and rendered as such.
   func StepHeadline(si *StepInfo, rows []*Node) (head, rest string, inferred bool) {
   	var texts []string
   	for _, m := range si.Narration {
   		if t := strings.TrimSpace(m.Content); t != "" {
   			texts = append(texts, t)
   		}
   	}
   	if len(texts) > 0 {
   		h, r := FirstSentence(texts[0])
   		parts := []string{}
   		if r != "" {
   			parts = append(parts, r)
   		}
   		parts = append(parts, texts[1:]...)
   		return StripEmphasis(h), strings.Join(parts, "\n\n"), false
   	}
   	if h := InferHeadline(rows); h != "" {
   		return h, "", true
   	}
   	if len(si.Thinking) > 0 || si.LiveThinking != "" {
   		return "thinking", "", true
   	}
   	return "working", "", true
   }

   // FirstSentence splits s at the first ". ", "! ", "? " or newline that comes
   // after at least 8 runes. A sentence-final mark at the very end of s stays
   // with the head.
   func FirstSentence(s string) (head, rest string) {
   	s = strings.TrimSpace(s)
   	runes := []rune(s)
   	for i := 0; i < len(runes); i++ {
   		r := runes[i]
   		switch {
   		case r == '\n':
   			if i >= 8 {
   				return strings.TrimSpace(string(runes[:i])), strings.TrimSpace(string(runes[i+1:]))
   			}
   		case (r == '.' || r == '!' || r == '?') && i >= 7:
   			if i+1 < len(runes) && unicode.IsSpace(runes[i+1]) {
   				return string(runes[:i+1]), strings.TrimSpace(string(runes[i+1:]))
   			}
   		}
   	}
   	return s, ""
   }

   // StripEmphasis removes markdown emphasis and code markers from a headline,
   // which is rendered as plain text.
   func StripEmphasis(s string) string {
   	return strings.NewReplacer("**", "", "__", "", "`", "", "*", "", "~~", "").Replace(s)
   }

   // InferHeadline describes what a step did from its tool rows when the model
   // did not narrate: up to two clauses joined by " · ", then "…" if more
   // follow. Clients mark it as inferred because it is a guess at intent, not
   // the agent's own words.
   func InferHeadline(rows []*Node) string {
   	type clause struct {
   		kind  string
   		count int
   		text  string
   	}
   	var order []string
   	byKind := map[string]*clause{}
   	touch := func(kind string) *clause {
   		c, ok := byKind[kind]
   		if !ok {
   			c = &clause{kind: kind}
   			byKind[kind] = c
   			order = append(order, kind)
   		}
   		return c
   	}
   	visit := func(name, target string, n int) {
   		switch {
   		case name == "file.read":
   			touch("read").count += n
   		case IsSearchTool(name):
   			c := touch("search")
   			c.count += n
   			if c.text == "" && target != "" {
   				c.text = target
   			}
   		case name == "shell.run" || name == "test.run":
   			c := touch("ran:" + name)
   			c.count += n
   			if c.text == "" {
   				if f := strings.Fields(target); len(f) > 0 {
   					c.text = f[0]
   				}
   			}
   		case name == "file.write_patch" || name == "patch.apply" || strings.HasPrefix(name, "file.write"):
   			c := touch("edit")
   			c.count += n
   			if c.text == "" && target != "" {
   				c.text = filepath.Base(target)
   			}
   		case name == "agent.run":
   			touch("agents").count += n
   		default:
   			c := touch("other:" + name)
   			c.count += n
   			c.text = DisplayToolName(name)
   		}
   	}
   	for _, r := range rows {
   		switch {
   		case r.Kind == KindSubagent:
   			visit("agent.run", "", 1)
   		case r.Active != nil:
   			visit(r.Active.Name, strings.TrimPrefix(r.Active.Args, "$ "), 1)
   		default:
   			for _, ev := range r.Tools {
   				visit(ev.ToolName, ToolTarget(ev), 1)
   			}
   		}
   	}
   	var parts []string
   	for _, k := range order {
   		c := byKind[k]
   		switch {
   		case k == "read":
   			parts = append(parts, fmt.Sprintf("read %d %s", c.count, plural(c.count, "file", "files")))
   		case k == "search":
   			if c.text != "" {
   				parts = append(parts, fmt.Sprintf("searched %q", c.text))
   			} else {
   				parts = append(parts, "searched")
   			}
   		case strings.HasPrefix(k, "ran:"):
   			if c.text != "" {
   				parts = append(parts, "ran "+c.text+" …")
   			} else {
   				parts = append(parts, "ran a command")
   			}
   		case k == "edit":
   			if c.text != "" {
   				parts = append(parts, "edited "+c.text)
   			} else {
   				parts = append(parts, fmt.Sprintf("edited %d %s", c.count, plural(c.count, "file", "files")))
   			}
   		case k == "agents":
   			parts = append(parts, fmt.Sprintf("dispatched %d %s", c.count, plural(c.count, "agent", "agents")))
   		default:
   			parts = append(parts, c.text)
   		}
   	}
   	if len(parts) == 0 {
   		return ""
   	}
   	out := strings.Join(parts[:min(len(parts), 2)], " · ")
   	if len(parts) > 2 {
   		out += " …"
   	}
   	return out
   }

   // IsSearchTool reports whether a tool's subject is a query.
   func IsSearchTool(name string) bool {
   	return strings.HasPrefix(name, "repo.search") || strings.HasPrefix(name, "codebase.search") ||
   		strings.HasPrefix(name, "symbols.") || name == "web.search" || strings.HasPrefix(name, "search.")
   }

   func plural(n int, one, many string) string {
   	if n == 1 {
   		return one
   	}
   	return many
   }

   // ToolTarget returns the short human-facing subject of a tool call — the
   // path it read, the command it ran, the query it searched for.
   func ToolTarget(event registry.AuditEvent) string {
   	if s := SymbolSubject(event); s != "" {
   		return s
   	}
   	if len(event.FilesChanged) > 0 {
   		return event.FilesChanged[0]
   	}
   	if len(event.Args) == 0 {
   		return ""
   	}
   	var args map[string]any
   	if err := json.Unmarshal(event.Args, &args); err != nil {
   		return ""
   	}
   	for _, key := range []string{"path", "command", "query", "name"} {
   		if s, ok := args[key].(string); ok && s != "" {
   			return s
   		}
   	}
   	return ""
   }

   // SymbolSubject renders "path › A(), B() +2" for an event carrying symbol
   // attribution, grouped by file in first-seen order. It returns "" when the
   // event carries no symbols, which is the common case on languages without
   // a tree-sitter grammar.
   func SymbolSubject(event registry.AuditEvent) string {
   	if len(event.Symbols) == 0 {
   		return ""
   	}
   	byFile := map[string][]string{}
   	var order []string
   	for _, s := range event.Symbols {
   		if _, seen := byFile[s.File]; !seen {
   			order = append(order, s.File)
   		}
   		byFile[s.File] = append(byFile[s.File], SymbolLabel(s))
   	}
   	parts := make([]string, 0, len(order))
   	for _, f := range order {
   		names := byFile[f]
   		extra := 0
   		if len(names) > maxSubjectSymbols {
   			extra = len(names) - maxSubjectSymbols
   			names = names[:maxSubjectSymbols]
   		}
   		p := f + " › " + strings.Join(names, ", ")
   		if extra > 0 {
   			p += fmt.Sprintf(" +%d", extra)
   		}
   		parts = append(parts, p)
   	}
   	return strings.Join(parts, " · ")
   }

   // SymbolLabel renders one symbol: callables get "()" so a function reads
   // differently from a type at a glance.
   func SymbolLabel(s registry.SymbolRef) string {
   	if s.Kind == "function" || s.Kind == "method" {
   		return s.Name + "()"
   	}
   	return s.Name
   }

   var toolDisplayNames = map[string]string{
   	"file.read":        "Read file",
   	"file.write_patch": "Edit file",
   	"patch.apply":      "Apply patch",
   	"shell.run":        "Run command",
   	"test.run":         "Run tests",
   	"repo.search":      "Search repo",
   	"codebase.search":  "Codebase search",
   	"json.query":       "Query JSON",
   	"csv.inspect":      "Inspect CSV",
   	"web.fetch":        "Fetch page",
   	"web.search":       "Search web",
   	"git.status":       "Git status",
   	"git.diff":         "Git diff",
   	"symbols.find":     "Find symbols",
   	"todos":            "Update todos",
   	"mode.request":     "Switch mode",
   	"question.ask":     "Ask question",
   	"ask_user":         "Ask user",
   	"agent.run":        "Run subagent",
   	// agent.await is multipurpose (subagents, background jobs, watches), so
   	// the transcript row describes the action, not the target class.
   	"agent.await": "Waiting…",
   }

   // DisplayToolName returns a human-readable label for a tool. Unknown tools
   // get a title-cased, dot-to-space transformation (only the first segment
   // is capitalized).
   func DisplayToolName(name string) string {
   	if label, ok := toolDisplayNames[name]; ok {
   		return label
   	}
   	parts := strings.Split(name, ".")
   	if len(parts) > 0 && len(parts[0]) > 0 {
   		parts[0] = strings.ToUpper(parts[0][:1]) + parts[0][1:]
   	}
   	return strings.Join(parts, " ")
   }
   ```

2. In `internal/app/tui/steps.go`, replace each of these functions,
   doc comment kept and body replaced, with the one-line delegation shown.
   The anchors are the function declarations: `stepOwner`, `stepRoleWord`,
   `stepHeadline`, `firstSentence`, `stripEmphasis`, `inferHeadline` and
   `isSearchTool`.

   ```go
   func stepOwner(st session.Step) string { return viewmodel.StepOwner(st) }
   func stepRoleWord(st session.Step) string { return viewmodel.StepRoleWord(st) }
   func stepHeadline(si *viewmodel.StepInfo, rows []*viewmodel.Node) (head, rest string, inferred bool) {
   	return viewmodel.StepHeadline(si, rows)
   }
   func firstSentence(s string) (head, rest string) { return viewmodel.FirstSentence(s) }
   func stripEmphasis(s string) string { return viewmodel.StripEmphasis(s) }
   func inferHeadline(rows []*viewmodel.Node) string { return viewmodel.InferHeadline(rows) }
   func isSearchTool(name string) bool { return viewmodel.IsSearchTool(name) }
   ```

   Keep `steps.go`'s own `plural`, which other TUI files use. Drop the now
   unused `"path/filepath"` and `"unicode"` imports.
3. In `internal/app/tui/toolgroup.go`, do the same for `toolTarget`:
   - replace it with
     `func toolTarget(event registry.AuditEvent) string { return viewmodel.ToolTarget(event) }`;
   - drop the `"encoding/json"` import and add `"marshal/internal/viewmodel"`.
4. In `internal/app/tui/symbolrow.go`:
   - replace `symbolSubject` with
     `func symbolSubject(event registry.AuditEvent) string { return viewmodel.SymbolSubject(event) }`;
   - replace `symbolLabel` with
     `func symbolLabel(s registry.SymbolRef) string { return viewmodel.SymbolLabel(s) }`;
   - delete the `maxSubjectSymbols` constant and its comment. Nothing else in
     `internal/app/tui` uses it.
5. In `internal/app/tui/toolnames.go`:
   - delete the `toolDisplayNames` map;
   - replace `DisplayToolName` with
     `func DisplayToolName(name string) string { return viewmodel.DisplayToolName(name) }`;
   - add the `"marshal/internal/viewmodel"` import. `toolPluralNames`,
     `toolCategoryGlyphs` and `toolCategoryGlyph` stay: they are styling.
6. Run `gofmt -w internal/app/tui internal/viewmodel`.

**Verify:**

```bash
CGO_ENABLED=1 go build ./...
go vet ./internal/viewmodel/ ./internal/app/tui/
CGO_ENABLED=1 go test ./internal/viewmodel/ ./internal/app/tui/...
gofmt -l internal/viewmodel internal/app/tui   # prints nothing
```

Expect the same results as Task 1. The TUI's headline tests
(`steps_test.go`, `symbolrow_test.go`, `toolgroup_test.go`) pass unchanged
through the wrappers. This was confirmed on a scratch checkout.

---

## Task 3: Wire projection

**Goal:** `viewmodel.Project` turns a built tree into the JSON shape in spec
§4, with the text cap of spec §4.3.

**Files:**
- `internal/viewmodel/wire.go` (new)
- `internal/viewmodel/wire_test.go` (new)

**Steps:**

1. Create `internal/viewmodel/wire.go`:

   ```go
   package viewmodel

   import (
   	"time"
   	"unicode/utf8"

   	"marshal/internal/app/session"
   	"marshal/internal/tools/registry"
   )

   // The wire projection is the tree as a client outside the process sees it.
   // Clients cannot import this package (web/ is standard library only), so the
   // projection is plain JSON: a flat node list in which each node names its
   // parent and its children by ID. Headlines, targets and owners are worked
   // out here so every client says the same thing as the TUI.

   // WireTextCap bounds every large text field (tool output, arguments, job
   // output, run-event bodies) in the projection. A capped field keeps its
   // first WireTextCap bytes and sets the node's Truncated flag. Full payloads
   // are a separate, on-demand request.
   const WireTextCap = 4096

   // WireTree is a whole stack: the turn IDs in order, and every node with
   // parents listed before their children.
   type WireTree struct {
   	Roots []string   `json:"roots"`
   	Nodes []WireNode `json:"nodes"`
   }

   // WireNode is one node. Exactly one payload pointer is set, chosen by Kind;
   // turn nodes carry none.
   type WireNode struct {
   	ID       string   `json:"id"`
   	Kind     string   `json:"kind"`
   	Parent   string   `json:"parent,omitempty"`
   	Children []string `json:"children,omitempty"`
   	Live     bool     `json:"live,omitempty"`

   	Step     *WireStep     `json:"step,omitempty"`
   	Tool     *WireTool     `json:"tool,omitempty"`
   	Message  *WireMessage  `json:"message,omitempty"`
   	Thinking *WireThinking `json:"thinking,omitempty"`
   	Subagent *WireSubagent `json:"subagent,omitempty"`
   	RunEvent *WireRunEvent `json:"runEvent,omitempty"`
   	JobExit  *WireJobExit  `json:"jobExit,omitempty"`
   	Task     *WireTask     `json:"task,omitempty"`
   	Receipt  *WireReceipt  `json:"receipt,omitempty"`
   }

   // WireStep is a step's header and reasoning. Times are Unix milliseconds;
   // zero means unset.
   type WireStep struct {
   	Headline     string        `json:"headline"`
   	Rest         string        `json:"rest,omitempty"`
   	Inferred     bool          `json:"inferred,omitempty"`
   	Owner        string        `json:"owner,omitempty"`
   	RoleWord     string        `json:"roleWord,omitempty"`
   	Role         string        `json:"role,omitempty"`
   	Model        string        `json:"model,omitempty"`
   	Provider     string        `json:"provider,omitempty"`
   	TodoID       string        `json:"todoId,omitempty"`
   	Heuristic    bool          `json:"heuristic,omitempty"`
   	StartedAt    int64         `json:"startedAt,omitempty"`
   	EndedAt      int64         `json:"endedAt,omitempty"`
   	Thoughts     []WireThought `json:"thoughts,omitempty"`
   	LiveThinking string        `json:"liveThinking,omitempty"`
   }

   // WireThought is one finished reasoning block inside a step.
   type WireThought struct {
   	Text       string `json:"text"`
   	DurationMs int64  `json:"durationMs,omitempty"`
   }

   // WireTool is a tool row: one call, a merged run of same-tool calls, or a
   // call still running.
   type WireTool struct {
   	Name    string       `json:"name"`
   	Display string       `json:"display"`
   	Calls   []WireCall   `json:"calls,omitempty"`
   	Running *WireRunning `json:"running,omitempty"`
   }

   // WireCall is one finished call.
   type WireCall struct {
   	CallID     string   `json:"callId,omitempty"`
   	Target     string   `json:"target,omitempty"`
   	Args       string   `json:"args,omitempty"`
   	Summary    string   `json:"summary,omitempty"`
   	Output     string   `json:"output,omitempty"`
   	Error      string   `json:"error,omitempty"`
   	ExitCode   *int     `json:"exitCode,omitempty"`
   	Failed     bool     `json:"failed,omitempty"`
   	Approval   string   `json:"approval,omitempty"`
   	Risk       string   `json:"risk,omitempty"`
   	Files      []string `json:"files,omitempty"`
   	Role       string   `json:"role,omitempty"`
   	Notice     string   `json:"notice,omitempty"`
   	DurationMs int64    `json:"durationMs,omitempty"`
   	At         int64    `json:"at,omitempty"`
   	Truncated  bool     `json:"truncated,omitempty"`
   }

   // WireRunning is a call that has not finished.
   type WireRunning struct {
   	Target    string `json:"target,omitempty"`
   	Output    string `json:"output,omitempty"`
   	StartedAt int64  `json:"startedAt,omitempty"`
   	Truncated bool   `json:"truncated,omitempty"`
   }

   // WireMessage is a user prompt, an assistant reply, or any other message the
   // transcript shows on its own row.
   type WireMessage struct {
   	Role          string `json:"role"`
   	ContentType   string `json:"contentType,omitempty"`
   	Content       string `json:"content"`
   	Final         bool   `json:"final,omitempty"`
   	Usage         string `json:"usage,omitempty"`
   	Salvaged      bool   `json:"salvaged,omitempty"`
   	SalvageReason string `json:"salvageReason,omitempty"`
   	ThinkMs       int64  `json:"thinkMs,omitempty"`
   	At            int64  `json:"at,omitempty"`
   }

   // WireThinking is a reasoning row outside any step, finished or live.
   type WireThinking struct {
   	Text       string `json:"text"`
   	DurationMs int64  `json:"durationMs,omitempty"`
   }

   // WireSubagent is a subagent card.
   type WireSubagent struct {
   	Label       string `json:"label"`
   	Status      string `json:"status"`
   	Role        string `json:"role,omitempty"`
   	Model       string `json:"model,omitempty"`
   	Provider    string `json:"provider,omitempty"`
   	ToolCalls   int    `json:"toolCalls,omitempty"`
   	CurrentTool string `json:"currentTool,omitempty"`
   	Tokens      int    `json:"tokens,omitempty"`
   	Summary     string `json:"summary,omitempty"`
   	Error       string `json:"error,omitempty"`
   	Salvaged    string `json:"salvaged,omitempty"`
   	StartedAt   int64  `json:"startedAt,omitempty"`
   	EndedAt     int64  `json:"endedAt,omitempty"`
   	Truncated   bool   `json:"truncated,omitempty"`
   }

   // WireRunEvent is one plan-run event (verify failure, review, commit, …).
   type WireRunEvent struct {
   	Kind      string `json:"kind"`
   	TaskN     int    `json:"taskN,omitempty"`
   	Title     string `json:"title,omitempty"`
   	Detail    string `json:"detail,omitempty"`
   	Body      string `json:"body,omitempty"`
   	Severity  string `json:"severity,omitempty"`
   	At        int64  `json:"at,omitempty"`
   	Truncated bool   `json:"truncated,omitempty"`
   }

   // WireJobExit is a background job's exit record.
   type WireJobExit struct {
   	JobID      string `json:"jobId"`
   	Command    string `json:"command"`
   	ExitCode   int    `json:"exitCode"`
   	DurationMs int64  `json:"durationMs,omitempty"`
   	Output     string `json:"output,omitempty"`
   	At         int64  `json:"at,omitempty"`
   	Truncated  bool   `json:"truncated,omitempty"`
   }

   // WireTask is a task header.
   type WireTask struct {
   	TodoID            string `json:"todoId"`
   	Content           string `json:"content"`
   	Status            string `json:"status"`
   	Index             int    `json:"index,omitempty"`
   	Total             int    `json:"total,omitempty"`
   	Dropped           bool   `json:"dropped,omitempty"`
   	Steps             int    `json:"steps"`
   	WorkMs            int64  `json:"workMs,omitempty"`
   	Tools             int    `json:"tools"`
   	Edits             int    `json:"edits"`
   	UnresolvedFailure bool   `json:"unresolvedFailure,omitempty"`
   	FirstNarration    string `json:"firstNarration,omitempty"`
   	StartedAt         int64  `json:"startedAt,omitempty"`
   	CompletedAt       int64  `json:"completedAt,omitempty"`
   }

   // WireReceipt is the summary line closing a finished turn.
   type WireReceipt struct {
   	DurationMs int64  `json:"durationMs"`
   	Tasks      int    `json:"tasks"`
   	Steps      int    `json:"steps"`
   	Tools      int    `json:"tools"`
   	Files      int    `json:"files"`
   	Usage      string `json:"usage,omitempty"`
   	Salvaged   bool   `json:"salvaged,omitempty"`
   }

   var kindNames = [...]string{
   	KindTurn:        "turn",
   	KindStep:        "step",
   	KindTool:        "tool",
   	KindMessage:     "message",
   	KindFinal:       "final",
   	KindSubagent:    "subagent",
   	KindRunEvent:    "runEvent",
   	KindJobExit:     "jobExit",
   	KindThinking:    "thinking",
   	KindPassthrough: "passthrough",
   	KindTask:        "task",
   	KindReceipt:     "receipt",
   }

   // String is the kind's wire name.
   func (k Kind) String() string {
   	if int(k) >= 0 && int(k) < len(kindNames) {
   		return kindNames[k]
   	}
   	return "unknown"
   }

   // Project flattens a built tree for the wire.
   func Project(turns []*Node) WireTree {
   	t := WireTree{Roots: make([]string, 0, len(turns))}
   	var walk func(n *Node, parent string)
   	walk = func(n *Node, parent string) {
   		w := wireNode(n)
   		w.Parent = parent
   		t.Nodes = append(t.Nodes, w)
   		for _, c := range n.Children {
   			walk(c, n.ID.Key)
   		}
   	}
   	for _, n := range turns {
   		t.Roots = append(t.Roots, n.ID.Key)
   		walk(n, "")
   	}
   	return t
   }

   func wireNode(n *Node) WireNode {
   	w := WireNode{ID: n.ID.Key, Kind: n.Kind.String(), Live: n.Live}
   	for _, c := range n.Children {
   		w.Children = append(w.Children, c.ID.Key)
   	}
   	switch {
   	case n.Task != nil:
   		w.Task = wireTask(n.Task)
   	case n.Receipt != nil:
   		r := n.Receipt
   		w.Receipt = &WireReceipt{DurationMs: r.Duration.Milliseconds(), Tasks: r.Tasks, Steps: r.Steps,
   			Tools: r.Tools, Files: r.Files, Usage: r.Usage, Salvaged: r.Salvaged}
   	case n.Kind == KindStep && n.Step != nil:
   		w.Step = wireStep(n)
   	case n.Kind == KindThinking && n.Step != nil:
   		// The live reasoning region when no step could hold it.
   		w.Thinking = &WireThinking{Text: n.Step.LiveThinking}
   	case n.Active != nil:
   		w.Tool = wireActive(n.Active)
   	case len(n.Tools) > 0:
   		w.Tool = wireTools(n.Tools)
   	case n.Item != nil:
   		wireItem(&w, n.Item)
   	}
   	return w
   }

   func wireStep(n *Node) *WireStep {
   	si := n.Step
   	head, rest, inferred := StepHeadline(si, n.Children)
   	st := si.Step
   	ws := &WireStep{
   		Headline: head, Rest: rest, Inferred: inferred,
   		Owner: StepOwner(st), RoleWord: StepRoleWord(st), Role: st.Actor.Role,
   		Model: st.Actor.Model, Provider: st.Actor.Provider,
   		TodoID: si.TodoID, Heuristic: si.Heuristic,
   		StartedAt: ms(st.StartedAt), EndedAt: ms(st.EndedAt),
   		LiveThinking: si.LiveThinking,
   	}
   	for _, t := range si.Thinking {
   		ws.Thoughts = append(ws.Thoughts, WireThought{Text: t.Text, DurationMs: t.Duration.Milliseconds()})
   	}
   	return ws
   }

   func wireTools(evs []registry.AuditEvent) *WireTool {
   	wt := &WireTool{Name: evs[0].ToolName, Display: DisplayToolName(evs[0].ToolName)}
   	for _, ev := range evs {
   		c := WireCall{
   			CallID: ev.ToolCallID, Target: ToolTarget(ev), Summary: ev.ResultSummary,
   			Error: ev.Error, ExitCode: ev.CommandExitCode, Failed: EventFailed(ev),
   			Approval: string(ev.Approval), Risk: string(ev.Risk), Files: ev.FilesChanged,
   			Role: ev.AgentRole, DurationMs: ev.Duration.Milliseconds(), At: ms(ev.Timestamp),
   		}
   		var cut1, cut2 bool
   		c.Args, cut1 = capText(string(ev.Args))
   		c.Output, cut2 = capText(ev.ResultContent)
   		c.Truncated = cut1 || cut2
   		if ev.Notice != nil {
   			c.Notice = ev.Notice.Text
   		}
   		wt.Calls = append(wt.Calls, c)
   	}
   	return wt
   }

   func wireActive(a *session.ActiveToolCall) *WireTool {
   	target := a.Path
   	if target == "" {
   		target = a.Args
   	}
   	r := &WireRunning{Target: target, StartedAt: ms(a.StartedAt)}
   	r.Output, r.Truncated = capTail(a.Output)
   	return &WireTool{Name: a.Name, Display: DisplayToolName(a.Name), Running: r}
   }

   func wireItem(w *WireNode, it *session.TranscriptItem) {
   	switch {
   	case it.Message != nil:
   		m := it.Message
   		w.Message = &WireMessage{
   			Role: string(m.Role), ContentType: string(m.ContentType), Content: m.Content,
   			Final: m.Final, Usage: m.Usage, Salvaged: m.Salvaged, SalvageReason: m.SalvageReason,
   			ThinkMs: m.ThinkDuration.Milliseconds(), At: ms(it.Timestamp),
   		}
   	case it.Thinking != nil:
   		w.Thinking = &WireThinking{Text: it.Thinking.Text, DurationMs: it.Thinking.Duration.Milliseconds()}
   	case it.Subagent != nil:
   		v := it.Subagent
   		ws := &WireSubagent{
   			Label: v.Label, Status: subagentStatus(v.Status), Role: string(v.Role), Model: v.Model,
   			Provider: v.Provider, ToolCalls: v.ToolCalls, CurrentTool: v.CurrentTool, Tokens: v.TokensUsed,
   			Error: v.Error, Salvaged: v.SalvagedReason, StartedAt: ms(v.StartedAt), EndedAt: ms(v.EndedAt),
   		}
   		ws.Summary, ws.Truncated = capText(v.Summary)
   		w.Subagent = ws
   	case it.RunEvent != nil:
   		e := it.RunEvent
   		wr := &WireRunEvent{Kind: runEventKind(e.Kind), TaskN: e.TaskN, Title: e.Title, Detail: e.Detail,
   			Severity: e.Severity, At: ms(e.At)}
   		wr.Body, wr.Truncated = capText(e.Body)
   		w.RunEvent = wr
   	case it.JobExit != nil:
   		j := it.JobExit
   		wj := &WireJobExit{JobID: j.ID, Command: j.Command, ExitCode: j.ExitCode,
   			DurationMs: j.Duration.Milliseconds(), At: ms(j.At)}
   		wj.Output, wj.Truncated = capTail(j.Output)
   		w.JobExit = wj
   	}
   }

   func wireTask(t *TaskInfo) *WireTask {
   	return &WireTask{
   		TodoID: t.TodoID, Content: t.Content, Status: t.Status, Index: t.Index, Total: t.Total,
   		Dropped: t.Dropped, Steps: t.Steps, WorkMs: t.Work.Milliseconds(), Tools: t.Tools, Edits: t.Edits,
   		UnresolvedFailure: t.UnresolvedFailure, FirstNarration: t.FirstNarration,
   		StartedAt: ms(t.StartedAt), CompletedAt: ms(t.CompletedAt),
   	}
   }

   func subagentStatus(s session.SubagentStatus) string {
   	switch s {
   	case session.SubagentRunning:
   		return "running"
   	case session.SubagentDone:
   		return "done"
   	case session.SubagentFailed:
   		return "failed"
   	}
   	return "unknown"
   }

   var runEventKinds = [...]string{
   	session.RunEventVerifyFailed: "verifyFailed",
   	session.RunEventGateSkipped:  "gateSkipped",
   	session.RunEventReview:       "review",
   	session.RunEventCommit:       "commit",
   	session.RunEventRetry:        "retry",
   	session.RunEventConcern:      "concern",
   	session.RunEventTaskDone:     "taskDone",
   }

   func runEventKind(k session.RunEventKind) string {
   	if int(k) >= 0 && int(k) < len(runEventKinds) {
   		return runEventKinds[k]
   	}
   	return "unknown"
   }

   // ms is t as Unix milliseconds, 0 for the zero time.
   func ms(t time.Time) int64 {
   	if t.IsZero() {
   		return 0
   	}
   	return t.UnixMilli()
   }

   // capText keeps the first WireTextCap bytes of s, cut on a rune boundary.
   func capText(s string) (string, bool) {
   	if len(s) <= WireTextCap {
   		return s, false
   	}
   	cut := WireTextCap
   	for cut > 0 && !utf8.RuneStart(s[cut]) {
   		cut--
   	}
   	return s[:cut], true
   }

   // capTail keeps the last WireTextCap bytes of s, for output whose end is the
   // part worth reading (a running command, a job's exit).
   func capTail(s string) (string, bool) {
   	if len(s) <= WireTextCap {
   		return s, false
   	}
   	cut := len(s) - WireTextCap
   	for cut < len(s) && !utf8.RuneStart(s[cut]) {
   		cut++
   	}
   	return s[cut:], true
   }
   ```

2. Create `internal/viewmodel/wire_test.go`. It reuses the fixtures in
   `stack_test.go` (`userMsg`, `narration`, `final`, `audit`, `at`):

   ```go
   package viewmodel

   import (
   	"encoding/json"
   	"strings"
   	"testing"

   	"marshal/internal/app/session"
   	"marshal/internal/tools/registry"
   )

   func wireByID(t *testing.T, tree WireTree) map[string]WireNode {
   	t.Helper()
   	out := map[string]WireNode{}
   	for _, n := range tree.Nodes {
   		if _, dup := out[n.ID]; dup {
   			t.Fatalf("duplicate wire id %q", n.ID)
   		}
   		out[n.ID] = n
   	}
   	return out
   }

   func TestProjectLinksParentsAndChildren(t *testing.T) {
   	tree := Project(Build(Snapshot{
   		Items: []session.TranscriptItem{
   			userMsg(1, 0),
   			narration(2, 7, 1, "Reading the config loader. Then the tests."),
   			audit("file.read", 7, "c1", 2),
   			final(3, 5),
   		},
   		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(4)}},
   		Now:   at(6),
   	}))
   	if len(tree.Roots) != 1 || tree.Roots[0] != "turn:1" {
   		t.Fatalf("roots = %v, want [turn:1]", tree.Roots)
   	}
   	byID := wireByID(t, tree)
   	seen := map[string]bool{}
   	for i, n := range tree.Nodes {
   		if n.Parent != "" && !seen[n.Parent] {
   			t.Fatalf("node %d (%s) listed before its parent %s", i, n.ID, n.Parent)
   		}
   		seen[n.ID] = true
   		for _, c := range n.Children {
   			if byID[c].Parent != n.ID {
   				t.Fatalf("child %s of %s has parent %q", c, n.ID, byID[c].Parent)
   			}
   		}
   	}
   	st := byID["step:7"]
   	if st.Kind != "step" || st.Step == nil {
   		t.Fatalf("step:7 = %+v", st)
   	}
   	if st.Step.Headline != "Reading the config loader." || st.Step.Rest != "Then the tests." || st.Step.Inferred {
   		t.Fatalf("headline = %q rest = %q inferred = %v", st.Step.Headline, st.Step.Rest, st.Step.Inferred)
   	}
   	tool := byID["tool:7:c1"]
   	if tool.Kind != "tool" || tool.Tool == nil || tool.Tool.Display != "Read file" || len(tool.Tool.Calls) != 1 {
   		t.Fatalf("tool:7:c1 = %+v", tool)
   	}
   	if fin := byID["msg:3"]; fin.Kind != "final" || fin.Message == nil || fin.Message.Content != "done" {
   		t.Fatalf("msg:3 = %+v", fin)
   	}
   	if rc, ok := byID["receipt:turn:1"]; !ok || rc.Receipt == nil {
   		t.Fatalf("finished turn has no receipt: %+v", rc)
   	}
   }

   func TestProjectInfersHeadlineWithoutNarration(t *testing.T) {
   	tree := Project(Build(Snapshot{
   		Items: []session.TranscriptItem{userMsg(1, 0), audit("file.read", 7, "c1", 2), audit("file.read", 7, "c2", 3)},
   		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(4)}},
   		Now:   at(6),
   	}))
   	st := wireByID(t, tree)["step:7"].Step
   	if st == nil || st.Headline != "read 2 files" || !st.Inferred {
   		t.Fatalf("step = %+v, want inferred \"read 2 files\"", st)
   	}
   }

   func TestProjectRunningCallIsLive(t *testing.T) {
   	tree := Project(Build(Snapshot{
   		Items:       []session.TranscriptItem{userMsg(1, 0)},
   		Steps:       []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1)}},
   		ActiveTools: []session.ActiveToolCall{{Name: "shell.run", Args: "$ go test ./...", StartedAt: at(2), StepID: 7, ToolCallID: "c9"}},
   		Busy:        true,
   		Now:         at(3),
   	}))
   	n := wireByID(t, tree)["tool:7:c9"]
   	if !n.Live || n.Tool == nil || n.Tool.Running == nil || n.Tool.Running.Target != "$ go test ./..." {
   		t.Fatalf("running call = %+v", n)
   	}
   }

   func TestProjectCapsLargeText(t *testing.T) {
   	big := strings.Repeat("é", WireTextCap) // 2 bytes per rune
   	ev := registry.AuditEvent{Timestamp: at(2), ToolName: "shell.run", StepID: 7, ToolCallID: "c1", ResultContent: big}
   	tree := Project(Build(Snapshot{
   		Items: []session.TranscriptItem{userMsg(1, 0), {Timestamp: at(2), Kind: session.KindAudit, Audit: &ev}},
   		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(3)}},
   		Now:   at(4),
   	}))
   	c := wireByID(t, tree)["tool:7:c1"].Tool.Calls[0]
   	if !c.Truncated || len(c.Output) > WireTextCap || !strings.HasPrefix(big, c.Output) {
   		t.Fatalf("output len %d truncated %v", len(c.Output), c.Truncated)
   	}
   	if _, err := json.Marshal(tree); err != nil {
   		t.Fatal(err)
   	}
   }

   func TestKindStringCoversEveryKind(t *testing.T) {
   	for k := KindTurn; k <= KindReceipt; k++ {
   		if s := k.String(); s == "" || s == "unknown" {
   			t.Fatalf("Kind(%d) has no wire name", k)
   		}
   	}
   }
   ```

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/viewmodel/ -run 'TestProject|TestKindString' -v
go vet ./internal/viewmodel/
gofmt -l internal/viewmodel   # prints nothing
```

Expect all five tests to pass. This was confirmed on a scratch checkout.

---

## Task 4: ACP stack projector and `session/stack`

**Goal:**
- `session/stack` answers with a snapshot (spec §5.1);
- the per-session projector can compute a `stack_patch` (spec §5.2);
- `initialize` advertises `stackView`.

Nothing flushes during turns yet; that is Task 5.

**Files:**
- `internal/acp/stack.go` (new)
- `internal/acp/stack_test.go` (new)
- `internal/acp/turn.go` (`TurnManager`, `NewTurnManager`)
- `internal/acp/host.go` (capabilities, handler registration)

**Steps:**

1. In `internal/acp/stack.go`, define the following.
   - `StackParams{SessionID string \`json:"sessionId"\`}`.
   - `StackSnapshot{SessionID string; Rev uint64; Roots []string; Nodes []viewmodel.WireNode}`,
     with JSON names `sessionId`, `rev`, `roots`, `nodes`. `Roots` and
     `Nodes` must encode as `[]`, never `null`, so initialise them as empty
     slices.
   - `stackPatch{Kind string; Rev, BaseRev uint64; Roots []string; Upsert []viewmodel.WireNode; Remove []string}`,
     with JSON names `kind`, `rev`, `baseRev`, `roots`, `upsert,omitempty`
     and `remove,omitempty`. `Kind` is always `"stack_patch"`.
   - `stackProjector`, a struct holding:
     - `mu sync.Mutex`;
     - `active` (true after the first snapshot);
     - `dirty`;
     - `rev uint64`;
     - `sent map[string][]byte` (node ID → `json.Marshal` of the node as
       last sent);
     - `sentOrder []string` (the IDs in the order last sent).
   - `func (p *stackProjector) diff(tree viewmodel.WireTree) (stackPatch, bool)`.
     The caller holds `p.mu`.
     - Encode each node in `tree.Nodes`, and collect into `Upsert` every
       node whose encoding differs from `sent[id]` or is new.
     - Collect into `Remove`, in `sentOrder` order, every ID in `sent`
       that is absent from the tree.
     - If both lists are empty, return `false` and leave the state alone.
     - Otherwise set `BaseRev = p.rev`, increment `p.rev`, set `Rev` to
       the new value, set `Roots = tree.Roots`, replace `sent` and
       `sentOrder`, and return `true`.
   - `func stackSnapshotOf(st *session.State, busy bool, now time.Time) viewmodel.Snapshot`,
     which builds a `viewmodel.Snapshot` from:
     - `st.Transcript()`, `st.Steps()`, `st.Todos()`, `st.ActiveToolCalls()`,
       `st.InProgress()` and `st.HasRunningSubagent()` (the TUI's
       `refreshViewport` in `internal/app/tui/viewport_render.go` reads
       the same accessors);
     - `Busy: busy || len(active) > 0 || len(inProgress.Reasoning) > 0`;
     - `Drilled: false` and `Now: now`.
   - On `TurnManager`, add:
     - `stackFor(sessionID string) *stackProjector`, which creates the
       projector on first use;
     - `markStackDirty(sessionID string)`, a no-op if there is no projector;
     - `flushStack(sessionID string, st *session.State, busy bool)`.
       It locks the projector and returns if `!active`. Otherwise it runs
       `diff(viewmodel.Project(viewmodel.Build(stackSnapshotOf(st, busy, time.Now()))))`,
       clears `dirty`, and on `true` calls
       `m.notify("session/update", SessionUpdateParams{SessionID: sessionID, Update: patch})`.
       A notify error is logged with `slog.Default().Warn`, like the
       telemetry failure in `finishTurn`, and is not returned.
   - `func (m *TurnManager) Stack(ctx context.Context, params json.RawMessage) (any, error)`.
     - Parse the params. A missing `sessionId` returns
       `fmt.Errorf("acp: session/stack requires sessionId")`, mirroring
       `SetMode`.
     - Run `rt, ok := m.lookup(p.SessionID)`. If not found, or if
       `rt.State == nil`, delete any projector for the session and return
       `serverErrorf("unknown session: %s", p.SessionID)`, exactly as
       `SetMode` does.
     - Otherwise lock the projector. If not `active`, set `active = true`
       and seed `sent`/`sentOrder`/`rev = 1` from the current tree without
       notifying. If active, call `diff` and notify a resulting patch
       (flush-then-answer, spec §5.1).
     - Return a `StackSnapshot` built from the encodings now in `sent`.
       Unmarshal each `sent[id]` in `sentOrder` back into a `WireNode`,
       or keep the tree's nodes from this call, which are identical.
     - Busy is `m.HasActiveTurn(p.SessionID)` (`turn.go:442`).
2. In `internal/acp/turn.go`, add two fields to `TurnManager` after
   `baseRefs map[string]string`:
   - `stacksMu sync.Mutex`
   - `stacks map[string]*stackProjector`

   Add a doc comment pointing at spec §5. In `NewTurnManager`'s literal,
   add `stacks: map[string]*stackProjector{},` after `baseRefs: map[string]string{},`.
3. In `internal/acp/host.go`:
   - in the `initialize` handler's `"sessionCapabilities"` map, add
     `"stackView": map[string]any{},` next to `"sessionTelemetry"`;
   - register the handler after `srv.Handle("session/steer", turns.Steer)`:
     `srv.Handle("session/stack", turns.Stack)`.
4. In `internal/acp/stack_test.go`, add the following tests. Build the
   manager with the helper `newResumeTestManager(t, state, goals, rt)` from
   `internal/acp/turn_test.go:3126`, or a smaller local equivalent that
   sets `Lookup` and a recording `Notify`, and seed `session.State` through
   its public methods (`AddMessage` and similar).
   - `TestStackSnapshotForKnownSession`: returns `rev` 1 and a `turn:` root;
     no notification is sent. Seed the state with `AddMessage`
     (`session/messages.go:425`).
   - `TestStackUnknownSession`: returns an error whose message contains
     `unknown session`.
   - `TestStackFlushEmitsPatchAfterChange`: snapshot, add a message, call
     `flushStack`. Exactly one `session/update` is sent, its update has
     kind `stack_patch` with `baseRev` 1 and `rev` 2, and its `upsert`
     holds the new message node and its turn.
   - `TestStackFlushWithoutChangeIsSilent`: two flushes with no change
     between them send nothing.
   - `TestStackFlushBeforeActivationIsSilent`: a flush with no prior
     `session/stack` sends nothing.
   - `TestStackRemoveListsVanishedNodes`: snapshot while an active tool
     call exists (`SetActiveToolCall` or the state's equivalent), clear it,
     then flush. The patch's `remove` holds the running row's ID.
     Set the call with `SetActiveToolCall` (`session/pending.go:326`) and
     clear it with `ClearActiveToolCallID` (`pending.go:369`).
   - Extend `TestRunInitializeCapabilities` (`internal/acp/run_test.go:42`).
     After its `sessionCapabilities.close` check, assert that
     `sessionCaps["stackView"]` is an empty object, the same way it asserts
     `worktreeIsolation`.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestStack|TestRunInitializeCapabilities' -v
CGO_ENABLED=1 go test ./internal/acp/
go vet ./internal/acp/
gofmt -l internal/acp   # prints nothing
```

Expect the new tests to pass. The full package run fails only on
`TestValidateWorkingPathsRejectsEtc`.

---

## Task 5: Flush stack patches during and at the end of a turn

**Goal:** while a turn runs, a client that has taken a snapshot receives
a `stack_patch` within about 200 ms of each change, and one final patch
when the turn ends (spec §5.3).

**Files:**
- `internal/acp/turn.go` (`runTurn`, `finishTurn`)
- `internal/acp/stack_test.go`

**Steps:**

1. Add a package constant to `stack.go`:
   `stackFlushInterval = 200 * time.Millisecond`, with a comment saying it
   bounds patch rate to five a second.
2. In `runTurn`, inside the `forward` closure (`forward := func(ev pubsub.Event[session.Event])`),
   call `m.markStackDirty(sessionID)` as the first statement.
3. In `runTurn`, before the `forwarding := true` loop:
   - create `flush := time.NewTicker(stackFlushInterval)` with
     `defer flush.Stop()`;
   - add a case to the `select` inside `for forwarding`:

     ```go
     case <-flush.C:
     	if rt.State != nil {
     		m.flushDirtyStack(sessionID, rt.State)
     	}
     ```

   - add `flushDirtyStack` to `stack.go`. It does the same as
     `flushStack(sessionID, st, true)`, but only when the projector is
     `dirty`, so an idle tick costs one mutex.
4. In `finishTurn`, inside the existing `if rt.State != nil {` block and
   before the telemetry notify, call `m.flushStack(sessionID, rt.State, false)`.
   Busy is `false` here even though the slot is still reserved: the runner
   has returned, and `runTurn`'s deferred cleanup releases the slot after
   `finishTurn`. This flush is what adds the receipt.
5. Add these tests to `stack_test.go`. Drive a turn through `PromptTurn`
   with a fake runner that emits events, as the existing turn tests in
   `turn_test.go` do.
   - `TestTurnFlushesStackPatches`: snapshot, then run a turn whose runner
     adds a narration message and an audit. At least one `stack_patch` is
     sent before the `session_telemetry` update.
   - `TestFinishTurnFlushSettlesLiveRows`: the last `stack_patch` before
     `session_telemetry` has no upserted node with `live: true`, and
     upserts a `receipt:` node.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestStack|TestTurnFlushes|TestFinishTurnFlush' -race -v
CGO_ENABLED=1 go test ./internal/acp/
go vet ./internal/acp/
```

Expect the new tests to pass under `-race`. The full package run fails only
on `TestValidateWorkingPathsRejectsEtc`.

---

## Task 6: Bridge — broadcast `stack_patch` without storing it

**Goal:** `stack_patch` updates reach live SSE subscribers but never enter
the replay ring (spec §6.2).

**Files:**
- `web/bridge/events.go` (`EventLog`, `Attach`)
- `web/bridge/events_test.go`

**Steps:**

1. Add `func (l *EventLog) Broadcast(sessionID string, payload any) error`
   below `Append`. It:
   - marshals with `marshalPayload`;
   - builds `Event{ID: 0, SessionID: sessionID, Data: data}`;
   - takes `l.mu`;
   - delivers to each subscriber with the same session filter `Append`
     uses (`if sessionID != "" && s.sessionID != sessionID { continue }`),
     through `s.deliver(ev)`.

   It stores nothing, and its doc comment says why: patches arrive at up to
   5/s and would evict permission prompts and chat history from the
   500-event ring.
2. In `Attach`'s `child.OnNotification` closure, widen the params struct to
   also read `Update struct{ Kind string \`json:"kind"\` } \`json:"update"\``.
   When `method == "session/update" && p.Update.Kind == "stack_patch"`,
   call `l.Broadcast(p.SessionID, json.RawMessage(envelope))` instead of
   `l.Append`. The `prevNotify` chain runs either way.
3. Add these tests to `events_test.go`:
   - `TestBroadcastDeliversWithoutStoring`: subscribe, broadcast. The
     subscriber receives an event with `ID == 0`, `Tail` is empty, and
     `Replay(sid, 1)` returns nothing.
   - `TestAttachRoutesStackPatchToBroadcast`: build a `Child` and `Registry`
     the way the existing `Attach` tests do. Feed `OnNotification` one
     `session/update` with `update.kind` `stack_patch` and one with
     `agent_message_chunk`. `Tail` holds only the second, and a subscriber
     got both.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestBroadcast|TestAttach' -v && go test ./ && go vet ./
```

Expect the new tests and the whole bridge package, including
`TestWebIsStdlibOnly`, to pass.

---

## Task 7: Bridge — `GET /api/sessions/{id}/stack`

**Goal:** the SPA can fetch a snapshot through the bridge. An agent
without `session/stack` gives `501 stack_unsupported` (spec §6.1).

**Files:**
- `web/bridge/registry.go`
- `web/bridge/http.go`
- `web/bridge/registry_test.go`, `web/bridge/http_test.go`

**Steps:**

1. In `registry.go`, add `ErrStackUnsupported = errors.New("stack_unsupported")`
   next to the package's other `Err*` values.
2. In `registry.go`, add
   `func (r *Registry) Stack(ctx context.Context, id string) (json.RawMessage, error)`,
   modelled on `SetMode` (`registry.go:245`):
   - an unknown id returns `ErrUnknownSession`;
   - otherwise it calls
     `r.child.Request(ctx, "session/stack", map[string]string{"sessionId": id})`;
   - if the error is an `*rpcError` (`child.go:42`) with `Code == -32601`,
     it returns `ErrStackUnsupported`;
   - otherwise it returns the raw result.
3. In `http.go`:
   - in `writeErr`, add a case before `default`: `errors.Is(err, ErrStackUnsupported)` →
     `writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "stack_unsupported"})`;
   - register `s.mux.HandleFunc("GET /api/sessions/{id}/stack", s.sessionStack)`
     next to `POST /api/sessions/{id}/mode`;
   - write `sessionStack`, modelled on `setMode` (`http.go:662`) but with
     no body. It resolves `registryForSession(r.PathValue("id"))`, calls
     `reg.Stack(r.Context(), sessionID)`, and on success writes the raw
     bytes with `Content-Type: application/json` and status 200.
4. Tests:
   - `registry_test.go` `TestRegistryStackProxiesResult`: a fake child that
     answers `session/stack` with a fixed JSON result, returned unchanged.
   - `TestRegistryStackUnsupported`: the fake child answers with
     `rpcError{Code: -32601}`, giving `ErrStackUnsupported`.
   - `http_test.go` `TestSessionStackRoute`: 200 with the body, 404 for an
     unknown session, and 501 `{"error":"stack_unsupported"}`. Use the
     same server-construction helper as the existing `setMode` route test.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestRegistryStack|TestSessionStack' -v && go test ./ && go vet ./
```

---

## Task 8: Bridge — owner and origin on `AgentStatus`

**Goal:** every agent in the fleet snapshot and the fleet SSE carries
`ownerId`, `origin` and, for MCP-submitted agents, `clientId`
(spec §6.3).

**Files:**
- `web/bridge/fleetevents.go` (`AgentStatus`, line 179)
- `web/bridge/fleet.go` (`Snapshot`, line 1159)
- `web/bridge/fleet_test.go`

**Steps:**

1. Add three fields to `AgentStatus` after `Name`:
   - `OwnerID string \`json:"ownerId,omitempty"\``
   - `Origin string \`json:"origin,omitempty"\``
   - `ClientID string \`json:"clientId,omitempty"\``

   Add a comment that says they mirror `Agent.OwnerID`, `Agent.Origin` and
   `Agent.ClientID` (`workspace.go`).
2. In `Fleet.Snapshot`'s `AgentStatus{…}` literal, add
   `OwnerID: a.OwnerID, Origin: a.Origin, ClientID: a.ClientID,`.
3. Search for any other place that builds an `AgentStatus` literal
   (`grep -n 'AgentStatus{' web/bridge/*.go`) and fill the same fields
   wherever an `Agent` is in scope.
4. Add `TestSnapshotCarriesOwnerAndOrigin` to `fleet_test.go`. Spawn or
   register an agent through the test helpers the other `Snapshot` tests
   use, with `Origin: OriginMCP` and a `ClientID`. Its status has
   `OwnerID == DefaultOwnerID`, the origin and the client id.

**Verify:**

```bash
cd web/bridge && go test ./ -run TestSnapshot -v && go test ./ && go vet ./
```

---

## Task 9: UI — fix the legacy `session/update` unwrap

**Goal:** the legacy session store renders streamed agent text from the
envelope the agent actually sends (spec §7.6).

**Files:**
- `web/ui/src/lib/store.ts` (`applyACP`, line 292; `applyEvent`)
- `web/ui/src/lib/store.test.ts`

**Steps:**

1. Write the failing test first. In `store.test.ts`, add
   `it('renders agent_message_chunk from a real session/update envelope')`,
   which feeds:

   ```json
   {"method":"session/update","params":{"sessionId":"s1","update":{"kind":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}
   ```

   This is the shape `internal/acp/turn.go`'s `messageUpdate` produces.
   Assert that the store's messages end with assistant text `"hi"`. Run it
   and see it fail.
2. In `applyACP`, before the existing `switch (method)`, add the unwrap: if
   `method === 'session/update'` and `params.update` is an object with a
   string `kind`, set `method = kind` and `params = update`. Every existing
   case then sees the same fields it expects today.
3. Convert the existing `store.test.ts` cases that build the old
   `{method: 'agent_message_chunk', …}` envelope to the real one. Keep one
   test on the old shape, to show it still works for any recorded logs.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/store.test.ts && npm test
```

---

## Task 10: UI — Warm Sunset tokens and Geist fonts

**Goal:** the SPA uses design §6's palette and type through Tailwind 4
tokens. Existing views pick it up without per-view edits.

**Files:**
- `web/ui/package.json`, `web/ui/package-lock.json`
- `web/ui/src/main.ts`
- `web/ui/src/app.css` (`@theme` block at line 12)

**Steps:**

1. `cd web/ui && npm install @fontsource-variable/geist@5.3.0 @fontsource-variable/geist-mono@5.3.0`.
2. In `src/main.ts`, import `@fontsource-variable/geist` and
   `@fontsource-variable/geist-mono` before `./app.css`.
3. Rewrite the `@theme` block in `app.css`, keeping every token name that
   existing components use, so nothing renders unstyled.
   - Before editing, list the names with
     `grep -o -- '--color-[a-z0-9-]*' src/app.css | sort -u` and
     `grep -rho '\b\(bg\|text\|border\|ring\)-[a-z]*-\?[a-z0-9]*' src | sort -u`.
   - Map the old names onto the new palette.
   - Add the Warm Sunset tokens:
     - `--color-accent: #ff875f`
     - `--color-violet: #d787ff`
     - `--color-gold: #ffaf00`
     - `--color-ok: #3fd4b4`
     - `--color-err: #ff87af`
     - `--color-warn: #ffaf5f`
     - `--color-info: #5fd7ff`
     - neutrals `--color-bg: #121113` (the page) through `--color-line: #2c2a30`,
       with the intermediate surface steps from the mockups'
       `docs/web-studio/mockups/*.html` `:root` variables (`--m-bg`, `--m-panel`,
       `--m-line`, `--m-sub`, `--m-mut`).
   - Set `--font-sans: 'Geist Variable', ui-sans-serif, system-ui, sans-serif`
     and `--font-mono: 'Geist Mono Variable', ui-monospace, monospace`.
4. Add a `[data-theme="light"]` block that overrides the neutrals for the
   light variant. Dark is the default.
5. Add a `@media (prefers-reduced-motion: reduce)` rule that disables the
   `animate-pulse` animation.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check && npm run build
```

Expect all three to succeed. Then run the bridge
(`go run ./cmd/webbridge --project <repo>`, see `cmd/webbridge/README.md`).
Run `npm run build` first so it serves the new bundle, but don't commit
`web/bridge/static` until Task 16. Check the existing views by eye: no
unstyled or invisible text.

---

## Task 11: UI — base components and glyph map

**Goal:** the shared primitives match the design system, and the TUI glyph
vocabulary is available to TypeScript.

**Files:**
- `web/ui/src/lib/glyphs.ts` (new), `web/ui/src/lib/glyphs.test.ts` (new)
- `web/ui/src/lib/ui/Badge.svelte`, `Button.svelte`, `Card.svelte`, `Modal.svelte`
- `web/ui/src/lib/ui/Tag.svelte`, `Segmented.svelte`, `Kbd.svelte` (new)

**Steps:**

1. Create `glyphs.ts`, exporting a `glyph` object whose values are copied
   from `internal/app/tui/glyph` (read that package's constants and copy
   them; do not invent new ones). Also export a
   `toolGlyph(name: string): string` that mirrors the prefix table
   `toolCategoryGlyphs` in `internal/app/tui/toolnames.go`, in the same
   order, falling back to `glyph.Ambient`.
2. In `glyphs.test.ts`, assert `toolGlyph` for one name per prefix row,
   plus the fallback.
3. Restyle `Badge`, `Button`, `Card` and `Modal` with the tokens. Keep
   their props and slots unchanged, so `Modal.test.ts` and every caller
   keep working.
4. Add the new components:
   - `Tag.svelte`: props `tone` (`'ok'|'err'|'warn'|'info'|'accent'|'violet'|'gold'|'neutral'`)
     and an optional `role`. A role maps to a tone through a
     `roleTone(role)` helper exported from `glyphs.ts`, following design §6:
     - implementer → gold;
     - reviewer → violet;
     - planner and branch reviewer → info;
     - anything else → neutral.
   - `Segmented.svelte`: props `options: {value,label}[]`, `value` and
     `onchange`.
   - `Kbd.svelte`: renders a key cap.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/glyphs.test.ts src/lib/ui && npm test && npx svelte-check
```

---

## Task 12: UI — shell

**Goal:** the app has a rail, a grouped and collapsible sidebar, a ⌘K
palette, and Home as the default route (spec §7.2).

**Files:**
- `web/ui/src/App.svelte` (hash routing, `hash` state at line 18)
- `web/ui/src/lib/routes.ts`, `routes.test.ts`
- `web/ui/src/lib/Sidebar.svelte`, `Sidebar.test.ts`
- `web/ui/src/lib/Rail.svelte` (new)
- `web/ui/src/lib/Palette.svelte` (new), `web/ui/src/lib/palette.ts` and
  `palette.test.ts` (new)
- `web/ui/src/lib/api.ts` (`AgentStatus`), `web/ui/src/lib/fleet.ts`,
  `web/ui/src/lib/fleet.test.ts`

**Steps:**

1. In `api.ts`, add `ownerId?: string; origin?: string; clientId?: string`
   to `export interface AgentStatus` (`api.ts:112`), mirroring Task 8.
   `fleet.ts` imports that type.
2. In `routes.ts`, add `'#fleet'` for the old Dashboard and make `''`/`'#'`
   resolve to Home. Extend `routes.test.ts` to cover both, and confirm that
   every existing route still resolves.
3. In `App.svelte`, render `Home` (added in Task 13; render a placeholder
   until then) for `#`, and `Dashboard` for `#fleet`. Wrap the content in
   the new layout: `Rail`, then `Sidebar`, then the content.
4. Create `Rail.svelte`, 52px wide:
   - Home ⌂, Agents ⧉ and Projects ≡ are active links;
   - Live ◉, Runs ⋔, Workspaces ▦, Watches ○, Library ◈ and Usage ∿ are
     disabled, with `title="Coming in W2"` (or the phase from design §9);
   - Settings ⚙ links to the existing clients/tokens page (`#clients`).
5. Regroup the existing `Sidebar.svelte` into the groups Needs you /
   Running / Ready to ship / Earlier. Put the grouping in a pure function
   `groupAgents(agents)` (in `fleet.ts`, so it can be tested):
   - **Needs you:** `pending` is set.
   - **Running:** status is running or busy, and the agent is not
     already in Needs you.
   - **Ready to ship:** idle, `changedFiles > 0` and no `prUrl`.
   - **Earlier:** everything else.
   - Sort each group by urgency (pending kind, then status), then by
     `updatedAt` descending.
6. Make the sidebar collapsible. Persist the state under
   `marshal.ui.sidebar` in `localStorage`, reading and writing inside
   try/catch.
7. Create `palette.ts` with `score(query, text): number`, a subsequence
   match that favours consecutive and word-start hits and returns 0 for
   no match. Write `palette.test.ts` first: exact > prefix > word-start >
   scattered > none.
8. Create `Palette.svelte`, a modal opened with ⌘K or Ctrl+K. It lists
   agents (name, project) and pages, filtered by `score`. ↑/↓ moves,
   Enter navigates, Esc closes.
9. Update `Sidebar.test.ts` for the new groups.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check
```

---

## Task 13: UI — Home inbox

**Goal:** `#` shows the inbox described in spec §7.3.

**Files:**
- `web/ui/src/views/Home.svelte` (new), `web/ui/src/views/Home.test.ts` (new)
- `web/ui/src/lib/inbox.ts` (new), `web/ui/src/lib/inbox.test.ts` (new)
- `web/ui/src/App.svelte` (replace the Task 12 placeholder)

**Steps:**

1. In `inbox.ts`, write
   `buildInbox(agents, pending, mine: boolean) → {needsYou, ready, running}`.
   - Reuse `groupAgents` from Task 12 for the agent rows.
   - Merge intake requests from `pending.ts` (the `/api/pending` store)
     into `needsYou`, oldest first.
   - When `mine` is true, keep only items whose `ownerId` is
     `'local'` or unset.
   - Write `inbox.test.ts` first.
2. In `Home.svelte`, render three sections and a side panel.
   - **Needs you rows:**
     - Show the agent name, project, an origin avatar (a letter from
       `origin`: `ui`→U, `cli`→C, `mcp`→M, `issue`→#) and the pending
       request's text.
     - A permission row has Approve and Deny buttons that call the same
       API function `PermissionModal.svelte` uses.
     - A question row links to `#chat/<sessionId>`.
     - An intake row has the approve and deny actions `PendingList.svelte`
       uses.
   - **Ready to ship rows:** branch and changed-file count, with Review
     (links to `#chat/<id>`) and Open PR (opens the existing exit panel the
     way `AgentCard.svelte` does).
   - **Running rows:** activity text and elapsed time since `updatedAt`.
   - **Side panel:** reuse `DiskPanel.svelte` in compact form, and
     `ActivityFeed.svelte` limited to 10 items.
   - A `Segmented` control switches Mine / Everyone. Persist the choice
     under `marshal.ui.inbox.scope`, in try/catch.
3. In `Home.test.ts`, use `@testing-library/svelte`, as `Chat.test.ts`
   does. Check that each section renders from a fixture, and that the
   Approve button calls the permission API.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/inbox.test.ts src/views/Home.test.ts && npm test && npx svelte-check
```

---

## Task 14: UI — stack store

**Goal:** a TypeScript store that holds the stack and applies the client
rules in spec §5.4.

**Files:**
- `web/ui/src/lib/stack.ts` (new), `web/ui/src/lib/stack.test.ts` (new)
- `web/ui/src/lib/api.ts` (add `getStack(sessionId)`)

**Steps:**

1. In `stack.ts`, declare TypeScript interfaces that mirror spec §4.2
   field for field: `WireNode`, `WireStep`, `WireTool`, `WireCall`,
   `WireRunning`, `WireMessage`, `WireThinking`, `WireSubagent`,
   `WireRunEvent`, `WireJobExit`, `WireTask`, `WireReceipt`,
   `StackSnapshot` and `StackPatch`. Optional fields are optional. The JSON
   names are those in `internal/viewmodel/wire.go` from Task 3.
2. In `api.ts`, add `getStack(sessionId)`. It calls
   `GET /api/sessions/${id}/stack` and resolves to the snapshot, or to
   `'unsupported'` on a 501 whose body is `{"error":"stack_unsupported"}`.
3. In `stack.ts`, write `createStackStore(sessionId, fetcher = getStack)`
   returning a Svelte store of
   `{status: 'loading'|'ready'|'unsupported', rev, roots, nodes: Map<string, WireNode>}`,
   plus these methods:
   - `load()`: fetch and replace the state.
   - `onEvent(envelope)`: given an SSE envelope, it:
     - handles `session/update`/`stack_patch` per spec §5.4 (ignore if
       `rev <= state.rev`; `load()` if `baseRev !== state.rev`; otherwise
       apply);
     - calls `load()` on `session/update`/`session_telemetry` and on
       `{type:'replay_overflow'}`.
   - `collect()`: run after each applied patch; deletes nodes not reachable
     from `roots`.
4. Write `stack.test.ts` first. Cover:
   - snapshot load;
   - applying a patch (upsert a new child and its updated parent);
   - a stale patch is ignored;
   - a `baseRev` gap triggers exactly one refetch;
   - `remove`;
   - garbage collection of an orphaned node;
   - `'unsupported'` status on 501;
   - telemetry triggers a refetch.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/stack.test.ts && npm test && npx svelte-check
```

---

## Task 15: UI — transcript renderers and density

**Goal:** a `Transcript` component renders a stack store the way the TUI
lays out the same tree (spec §7.4).

**Files:**
- `web/ui/src/lib/transcript/Transcript.svelte` (new)
- `web/ui/src/lib/transcript/{TurnNode,TaskRow,StepHeader,ToolRow,MessageNode,ThinkingRow,SubagentCard,RunEventRow,JobExitRow,ReceiptLine}.svelte` (new)
- `web/ui/src/lib/transcript/density.ts`, `density.test.ts` (new)
- `web/ui/src/lib/transcript/Transcript.test.ts` (new)

**Steps:**

1. In `density.ts`:
   - `type Density = 'outline' | 'steps' | 'full'`;
   - `nextGlobal(d)`, cycling outline → steps → full, as the TUI's
     `nextGlobal` in `internal/app/tui/density.go`;
   - `effective(nodeId, overrides, parentOf, global)`: a node without an
     override inherits its parent's effective level;
   - `visible(node, density)`: at outline, tool rows are hidden. Tests
     first.
2. `Transcript.svelte` takes `store`, `density`, `overrides`,
   `foldTasks` and `cursor`, and walks `roots` → `children` with a
   recursive `{#each … (id)}` keyed by node ID, so unchanged nodes are not
   re-rendered. It dispatches each node to its renderer by `kind`.
3. Renderers. Read the matching TUI renderer for layout, but don't port
   styling code:
   - `TaskRow`: `index/total`, content, status glyph, then
     `steps · tools · edits` and work time on the right (TUI:
     `tasks_render.go`). A task is folded when `foldTasks` is on, its
     status is `completed`, and `!unresolvedFailure`.
   - `StepHeader`:
     - an owner `Tag` with `roleTone(step.role)`;
     - the headline, in italics with a small "inferred" tag when
       `inferred`;
     - the duration on the right, from `startedAt`/`endedAt` (live steps
       tick every second);
     - `rest` as one muted truncated line at `steps`, and as markdown
       (`markdown.ts`) at `full`;
     - `thoughts` as "thought for Ns" rows.
   - `ToolRow`:
     - `toolGlyph(name)`;
     - target-first for `file.write_patch`, `file.write`, `file.read`,
       `symbols.find`, `shell.run` and `test.run` (TUI
       `subjectFirstTool`), otherwise `display` then target;
     - `summary`;
     - a failed call shows the `err` tone and `error` or `exit N`;
     - a merged run shows `×n` and expands to one line per call;
     - a running row shows a spinner and `running.output` in a mono block
       at `full`;
     - when `truncated` is set, a muted note: "output truncated (full view
       in W2)".
   - `MessageNode`: a final message renders as markdown; a user prompt
     renders as the turn heading.
   - `ThinkingRow`, `SubagentCard` (label, status glyph, role tag, tool
     count, current tool, summary), `RunEventRow`, `JobExitRow` and
     `ReceiptLine` (`duration · N tasks · N steps · N tools · N files ·
     usage`, matching the TUI receipt format in `tasks_render.go`).
4. In `Transcript.test.ts`, render a fixture snapshot (one turn, one task,
   one narrated step, one merged tool run, a final message and a receipt)
   and check the visible text at each density, and that folding hides a
   finished task's steps.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/transcript && npm test && npx svelte-check
```

---

## Task 16: UI — browse keys, follow, now bar, Chat integration, rebuild static

**Goal:** the session view uses the stack transcript with TUI browse keys,
follow-to-bottom and a now bar, and falls back to the legacy list on 501.
The built SPA is committed.

**Files:**
- `web/ui/src/lib/transcript/browse.ts`, `browse.test.ts` (new)
- `web/ui/src/lib/transcript/NowBar.svelte` (new)
- `web/ui/src/views/Chat.svelte`, `web/ui/src/views/Chat.test.ts`
- `web/bridge/static/**` (rebuilt)

**Steps:**

1. In `browse.ts`, write a pure reducer
   `browseKey(state, key, ctx) → {state, effect?}`. `ctx` holds the flat
   visible-node list and the stop indices. Bindings, from
   `internal/app/tui/browse.go`'s `handleBrowseKey`:
   - `j`/ArrowDown and `k`/ArrowUp move by one;
   - `J`/`]` and `K`/`[` jump to the next or previous stop (a task header
     or the first node of a turn, as `jumpCursor`'s comment says);
   - `g`/Home and `G`/End go to the first or last node, and `G` on a live
     node sets `follow = true`;
   - `Enter` gives `effect: {toggleDensity: id}`;
   - `z` gives `effect: {toggleFold: true}`;
   - `y` gives `effect: {copy: id}`;
   - `i`/`o`/`f` give `effect: {toast: 'Coming in W2'}`;
   - `Escape` gives `effect: {exitBrowse: true}`.

   Write `browse.test.ts` first, one case per binding.
2. In `Chat.svelte`:
   - create `createStackStore(sessionId)` on mount and call `load()`, and
     forward each SSE envelope the view already receives to `onEvent`;
   - when `status === 'unsupported'`, render the existing legacy message
     list (fixed in Task 9); otherwise render `Transcript`;
   - keep permissions, questions, the mode switcher, steer and the exit
     panel exactly as they are.
3. Browse mode: `Esc` in the composer blurs it and enters browse mode at
   the last node. A document `keydown` listener routes keys through
   `browseKey`, but only while browse is on and focus isn't in an input.
   The cursor node gets a violet left rule and is scrolled into view.
   `copy` uses `navigator.clipboard.writeText` with the node's text:
   - a step: headline + rest;
   - a call: target + output;
   - a message: its content.
4. Follow: stick to the bottom while the last node is live, unless the user
   scrolled up or is browsing. `G` resumes it.
5. `NowBar.svelte` is shown while any node is `live`. It shows:
   - the live step's owner tag and headline;
   - the running tool's `display` and target;
   - elapsed time;
   - a Stop button that calls the existing cancel API (`POST
     /api/sessions/{id}/cancel`).

   A second Ctrl+C within 1s of the first also calls cancel; the first
   shows a "Press Ctrl+C again to stop" hint. `Esc` never cancels.
6. In the session header, add a global density `Segmented`
   (Outline / Steps / Full), stored per browser under
   `marshal.ui.density` (try/catch).
7. Extend `Chat.test.ts`:
   - with a mocked `getStack` returning a fixture, the transcript renders;
   - with `'unsupported'`, the legacy list renders;
   - a `stack_patch` event updates the DOM;
   - two Ctrl+C presses within 1s call cancel once.
8. Rebuild the bundle with `npm run build` (output goes to
   `web/bridge/static` per `vite.config.ts`), and include the rebuilt
   assets in this task's commit.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check && npm run build
cd ../bridge && go test ./ -run 'TestAssets|TestWebIsStdlibOnly' -v
```

Then check the session view by hand, against both an agent from this branch
and an older one:
- build `marshal` and run the bridge (`go run ./cmd/webbridge --project <repo>`);
- open a session and prompt it;
- the transcript shows a live step and tool rows, and ends with a receipt;
- reloading mid-turn resyncs;
- the same session opened in the TUI (`marshal --resume <id>`) shows the
  same turn, task, headlines and receipt;
- an older agent image falls back to the legacy list.

---

## Task 17: Docs — AGENTS.md tree and design §5.2

**Goal:** the repo map and the parent design match what W1 built.

**Files:**
- `AGENTS.md`
- `docs/web-studio/design.md` (§5.2, §5.3 Sessions row, §10)

**Steps:**

1. In `AGENTS.md`'s tree:
   - remove the `internal/app/tui/stack/` line from the TUI group;
   - add, under "Runtime and orchestration" or a "View model" line
     before TUI:
     `internal/viewmodel/                    — transcript view model (turn → task → step → row, receipts), text helpers, and its JSON wire projection; shared by the TUI and ACP`.
2. In `design.md` §5.2:
   - replace the `_marshal/stack` notification sketch with a short
     paragraph pointing at the W1 spec §5: the `session/stack` request,
     the `session/update` `stack_patch` kind, snapshot on request,
     encoded-node diffing, and the 200 ms flush;
   - in the §4 table, change `internal/app/tui/stack` to `internal/viewmodel`;
   - in the §5.3 "Sessions" row, change "ACP `_marshal/stack`" to
     "ACP `session/stack` + `stack_patch`".
3. In §9's W1 row, change "`_marshal/stack` stream" to "stack stream".

**Verify:**

```bash
grep -n 'internal/app/tui/stack\|_marshal/stack' AGENTS.md docs/web-studio/design.md   # prints nothing
```

---

## Final verification

Run from the repo root after Task 17:

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
go vet ./...
gofmt -l .
cd web/bridge && go test ./... && go vet ./...
cd ../ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
```

Expected results:
- `go test ./...` fails only on the five tests listed under Assumptions;
- `gofmt -l .` prints nothing;
- the `web/` commands pass;
- the final `git status` prints nothing, which means the committed static
  bundle matches the source.

## Integration notes

- **Older agents.** An agent from before Task 4 has no `session/stack`, so
  its web session shows the legacy list. An agent from Task 4 on works with
  either bridge. A bridge from before Task 6 would store patches in the
  ring, so deploy the bridge and agent images together.
- **Other ACP clients** (editors) never call `session/stack`, so they never
  get patches. `stackView` in `initialize` is how a client discovers the
  feature.
- **Idle-time changes** (a background subagent finishing between turns)
  show up only after a refetch. W2 adds the idle ticker (spec §5.3).
- **Multi-user.** `ownerId`/`origin` now reach the SPA. Nothing enforces
  them yet (design §5.4).
- **PR stacking.** This branch is stacked on #23. Merge #23 first, then
  retarget this branch's PR to `main`.

## Self-review

| Check | Result |
|---|---|
| Every task self-contained and independently verifiable? | Yes. Each task has its own Verify command. Tasks 1–3 leave the TUI unchanged. Task 4 is testable before Task 5. The UI tasks build on each other only through files they create. |
| Every anchor verified to exist? | Yes, on `2ddc09e`. Checked anchors: `TestRunInitializeCapabilities` (`run_test.go:42`), `AddMessage`, `SetActiveToolCall`/`ClearActiveToolCallID`, `AgentStatus` in `api.ts:112`, `cmd/webbridge`, `TurnManager`/`NewTurnManager` (`turn.go:181`/`233`), `forward`, the `for forwarding` select, `finishTurn` (`turn.go:1198`), `SetMode`'s unknown-session error, `HasActiveTurn` (`turn.go:442`), `newResumeTestManager` (`turn_test.go:3126`), the `sessionCapabilities` map and `session/steer` registration in `host.go`, `EventLog.Append`/`Attach`/`deliver` (`events.go`), `Registry.SetMode` (`registry.go:245`), `rpcError` (`child.go:42`), `writeErr`/`setMode`/routes (`http.go:162`/`662`/`103–149`), `AgentStatus` (`fleetevents.go:179`), `Fleet.Snapshot` (`fleet.go:1159`), `Agent.OwnerID/Origin/ClientID` (`workspace.go`), `applyACP` (`store.ts:292`), `@theme` (`app.css:12`), and the `web/ui/src` file list. |
| Code blocks complete and compilable in isolation? | The embedded Go (`describe.go`, the TUI wrappers, `wire.go`, `wire_test.go`) and the Task 1 commands were applied to a scratch checkout of `2ddc09e`. `go build ./...`, `go vet` and `gofmt -l` were clean, and `go test ./internal/viewmodel/` passed. Tasks 4–16 use prose with anchors, because their code depends on the surrounding files. |
| Verification commands correct per AGENTS.md? | Yes: `CGO_ENABLED=1 go build/test`, `go vet ./...`, `gofmt`. `web/ui` uses its `package.json` scripts (`test`, `build`) plus `npx svelte-check`. |
| No placeholders or TBDs? | None. The Task 12 placeholder for Home is replaced in Task 13 by design. |
| Contradicts nothing in the spec? | Matches spec §3–§7. It refines design §5.2 as the spec says, and Task 17 updates the design doc. |
