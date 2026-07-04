# Streaming Thinking Blocks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stream a model's reasoning/thinking content live into the TUI (so the screen isn't blank while the model generates), collapse it to a one-line summary once the answer lands, and persist it so it survives a session reload.

**Architecture:** A new `DeltaKind` on `schema.ChatEvent` distinguishes thinking deltas from answer deltas at the provider layer. `agent.Runner.chatOnce` routes thinking deltas into a mutable in-progress message on `session.State` instead of buffering them with the answer text (which remains a structured action-JSON envelope that cannot be streamed live). The TUI's existing 150ms busy-tick renders that in-progress message as a bordered "thinking" box, then collapses it to `⚙ thought for Ns` once the turn finalizes into a real `Message`. Reasoning persists to SQLite alongside message content.

**Tech Stack:** Go, Bubble Tea / Bubbles / Lipgloss (TUI), `modernc.org/sqlite`, standard library only — no new dependencies.

## Global Constraints

- Local-first: no behavior in this plan depends on a remote provider; `reasoning_content` parsing works against any OpenAI-compatible endpoint (local or remote) that emits it, and is a no-op for those that don't.
- Provider-flexible: `DeltaKind` lives in `internal/llm/schema`, not in the OpenAI-compatible provider package, so a future non-OpenAI-compatible provider can populate the same field.
- Tool-safe / TUI-is-render-only: no policy or approval logic changes; the TUI only renders what `session.State` exposes.
- The action-JSON contract in `internal/agent` (`ParseAction`, plan/tool-call/answer envelope) is unchanged — only the reasoning/thinking channel streams live, per the approved design spec.
- Backward compatible: models/providers that never emit `reasoning_content` must see zero behavior change (empty reasoning, no thinking box, existing tests unaffected).

**Design reference:** `docs/superpowers/specs/2026-07-04-streaming-thinking-blocks-design.md`

**Correction from the design spec:** the spec's preview text used `Ctrl+H` for the expand/collapse toggle. `Ctrl+H` is indistinguishable from `Backspace` at the terminal level (`bubbletea.KeyCtrlH == KeyBackspace == 0x08`) and is already bound to `DeleteCharacterBackward` in `bubbles/textinput`'s default keymap — binding it globally would break backspacing in the input box. This plan uses **`Ctrl+G`** instead (`bubbletea.KeyCtrlG`, ASCII BEL, confirmed free of collisions with both the app's existing global hotkeys and textinput's defaults).

---

### Task 1: Provider-agnostic thinking delta

**Files:**
- Modify: `internal/llm/schema/event.go`
- Modify: `internal/llm/provider/openai_compatible_wire.go`
- Modify: `internal/llm/provider/openai_compatible.go:203-210` (`streamChatEvents`)
- Test: `internal/llm/provider/openai_compatible_test.go`

**Interfaces:**
- Produces: `schema.DeltaKind` (`int`), constants `schema.DeltaAnswer` (zero value) and `schema.DeltaThinking`; `schema.ChatEvent.Kind DeltaKind` field, meaningful only when `Type == ChatEventDelta`.

- [ ] **Step 1: Write the failing test**

Add to `internal/llm/provider/openai_compatible_test.go` (place after `TestChatStreamingDeltasAndDone`, using the same `newTestProvider`/`recvEvent`/`assertChannelClosed`/`chatReq` helpers already in the file):

```go
func TestChatStreamingReasoningContentEmitsThinkingDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking...\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before first event")
	}
	if ev1.Type != schema.ChatEventDelta || ev1.Kind != schema.DeltaThinking || ev1.Delta != "thinking..." {
		t.Fatalf("event 1 = %+v, want thinking delta %q", ev1, "thinking...")
	}

	ev2, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before second event")
	}
	if ev2.Type != schema.ChatEventDelta || ev2.Kind != schema.DeltaAnswer || ev2.Delta != "answer" {
		t.Fatalf("event 2 = %+v, want answer delta %q", ev2, "answer")
	}

	ev3, ok := recvEvent(t, events)
	if !ok || ev3.Type != schema.ChatEventDone {
		t.Fatalf("event 3 = %+v ok=%v, want Done", ev3, ok)
	}

	assertChannelClosed(t, events)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/provider/... -run TestChatStreamingReasoningContentEmitsThinkingDelta -v`
Expected: FAIL — compile error (`ev1.Kind undefined`, `schema.DeltaThinking undefined`) since neither exists yet.

- [ ] **Step 3: Add `DeltaKind` to the schema**

In `internal/llm/schema/event.go`, replace the file contents with:

```go
package schema

// ChatEventType discriminates the three shapes a ChatEvent can take. Both
// streaming (SSE) and non-streaming (single JSON body) provider responses
// are normalized into the same event stream: non-streaming responses are
// synthesized as exactly one Delta (the full content) followed by one Done.
type ChatEventType string

const (
	ChatEventDelta ChatEventType = "delta"
	ChatEventDone  ChatEventType = "done"
	ChatEventError ChatEventType = "error"
)

// DeltaKind distinguishes the model's free-form reasoning/thinking narration
// (DeltaThinking) from its normal output (DeltaAnswer), when a provider
// exposes the two as separate channels — e.g. the `reasoning_content` field
// emitted by DeepSeek-R1-style reasoning models over an OpenAI-compatible
// streaming API. DeltaAnswer is the zero value, so providers/models that
// never populate a reasoning channel are unaffected: every ChatEvent they
// emit defaults to DeltaAnswer exactly as before this field existed.
type DeltaKind int

const (
	DeltaAnswer DeltaKind = iota
	DeltaThinking
)

type ChatEvent struct {
	Type ChatEventType

	// Kind discriminates Delta as answer text vs. reasoning/thinking text.
	// Populated only when Type == ChatEventDelta.
	Kind DeltaKind

	// Delta holds incremental (or, for non-streaming, complete) assistant
	// text content. Populated only when Type == ChatEventDelta.
	Delta string

	// FinishReason mirrors the upstream `finish_reason` field ("stop",
	// "length", etc.) when known. Populated only when Type == ChatEventDone.
	FinishReason string

	// Err is populated only when Type == ChatEventError. The channel is
	// always closed immediately after an error event.
	Err error
}
```

- [ ] **Step 4: Parse `reasoning_content` in the wire chunk struct**

In `internal/llm/provider/openai_compatible_wire.go`, change the `chatCompletionChunk.Choices[].Delta` struct:

```go
// chatCompletionChunk is a single SSE `data:` payload for streaming
// responses: choices[0].delta.content, plus the reasoning_content
// convention used by DeepSeek-R1-style reasoning models served over an
// OpenAI-compatible endpoint (vLLM, Ollama, etc.).
type chatCompletionChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *apiError `json:"error"`
}
```

- [ ] **Step 5: Emit a thinking delta from `streamChatEvents`**

In `internal/llm/provider/openai_compatible.go`, replace the choice-handling block inside `streamChatEvents` (currently lines 203-210):

```go
		choice := chunk.Choices[0]
		if choice.Delta.ReasoningContent != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: choice.Delta.ReasoningContent}
		}
		if choice.Delta.Content != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: choice.Delta.Content}
		}
		if choice.FinishReason != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: choice.FinishReason}
			return
		}
```

(The answer-delta event omits `Kind` entirely, so it defaults to `schema.DeltaAnswer` — this is the same event construction as before Step 4/5, just with the new thinking branch added ahead of it.)

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/llm/provider/... -v`
Expected: PASS for all tests in the package, including the new one. This also re-runs every pre-existing provider test (e.g. `TestChatStreamingDeltasAndDone`) to confirm the change is additive.

- [ ] **Step 7: Commit**

```bash
git add internal/llm/schema/event.go internal/llm/provider/openai_compatible_wire.go internal/llm/provider/openai_compatible.go internal/llm/provider/openai_compatible_test.go
git commit -m "feat(llm): parse reasoning_content into a provider-agnostic thinking delta"
```

---

### Task 2: Persist reasoning in SQLite

**Files:**
- Modify: `internal/db/migrations.go`
- Modify: `internal/db/db.go` (`Migrate`)
- Modify: `internal/db/sessions.go` (`Message`, `SaveMessage`, `GetMessages`)
- Test: `internal/db/sessions_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `db.Message.Reasoning string`, `db.Message.ThinkDurationMs int64`; `db.SaveMessage(sessionID, role, content string, createdAt time.Time, reasoning string, thinkDuration time.Duration) error` (signature change — 2 new trailing params); `db.GetMessages` returns the new fields populated (empty/zero when the DB has `NULL`).

- [ ] **Step 1: Write the failing test**

In `internal/db/sessions_test.go`, replace the body of `TestCreateSessionAndMessages` (it currently calls `db.SaveMessage` with 4 args) with:

```go
func TestCreateSessionAndMessages(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	sessionID := "session-abc"
	now := time.Now().UTC()
	if err := db.CreateSession(sessionID, projectID, "test session", now); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if err := db.SaveMessage(sessionID, "user", "hello", now.Add(time.Second), "", 0); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	if err := db.SaveMessage(sessionID, "assistant", "hi there", now.Add(2*time.Second), "considering the greeting", 4*time.Second); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	messages, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "hello" {
		t.Errorf("message 0 mismatch: %+v", messages[0])
	}
	if messages[0].Reasoning != "" || messages[0].ThinkDurationMs != 0 {
		t.Errorf("message 0 should have no reasoning: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "hi there" {
		t.Errorf("message 1 mismatch: %+v", messages[1])
	}
	if messages[1].Reasoning != "considering the greeting" {
		t.Errorf("message 1 reasoning = %q, want %q", messages[1].Reasoning, "considering the greeting")
	}
	if messages[1].ThinkDurationMs != 4000 {
		t.Errorf("message 1 think duration = %d ms, want 4000", messages[1].ThinkDurationMs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -run TestCreateSessionAndMessages -v`
Expected: FAIL — compile error (`too many arguments in call to db.SaveMessage`) and/or `messages[0].Reasoning undefined`.

- [ ] **Step 3: Add the new columns to the base schema**

In `internal/db/migrations.go`, change the `messages` table definition:

```sql
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    reasoning TEXT,
    think_duration_ms INTEGER,
    created_at TEXT NOT NULL
);
```

- [ ] **Step 4: Add the backward-compatible column migration**

In `internal/db/db.go`, inside `Migrate()`, add this block after the existing `files` column block (after the `if !fileColumns["summary"] { ... }` block, before `return nil`):

```go
	messageColumns, err := db.tableColumns("messages")
	if err != nil {
		return fmt.Errorf("inspect messages columns: %w", err)
	}
	messageColumnDefs := map[string]string{
		"reasoning":         "TEXT",
		"think_duration_ms": "INTEGER",
	}
	for name, def := range messageColumnDefs {
		if messageColumns[name] {
			continue
		}
		query := fmt.Sprintf("ALTER TABLE messages ADD COLUMN %s %s", name, def)
		if _, err := db.sqlDB.Exec(query); err != nil {
			return fmt.Errorf("add column %s to messages: %w", name, err)
		}
	}
```

- [ ] **Step 5: Update `Message`, `SaveMessage`, and `GetMessages`**

In `internal/db/sessions.go`:

```go
type Message struct {
	ID              int64
	Role            string
	Content         string
	Reasoning       string
	ThinkDurationMs int64
	CreatedAt       time.Time
}
```

```go
// SaveMessage appends a message to the session transcript. reasoning and
// thinkDuration are the model's captured reasoning/thinking text and how
// long it took, if any (empty string / zero duration otherwise) — both
// stored as SQL NULL so a message with no captured reasoning round-trips
// identically to how it did before these columns existed.
func (db *DB) SaveMessage(sessionID string, role string, content string, createdAt time.Time, reasoning string, thinkDuration time.Duration) error {
	var reasoningArg sql.NullString
	if reasoning != "" {
		reasoningArg = sql.NullString{String: reasoning, Valid: true}
	}
	var thinkDurationArg sql.NullInt64
	if thinkDuration > 0 {
		thinkDurationArg = sql.NullInt64{Int64: thinkDuration.Milliseconds(), Valid: true}
	}
	_, err := db.exec(
		`INSERT INTO messages (session_id, role, content, reasoning, think_duration_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID, role, content, reasoningArg, thinkDurationArg, createdAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("save message: %w", err)
	}
	return nil
}

// GetMessages returns all messages for a session in chronological order.
func (db *DB) GetMessages(sessionID string) ([]Message, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, role, content, reasoning, think_duration_ms, created_at
		 FROM messages
		 WHERE session_id = ?
		 ORDER BY id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		var created string
		var reasoning sql.NullString
		var thinkDurationMs sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &reasoning, &thinkDurationMs, &created); err != nil {
			return nil, fmt.Errorf("scan message row: %w", err)
		}
		if reasoning.Valid {
			m.Reasoning = reasoning.String
		}
		if thinkDurationMs.Valid {
			m.ThinkDurationMs = thinkDurationMs.Int64
		}
		parsed, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		m.CreatedAt = parsed.UTC()
		messages = append(messages, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message rows: %w", err)
	}
	return messages, nil
}
```

Add `"database/sql"` alongside the existing imports in `sessions.go` if not already present (it is — check the top of the file before editing; `sessions.go` already imports `database/sql` for `GetSession`/`EndSession`).

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/db/... -v`
Expected: PASS for all tests in the package, including `TestCreateSessionAndMessages`, `TestGetMessagesEmptySession`, `TestEndSessionSetsEndedAtAndSummary`, and every other `db` package test (confirms the migration/column changes don't break unrelated tables).

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations.go internal/db/db.go internal/db/sessions.go internal/db/sessions_test.go
git commit -m "feat(db): persist reasoning text and think duration on messages"
```

---

### Task 3: Session state streaming lifecycle

**Files:**
- Modify: `internal/app/session/session.go`
- Test: `internal/app/session/session_test.go`

**Interfaces:**
- Consumes: `db.SaveMessage(sessionID, role, content string, createdAt time.Time, reasoning string, thinkDuration time.Duration) error` (Task 2).
- Produces: `session.InProgressMessage{Reasoning string, StartedAt time.Time, Active bool}`; `(*State) BeginStreaming()`, `(*State) AppendThinking(delta string)`, `(*State) EndStreaming()`, `(*State) InProgress() InProgressMessage`; `session.Message` gains `Reasoning string` and `ThinkDuration time.Duration`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/app/session/session_test.go`:

```go
func TestBeginStreamingThenAppendThinkingAccumulatesReasoning(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.BeginStreaming()
	state.AppendThinking("checking the ")
	state.AppendThinking("auth flow")

	got := state.InProgress()
	if !got.Active {
		t.Fatal("InProgress().Active = false, want true after BeginStreaming")
	}
	if got.Reasoning != "checking the auth flow" {
		t.Fatalf("InProgress().Reasoning = %q, want %q", got.Reasoning, "checking the auth flow")
	}
}

func TestEndStreamingMarksInactiveButPreservesReasoning(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	state.EndStreaming()

	got := state.InProgress()
	if got.Active {
		t.Fatal("InProgress().Active = true, want false after EndStreaming")
	}
	if got.Reasoning != "checking the auth flow" {
		t.Fatalf("InProgress().Reasoning = %q, want preserved after EndStreaming", got.Reasoning)
	}
}

func TestAddMessageAttachesReasoningFromInProgressAndClearsIt(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	state.EndStreaming()
	state.AddMessage(RoleAssistant, "Here's the fix.")

	messages := state.Messages()
	if len(messages) != 1 {
		t.Fatalf("len(Messages()) = %d, want 1", len(messages))
	}
	if messages[0].Reasoning != "checking the auth flow" {
		t.Fatalf("messages[0].Reasoning = %q, want %q", messages[0].Reasoning, "checking the auth flow")
	}
	if messages[0].ThinkDuration <= 0 {
		t.Fatalf("messages[0].ThinkDuration = %v, want > 0", messages[0].ThinkDuration)
	}

	if got := state.InProgress().Reasoning; got != "" {
		t.Fatalf("InProgress().Reasoning after AddMessage = %q, want empty (cleared)", got)
	}

	state.AddMessage(RoleUser, "thanks")
	messages = state.Messages()
	if messages[1].Reasoning != "" || messages[1].ThinkDuration != 0 {
		t.Fatalf("messages[1] should have no reasoning when nothing was streamed: %#v", messages[1])
	}
}

func TestStreamingLifecycleIsRaceFree(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			state.BeginStreaming()
			state.AppendThinking("step")
			state.EndStreaming()
			state.AddMessage(RoleAssistant, "answer")
		}
	}()

	for i := 0; i < 100; i++ {
		_ = state.InProgress()
		_ = state.Messages()
	}
	<-done
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/session/... -run 'TestBeginStreaming|TestEndStreaming|TestAddMessageAttachesReasoning|TestStreamingLifecycleIsRaceFree' -v`
Expected: FAIL — compile errors (`state.BeginStreaming undefined`, etc.).

- [ ] **Step 3: Add `InProgressMessage` and the streaming lifecycle methods**

In `internal/app/session/session.go`, add after the `Message` struct definition:

```go
// InProgressMessage holds the reasoning text accumulated for the model call
// currently in flight (if any). It is not itself a Message: it becomes the
// Reasoning/ThinkDuration of the next Message added via AddMessage, at which
// point it is cleared for the next call.
type InProgressMessage struct {
	Reasoning string
	StartedAt time.Time
	Active    bool
}
```

Add the `inProgress InProgressMessage` field to the `State` struct (alongside the other `mu`-guarded fields):

```go
	mu              sync.Mutex
	messages        []Message
	inProgress      InProgressMessage
	providerErr     error
	pendingApproval *PendingToolCall
	sessionRules    []string
	auditLog        []registry.AuditEvent
	lastBackup      []BackupFile
	contextPack     contextpack.Pack
	activeRoute     RouteInfo
```

Add the four new methods (near `AddMessage`/`Messages`):

```go
// BeginStreaming starts a new in-progress message, resetting any reasoning
// left over from a previous call. Call this once per model call that may
// stream reasoning content, before consuming its event stream.
func (s *State) BeginStreaming() {
	s.mu.Lock()
	s.inProgress = InProgressMessage{StartedAt: time.Now(), Active: true}
	s.mu.Unlock()
}

// AppendThinking appends a chunk of reasoning/thinking text to the
// in-progress message.
func (s *State) AppendThinking(delta string) {
	s.mu.Lock()
	s.inProgress.Reasoning += delta
	s.mu.Unlock()
}

// EndStreaming marks the in-progress message inactive. Reasoning captured so
// far is preserved (not cleared) so a subsequent AddMessage call can still
// pick it up.
func (s *State) EndStreaming() {
	s.mu.Lock()
	s.inProgress.Active = false
	s.mu.Unlock()
}

// InProgress returns a copy of the current in-progress message.
func (s *State) InProgress() InProgressMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inProgress
}
```

- [ ] **Step 4: Attach reasoning to `Message` and wire it through `AddMessage`**

Change the `Message` struct:

```go
type Message struct {
	Role          Role
	Content       string
	Reasoning     string
	ThinkDuration time.Duration
	CreatedAt     time.Time
}
```

Replace `AddMessage`:

```go
func (s *State) AddMessage(role Role, content string) {
	s.mu.Lock()
	reasoning := s.inProgress.Reasoning
	var thinkDuration time.Duration
	if reasoning != "" {
		thinkDuration = time.Since(s.inProgress.StartedAt)
	}
	s.inProgress = InProgressMessage{}

	msg := Message{
		Role:          role,
		Content:       content,
		Reasoning:     reasoning,
		ThinkDuration: thinkDuration,
		CreatedAt:     time.Now(),
	}
	s.messages = append(s.messages, msg)
	s.mu.Unlock()

	if s.persistenceEnabled() {
		// Best-effort persistence; do not fail the in-memory transcript.
		if err := s.db.SaveMessage(s.sessionID, string(role), content, msg.CreatedAt, reasoning, thinkDuration); err != nil {
			s.logger.Error("save message failed", "error", err, "session_id", s.sessionID, "role", role)
		}
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/app/session/... -race -v`
Expected: PASS for all tests in the package, including the 4 new ones. `-race` confirms no data race in the new lock-guarded fields.

- [ ] **Step 6: Commit**

```bash
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): add in-progress streaming message with reasoning capture"
```

---

### Task 4: Route reasoning deltas through the agent runner

**Files:**
- Modify: `internal/agent/runner.go:247-269` (`chatOnce`)
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `schema.DeltaThinking` (Task 1); `session.State.BeginStreaming()`, `AppendThinking(string)`, `EndStreaming()`, `InProgress() session.InProgressMessage` (Task 3).
- Produces: no new exported symbols — `chatOnce`'s behavior changes internally; its signature (`func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage) (string, error)`) is unchanged.

- [ ] **Step 1: Extend the test double to script thinking deltas**

In `internal/agent/runner_test.go`, modify `scriptedProvider` to add a `thinking []string` field and emit it before the content delta:

```go
type scriptedProvider struct {
	responses []string
	thinking  []string
	errs      []error
	calls     int
	requests  []schema.ChatRequest
}
```

```go
func (p *scriptedProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	idx := p.calls
	p.requests = append(p.requests, req)
	p.calls++

	ch := make(chan schema.ChatEvent, 3)
	if idx < len(p.errs) && p.errs[idx] != nil {
		ch <- schema.ChatEvent{Type: schema.ChatEventError, Err: p.errs[idx]}
		close(ch)
		return ch, nil
	}

	if idx < len(p.thinking) && p.thinking[idx] != "" {
		ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: p.thinking[idx]}
	}

	content := ""
	switch {
	case idx < len(p.responses):
		content = p.responses[idx]
	case len(p.responses) > 0:
		content = p.responses[len(p.responses)-1]
	}
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: content}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
	close(ch)
	return ch, nil
}
```

(The channel buffer grows from 2 to 3 to hold the extra thinking event without blocking; every existing call site constructs `scriptedProvider{responses: ...}` with `thinking` left as its zero value `nil`, so `idx < len(p.thinking)` is always false for them — no existing test's event sequence changes.)

Add the new test:

```go
func TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText(t *testing.T) {
	p := &scriptedProvider{
		thinking:  []string{"considering the question"},
		responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	text, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("chatOnce returned error: %v", err)
	}
	wantText := `{"rationale":"r","action":{"type":"answer","content":"done"}}`
	if text != wantText {
		t.Fatalf("chatOnce returned %q, want %q", text, wantText)
	}
	if got := state.InProgress().Reasoning; got != "considering the question" {
		t.Fatalf("InProgress().Reasoning = %q, want %q", got, "considering the question")
	}
	if state.InProgress().Active {
		t.Fatal("InProgress().Active = true, want false after chatOnce returns")
	}
}

func TestChatOnceEndsStreamingEvenOnProviderError(t *testing.T) {
	p := &scriptedProvider{errs: []error{errors.New("boom")}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	state.BeginStreaming()
	state.AppendThinking("partial thought")

	_, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})
	if err == nil {
		t.Fatal("chatOnce returned nil error, want the provider error")
	}
	if state.InProgress().Active {
		t.Fatal("InProgress().Active = true after error, want false (EndStreaming must still run)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/... -run 'TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText|TestChatOnceEndsStreamingEvenOnProviderError' -v`
Expected: FAIL — `TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText` fails because `InProgress().Reasoning` is empty (nothing routes thinking deltas yet); `TestChatOnceEndsStreamingEvenOnProviderError` fails because `BeginStreaming`/`EndStreaming` aren't called by `chatOnce` yet (`Active` stays whatever the test set it to, but more importantly no lifecycle exists to reset it — this test's `Active` field isn't set true here, so adjust: the meaningful assertion is that this compiles and the reasoning-preservation behavior is exercised once Step 3 lands).

- [ ] **Step 3: Update `chatOnce`**

In `internal/agent/runner.go`, replace `chatOnce` (lines 247-269):

```go
func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage) (string, error) {
	events, err := p.Chat(ctx, schema.ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
	})
	if err != nil {
		return "", err
	}

	r.State.BeginStreaming()
	defer r.State.EndStreaming()

	var sb strings.Builder
	for event := range events {
		switch event.Type {
		case schema.ChatEventDelta:
			if event.Kind == schema.DeltaThinking {
				r.State.AppendThinking(event.Delta)
			} else {
				sb.WriteString(event.Delta)
			}
		case schema.ChatEventError:
			return "", event.Err
		case schema.ChatEventDone:
			return sb.String(), nil
		}
	}
	return sb.String(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/... -race -v`
Expected: PASS for every test in the package — the two new ones, plus all pre-existing `TestRun*` tests (confirms `chatOnce`'s answer-text behavior for the ReAct loop, retries, planning, etc. is unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "feat(agent): stream reasoning deltas into session state during chatOnce"
```

---

### Task 5: Render the thinking block in the TUI

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: `session.State.InProgress() session.InProgressMessage`, `session.Message.Reasoning string`, `session.Message.ThinkDuration time.Duration` (Task 3).
- Produces: no new exported symbols — this is the final consumer. New unexported `Model` fields `lastStreamLen int` and `thinkingExpanded bool`; new unexported functions `renderThinkingBox`, `renderThinkingSummary`, `formatThinkDuration`, `tailRunes`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/app/tui/model_test.go`:

```go
func TestThinkingBoxRendersWhileStreaming(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	model.busy = true
	model.refreshViewport()

	view := model.View()
	if !strings.Contains(view, "thinking") {
		t.Fatalf("view missing thinking box:\n%s", view)
	}
	if !strings.Contains(view, "checking the auth flow") {
		t.Fatalf("view missing live reasoning text:\n%s", view)
	}
}

func TestFinishedMessageShowsCollapsedThinkingSummary(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	state.EndStreaming()
	state.AddMessage(session.RoleAssistant, "Here's the fix.")
	model.refreshViewport()

	view := model.View()
	if !strings.Contains(view, "thought for") {
		t.Fatalf("view missing collapsed thinking summary:\n%s", view)
	}
	if strings.Contains(view, "checking the auth flow") {
		t.Fatalf("full reasoning text should not be visible when collapsed:\n%s", view)
	}
}

func TestCtrlGTogglesThinkingExpansion(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	state.EndStreaming()
	state.AddMessage(session.RoleAssistant, "Here's the fix.")
	model.refreshViewport()

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	model = updated.(Model)
	if !strings.Contains(model.View(), "checking the auth flow") {
		t.Fatalf("expected expanded reasoning after Ctrl+G:\n%s", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	model = updated.(Model)
	if strings.Contains(model.View(), "checking the auth flow") {
		t.Fatalf("expected collapsed reasoning after second Ctrl+G:\n%s", model.View())
	}
}

func TestBusyTickRefreshesViewportOnReasoningGrowthAlone(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.busy = true

	state.BeginStreaming()
	state.AppendThinking("step one")
	updated, _ = model.Update(agentTickMsg{})
	model = updated.(Model)
	if !strings.Contains(model.View(), "step one") {
		t.Fatal("expected viewport to show reasoning after first tick")
	}

	state.AppendThinking(" step two")
	updated, _ = model.Update(agentTickMsg{})
	model = updated.(Model)
	if !strings.Contains(model.View(), "step one step two") {
		t.Fatalf("expected viewport to refresh on reasoning growth alone (message count unchanged):\n%s", model.View())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/... -run 'TestThinkingBoxRendersWhileStreaming|TestFinishedMessageShowsCollapsedThinkingSummary|TestCtrlGTogglesThinkingExpansion|TestBusyTickRefreshesViewportOnReasoningGrowthAlone' -v`
Expected: FAIL — the thinking box and collapsed summary never render (no such text in the view), and Ctrl+G does nothing yet.

- [ ] **Step 3: Add `Model` fields**

In `internal/app/tui/model.go`, extend the `Model` struct's dirty-tracking section:

```go
	// Viewport dirty tracking.
	lastMessageCount int
	lastStreamLen    int
	thinkingExpanded bool
```

- [ ] **Step 4: Add rendering helpers**

Add near `truncateRunes` (after it):

```go
// tailRunes returns the last limit runes of s (the most recent text),
// unlike truncateRunes which keeps the first limit runes.
func tailRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[len(runes)-limit:])
}

func formatThinkDuration(d time.Duration) string {
	return fmt.Sprintf("%.0fs", d.Seconds())
}

const thinkingBoxTailLines = 6

// renderThinkingBox renders the live "thinking" panel shown while a model
// call's reasoning is still streaming in.
func renderThinkingBox(reasoning string, width int) string {
	boxWidth := width - 2
	if boxWidth < 1 {
		boxWidth = 1
	}
	style := lipgloss.NewStyle().
		Width(boxWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(dimColor).
		Foreground(dimColor).
		Italic(true)
	tail := tailRunes(reasoning, boxWidth*thinkingBoxTailLines)
	return style.Render("thinking\n\n"+tail) + "\n\n"
}

// renderThinkingSummary renders a finished message's captured reasoning,
// either collapsed to one line or, when expanded, as a full boxed panel
// matching renderThinkingBox's style.
func renderThinkingSummary(reasoning string, duration time.Duration, expanded bool, width int) string {
	if !expanded {
		return thinkingLineStyle.Render(fmt.Sprintf("  ⚙ thought for %s", formatThinkDuration(duration))) + "\n"
	}
	boxWidth := width - 2
	if boxWidth < 1 {
		boxWidth = 1
	}
	style := lipgloss.NewStyle().
		Width(boxWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(dimColor).
		Foreground(dimColor).
		Italic(true)
	return style.Render(fmt.Sprintf("thinking (%s)\n\n%s", formatThinkDuration(duration), reasoning)) + "\n\n"
}
```

Add `thinkingLineStyle` to the existing `var (...)` style block (alongside `accentColor`/`dimColor`):

```go
	thinkingLineStyle = lipgloss.NewStyle().Foreground(dimColor).Italic(true)
```

- [ ] **Step 5: Wire the helpers into `refreshViewport`**

Replace `refreshViewport` (lines 453-469):

```go
func (m *Model) refreshViewport() {
	messages := m.state.Messages()
	inProgress := m.state.InProgress()
	streamLen := len(inProgress.Reasoning)
	if len(messages) == m.lastMessageCount && streamLen == m.lastStreamLen && !m.busy {
		return
	}
	m.lastMessageCount = len(messages)
	m.lastStreamLen = streamLen

	var b strings.Builder
	if len(messages) == 0 {
		b.WriteString("  No messages yet.\n")
	}
	for _, message := range messages {
		if message.Reasoning != "" {
			b.WriteString(renderThinkingSummary(message.Reasoning, message.ThinkDuration, m.thinkingExpanded, m.viewport.Width))
		}
		b.WriteString(fmt.Sprintf("  %s: %s\n\n", message.Role, message.Content))
	}
	if inProgress.Active {
		b.WriteString(renderThinkingBox(inProgress.Reasoning, m.viewport.Width))
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}
```

- [ ] **Step 6: Add the `Ctrl+G` toggle**

In `Update`'s `tea.KeyMsg` handling, inside the `else` branch (no pending approval) that already handles the global hotkeys (after the `if msg.Type == tea.KeyCtrlR { ... }` block, before `if m.inputFocused {`):

```go
			if msg.Type == tea.KeyCtrlG {
				m.thinkingExpanded = !m.thinkingExpanded
				m.lastMessageCount = -1 // force refreshViewport to rebuild despite unchanged message/stream state
				m.refreshViewport()
				return m, nil
			}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/app/tui/... -race -v`
Expected: PASS for every test in the package — the 4 new ones, plus all pre-existing tests (`TestApprovalBannerHasSingleBorder`, `TestRenderWhileStateMutatedDoesNotRace`, `TestBusyTickRefreshesViewport`, etc. — confirms no regression in approval rendering, layout, or the pre-existing race safety).

- [ ] **Step 8: Full-suite check**

Run: `go build ./cmd/marshal && go vet ./... && go test ./... -race`
Expected: build succeeds, vet is clean, all tests across every package pass.

- [ ] **Step 9: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): render live thinking box and collapsible thought summary"
```

---

## Self-Review

**Spec coverage:**
- §1 Provider/schema layer → Task 1.
- §2 Agent runtime → Task 4.
- §3 Session state → Task 3.
- §4 Persistence → Task 2.
- §5 TUI rendering → Task 5.
- §6 Error handling (no `reasoning_content` support, mid-stream error, DB write failure) → covered by: Task 1 Step 6 (existing tests unaffected by additive change), Task 4's `TestChatOnceEndsStreamingEvenOnProviderError`, Task 2's existing best-effort logging pattern reused unchanged.
- §7 Testing → one test task per layer, all listed above.
- Deferred items (per-message navigation, live answer streaming, non-OpenAI providers) → intentionally have no task; confirmed absent from this plan.
- Design-spec correction (`Ctrl+H` → `Ctrl+G`) → called out in Global Constraints and implemented in Task 5 Step 6.

**Placeholder scan:** no TBD/TODO markers; every step has complete, runnable code; no step says "similar to Task N" without repeating the code.

**Type consistency:** `schema.DeltaKind`/`DeltaAnswer`/`DeltaThinking` (Task 1) used identically in Task 4. `db.SaveMessage`'s 6-arg signature (Task 2) matches its call site in `session.AddMessage` (Task 3). `session.InProgressMessage{Reasoning, StartedAt, Active}` (Task 3) matches every read site in Task 4 (`state.InProgress().Reasoning`/`.Active`) and Task 5 (`inProgress.Reasoning`/`.Active`). `session.Message.Reasoning`/`.ThinkDuration` (Task 3) match Task 5's `message.Reasoning`/`message.ThinkDuration` reads exactly.
