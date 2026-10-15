# Context diet Tier 2b (LSP integration) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the language servers actually answer symbol queries and report the errors each edit caused, behind one `symbols` tool.

**Architecture:**
- **`lsp.Client`:** reports server exit and when diagnostics arrive.
- **`lsp.Manager`:** starts servers only for the repo's languages, exposes `WaitReady`, and restarts a crashed server once.
- **`lsp.Workspace` (new):** keeps documents in sync after writes and shell commands, caches diagnostics, and compares errors before and after each edit.
- **`lsp.SymbolQuery` (new):** resolves names through the server's symbol search, queries at the identifier's real column, and falls back to Marshal's index.
- **Native tools:** the write path and `shell.run` use a small `LSPSync` seam. One `symbols` tool replaces five surfaces.
- **Command checker:** runs in the active worktree.

**Tech Stack:** Go with cgo, LSP over JSON-RPC stdio, gopls, typescript-language-server, SQLite symbol index.

**Spec:** `docs/context-diet/tier2b-design.md`.

## Global Constraints

- **Base:** lands after Tier 2a (`docs/context-diet/plans/2026-10-14-tier2a-tool-surface-plan.md`), which provides `applyFilePatches`, `file.edit`, the `Internal` flag and `TestNoToolDescriptionNamesARetiredTool`.
  - Anchors are quoted code text. If one has moved, apply the change at its new location.
  - If an anchor is gone, stop and report it.
- Build and test with `CGO_ENABLED=1`. Run `gofmt -w` on changed files, and keep `go vet` clean per changed package.
- **No LSP failure may fail a tool call.** Every LSP path degrades to the index or the command checker.
- **Durations, from the spec:**
  - settle: 300 ms quiet, 1.5 s grace, 3 s maximum
  - symbol readiness wait: 10 s
  - per-request timeout: 10 s
  - repo-language walk: first 5,000 files
- **Output caps, from the spec:**
  - new errors: 10 lines, then `+N more across M files`
  - references: 50
  - hover: 1,500 characters
  - find: 20 lines
  - near matches: 5
  - definition excerpt: 5 lines
- **Keep:** `lsp.SymbolAdapter` (used by repo indexing), `lsp.Handle`'s worktree restart, and `tui.ReferenceFinder`.
- **Deletion rule (as in 2a):** fix compile errors only by deleting or redirecting callers this plan names. Never stub a deleted symbol back in.
- One commit per task, in `area: summary` style.

## Review Focus

1. **A session that enters a worktree:**
   - edits there must be diagnosed against the worktree's files
   - `symbols` must answer with worktree paths
   - the command fallback must run in the worktree

   Task 5 adds `TestCheckerRunsInGivenDir`; Task 7 adds `TestLiveWorktreeDiagnosticsUseWorktreeCopy`.
2. **A file changed by `shell.run`** (gofmt, `git checkout`) must not leave the server with stale content for an open document. Task 3 adds `TestRescanResyncsOpenDocumentChangedOnDisk`.
3. **A multi-byte character before the identifier on its line** (e.g. `// café` then a call) must still query at the right LSP column, which counts UTF-16 units. Task 4 adds `TestIdentifierColumnCountsUTF16`.
4. **A language whose server never starts** (missing binary, init failure) must answer from the index with a note, and must not spin. Task 2 adds `TestManagerDisablesAfterTwoFailures`; Task 4 adds `TestQueryFallsBackToIndexWhenNotReady`.
5. **An absolute path outside the root** (system access) must skip the LSP and use the command checker. Task 3 adds `TestBeginEditRejectsPathsOutsideRoot`.

---

### Task 1: Client reports exit and diagnostic arrival; scripted fake server

**Files:**
- Modify: `internal/lsp/client.go`
- Modify: `internal/lsp/protocol.go` (add `SymbolInformation`)
- Create: `internal/lsp/scripted_test.go` (a reusable scripted fake server, test-only)
- Test: `internal/lsp/client_test.go`

**Interfaces:**
- Produces:
  - `(*Client).Done() <-chan struct{}`, closed when the read loop ends (server exited or the stream broke).
  - `(*Client).DiagState() (seq uint64, last time.Time)`, the count and time of the last `publishDiagnostics`.
  - `(*Client).AllDiagnostics() map[string][]Diagnostic`, a copy of the cache.
  - `Request` returns `errors.New("lsp server exited")` once `Done` is closed.
  - `type SymbolInformation struct { Name string; Kind int; ContainerName string; Location Location }`, with JSON tags `name`, `kind`, `containerName`, `location`.
  - Test helper `newScripted(t *testing.T) (*Client, *scripted)`, with methods:
    - `handle(method, resultJSON string)`
    - `handleFunc(method string, fn func(params json.RawMessage) string)`
    - `publish(uri string, diags ...Diagnostic)`
    - `notes(method string) []json.RawMessage`
    - `lastRequest(method string) json.RawMessage`
    - `kill()`

- [ ] **Step 1: Write the scripted fake server.** Create `internal/lsp/scripted_test.go`:

```go
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
)

// scripted is an in-process LSP server for tests: per-method canned or
// computed results, recorded notifications and requests, and server-pushed
// publishDiagnostics.
type scripted struct {
	t        *testing.T
	mu       sync.Mutex
	handlers map[string]func(json.RawMessage) string
	notified map[string][]json.RawMessage
	requests map[string][]json.RawMessage
	out      io.WriteCloser
	outMu    sync.Mutex
}

func newScripted(t *testing.T) (*Client, *scripted) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	s := &scripted{t: t, handlers: map[string]func(json.RawMessage) string{}, notified: map[string][]json.RawMessage{}, requests: map[string][]json.RawMessage{}, out: s2cW}
	s.handle("initialize", `{"capabilities":{}}`)
	go s.loop(c2sR)
	c := newClient(c2sW, s2cR)
	t.Cleanup(func() { s.kill(); c.Close() })
	return c, s
}

func (s *scripted) handle(method, result string) {
	s.handleFunc(method, func(json.RawMessage) string { return result })
}

func (s *scripted) handleFunc(method string, fn func(json.RawMessage) string) {
	s.mu.Lock()
	s.handlers[method] = fn
	s.mu.Unlock()
}

func (s *scripted) loop(in io.Reader) {
	r := bufio.NewReader(in)
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		s.mu.Lock()
		if m.ID == nil {
			s.notified[m.Method] = append(s.notified[m.Method], m.Params)
			s.mu.Unlock()
			continue
		}
		s.requests[m.Method] = append(s.requests[m.Method], m.Params)
		fn := s.handlers[m.Method]
		s.mu.Unlock()
		result := "null"
		if fn != nil {
			result = fn(m.Params)
		}
		s.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, m.ID, result))
	}
}

func (s *scripted) write(body string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_ = writeMessage(s.out, []byte(body))
}

func (s *scripted) publish(uri string, diags ...Diagnostic) {
	if diags == nil {
		diags = []Diagnostic{}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": uri, "diagnostics": diags}})
	s.write(string(b))
}

func (s *scripted) notes(method string) []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage(nil), s.notified[method]...)
}

func (s *scripted) lastRequest(method string) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.requests[method]
	if len(rs) == 0 {
		return nil
	}
	return rs[len(rs)-1]
}

// kill closes the server→client stream, as a crashed server would.
func (s *scripted) kill() { _ = s.out.Close() }
```

- [ ] **Step 2: Write the failing client tests.** Add to `internal/lsp/client_test.go`:

```go
func TestClientDoneClosesWhenServerExits(t *testing.T) {
	c, s := newScripted(t)
	s.kill()
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done not closed after server exit")
	}
	if _, err := c.Request(context.Background(), "x", nil); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("Request after exit = %v, want server-exited error", err)
	}
}

func TestClientTracksDiagnosticArrival(t *testing.T) {
	c, s := newScripted(t)
	seq0, _ := c.DiagState()
	s.publish("file:///a.go", Diagnostic{Severity: 1, Message: "boom"})
	deadline := time.Now().Add(2 * time.Second)
	for {
		seq, last := c.DiagState()
		if seq > seq0 && !last.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("diagnostic arrival not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := c.AllDiagnostics()["file:///a.go"]; len(got) != 1 || got[0].Message != "boom" {
		t.Fatalf("AllDiagnostics = %+v", got)
	}
}

func TestInitializeAdvertisesSymbolAndSyncCapabilities(t *testing.T) {
	c, s := newScripted(t)
	if err := c.Initialize(context.Background(), "file:///r"); err != nil {
		t.Fatal(err)
	}
	p := string(s.lastRequest("initialize"))
	for _, want := range []string{`"symbol"`, `"didChangeWatchedFiles"`, `"didSave":true`, `"publishDiagnostics"`} {
		if !strings.Contains(p, want) {
			t.Errorf("initialize params missing %s: %s", want, p)
		}
	}
}
```

  Add `context`, `strings` and `time` to the imports if they're missing.

- [ ] **Step 3: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/lsp/ -run 'TestClientDone|TestClientTracks|TestInitializeAdvertises' -count=1`
Expected: compile errors (`Done`, `DiagState` and `AllDiagnostics` undefined).

- [ ] **Step 4: Implement.** In `client.go`:
  1. Add `done chan struct{}` to the struct, and `diagSeq uint64` and `lastDiag time.Time` under `diagMu`. Create `done: make(chan struct{})` in `newClient`.
  2. Make `readLoop` start with `defer close(c.done)`. In the `publishDiagnostics` branch, inside the lock, add `c.diagSeq++; c.lastDiag = time.Now()`.
  3. Add these methods:

```go
// Done is closed when the server stream ends: the server exited or broke.
func (c *Client) Done() <-chan struct{} { return c.done }

// DiagState reports how many publishDiagnostics notifications have arrived
// and when the last one did.
func (c *Client) DiagState() (uint64, time.Time) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	return c.diagSeq, c.lastDiag
}

// AllDiagnostics returns a copy of the diagnostics cache keyed by URI.
func (c *Client) AllDiagnostics() map[string][]Diagnostic {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	out := make(map[string][]Diagnostic, len(c.diags))
	for k, v := range c.diags {
		out[k] = append([]Diagnostic(nil), v...)
	}
	return out
}
```

  4. In `Request`'s `select`, add a case before `res := <-ch`:

```go
	case <-c.done:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, errors.New("lsp server exited")
```

  5. In `Initialize`, replace the `capabilities` map with:

```go
		"capabilities": map[string]any{
			"workspace": map[string]any{
				"symbol":                map[string]any{},
				"didChangeWatchedFiles": map[string]any{"dynamicRegistration": false},
			},
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": true},
				"publishDiagnostics": map[string]any{},
				"documentSymbol":     map[string]any{},
				"definition":         map[string]any{},
				"references":         map[string]any{},
				"hover":              map[string]any{},
			},
		},
```

  6. In `protocol.go`, add:

```go
// SymbolInformation is one workspace/symbol result.
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	ContainerName string   `json:"containerName"`
	Location      Location `json:"location"`
}
```

- [ ] **Step 5: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go test ./internal/lsp/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
gofmt -w internal/lsp
git add internal/lsp
git commit -m "lsp: client reports server exit and diagnostic arrival"
```

---

### Task 2: Manager starts only repo languages, waits for readiness, restarts once

**Files:**
- Modify: `internal/lsp/manager.go`, `internal/lsp/handle.go`
- Modify: `internal/app/app.go` (the `lsp.NewManager` call near `servers := lsp.DetectServers(`)
- Test: `internal/lsp/manager_test.go`

**Interfaces:**
- Consumes: `Client.Done()` (Task 1).
- Produces:
  - `DetectRepoLanguages(root string, limit int) map[string]bool`
  - `ServersForRoot(servers map[string]ServerSpec, root string) map[string]ServerSpec`
  - `(*Manager).WaitReady(ctx context.Context, lang string, timeout time.Duration) bool`
  - `(*Manager).Disabled(lang string) string`
  - `(*Manager).Languages() []string`
  - test seam field `start func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error)`
  - `serverLang(path string) string` (package-level; maps `javascript` → `typescript`)
  - `languageID(path string) string`
  - `Handle.Restart` filters servers with `ServersForRoot`.

- [ ] **Step 1: Write the failing tests.** Add to `internal/lsp/manager_test.go`:

```go
func TestServersForRootKeepsOnlyRepoLanguages(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "i.ts"), []byte(""), 0o644)
	got := ServersForRoot(map[string]ServerSpec{"go": {Command: "gopls"}, "typescript": {Command: "tls"}}, dir)
	if _, ok := got["go"]; !ok {
		t.Fatal("go server dropped")
	}
	if _, ok := got["typescript"]; ok {
		t.Fatal("typescript kept though only node_modules has .ts")
	}
}

func TestServersForRootMapsJavaScriptToTypeScript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.js"), []byte(""), 0o644)
	if _, ok := ServersForRoot(map[string]ServerSpec{"typescript": {Command: "tls"}}, dir)["typescript"]; !ok {
		t.Fatal("a .js repo must start the typescript server")
	}
}

func TestWaitReadyBecomesTrueOnceStarted(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{"go": {}}, nil)
	m.start = func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error) {
		c, _ := newScripted(t)
		time.Sleep(50 * time.Millisecond)
		return c, func() { c.Close() }, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	if !m.WaitReady(ctx, "go", 2*time.Second) {
		t.Fatal("WaitReady never became true")
	}
	if m.WaitReady(ctx, "rust", 100*time.Millisecond) {
		t.Fatal("an unconfigured language must not be ready")
	}
}

// Review focus 4.
func TestManagerDisablesAfterTwoFailures(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{"go": {Command: "gopls"}}, nil)
	var calls int32
	m.start = func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil, errors.New("initialize: boom")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for m.Disabled("go") == "" {
		if time.Now().After(deadline) {
			t.Fatal("language never disabled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("start called %d times, want 2 (one restart)", n)
	}
	start := time.Now()
	if m.WaitReady(ctx, "go", 5*time.Second) || time.Since(start) > time.Second {
		t.Fatal("WaitReady on a disabled language must return false promptly")
	}
	if !strings.Contains(m.Disabled("go"), "boom") {
		t.Fatalf("Disabled reason = %q", m.Disabled("go"))
	}
}

func TestManagerRestartsOnceAfterExit(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{"go": {}}, nil)
	var calls int32
	m.start = func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error) {
		n := atomic.AddInt32(&calls, 1)
		c, s := newScripted(t)
		if n == 1 {
			go func() { time.Sleep(50 * time.Millisecond); s.kill() }()
		}
		return c, func() { c.Close() }, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("server not restarted after exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !m.WaitReady(ctx, "go", 2*time.Second) || m.Disabled("go") != "" {
		t.Fatal("restarted server must be ready, not disabled")
	}
}
```

  Add imports as needed: `errors`, `strings`, `sync/atomic`, `context`.

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/lsp/ -run 'ServersForRoot|WaitReady|Disables|RestartsOnce' -count=1`
Expected: compile errors.

- [ ] **Step 3: Implement.** In `manager.go`:
  1. Add the language helpers:

```go
// serverLang is the language key a file's server is registered under. The
// TypeScript server also serves JavaScript.
func serverLang(path string) string {
	if l := repo.DetectLanguage(path); l == "javascript" {
		return "typescript"
	} else {
		return l
	}
}

// languageID is the LSP languageId for didOpen.
func languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsx":
		return "typescriptreact"
	case ".jsx":
		return "javascriptreact"
	}
	return repo.DetectLanguage(path)
}

// DetectRepoLanguages walks root (skipping repo.IsDefaultIgnoredDir
// directories) and returns the server languages of the first limit files.
func DetectRepoLanguages(root string, limit int) map[string]bool {
	langs := map[string]bool{}
	n := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && repo.IsDefaultIgnoredDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if n >= limit {
			return filepath.SkipAll
		}
		n++
		if l := serverLang(path); l != "" {
			langs[l] = true
		}
		return nil
	})
	return langs
}

// repoLanguageWalkLimit bounds DetectRepoLanguages at manager start.
const repoLanguageWalkLimit = 5000

// ServersForRoot keeps the servers whose language occurs in root.
func ServersForRoot(servers map[string]ServerSpec, root string) map[string]ServerSpec {
	langs := DetectRepoLanguages(root, repoLanguageWalkLimit)
	out := map[string]ServerSpec{}
	for lang, spec := range servers {
		if langs[lang] {
			out[lang] = spec
		}
	}
	return out
}
```

  2. Replace `serverState` with `type serverState struct { client *Client; ready bool; disabled string }`. Pre-populate `m.state[lang] = &serverState{}` for every server in `NewManager`. Add the field `start func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error)`, which defaults to `m.startProcess` in `NewManager`.
  3. Rename `startServer` to `startProcess(ctx, lang, spec) (*Client, func(), error)`:
     - Keep the process start, the `Initialize` handshake, the `afterInit` hook and the post-init `ctx.Err()` guard.
     - Return `client, stop, nil`, where `stop` closes the client and kills and waits for the process.
     - On failure, return the error after reaping as today.
     - It no longer writes `m.state`.
  4. Replace `Run` and `shutdownAll` with supervision:

```go
func (m *Manager) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for lang, spec := range m.servers {
		wg.Add(1)
		go func(lang string, spec ServerSpec) {
			defer wg.Done()
			m.supervise(ctx, lang, spec)
		}(lang, spec)
	}
	wg.Wait()
	return nil
}

// supervise runs one server, restarting it once if it fails or exits; a
// second failure disables the language for this manager's life.
func (m *Manager) supervise(ctx context.Context, lang string, spec ServerSpec) {
	for attempt := 1; ; attempt++ {
		client, stop, err := m.start(ctx, lang, spec)
		if ctx.Err() != nil {
			if stop != nil {
				stop()
			}
			return
		}
		reason := ""
		if err != nil {
			reason = err.Error()
		} else {
			m.setState(lang, client, true, "")
			select {
			case <-ctx.Done():
				stop()
				m.setState(lang, nil, false, "")
				return
			case <-client.Done():
				stop()
				m.setState(lang, nil, false, "")
				reason = "server exited"
			}
		}
		m.log.Warn("lsp server failed", "lang", lang, "attempt", attempt, "reason", reason)
		if attempt >= 2 {
			m.setState(lang, nil, false, strings.TrimSpace(spec.Command+": "+reason))
			return
		}
	}
}

func (m *Manager) setState(lang string, c *Client, ready bool, disabled string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state[lang] = &serverState{client: c, ready: ready, disabled: disabled}
}

// WaitReady polls until lang's server is ready, the language is disabled or
// unconfigured, timeout elapses, or ctx ends.
func (m *Manager) WaitReady(ctx context.Context, lang string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		m.mu.Lock()
		st, ok := m.state[lang]
		ready, disabled := ok && st.ready, ok && st.disabled != ""
		m.mu.Unlock()
		switch {
		case ready:
			return true
		case !ok, disabled, ctx.Err() != nil, !time.Now().Before(deadline):
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Disabled returns why lang was disabled, or "".
func (m *Manager) Disabled(lang string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.state[lang]; ok {
		return st.disabled
	}
	return ""
}

// Languages returns the configured server languages, sorted.
func (m *Manager) Languages() []string {
	out := make([]string, 0, len(m.servers))
	for l := range m.servers {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}
```

     `ServerFor` stays as it is: a ready client only.
  5. In `handle.go` `Restart`, change `NewManager(root, h.servers, h.log)` to `NewManager(root, ServersForRoot(h.servers, root), h.log)`.
  6. In `internal/app/app.go`, change `lsp.NewManager(state.Workspace().ActiveRoot, servers, state.Logger())` to `lsp.NewManager(state.Workspace().ActiveRoot, lsp.ServersForRoot(servers, state.Workspace().ActiveRoot), state.Logger())`. Keep `NewHandle(…, servers, …)` with the unfiltered map, so a restart into a different root re-filters.
  7. Update `TestStartServerCancelledAfterInit` and `TestStartServerCancelledAfterInitPostInit`:
     - Call `m.startProcess`.
     - Assert it returns an error, or a non-nil `stop`, with no live process left (`processAlive`).
     - Keep their cancellation intent. `TestRunStartsServersInParallel` keeps its assertion through `m.start`.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go test ./internal/lsp/ ./internal/app/ -count=1`
Expected: PASS, apart from the known environmental `internal/app` failures.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal/lsp internal/app
git add internal/lsp internal/app/app.go
git commit -m "lsp: start servers for repo languages only, WaitReady, restart once then disable"
```

---

### Task 3: `lsp.Workspace`: document sync, diagnostics comparison, rescan

**Files:**
- Create: `internal/lsp/workspace.go`, `internal/lsp/workspace_test.go`
- Modify: `internal/lsp/manager.go` (one `*Workspace` per manager, accessor `Workspace()`), `internal/lsp/handle.go` (add the `Sync` adapter)

**Interfaces:**
- Consumes: `Client.DiagState` and `AllDiagnostics` (Task 1); `ServerFor`, `Root` and `serverLang`/`languageID` (Task 2).
- Produces:
  - `type clientSource interface { ServerFor(lang string) (*Client, bool); Root() string }`
  - `NewWorkspace(src clientSource) *Workspace`, with fields `quiet`, `grace`, `maxWait time.Duration` settable in tests
  - `(*Workspace).DidWrite(path string) error`
  - `(*Workspace).BeginEdit(paths []string) (func(ctx context.Context) string, bool)`
  - `(*Workspace).Rescan()`
  - `type FileError struct { Path string; Line int; Message string }`
  - `NewErrors(before, after map[string][]Diagnostic) []FileError`
  - `RenderNewErrors(errs []FileError, settled bool) string`
  - `(*Manager).Workspace() *Workspace`
  - `type Sync struct{ h *Handle }`, `NewSync(h *Handle) *Sync`. Both `(*Sync).BeginEdit` and `(*Sync).Rescan` are nil-safe and delegate to the current manager's workspace.

- [ ] **Step 1: Write the failing tests.** Create `internal/lsp/workspace_test.go`:

```go
package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	root    string
	clients map[string]*Client
}

func (f *fakeSource) ServerFor(lang string) (*Client, bool) { c, ok := f.clients[lang]; return c, ok }
func (f *fakeSource) Root() string                          { return f.root }

func newTestWorkspace(t *testing.T) (*Workspace, *scripted, string) {
	t.Helper()
	root := t.TempDir()
	c, s := newScripted(t)
	w := NewWorkspace(&fakeSource{root: root, clients: map[string]*Client{"go": c}})
	w.quiet, w.grace, w.maxWait = 50*time.Millisecond, 200*time.Millisecond, time.Second
	return w, s, root
}

func TestNewErrorsIgnoresLineShiftsAndPreexisting(t *testing.T) {
	before := map[string][]Diagnostic{
		"file:///r/a.go": {{Severity: 1, Message: "old", Range: Range{Start: Position{Line: 3}}}},
		"file:///r/b.go": {{Severity: 1, Message: "elsewhere"}},
	}
	after := map[string][]Diagnostic{
		"file:///r/a.go": {{Severity: 1, Message: "old", Range: Range{Start: Position{Line: 9}}}, {Severity: 1, Message: "new one", Range: Range{Start: Position{Line: 4}}}},
		"file:///r/b.go": {{Severity: 1, Message: "elsewhere"}},
		"file:///r/c.go": {{Severity: 2, Message: "just a warning"}},
	}
	got := NewErrors(before, after)
	if len(got) != 1 || got[0].Message != "new one" || got[0].Line != 5 {
		t.Fatalf("NewErrors = %+v, want only the new error at line 5", got)
	}
}

func TestRenderNewErrorsCapsAndCounts(t *testing.T) {
	var errs []FileError
	for i := 0; i < 13; i++ {
		errs = append(errs, FileError{Path: "p" + string(rune('a'+i%3)) + ".go", Line: i + 1, Message: "x"})
	}
	out := RenderNewErrors(errs, true)
	if strings.Count(out, ": x") != 10 || !strings.Contains(out, "+3 more across 3 files") {
		t.Fatalf("render = %q", out)
	}
	if RenderNewErrors(nil, true) != "diagnostics: no new errors" {
		t.Fatal("empty render wrong")
	}
	if !strings.Contains(RenderNewErrors(nil, false), "still settling") {
		t.Fatal("unsettled note missing")
	}
}

func TestBeginEditReportsErrorCausedInAnotherFile(t *testing.T) {
	w, s, root := newTestWorkspace(t)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	finish, ok := w.BeginEdit([]string{"a.go"})
	if !ok {
		t.Fatal("BeginEdit not ok with a ready server")
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		s.publish(toFileURI(root, "b.go"), Diagnostic{Severity: 1, Message: "not enough arguments in call to a.Add", Range: Range{Start: Position{Line: 6}}})
	}()
	out := finish(context.Background())
	if !strings.Contains(out, "b.go:7: not enough arguments") {
		t.Fatalf("finish = %q", out)
	}
	if len(s.notes("textDocument/didOpen")) != 1 || len(s.notes("textDocument/didSave")) != 1 {
		t.Fatal("write must be synced with didOpen + didSave")
	}
}

func TestBeginEditCleanEditReturnsAfterGrace(t *testing.T) {
	w, _, root := newTestWorkspace(t)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	finish, _ := w.BeginEdit([]string{"a.go"})
	start := time.Now()
	out := finish(context.Background())
	if out != "diagnostics: no new errors" || time.Since(start) > 600*time.Millisecond {
		t.Fatalf("clean edit: %q after %v", out, time.Since(start))
	}
}

func TestSecondWriteSendsDidChange(t *testing.T) {
	w, s, root := newTestWorkspace(t)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	_ = w.DidWrite("a.go")
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nvar x = 1\n"), 0o644)
	_ = w.DidWrite("a.go")
	changes := s.notes("textDocument/didChange")
	if len(changes) != 1 || !strings.Contains(string(changes[0]), "var x = 1") {
		t.Fatalf("didChange = %s", changes)
	}
}

// Review focus 5.
func TestBeginEditRejectsPathsOutsideRoot(t *testing.T) {
	w, _, _ := newTestWorkspace(t)
	if _, ok := w.BeginEdit([]string{"/etc/hosts"}); ok {
		t.Fatal("a path outside the root must not use the LSP")
	}
	if _, ok := w.BeginEdit([]string{"notes.txt"}); ok {
		t.Fatal("a language with no server must not use the LSP")
	}
}

// Review focus 2.
func TestRescanResyncsOpenDocumentChangedOnDisk(t *testing.T) {
	w, s, root := newTestWorkspace(t)
	p := filepath.Join(root, "a.go")
	os.WriteFile(p, []byte("package a\n"), 0o644)
	_ = w.DidWrite("a.go")
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package a\n"), 0o644)
	time.Sleep(20 * time.Millisecond)
	os.WriteFile(p, []byte("package a // gofmt'd\n"), 0o644)
	future := time.Now().Add(time.Second)
	os.Chtimes(p, future, future)
	w.Rescan()
	time.Sleep(50 * time.Millisecond)
	if ch := s.notes("textDocument/didChange"); len(ch) != 1 || !strings.Contains(string(ch[0]), "gofmt'd") {
		t.Fatalf("open doc not resynced: %s", ch)
	}
	var sawB bool
	for _, n := range s.notes("workspace/didChangeWatchedFiles") {
		var p struct{ Changes []struct{ URI string } }
		_ = json.Unmarshal(n, &p)
		for _, c := range p.Changes {
			sawB = sawB || strings.HasSuffix(c.URI, "/b.go")
		}
	}
	if !sawB {
		t.Fatal("unopened changed file not reported via didChangeWatchedFiles")
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/lsp/ -run 'NewErrors|RenderNewErrors|BeginEdit|SecondWrite|Rescan' -count=1`
Expected: compile errors.

- [ ] **Step 3: Implement `internal/lsp/workspace.go`:**

```go
package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/repo"
)

type clientSource interface {
	ServerFor(lang string) (*Client, bool)
	Root() string
}

type openDoc struct {
	version int
	mtime   time.Time
}

// Workspace keeps a manager's servers in sync with files on disk and turns
// published diagnostics into "errors this edit caused".
type Workspace struct {
	src                   clientSource
	quiet, grace, maxWait time.Duration

	mu       sync.Mutex
	open     map[string]openDoc
	lastScan time.Time
}

func NewWorkspace(src clientSource) *Workspace {
	return &Workspace{src: src, quiet: 300 * time.Millisecond, grace: 1500 * time.Millisecond, maxWait: 3 * time.Second, open: map[string]openDoc{}, lastScan: time.Now()}
}

// FileError is one error diagnostic, 1-based line, path relative to root.
type FileError struct {
	Path    string
	Line    int
	Message string
}

func (w *Workspace) abs(path string) (string, bool) {
	root := w.src.Root()
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, path)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

// DidWrite tells path's server about the bytes now on disk.
func (w *Workspace) DidWrite(path string) error {
	abs, ok := w.abs(path)
	if !ok {
		return fmt.Errorf("lsp: %s is outside the workspace root", path)
	}
	c, ok := w.src.ServerFor(serverLang(abs))
	if !ok {
		return fmt.Errorf("lsp: no ready server for %s", path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	w.mu.Lock()
	doc, isOpen := w.open[abs]
	doc.version++
	doc.mtime = info.ModTime()
	w.open[abs] = doc
	w.mu.Unlock()
	uri := toFileURI("", abs)
	if !isOpen {
		_ = c.Notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": languageID(abs), "version": doc.version, "text": string(data)}})
	} else {
		_ = c.Notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": doc.version}, "contentChanges": []any{map[string]any{"text": string(data)}}})
	}
	return c.Notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}})
}

// clientsFor returns the distinct ready clients covering every path, or
// false when any path is outside the root or has no ready server.
func (w *Workspace) clientsFor(paths []string) ([]*Client, bool) {
	seen := map[*Client]bool{}
	var out []*Client
	for _, p := range paths {
		abs, ok := w.abs(p)
		if !ok {
			return nil, false
		}
		c, ok := w.src.ServerFor(serverLang(abs))
		if !ok {
			return nil, false
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, len(out) > 0
}

func snapshot(clients []*Client) map[string][]Diagnostic {
	out := map[string][]Diagnostic{}
	for _, c := range clients {
		for uri, ds := range c.AllDiagnostics() {
			out[uri] = append(out[uri], ds...)
		}
	}
	return out
}

// settle waits for the servers to finish reacting to writes made at since.
func (w *Workspace) settle(ctx context.Context, clients []*Client, since time.Time) bool {
	for {
		now := time.Now()
		var latest time.Time
		for _, c := range clients {
			if _, last := c.DiagState(); last.After(latest) {
				latest = last
			}
		}
		switch {
		case latest.After(since) && now.Sub(latest) >= w.quiet:
			return true
		case !latest.After(since) && now.Sub(since) >= w.grace:
			return true
		case now.Sub(since) >= w.maxWait, ctx.Err() != nil:
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// BeginEdit snapshots errors before a write to paths. ok is false when any
// path is outside the root or lacks a ready server; the caller then uses the
// command checker. finish syncs the written files, waits for the servers to
// settle and renders the errors the edit introduced, in any file.
func (w *Workspace) BeginEdit(paths []string) (func(ctx context.Context) string, bool) {
	clients, ok := w.clientsFor(paths)
	if !ok {
		return nil, false
	}
	before := snapshot(clients)
	return func(ctx context.Context) string {
		since := time.Now()
		for _, p := range paths {
			_ = w.DidWrite(p)
		}
		settled := w.settle(ctx, clients, since)
		return RenderNewErrors(w.relative(NewErrors(before, snapshot(clients))), settled)
	}, true
}

func (w *Workspace) relative(errs []FileError) []FileError {
	root := w.src.Root()
	for i := range errs {
		if rel, err := filepath.Rel(root, errs[i].Path); err == nil && !strings.HasPrefix(rel, "..") {
			errs[i].Path = rel
		}
	}
	return errs
}

// NewErrors returns errors in after that before did not have, matching by
// (file, message) as a multiset so a line shift is not a new error.
func NewErrors(before, after map[string][]Diagnostic) []FileError {
	uris := make([]string, 0, len(after))
	for uri := range after {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	var out []FileError
	for _, uri := range uris {
		seen := map[string]int{}
		for _, d := range before[uri] {
			if d.Severity == 1 {
				seen[d.Message]++
			}
		}
		path, err := fromFileURI(uri, "")
		if err != nil {
			path = uri
		}
		for _, d := range after[uri] {
			if d.Severity != 1 {
				continue
			}
			if seen[d.Message] > 0 {
				seen[d.Message]--
				continue
			}
			out = append(out, FileError{Path: path, Line: d.Range.Start.Line + 1, Message: d.Message})
		}
	}
	return out
}

const maxRenderedErrors = 10

// RenderNewErrors formats NewErrors output for a tool result.
func RenderNewErrors(errs []FileError, settled bool) string {
	note := ""
	if !settled {
		note = " (diagnostics still settling)"
	}
	if len(errs) == 0 {
		return "diagnostics: no new errors" + note
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diagnostics: %d new error(s)%s\n", len(errs), note)
	for i, e := range errs {
		if i == maxRenderedErrors {
			files := map[string]bool{}
			for _, r := range errs[i:] {
				files[r.Path] = true
			}
			fmt.Fprintf(&b, "+%d more across %d files\n", len(errs)-i, len(files))
			break
		}
		fmt.Fprintf(&b, "%s:%d: %s\n", e.Path, e.Line, e.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Rescan reports files changed on disk since the last scan (by shell
// commands, git, formatters) to their servers: open documents are re-sent,
// others announced via workspace/didChangeWatchedFiles.
func (w *Workspace) Rescan() {
	root := w.src.Root()
	w.mu.Lock()
	cutoff := w.lastScan
	w.lastScan = time.Now()
	w.mu.Unlock()
	changes := map[*Client][]any{}
	var reopen []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && repo.IsDefaultIgnoredDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		c, ok := w.src.ServerFor(serverLang(path))
		if !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().After(cutoff) {
			return nil
		}
		w.mu.Lock()
		doc, isOpen := w.open[path]
		w.mu.Unlock()
		if isOpen {
			if info.ModTime().After(doc.mtime) {
				reopen = append(reopen, path)
			}
			return nil
		}
		changes[c] = append(changes[c], map[string]any{"uri": toFileURI("", path), "type": 2})
		return nil
	})
	for _, p := range reopen {
		_ = w.DidWrite(p)
	}
	for c, ch := range changes {
		_ = c.Notify("workspace/didChangeWatchedFiles", map[string]any{"changes": ch})
	}
}
```

  `fromFileURI(uri, "")` exists in `adapters.go`. Task 5 deletes `DiagnosticsAdapter`, so before then move `fromFileURI`, `toFileURI`, `posParams`, `langFor`, `flattenHoverContents` and `jsonUnmarshal` into a new `internal/lsp/uri.go` unchanged, so they survive.

- [ ] **Step 4: Wire `Workspace` into `Manager` and add the `Sync` adapter.**
  - In `NewManager`, set `m.ws = NewWorkspace(m)` (`Manager` already has `ServerFor` and `Root`), and add `func (m *Manager) Workspace() *Workspace { return m.ws }`.
  - In `handle.go`:

```go
// Sync adapts a Handle to native.LSPSync: it always talks to the current
// manager's Workspace, so a worktree restart needs no rewiring.
type Sync struct{ h *Handle }

func NewSync(h *Handle) *Sync { return &Sync{h: h} }

func (s *Sync) BeginEdit(paths []string) (func(ctx context.Context) string, bool) {
	if s == nil || s.h == nil || s.h.Get() == nil {
		return nil, false
	}
	return s.h.Get().Workspace().BeginEdit(paths)
}

func (s *Sync) Rescan() {
	if s == nil || s.h == nil || s.h.Get() == nil {
		return
	}
	s.h.Get().Workspace().Rescan()
}
```

- [ ] **Step 5: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go test ./internal/lsp/ -count=1 -race`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
gofmt -w internal/lsp
git add internal/lsp
git commit -m "lsp: add Workspace for document sync, edit diagnostics and rescan"
```

---

### Task 4: `lsp.SymbolQuery`

**Files:**
- Create: `internal/lsp/symbols_query.go`, `internal/lsp/symbols_query_test.go`

**Interfaces:**
- Consumes: `Manager.WaitReady`/`Languages`/`Disabled`/`ServerFor` (Task 2), `SymbolInformation` (Task 1), `flattenHoverContents` and `toFileURI`/`fromFileURI` (`uri.go`, Task 3).
- Produces:
  - `type SymbolRequest struct { Op, Name, Path string; Line int }`
  - `type SymbolResult struct { Summary, Content string }`
  - `type SymbolIndex interface { FindSymbols(projectID int64, name, kind string, limit int) ([]db.Symbol, error) }`
  - `NewSymbolQuery(h *Handle, index SymbolIndex, projectID int64) *SymbolQuery`. `h` and `index` may each be nil. The test field `wait time.Duration` defaults to 10 s.
  - `(*SymbolQuery).Query(ctx context.Context, req SymbolRequest) (SymbolResult, error)`
  - `(*SymbolQuery).References(ctx context.Context, filePath string, line, col int) ([]string, bool)` (positional; satisfies `tui.ReferenceFinder`)
  - `identifierColumn(lineText, name string) int` (UTF-16 offset, or -1)

- [ ] **Step 1: Write the failing tests.** Create `internal/lsp/symbols_query_test.go`. It drives a `Handle` whose manager's `start` returns a scripted client:

```go
package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
)

func newQueryHarness(t *testing.T, files map[string]string) (*SymbolQuery, *scripted, string) {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	c, s := newScripted(t)
	m := NewManager(root, map[string]ServerSpec{"go": {}}, nil)
	m.start = func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error) { return c, func() {}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Run(ctx)
	if !m.WaitReady(ctx, "go", 2*time.Second) {
		t.Fatal("harness server not ready")
	}
	q := NewSymbolQuery(NewHandle(m, nil, nil), nil, 0)
	q.wait = 200 * time.Millisecond
	return q, s, root
}

func symJSON(root, name, container, path string, line int) string {
	return fmt.Sprintf(`{"name":%q,"kind":6,"containerName":%q,"location":{"uri":%q,"range":{"start":{"line":%d,"character":0},"end":{"line":%d,"character":1}}}}`,
		name, container, toFileURI(root, path), line, line)
}

func TestDefinitionByQualifiedNameQueriesIdentifierColumn(t *testing.T) {
	src := "package a\n\ntype Runner struct{}\n\nfunc (r *Runner) RunTask() {}\n"
	q, s, root := newQueryHarness(t, map[string]string{"a/r.go": src})
	s.handle("workspace/symbol", "["+symJSON(root, "RunTask", "Runner", "a/r.go", 4)+"]")
	s.handle("textDocument/definition", `[{"uri":"`+toFileURI(root, "a/r.go")+`","range":{"start":{"line":4,"character":17},"end":{"line":4,"character":24}}}]`)
	res, err := q.Query(context.Background(), SymbolRequest{Op: "definition", Name: "Runner.RunTask"})
	if err != nil {
		t.Fatal(err)
	}
	var p struct{ Position Position }
	json.Unmarshal(s.lastRequest("textDocument/definition"), &p)
	if p.Position.Line != 4 || p.Position.Character != 17 {
		t.Fatalf("definition queried at %+v, want line 4 char 17 (the identifier)", p.Position)
	}
	if !strings.Contains(res.Content, "a/r.go:5") || !strings.Contains(res.Content, "func (r *Runner) RunTask()") {
		t.Fatalf("result = %q", res.Content)
	}
}

func TestAmbiguousNameListsCandidates(t *testing.T) {
	q, s, root := newQueryHarness(t, map[string]string{"a.go": "package a\nfunc Parse() {}\n", "b.go": "package a\nfunc Parse() {}\n"})
	s.handle("workspace/symbol", "["+symJSON(root, "Parse", "", "a.go", 1)+","+symJSON(root, "Parse", "", "b.go", 1)+"]")
	res, err := q.Query(context.Background(), SymbolRequest{Op: "definition", Name: "Parse"})
	if err != nil || res.Summary != "ambiguous" || !strings.Contains(res.Content, "a.go:2") || !strings.Contains(res.Content, "b.go:2") {
		t.Fatalf("ambiguous = %+v, %v", res, err)
	}
}

func TestUnknownNameOffersNearMatches(t *testing.T) {
	q, s, root := newQueryHarness(t, map[string]string{"a.go": "package a\nfunc ParseAll() {}\n"})
	s.handleFunc("workspace/symbol", func(p json.RawMessage) string {
		if strings.Contains(string(p), `"Parse"`) {
			return "[" + symJSON(root, "ParseAll", "", "a.go", 1) + "]"
		}
		return "[]"
	})
	res, _ := q.Query(context.Background(), SymbolRequest{Op: "definition", Name: "Parse"})
	if !strings.Contains(res.Content, "no symbol named Parse") || !strings.Contains(res.Content, "ParseAll") {
		t.Fatalf("unknown = %q", res.Content)
	}
}

func TestPathLineAddressingSkipsWorkspaceSymbol(t *testing.T) {
	q, s, root := newQueryHarness(t, map[string]string{"a.go": "package a\n\nvar total = sum(1, 2)\n"})
	s.handle("textDocument/references", `[{"uri":"`+toFileURI(root, "a.go")+`","range":{"start":{"line":2,"character":12}}}]`)
	res, err := q.Query(context.Background(), SymbolRequest{Op: "references", Name: "sum", Path: "a.go", Line: 3})
	if err != nil || s.lastRequest("workspace/symbol") != nil {
		t.Fatalf("path+line must not search symbols: %v", err)
	}
	var p struct{ Position Position }
	json.Unmarshal(s.lastRequest("textDocument/references"), &p)
	if p.Position.Line != 2 || p.Position.Character != 12 || !strings.Contains(res.Content, "a.go:3") {
		t.Fatalf("position %+v, result %q", p.Position, res.Content)
	}
}

// Review focus 3.
func TestIdentifierColumnCountsUTF16(t *testing.T) {
	if got := identifierColumn(`	x := "café"; y := sum(x)`, "sum"); got != 19 {
		t.Fatalf("identifierColumn = %d, want 19 (tab=1, é=1 UTF-16 unit)", got)
	}
	if identifierColumn("summary := 1", "sum") != -1 {
		t.Fatal("must match whole identifiers only")
	}
}

type fakeIndex struct{ syms []db.Symbol }

func (f fakeIndex) FindSymbols(_ int64, name, _ string, _ int) ([]db.Symbol, error) {
	var out []db.Symbol
	for _, s := range f.syms {
		if strings.Contains(s.Name, name) {
			out = append(out, s)
		}
	}
	return out, nil
}

// Review focus 4.
func TestQueryFallsBackToIndexWhenNotReady(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc Parse() {}\n"), 0o644)
	m := NewManager(root, map[string]ServerSpec{"go": {}}, nil)
	m.start = func(ctx context.Context, _ string, _ ServerSpec) (*Client, func(), error) { <-ctx.Done(); return nil, nil, ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	q := NewSymbolQuery(NewHandle(m, nil, nil), fakeIndex{syms: []db.Symbol{{Name: "Parse", Kind: "function", FilePath: "a.go", LineStart: 3}}}, 1)
	q.wait = 100 * time.Millisecond
	res, err := q.Query(context.Background(), SymbolRequest{Op: "definition", Name: "Parse"})
	if err != nil || !strings.Contains(res.Content, "a.go:3") || !strings.Contains(res.Content, "answered from index") {
		t.Fatalf("fallback = %+v, %v", res, err)
	}
	res, _ = q.Query(context.Background(), SymbolRequest{Op: "references", Name: "Parse"})
	if !strings.Contains(res.Content, "need a language server") {
		t.Fatalf("references without a server = %q", res.Content)
	}
}

func TestFindWithoutHandleUsesIndex(t *testing.T) {
	q := NewSymbolQuery(nil, fakeIndex{syms: []db.Symbol{{Name: "ParseAll", Kind: "function", FilePath: "x.go", LineStart: 9}}}, 1)
	res, err := q.Query(context.Background(), SymbolRequest{Op: "find", Name: "Parse"})
	if err != nil || !strings.Contains(res.Content, "x.go:9 function ParseAll") {
		t.Fatalf("find = %+v, %v", res, err)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/lsp/ -run 'Definition|Ambiguous|Unknown|PathLine|IdentifierColumn|FallsBack|FindWithout' -count=1`
Expected: compile errors.

- [ ] **Step 3: Implement `internal/lsp/symbols_query.go`.** Write it to these rules, each of which a Step 1 test pins:
  1. **`Query` validation:**
     - `op` defaults to `definition`. It must be one of `find`, `definition`, `references` or `hover`; otherwise return the error `symbols: op must be find, definition, references or hover`.
     - `name` is required (error `symbols: name is required`).
  2. **Root and clients:**
     - `root := h.Root()` when `h != nil`.
     - With `Path`, use that file's `serverLang`; otherwise use every language from `h.Get().Languages()`.
     - For each language, `ready := h.Get().WaitReady(ctx, lang, q.wait)`, and collect `ServerFor` clients.
     - `note` stays empty when ready. Otherwise it's `(language server still starting; answered from index)`, or `(<Disabled reason>; answered from index)` when `Disabled(lang)` is set.
  3. **`find`:**
     - With clients: `workspace/symbol` `{"query": name}` on each. Render up to 20 lines of `path:line kind name`, with paths relative to `root` and `kind` from a small map (5 class, 6 method, 8 field, 10 enum, 11 interface, 12 function, 13 variable, 14 constant, 23 struct; anything else `symbol`).
     - Without clients: `index.FindSymbols(projectID, name, "", 20)`, rendered the same way with the index `Kind`.
     - Summary: `N symbols`.
  4. **Resolution** (all other ops) produces `(path, line0, col)`:
     - **`Path` and `Line` given:** read line `Line` of `root/Path`. `col := identifierColumn(text, lastSegment(Name))`, where `lastSegment` takes the part after the final `.`. If `col < 0`, return the result `no identifier <name> on <path>:<line>` (not an error).
     - **Otherwise, with clients:** call `workspace/symbol` with `{"query": base}`, where `base = lastSegment(Name)` and `container` is the part before the last `.` (empty if none). Keep a match when either holds:
       - `s.Name == Name`
       - `s.Name == base` and (`container == ""` or `strings.HasSuffix(s.ContainerName, container)` or `strings.Contains(s.ContainerName, container)`)

       Then filter by `Path` when given (relative path equality).
       - One match: `line0 = location.range.start.line`. Read that line from disk; `col = identifierColumn(text, base)`, falling back to `range.start.character` when -1.
       - Several matches: result `Summary: "ambiguous"`, `Content: "ambiguous symbol; pass path or path+line:\n  path:line\n…"`.
       - Zero matches: go to the index branch.
     - **Index branch (also used without clients):**
       - `index.FindSymbols(projectID, base, "", 50)` filtered by exact `Name == base`, by `Receiver` containing `container` when set, and by `Path`.
       - Skip this branch entirely when `index` is nil.
       - Exactly one hit gives `line0 = LineStart-1`, with `col` from `identifierColumn`.
       - Otherwise return `Summary: "not found"`, `Content: "no symbol named <Name>"`, plus up to 5 near matches (fuzzy `workspace/symbol` names when clients exist, else index names), listed as `\nclose matches: a, b, c`.
  5. **Operations** at `(path, line0, col)`, using `posParams(root, path, line0, col, extra)` with a 10 s request context:
     - **`definition`:** `textDocument/definition`, accepting an array or a single `Location`. For up to 5 locations, render `rel:line` followed by up to 5 source lines starting at that line. With no client, render the index hit the same way, plus `note`.
     - **`references`:** `textDocument/references` with `{"context":{"includeDeclaration":false}}`. Render up to 50 `rel:line` entries, then `… N more`. Summary: `N reference(s)`. With no client, return `references need a language server for <lang> <note>`.
     - **`hover`:** `textDocument/hover`, `flattenHoverContents`, truncated to 1,500 characters. With no client, return `hover needs a language server for <lang> <note>`.
     - Append `note` to `Content` (after a blank line) when it's non-empty.
  6. **`References(ctx, filePath, line, col)`:** port `QueryAdapter.locations` for `textDocument/references` unchanged, so the TUI's blast-radius lookup keeps its behaviour.
  7. **`identifierColumn`:** find `name` bounded by non-identifier runes on both sides (identifier runes are letters, digits and `_`). Return the UTF-16 length of the prefix (count `utf16.RuneLen` per rune), or -1.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go test ./internal/lsp/ -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal/lsp
git add internal/lsp
git commit -m "lsp: add SymbolQuery resolving names through the server with index fallback"
```

---

### Task 5: Edit diagnostics through `Workspace`; command checker runs in the active root; shell rescans

**Files:**
- Modify: `internal/diagnostics/checkers.go` (`Check(dir, files, language)`, runner takes `dir`, remove `LSPSource`/`SetLSPSource`)
- Modify: `internal/diagnostics/checkers_test.go`
- Modify: `internal/tools/native/native.go`:
  - `Options`: add `LSPSync LSPSync`; remove `LSPSource`
  - `toolSet`: add `lspSync`
- Modify: `internal/tools/native/diagnostics.go` (`newDiagnosticsChecker`: drop the `SetLSPSource` wiring)
- Modify: `internal/tools/native/file.go`:
  - `applyFilePatches`: `BeginEdit` before the apply loop
  - the `file.write` handler: same
  - both diagnostics call sites
- Modify: `internal/tools/native/command.go` (`shell.run` foreground: `Rescan` after `runShellCommand`; the internal `test.run` too)
- Modify: `internal/app/app.go`:
  - `nativeOpts.LSPSync = lsp.NewSync(lspHandle)`
  - delete `nativeOpts.LSPSource = …`
  - job-exit `Rescan`: declare `var lspSync native.LSPSync` before `jobManager.SetOnExit(`, call `if lspSync != nil { lspSync.Rescan() }` at the end of that callback, and assign `lspSync = lsp.NewSync(lspHandle)` where `lspHandle` is created
- Delete: `lsp.DiagnosticsAdapter`, `NewDiagnosticsAdapter`, `readDiagnosticsFile` and `diagnosticsFileCap` from `internal/lsp/adapters.go`, plus their tests in `adapters_test.go`
- Test: `internal/diagnostics/checkers_test.go`, `internal/tools/native/file_edit_test.go`

**Interfaces:**
- Consumes: `lsp.Sync` (Task 3).
- Produces:
  - `native.LSPSync interface { BeginEdit(paths []string) (func(ctx context.Context) string, bool); Rescan() }`
  - `diagnostics.(*Checker).Check(dir string, files []string, language string) (string, error)`
  - `diagnostics.Runner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)`

- [ ] **Step 1: Write the failing tests.** In `internal/diagnostics/checkers_test.go`:
  - Update every `c.runner = func(ctx context.Context, name string, args ...string)` to the new signature `func(ctx context.Context, dir, name string, args ...string)`, and every `c.Check(files, lang)` to `c.Check("/repo", files, lang)`.
  - Delete `fakeLSPSource`, `lspStubFunc`, `TestCheckLSPConsultsEveryFile`, `TestCheckerPrefersLSP` and the `SetLSPSource` concurrency test.
  - Add:

```go
// Review focus 1: the fallback must check the session's worktree copy.
func TestCheckerRunsInGivenDir(t *testing.T) {
	var gotDir string
	c := NewChecker(map[string]string{"go": "go vet {package}"})
	c.runner = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		gotDir = dir
		return nil, nil
	}
	if _, err := c.Check("/wt/feature", []string{"a/x.go"}, "go"); err != nil {
		t.Fatal(err)
	}
	if gotDir != "/wt/feature" {
		t.Fatalf("checker ran in %q, want the active root", gotDir)
	}
}
```

  In `internal/tools/native/file_edit_test.go`, add:

```go
type fakeSync struct {
	begun   []string
	rescans int
	ok      bool
}

func (f *fakeSync) BeginEdit(paths []string) (func(context.Context) string, bool) {
	f.begun = append(f.begun, paths...)
	if !f.ok {
		return nil, false
	}
	return func(context.Context) string { return "diagnostics: no new errors" }, true
}
func (f *fakeSync) Rescan() { f.rescans++ }

func TestFileEditReportsLSPDiagnostics(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nvar x = 1\n"), 0o644)
	sync := &fakeSync{ok: true}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, LSPSync: sync}); err != nil {
		t.Fatal(err)
	}
	res, err := invokeTool(t, reg, "file.edit", `{"path":"a.go","old_string":"var x = 1","new_string":"var x = 2"}`)
	if err != nil || !strings.Contains(res.Content, "diagnostics: no new errors") || len(sync.begun) != 1 {
		t.Fatalf("res=%+v err=%v begun=%v", res, err, sync.begun)
	}
}

func TestShellRunRescansLSP(t *testing.T) {
	sync := &fakeSync{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, LSPSync: sync}); err != nil {
		t.Fatal(err)
	}
	if _, err := invokeTool(t, reg, "shell.run", `{"command":"true"}`); err != nil {
		t.Fatal(err)
	}
	if sync.rescans != 1 {
		t.Fatalf("rescans = %d, want 1", sync.rescans)
	}
}
```

  Add `context` to that file's imports.

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/diagnostics/ ./internal/tools/native/ -run 'InGivenDir|ReportsLSPDiagnostics|RescansLSP' -count=1`
Expected: compile errors.

- [ ] **Step 3: Implement.**
  1. `checkers.go`:
     - `type Runner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)`.
     - `execRunner` sets `cmd.Dir = dir`.
     - `Check(dir string, files []string, language string)` passes `dir` to every runner call.
     - Delete the `LSPSource` interface, the `lsp` field, `SetLSPSource`, and the LSP branch at the top of `Check`. Keep the `mu` only if something else still uses it.
  2. `native.go`:
     - Add:

```go
// LSPSync is the live language-server seam (lsp.Sync): BeginEdit brackets a
// write to report the errors it caused; Rescan announces files changed by
// shell commands. Nil when no language server is configured.
type LSPSync interface {
	BeginEdit(paths []string) (func(ctx context.Context) string, bool)
	Rescan()
}
```

     - Add the `LSPSync LSPSync` option, assigned to `ts.lspSync` in `newToolSet`. Remove the `LSPSource` option and its use in `diagnostics.go`.
  3. `file.go` `applyFilePatches`: compute `paths` (the patch paths) before the `// Apply for real` loop, then add:

```go
	var finishDiag func(context.Context) string
	if t.lspSync != nil {
		if f, ok := t.lspSync.BeginEdit(paths); ok {
			finishDiag = f
		}
	}
```

     Replace the block `if t.diagnostics != nil { diag, _ := t.diagnostics.Check(paths, languageOf(paths)) … }` with:

```go
		if finishDiag != nil {
			content += "\n\n" + finishDiag(ctx)
		} else if t.diagnostics != nil {
			diag, _ := t.diagnostics.Check(t.activeRoot(), paths, languageOf(paths))
			if diag != "" {
				content += "\n\n" + diag
			}
		}
```

     Do the same in the `file.write` handler, with `[]string{args.Path}`: call `BeginEdit` before its `os.WriteFile` and replace its diagnostics block.
  4. `command.go`, `shell.run` foreground: replace `return t.runShellCommand(ctx, call.ID, args.Command, timeout)` with:

```go
		res, err := t.runShellCommand(ctx, call.ID, args.Command, timeout)
		if t.lspSync != nil {
			t.lspSync.Rescan()
		}
		return res, err
```

     Do the same in `test.run`'s handler.
  5. `app.go`: wire `LSPSync` and the job-exit rescan as listed in Files.
  6. Delete the `DiagnosticsAdapter` pieces listed in Files. `go build ./...` must pass with nothing else referencing them.

- [ ] **Step 4: Check the cgo error.** In a scratch worktree of this repo, run the fixed command-checker path by hand:

```bash
git worktree add --detach /tmp/tier2b-vet HEAD && (cd /tmp/tier2b-vet && go vet ./internal/repo); git worktree remove --force /tmp/tier2b-vet
```

  Expected: no `undefined: Node` / tree-sitter errors. If they appear, set `cmd.Env = append(scrubDiagnosticsEnv(os.Environ()), "CGO_ENABLED=1")` in `execRunner` when it's not already present, add a test asserting the env contains `CGO_ENABLED=1`, and re-run.

- [ ] **Step 5: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/diagnostics/ ./internal/tools/native/ ./internal/lsp/ ./internal/app/ -count=1`
Expected: PASS, apart from the known environmental failures.

- [ ] **Step 6: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "diagnostics: edits report LSP errors they caused; command checker runs in the active root"
```

---

### Task 6: The `symbols` tool replaces five surfaces

**Files:**
- Create: `internal/tools/native/symbols.go`, `internal/tools/native/symbols_test.go`
- Delete: `internal/tools/native/lsp_tools.go`, `lsp_tools_test.go`, `symbols_find.go`, `symbols_find_test.go`
- Modify: `internal/tools/native/diagnostics.go` (delete `diagnosticsCheckTool`; keep `newDiagnosticsChecker`)
- Modify: `internal/tools/native/search.go` (`repo.search`: delete the `kind` field, the schema property, `validSymbolKinds`, the kind branch and `finishKindResult`; update the description)
- Modify: `internal/tools/native/native.go`:
  - `Options`: delete `LSP LSPQuerier`; add `Symbols SymbolQuerier`
  - `all`: remove `symbolsFindTool`, `diagnosticsCheckTool` and the `if tools.lsp != nil { references/definition/hover }` block; add `tools.symbolsTool()` when `t.symbols != nil`
- Modify: `internal/app/app.go`:
  - delete `nativeOpts.LSP = lsp.NewQueryAdapter(lspHandle)`
  - add `nativeOpts.Symbols = lsp.NewSymbolQuery(lspHandle, database, projectID)`, set unconditionally, with `lspHandle` possibly nil
  - change `tui.WithReferenceFinder(lsp.NewQueryAdapter(rt.LSPManager))` to `tui.WithReferenceFinder(lsp.NewSymbolQuery(rt.LSPManager, nil, 0))`
- Delete: `lsp.QueryAdapter`, `NewQueryAdapter` and `locations` from `internal/lsp/adapters.go` (Task 4 ported `References`)
- Modify, replacing `symbols.find` with `symbols`:
  - `internal/agent/prompts.go`: `ruleReadBeforeEdit`; the `RoleSubtask` example becomes `{"tool": "symbols", "args": {"name": "Parse", "op": "find"}}`
  - `internal/tools/native/command.go` (`shell.run` description)
  - `internal/viewmodel/describe.go`, `internal/app/tui/toolnames.go` (add `"symbols": "Symbols"`; keep the old labels)
  - `internal/tools/registry/view.go` (`PlanWriterView`'s `"diagnostics.check"` case: drop it)
- Modify: `internal/app/app_test.go` (`TestNoToolDescriptionNamesARetiredTool`: add `"symbols.find"`, `"diagnostics.check"` and `"repo.search kind"`. Bare `definition`/`references`/`hover` aren't added because `symbols`' own description uses those words; `TestRetiredSymbolToolsAreGone` covers their absence.)
- Test: the files above

**Interfaces:**
- Consumes: `lsp.SymbolQuery`, `lsp.SymbolRequest` and `lsp.SymbolResult` (Task 4).
- Produces:
  - `native.SymbolQuerier interface { Query(ctx context.Context, req lsp.SymbolRequest) (lsp.SymbolResult, error) }`
  - tool `symbols` with schema `{name, op, path, line}`

- [ ] **Step 1: Write the failing tests.** Create `internal/tools/native/symbols_test.go`:

```go
package native

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/lsp"
	"marshal/internal/tools/registry"
)

type fakeQuerier struct{ got lsp.SymbolRequest }

func (f *fakeQuerier) Query(_ context.Context, req lsp.SymbolRequest) (lsp.SymbolResult, error) {
	f.got = req
	return lsp.SymbolResult{Summary: "1 reference", Content: "a.go:3"}, nil
}

func TestSymbolsToolPassesRequestThrough(t *testing.T) {
	q := &fakeQuerier{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, Symbols: q}); err != nil {
		t.Fatal(err)
	}
	res, err := invokeTool(t, reg, "symbols", `{"name":"Runner.RunTask","op":"references","path":"a.go","line":3}`)
	if err != nil || res.Content != "a.go:3" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q.got != (lsp.SymbolRequest{Op: "references", Name: "Runner.RunTask", Path: "a.go", Line: 3}) {
		t.Fatalf("request = %+v", q.got)
	}
}

func TestRetiredSymbolToolsAreGone(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, Symbols: &fakeQuerier{}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"symbols.find", "definition", "references", "hover", "diagnostics.check"} {
		if _, ok := reg.Lookup(name); ok {
			t.Errorf("%s still registered", name)
		}
	}
	tool, _ := reg.Lookup("repo.search")
	if strings.Contains(string(tool.Schema), `"kind"`) {
		t.Error("repo.search still has a kind parameter")
	}
}
```

  Delete the `repo.search` kind tests in `search_test.go` (`grep -n 'Kind\|kind' internal/tools/native/search_test.go`).

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/tools/native/ -run 'SymbolsTool|RetiredSymbolTools' -count=1`
Expected: compile errors (`Symbols` option undefined).

- [ ] **Step 3: Implement.** Create `internal/tools/native/symbols.go`:

```go
package native

import (
	"context"
	"encoding/json"

	"marshal/internal/lsp"
	"marshal/internal/tools/registry"
)

// SymbolQuerier answers symbol lookups (lsp.SymbolQuery: language server
// first, symbol index as fallback).
type SymbolQuerier interface {
	Query(ctx context.Context, req lsp.SymbolRequest) (lsp.SymbolResult, error)
}

type symbolsArgs struct {
	Name string `json:"name"`
	Op   string `json:"op"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

func (t *toolSet) symbolsTool() registry.Tool {
	tool := registry.Tool{
		Name: "symbols",
		Description: "Look up code symbols through the language server (falling back to the symbol index). " +
			"op: find (search by name), definition (where it is declared, with an excerpt; the default), references (where it is used), hover (signature and docs). " +
			"name may be qualified as Type.Method. Pass path, or path and line from earlier output, to pin one occurrence.",
		Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"op":{"type":"string","enum":["find","definition","references","hover"]},"path":{"type":"string"},"line":{"type":"integer"}},"required":["name"],"additionalProperties":false}`),
		Risk:   registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[symbolsArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		res, err := t.symbols.Query(ctx, lsp.SymbolRequest{Op: args.Op, Name: args.Name, Path: args.Path, Line: args.Line})
		if err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{Summary: res.Summary, Content: res.Content}, nil
	}
	return tool
}
```

  Then make the changes listed in Files:
  - `repo.search`'s new description: `Search workspace files for matching lines. Default mode=auto: bare words do a case-sensitive substring match, pattern-shaped queries are compiled as RE2. context adds 0-3 surrounding lines; results cap at 200 with guidance. Use symbols to find declarations and references.`
  - Delete `NoticeKindKnobIgnored` from `internal/tools/registry` if nothing else uses it.
  - In `shell.run`'s description: `repo.search, symbols.find, and file.read` → `repo.search, symbols, and file.read`.
  - In `ruleReadBeforeEdit`: `repo.search or symbols.find` → `repo.search or symbols`.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/tools/... ./internal/agent/ ./internal/lsp/ ./internal/app/ ./internal/app/tui/ -count=1`
Expected: PASS, apart from the known environmental failures. `TestNoToolDescriptionNamesARetiredTool` passes with the extended list.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "tools: one symbols tool replaces symbols.find, definition, references, hover and repo.search kind"
```

---

### Task 7: Live tests against real language servers

**Files:**
- Create: `internal/lsp/live_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–4.

- [ ] **Step 1: Write the live tests.** Create `internal/lsp/live_test.go`:

```go
package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireServer(t *testing.T, bin string) {
	t.Helper()
	if testing.Short() {
		t.Skip("live LSP test skipped under -short")
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("%s not installed", bin)
	}
}

func writeGoModule(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"go.mod":  "module example.com/m\n\ngo 1.22\n",
		"a/a.go":  "package a\n\nfunc Add(x, y int) int { return x + y }\n\ntype T struct{}\n\nfunc (T) Hello() string { return \"hi\" }\n",
		"b/b.go":  "package b\n\nimport \"example.com/m/a\"\n\nfunc Use() int { return a.Add(1, 2) }\n",
	}
	for p, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644)
	}
}

func startLive(t *testing.T, dir string) (*Manager, *Handle, context.Context) {
	t.Helper()
	servers := map[string]ServerSpec{"go": {Command: "gopls"}}
	m := NewManager(dir, ServersForRoot(servers, dir), nil)
	h := NewHandle(m, servers, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Run(ctx)
	if !m.WaitReady(ctx, "go", 60*time.Second) {
		t.Fatal("gopls never became ready")
	}
	return m, h, ctx
}

// warm polls until gopls has loaded the module (workspace/symbol answers).
func warm(t *testing.T, h *Handle, ctx context.Context) {
	t.Helper()
	q := NewSymbolQuery(h, nil, 0)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if res, _ := q.Query(ctx, SymbolRequest{Op: "find", Name: "Add"}); strings.Contains(res.Content, "a/a.go") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("gopls never loaded the module")
}

func TestLiveSignatureChangeReportsBrokenCaller(t *testing.T) {
	requireServer(t, "gopls")
	dir := t.TempDir()
	writeGoModule(t, dir)
	m, h, ctx := startLive(t, dir)
	warm(t, h, ctx)
	m.Workspace().maxWait = 15 * time.Second
	finish, ok := m.Workspace().BeginEdit([]string{"a/a.go"})
	if !ok {
		t.Fatal("BeginEdit not ok")
	}
	os.WriteFile(filepath.Join(dir, "a/a.go"), []byte("package a\n\nfunc Add(x int) int { return x }\n\ntype T struct{}\n\nfunc (T) Hello() string { return \"hi\" }\n"), 0o644)
	out := finish(ctx)
	if !strings.Contains(out, "b/b.go") {
		t.Fatalf("broken caller not reported: %q", out)
	}
}

func TestLiveMethodResolvesByName(t *testing.T) {
	requireServer(t, "gopls")
	dir := t.TempDir()
	writeGoModule(t, dir)
	_, h, ctx := startLive(t, dir)
	warm(t, h, ctx)
	res, err := NewSymbolQuery(h, nil, 0).Query(ctx, SymbolRequest{Op: "definition", Name: "T.Hello"})
	if err != nil || !strings.Contains(res.Content, "a/a.go:7") {
		t.Fatalf("definition = %+v, %v", res, err)
	}
}

func TestLiveReferencesCounted(t *testing.T) {
	requireServer(t, "gopls")
	dir := t.TempDir()
	writeGoModule(t, dir)
	_, h, ctx := startLive(t, dir)
	warm(t, h, ctx)
	res, err := NewSymbolQuery(h, nil, 0).Query(ctx, SymbolRequest{Op: "references", Name: "Add"})
	if err != nil || !strings.Contains(res.Content, "b/b.go:5") || res.Summary != "1 reference(s)" {
		t.Fatalf("references = %+v, %v", res, err)
	}
}

// Review focus 1.
func TestLiveWorktreeDiagnosticsUseWorktreeCopy(t *testing.T) {
	requireServer(t, "gopls")
	project, worktree := t.TempDir(), t.TempDir()
	writeGoModule(t, project)
	writeGoModule(t, worktree)
	_, h, ctx := startLive(t, project)
	newM, old := h.Restart(worktree)
	cctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	_ = old
	go newM.Run(cctx)
	if !newM.WaitReady(cctx, "go", 60*time.Second) {
		t.Fatal("worktree gopls not ready")
	}
	warm(t, h, cctx)
	newM.Workspace().maxWait = 15 * time.Second
	finish, ok := newM.Workspace().BeginEdit([]string{"a/a.go"})
	if !ok {
		t.Fatal("BeginEdit not ok in worktree")
	}
	os.WriteFile(filepath.Join(worktree, "a/a.go"), []byte("package a\n\nfunc Add(x int) int { return x }\n"), 0o644)
	out := finish(cctx)
	if !strings.Contains(out, "b/b.go") {
		t.Fatalf("worktree edit not diagnosed: %q", out)
	}
	if body, _ := os.ReadFile(filepath.Join(project, "a/a.go")); !strings.Contains(string(body), "x, y int") {
		t.Fatal("project copy must be untouched")
	}
}

func TestLiveTypeScriptDefinition(t *testing.T) {
	requireServer(t, "typescript-language-server")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{"compilerOptions":{"strict":true},"include":["*.ts"]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "lib.ts"), []byte("export function greet(n: string): string { return n }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.ts"), []byte("import { greet } from './lib'\ngreet('x')\n"), 0o644)
	servers := map[string]ServerSpec{"typescript": {Command: "typescript-language-server", Args: []string{"--stdio"}}}
	m := NewManager(dir, ServersForRoot(servers, dir), nil)
	h := NewHandle(m, servers, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	if !m.WaitReady(ctx, "typescript", 60*time.Second) {
		t.Fatal("typescript-language-server not ready")
	}
	_ = m.Workspace().DidWrite("main.ts")
	res, err := NewSymbolQuery(h, nil, 0).Query(ctx, SymbolRequest{Op: "definition", Name: "greet", Path: "main.ts", Line: 2})
	if err != nil || !strings.Contains(res.Content, "lib.ts:1") {
		t.Fatalf("ts definition = %+v, %v", res, err)
	}
}
```

- [ ] **Step 2: Run them.**

Run: `CGO_ENABLED=1 go test ./internal/lsp/ -run TestLive -count=1 -v -timeout 5m`
Expected:
- All PASS on this machine (gopls and typescript-language-server are installed).
- Under `-short`, or without the binaries, all SKIP.

If a gopls message or line differs from these assertions, check whether the code or the assertion is wrong before changing either:
- **Code bug:** fix it in the owning task's file, and add a unit test there that reproduces it.
- **Fixture assumption** (for example, a line number in `writeGoModule`): fix the assertion.

- [ ] **Step 3: Commit.**

```bash
gofmt -w internal/lsp
git add internal/lsp/live_test.go
git commit -m "lsp: live gopls and typescript-language-server tests"
```

---

### Task 8: Budget ceiling, docs, full verification

**Files:**
- Modify: `internal/app/prompt_budget_test.go` (`maxToolSchemaBytes = 15000`; add `"diagnostics.check"` and `"symbols.find"` to the banned-text list)
- Modify: `CHANGELOG.md` (`## [Unreleased]`, under `### Changed` and `### Fixed`)
- Modify: `docs/context-diet/tier2b-design.md` (append `## Measured result` and `## Post-landing check`)
- Modify: `README.md` and `docs/ARCHITECTURE.md` where they describe LSP tools or diagnostics (`grep -n -i 'lsp\|definition\|diagnostics' README.md docs/ARCHITECTURE.md`)

**Interfaces:**
- Consumes: Tasks 1–7.

- [ ] **Step 1: Lower the ceiling and run the guard.**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run TestDefaultPromptBudget -count=1 -v`
Expected: PASS, with tool schemas at or under 15,000 bytes. If it's over, report which tool is larger than expected. Don't raise the ceiling.

- [ ] **Step 2: Docs.**
  - `CHANGELOG.md`, under `### Changed`:

```markdown
- One `symbols` tool (find / definition / references / hover) replaces
  `symbols.find`, `definition`, `references`, `hover` and `repo.search kind=`.
  It resolves names through the language server, queries at the identifier's
  real position, and falls back to the symbol index with a note.
- After each edit, the tool result reports the errors that edit caused in any
  file, from the language server, instead of running `go vet` on the edited
  package. `diagnostics.check` is gone.
- Language servers start only for languages present in the repository, restart
  once after a crash, and are kept in sync after shell commands.
```

  - Under `### Fixed`:

```markdown
- The `[diagnostics]` command checker ran in Marshal's launch directory, so a
  worktree session checked the project checkout's copy and reported stale
  errors. It now runs in the session's active root.
- A clean file no longer looks like "no language server" to the diagnostics
  path.
```

  - Update the README/ARCHITECTURE passages found by the grep.
  - Append `## Measured result` to `tier2b-design.md`, with the budget numbers and the date.
  - Append `## Post-landing check` with the instruction: "after a week of real use, run the tool-call analysis from the bloat review (per-op `symbols` result summaries from `tool_calls`) and record the success rate per op here."

- [ ] **Step 3: Full verification.**

```bash
gofmt -l . | grep -v '^\.home/' ; CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./... 2>&1 | tail -60
CGO_ENABLED=1 go test ./internal/lsp/ -run TestLive -count=1 -timeout 5m
CGO_ENABLED=1 go build -o marshal ./cmd/marshal && go test ./test/usability/... -run TestScripted -timeout 5m
```

Expected: `gofmt -l` prints nothing, `go vet` is clean, everything passes apart from the known environmental failures, and the live tests pass here.

- [ ] **Step 4: Commit.**

```bash
git add internal/app/prompt_budget_test.go CHANGELOG.md docs README.md
git commit -m "docs: record the Tier 2b LSP integration and lower the schema ceiling"
```

## Integration notes

- **Order:** 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8. Tasks 3 and 4 both need `uri.go`; Task 3 creates it.
- **Subagents:** `agent.run` children receive `nativeOpts`, so they get the same `LSPSync` and `Symbols`. Confirm this in Task 6 by adding to `internal/app/app_test.go` a check that a subagent child's registry has `symbols`. Use the existing subagent-factory test pattern: `grep -n 'buildSubagentFactory' internal/app/*_test.go`.
- **Fallback-only behaviour:** with no language servers installed, `symbols` still works for `find` and `definition` via the index, and edits use the fixed command checker.
