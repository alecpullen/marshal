# LSP Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Real cross-language symbols, navigation (`definition`/`references`/`hover`), and diagnostics via LSP, layered over the tree-sitter index.

**Architecture:** A hand-rolled LSP client (JSON-RPC/stdio) + a server manager (auto-detected defaults, `[lsp]` override, implements `worker.Worker`). The index pass populates `symbols` from LSP when a server is ready (`source='lsp'`), else tree-sitter (`source='treesitter'`). Symbol-name-addressed tools resolve via the symbols table. LSP diagnostics slot behind the existing `diagnostics.Checker`.

**Tech Stack:** Go, standard library only (`encoding/json`, `bufio`, `os/exec`). Existing `internal/index` (#2/#3), `internal/worker` (#3), `internal/db`, `internal/diagnostics`.

**Spec:** [docs/superpowers/specs/2026-07-25-lsp-integration-design.md](../specs/2026-07-25-lsp-integration-design.md)

## Global Constraints

- **Depends on subsystems #2 and #3:** `index.Run` orchestrator, `worker.Worker`, the symbol index.
- **No new dependencies:** hand-roll the JSON-RPC framing and LSP type subset.
- **Local-first graceful degradation:** no server on PATH ⇒ LSP inert; symbols fall back to tree-sitter, tools return "no language server", diagnostics fall back to the command checker. Never an error.
- **Reconcile, don't duplicate:** LSP diagnostics go *behind* the existing `diagnostics.check` tool, not a competing tool.
- **Format/vet before commit:** `gofmt -w .` and `go vet ./...` must pass.

---

### Task 1: `source` column on symbols

**Files:**
- Modify: `internal/db/db.go` (`migrationColumns`)
- Modify: `internal/db/symbols.go` (Symbol.Source; SaveSymbols; scanSymbol; queries)
- Test: `internal/db/symbols_test.go`

**Interfaces:**
- Produces: `db.Symbol.Source string`; `source` persisted and read back.

- [ ] **Step 1: Write the failing test**

Append to `internal/db/symbols_test.go`:

```go
func TestSymbolSourceRoundTrip(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")
	in := []Symbol{
		{FilePath: "a.go", Kind: "function", Name: "F", Signature: "func F()", LineStart: 1, LineEnd: 2, Source: "lsp"},
		{FilePath: "a.go", Kind: "type", Name: "T", Signature: "type T struct", LineStart: 4, LineEnd: 6, Source: "treesitter"},
	}
	if err := database.SaveSymbols(pid, in); err != nil {
		t.Fatalf("SaveSymbols: %v", err)
	}
	got, err := database.GetSymbols(pid, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("GetSymbols = %d err=%v", len(got), err)
	}
	bySource := map[string]string{}
	for _, s := range got {
		bySource[s.Name] = s.Source
	}
	if bySource["F"] != "lsp" || bySource["T"] != "treesitter" {
		t.Fatalf("sources = %#v", bySource)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestSymbolSourceRoundTrip -v`
Expected: FAIL — `unknown field Source` (build error).

- [ ] **Step 3: Write minimal implementation**

In `internal/db/db.go`, append to `migrationColumns`:

```go
	{"symbols", "source", "TEXT"},
```

In `internal/db/symbols.go`:

- Add `Source string` to the `Symbol` struct.
- In `SaveSymbols`, change the column count 8 → 9: `placeholders := buildValues(len(chunk), 9)`, allocate `args` with `len(chunk)*9`, and append `s.Source` to the per-row args and `source` to the INSERT column list:

  ```go
  args = append(args, projectID, s.FilePath, s.Kind, s.Name, s.Receiver, s.Signature, s.LineStart, s.LineEnd, s.Source)
  ...
  tx.Exec(`INSERT INTO symbols (project_id, file_path, kind, name, receiver, signature, line_start, line_end, source) VALUES `+placeholders, args...)
  ```

- In `scanSymbol`, scan the extra column into `&s.Source` (using `sql.NullString` → `s.Source` to tolerate legacy NULL rows):

  ```go
  var source sql.NullString
  if err := rows.Scan(&s.ID, &s.FilePath, &s.Kind, &s.Name, &s.Receiver, &s.Signature, &s.LineStart, &s.LineEnd, &source); err != nil {
      return Symbol{}, err
  }
  s.Source = source.String
  ```

- Add `source` to the SELECT column lists in `GetSymbols` and `FindSymbols` (append `, source` after `line_end`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/ -run TestSymbol -v`
Expected: PASS (new + existing symbol tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/db/ && go vet ./internal/db/
git add internal/db/db.go internal/db/symbols.go internal/db/symbols_test.go
git commit -m "feat(db): add source column to symbols (lsp|treesitter)"
```

---

### Task 2: LSP JSON-RPC client

**Files:**
- Create: `internal/lsp/protocol.go` (type subset)
- Create: `internal/lsp/client.go` (framing + request/response + notifications)
- Test: `internal/lsp/client_test.go`

**Interfaces:**
- Produces: `func newClient(w io.Writer, r io.Reader) *Client`; `(*Client).Initialize(ctx, rootURI string) error`; `(*Client).Request(ctx, method string, params any) (json.RawMessage, error)`; `(*Client).Notify(method string, params any) error`; `(*Client).Diagnostics(uri string) []Diagnostic`; `(*Client).Close()`; protocol types.

- [ ] **Step 1: Write the failing test**

Create `internal/lsp/client_test.go`:

```go
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"
)

// fakeServer speaks the base protocol: it echoes an initialize result and
// answers a documentSymbol request with one symbol.
func fakeServer(t *testing.T, in io.Reader, out io.Writer) {
	r := bufio.NewReader(in)
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(msg, &req)
		if req.ID == nil {
			continue // notification
		}
		var result string
		switch req.Method {
		case "initialize":
			result = `{"capabilities":{}}`
		case "textDocument/documentSymbol":
			result = `[{"name":"Foo","kind":12,"range":{"start":{"line":3,"character":0},"end":{"line":5,"character":1}}}]`
		default:
			result = `null`
		}
		resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
		_ = writeMessage(out, []byte(resp))
	}
}

func TestClientInitializeAndRequest(t *testing.T) {
	cToS := newPipe()
	sToC := newPipe()
	go fakeServer(t, cToS.r, sToC.w)

	c := newClient(cToS.w, sToC.r)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///tmp"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	raw, err := c.Request(ctx, "textDocument/documentSymbol", map[string]any{})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		t.Fatalf("empty documentSymbol result: %s", raw)
	}
}
```

Add a tiny pipe helper in the test file:

```go
type pipe struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func newPipe() pipe {
	r, w := io.Pipe()
	return pipe{r: r, w: w}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestClientInitialize -v`
Expected: FAIL — `undefined: newClient` / `readMessage`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/lsp/protocol.go`:

```go
package lsp

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children"`
}
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Message  string `json:"message"`
}
type Hover struct {
	Contents json.RawMessage `json:"contents"`
}
```

Create `internal/lsp/client.go`:

```go
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// Client is one JSON-RPC connection to one language server.
type Client struct {
	w   io.Writer
	r   *bufio.Reader

	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage

	diagMu sync.Mutex
	diags  map[string][]Diagnostic

	closed chan struct{}
}

func newClient(w io.Writer, r io.Reader) *Client {
	c := &Client{
		w:       w,
		r:       bufio.NewReader(r),
		pending: map[int]chan json.RawMessage{},
		diags:   map[string][]Diagnostic{},
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Client) Close() { close(c.closed) }

func writeMessage(w io.Writer, body []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	var length int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
		if n, err := strconv.Atoi(trimHeader(line, "Content-Length:")); err == nil {
			length = n
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func trimHeader(line, key string) string {
	if len(line) >= len(key) && line[:len(key)] == key {
		v := line[len(key):]
		return trimSpace(v)
	}
	return ""
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[len(s)-1] == '\r' || s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		if s[0] == ' ' {
			s = s[1:]
			continue
		}
		s = s[:len(s)-1]
	}
	return s
}

func (c *Client) readLoop() {
	for {
		body, err := readMessage(c.r)
		if err != nil {
			return
		}
		var m struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			continue
		}
		if m.ID != nil {
			c.mu.Lock()
			ch := c.pending[*m.ID]
			delete(c.pending, *m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m.Result
			}
			continue
		}
		if m.Method == "textDocument/publishDiagnostics" {
			var p struct {
				URI         string       `json:"uri"`
				Diagnostics []Diagnostic `json:"diagnostics"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				c.diagMu.Lock()
				c.diags[p.URI] = p.Diagnostics
				c.diagMu.Unlock()
			}
		}
	}
}

// Request sends a request and waits for its response.
func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if err := writeMessage(c.w, body); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, fmt.Errorf("lsp client closed")
	case res := <-ch:
		return res, nil
	}
}

// Notify sends a notification (no response).
func (c *Client) Notify(method string, params any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	return writeMessage(c.w, body)
}

// Initialize performs the handshake.
func (c *Client) Initialize(ctx context.Context, rootURI string) error {
	_, err := c.Request(ctx, "initialize", map[string]any{
		"processId": nil,
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"documentSymbol": map[string]any{},
				"definition":     map[string]any{},
				"references":     map[string]any{},
				"hover":          map[string]any{},
			},
		},
	})
	if err != nil {
		return err
	}
	return c.Notify("initialized", map[string]any{})
}

// Diagnostics returns the last-published diagnostics for a URI.
func (c *Client) Diagnostics(uri string) []Diagnostic {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	return c.diags[uri]
}
```

Add `import "encoding/json"` to `protocol.go` for the `Hover.Contents json.RawMessage` field.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestClientInitialize -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/lsp/ && go vet ./internal/lsp/
git add internal/lsp/protocol.go internal/lsp/client.go internal/lsp/client_test.go
git commit -m "feat(lsp): add JSON-RPC/stdio client with request/notify/diagnostics"
```

---

### Task 3: documentSymbol → db.Symbol mapping

**Files:**
- Create: `internal/lsp/symbols.go`
- Test: `internal/lsp/symbols_test.go`

**Interfaces:**
- Consumes: `DocumentSymbol` (Task 2), `db.Symbol`.
- Produces: `func MapSymbols(filePath string, docSyms []DocumentSymbol) []db.Symbol`.

- [ ] **Step 1: Write the failing test**

Create `internal/lsp/symbols_test.go`:

```go
package lsp

import (
	"testing"
)

func TestMapSymbolsFlattens(t *testing.T) {
	docs := []DocumentSymbol{{
		Name: "T", Kind: 23, // Struct
		Range: Range{Start: Position{Line: 0}, End: Position{Line: 4}},
		Children: []DocumentSymbol{{
			Name: "M", Kind: 6, // Method
			Range: Range{Start: Position{Line: 1}, End: Position{Line: 3}},
		}},
	}}
	got := MapSymbols("a.go", docs)
	if len(got) != 2 {
		t.Fatalf("got %d symbols", len(got))
	}
	for _, s := range got {
		if s.Source != "lsp" || s.FilePath != "a.go" {
			t.Fatalf("symbol = %#v", s)
		}
		if s.Name == "T" && s.Kind != "type" {
			t.Fatalf("T kind = %q", s.Kind)
		}
		if s.Name == "M" && s.Kind != "method" {
			t.Fatalf("M kind = %q", s.Kind)
		}
		if s.LineStart == 0 {
			t.Fatalf("1-based line expected, got %#v", s)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestMapSymbols -v`
Expected: FAIL — `undefined: MapSymbols`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/lsp/symbols.go`:

```go
package lsp

import "marshal/internal/db"

// lspKind maps LSP SymbolKind numbers to Marshal's symbol kinds.
func lspKind(k int) string {
	switch k {
	case 12: // Function
		return "function"
	case 6: // Method
		return "method"
	case 5, 23: // Class, Struct
		return "type"
	case 11, 10: // Interface, Enum
		return "type"
	default:
		return "symbol"
	}
}

// MapSymbols flattens an LSP DocumentSymbol tree into db.Symbols tagged
// source="lsp". LSP positions are 0-based; db lines are 1-based.
func MapSymbols(filePath string, docSyms []DocumentSymbol) []db.Symbol {
	var out []db.Symbol
	var walk func(ds DocumentSymbol)
	walk = func(ds DocumentSymbol) {
		out = append(out, db.Symbol{
			FilePath:  filePath,
			Kind:      lspKind(ds.Kind),
			Name:      ds.Name,
			Signature: ds.Detail,
			LineStart: ds.Range.Start.Line + 1,
			LineEnd:   ds.Range.End.Line + 1,
			Source:    "lsp",
		})
		for _, c := range ds.Children {
			walk(c)
		}
	}
	for _, ds := range docSyms {
		walk(ds)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestMapSymbols -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/lsp/ && go vet ./internal/lsp/
git add internal/lsp/symbols.go internal/lsp/symbols_test.go
git commit -m "feat(lsp): map documentSymbol trees to db.Symbol (source=lsp)"
```

---

### Task 4: server manager

**Files:**
- Create: `internal/lsp/manager.go`
- Test: `internal/lsp/manager_test.go`

**Interfaces:**
- Consumes: `newClient`, `worker.Worker`, `config` (Task 5 types — build config first or stub locally; see note), `os/exec`.
- Produces: `type Manager`, `func NewManager(root string, servers map[string]ServerSpec, log *slog.Logger) *Manager`, `(*Manager).ServerFor(lang string) (*Client, bool)`, `(*Manager).Run(ctx) error` (implements `worker.Worker`), `type ServerSpec{ Command string; Args []string }`, `var DefaultServers map[string]ServerSpec`.

Note: to keep this task independent of config, `NewManager` takes a pre-resolved `map[string]ServerSpec` (Task 9 wiring builds it from config + `DefaultServers` + PATH detection). `DetectServers(configured, disabled)` is a pure helper tested here.

- [ ] **Step 1: Write the failing test**

Create `internal/lsp/manager_test.go`:

```go
package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectServersOnPath(t *testing.T) {
	dir := t.TempDir()
	// Create a stub "gopls" executable on a temp PATH.
	stub := filepath.Join(dir, "gopls")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got := DetectServers(map[string]ServerSpec{}, map[string]bool{})
	if _, ok := got["go"]; !ok {
		t.Fatalf("expected go server detected on PATH, got %#v", got)
	}

	// Disabled language is excluded even when present.
	got = DetectServers(map[string]ServerSpec{}, map[string]bool{"go": true})
	if _, ok := got["go"]; ok {
		t.Fatal("disabled go should not be detected")
	}

	// Explicit config override for a language whose binary is not on PATH is
	// still included (user asked for it).
	got = DetectServers(map[string]ServerSpec{"python": {Command: "pyright-langserver", Args: []string{"--stdio"}}}, map[string]bool{})
	if _, ok := got["python"]; !ok {
		t.Fatal("configured python server should be included")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestDetectServers -v`
Expected: FAIL — `undefined: DetectServers`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/lsp/manager.go`:

```go
package lsp

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
)

type ServerSpec struct {
	Command string
	Args    []string
}

// DefaultServers is the built-in language→server map.
var DefaultServers = map[string]ServerSpec{
	"go":         {Command: "gopls"},
	"typescript": {Command: "typescript-language-server", Args: []string{"--stdio"}},
	"python":     {Command: "pyright-langserver", Args: []string{"--stdio"}},
	"rust":       {Command: "rust-analyzer"},
}

// DetectServers resolves the effective server map: defaults overlaid with
// configured specs, keeping a language when it is not disabled AND either it is
// explicitly configured or its default command resolves on PATH.
func DetectServers(configured map[string]ServerSpec, disabled map[string]bool) map[string]ServerSpec {
	out := map[string]ServerSpec{}
	for lang, spec := range DefaultServers {
		if disabled[lang] {
			continue
		}
		if _, err := exec.LookPath(spec.Command); err == nil {
			out[lang] = spec
		}
	}
	for lang, spec := range configured {
		if disabled[lang] {
			delete(out, lang)
			continue
		}
		out[lang] = spec // explicit config always included
	}
	return out
}

type serverState struct {
	client *Client
	ready  bool
}

type Manager struct {
	root    string
	servers map[string]ServerSpec
	log     *slog.Logger

	mu    sync.Mutex
	state map[string]*serverState
}

func NewManager(root string, servers map[string]ServerSpec, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{root: root, servers: servers, log: log, state: map[string]*serverState{}}
}

func (m *Manager) Name() string { return "lsp-manager" }

// Run spawns configured servers and supervises them until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	for lang, spec := range m.servers {
		m.startServer(ctx, lang, spec)
	}
	<-ctx.Done()
	m.shutdownAll()
	return nil
}

func (m *Manager) startServer(ctx context.Context, lang string, spec ServerSpec) {
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir = m.root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		m.log.Debug("lsp stdin", "lang", lang, "err", err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.log.Debug("lsp stdout", "lang", lang, "err", err)
		return
	}
	if err := cmd.Start(); err != nil {
		m.log.Debug("lsp start", "lang", lang, "err", err)
		return
	}
	client := newClient(stdin, stdout)
	if err := client.Initialize(ctx, "file://"+m.root); err != nil {
		m.log.Debug("lsp initialize", "lang", lang, "err", err)
		return
	}
	m.mu.Lock()
	m.state[lang] = &serverState{client: client, ready: true}
	m.mu.Unlock()
}

func (m *Manager) shutdownAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range m.state {
		if st.client != nil {
			st.client.Close()
		}
	}
}

// ServerFor returns the client for a language and whether it is ready.
func (m *Manager) ServerFor(lang string) (*Client, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[lang]
	if !ok || !st.ready {
		return nil, false
	}
	return st.client, true
}
```

Note: crash-restart with bounded backoff is a documented spec open-question; a minimal version can be added later by wrapping `startServer` in a supervised loop. This task ships spawn + ready-tracking + graceful shutdown, which the tests and downstream tasks need.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestDetectServers -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/lsp/ && go vet ./internal/lsp/
git add internal/lsp/manager.go internal/lsp/manager_test.go
git commit -m "feat(lsp): add server manager with PATH detection and lifecycle"
```

---

### Task 5: [lsp] config

**Files:**
- Modify: `internal/app/config/types.go` (`LSPConfig`, `LSPServerConfig`; add `LSP LSPConfig` to root Config)
- Test: `internal/app/config/config_test.go`

**Interfaces:**
- Produces: `config.LSPConfig{ Enabled *bool; Servers map[string]LSPServerConfig }`, `config.LSPServerConfig{ Command string; Args []string; Disabled bool }`.

- [ ] **Step 1: Write the failing test**

Append to `internal/app/config/config_test.go`:

```go
func TestLoadLSPConfig(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[lsp]
enabled = true
[lsp.servers.go]
command = "gopls"
[lsp.servers.python]
disabled = true
`)
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LSP.Servers["go"].Command != "gopls" {
		t.Fatalf("go server = %#v", cfg.LSP.Servers["go"])
	}
	if !cfg.LSP.Servers["python"].Disabled {
		t.Fatal("python should be disabled")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/config/ -run TestLoadLSPConfig -v`
Expected: FAIL — `cfg.LSP undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/app/config/types.go`, add the types and a root field (add `LSP LSPConfig \`toml:"lsp"\`` to the top-level `Config` struct):

```go
type LSPConfig struct {
	Enabled *bool                        `toml:"enabled"` // nil => true
	Servers map[string]LSPServerConfig   `toml:"servers"`
}

type LSPServerConfig struct {
	Command  string   `toml:"command"`
	Args     []string `toml:"args"`
	Disabled bool     `toml:"disabled"`
}
```

If the config uses an explicit merge step per section (see `merge.go`), add a shallow merge for `LSP` mirroring how `Providers`/`Models` are merged (whole-entry overwrite by key for `Servers`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/config/ -run TestLoadLSPConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/config/ && go vet ./internal/app/config/
git add internal/app/config/types.go internal/app/config/merge.go internal/app/config/config_test.go
git commit -m "feat(config): add [lsp] server configuration"
```

---

### Task 6: LSP symbol layering in index.Run

**Files:**
- Modify: `internal/index/run.go` (use LSP symbols when a ready server exists)
- Test: `internal/index/run_test.go`

**Interfaces:**
- Consumes: an abstraction over the manager. Add to `index.Deps` a `SymbolSource` interface so `index` does not import `lsp` directly:
  ```go
  type LSPSymbols interface {
      DocumentSymbols(ctx context.Context, lang, filePath string, content []byte) ([]db.Symbol, bool)
  }
  ```
  `bool` = whether a ready server handled it. Task 9 provides an adapter over `lsp.Manager`.
- Produces: `index.Run` prefers LSP symbols per file, else tree-sitter (Go), tagging `source`.

- [ ] **Step 1: Write the failing test**

Append to `internal/index/run_test.go`:

```go
type fakeLSP struct{ lang string }

func (f fakeLSP) DocumentSymbols(_ context.Context, lang, filePath string, _ []byte) ([]db.Symbol, bool) {
	if lang != f.lang {
		return nil, false
	}
	return []db.Symbol{{FilePath: filePath, Kind: "function", Name: "L", Signature: "fn L", LineStart: 1, LineEnd: 1, Source: "lsp"}}, true
}

func TestRunPrefersLSPSymbols(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nfunc F(){}\n"), 0o644)
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	_, err := Run(context.Background(), Deps{DB: database, Root: root, LSP: fakeLSP{lang: "go"}}, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	syms, _ := database.GetSymbols(pid, 0)
	if len(syms) == 0 {
		t.Fatal("no symbols")
	}
	for _, s := range syms {
		if s.Source != "lsp" {
			t.Fatalf("expected source=lsp, got %#v", s)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/index/ -run TestRunPrefersLSP -v`
Expected: FAIL — `unknown field LSP in Deps` / `undefined LSPSymbols`.

- [ ] **Step 3: Write minimal implementation**

In `internal/index/run.go`, add the interface and `Deps` field:

```go
type LSPSymbols interface {
	DocumentSymbols(ctx context.Context, lang, filePath string, content []byte) ([]db.Symbol, bool)
}
```

Add `LSP LSPSymbols` to `Deps`. In the symbol-extraction loop, prefer LSP:

```go
	for _, sf := range scanned {
		if sf.ReadErr != nil {
			rep.Warnings = append(rep.Warnings, sf.Path+": read error")
			continue
		}
		if deps.LSP != nil {
			if lspSyms, ok := deps.LSP.DocumentSymbols(ctx, sf.Language, sf.Path, sf.Content); ok {
				symbols = append(symbols, lspSyms...)
				symbolsByFile[sf.Path] = lspSyms
				continue
			}
		}
		if sf.Language != "go" {
			continue
		}
		fileSyms, extractErr := repo.ExtractSymbols(ctx, sf.Path, sf.Content)
		if extractErr != nil {
			rep.Warnings = append(rep.Warnings, sf.Path+": parse error")
			continue
		}
		for i := range fileSyms {
			fileSyms[i].Source = "treesitter"
		}
		symbols = append(symbols, fileSyms...)
		symbolsByFile[sf.Path] = fileSyms
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/index/ -run TestRun -v`
Expected: PASS (LSP-prefers + existing Run tests; existing tests pass `LSP: nil`).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/index/ && go vet ./internal/index/
git add internal/index/run.go internal/index/run_test.go
git commit -m "feat(index): prefer LSP symbols in the index pass, tree-sitter fallback"
```

---

### Task 7: live tools (definition/references/hover)

**Files:**
- Create: `internal/tools/native/lsp_tools.go`
- Modify: `internal/tools/native/native.go` (register + hold an LSP accessor)
- Test: `internal/tools/native/lsp_tools_test.go`

**Interfaces:**
- Consumes: `db.FindSymbols` (name resolution), an LSP accessor on the tool set. Add to `toolSet`:
  ```go
  lsp LSPQuerier // nil when LSP unavailable
  ```
  with `type LSPQuerier interface { References(ctx, filePath string, line, col int) ([]string, bool); Hover(ctx, filePath string, line, col int) (string, bool); Definition(ctx, filePath string, line, col int) ([]string, bool) }` (returns rendered `path:line` locations / hover text; `bool` = server ready).
- Produces: `definition`, `references`, `hover` tools.

- [ ] **Step 1: Write the failing test**

Create `internal/tools/native/lsp_tools_test.go` with cases: name not found → friendly message; ambiguous (two matches, no path) → candidate list; found + ready server → LSP result; found + no server → "no language server". Build the `toolSet` inline (db + projectID seeded with symbols via `SaveSymbols`; inject a fake `lsp`).

```go
type fakeLSPQ struct{ refs []string; ready bool }

func (f fakeLSPQ) References(context.Context, string, int, int) ([]string, bool) { return f.refs, f.ready }
func (f fakeLSPQ) Hover(context.Context, string, int, int) (string, bool)        { return "sig", f.ready }
func (f fakeLSPQ) Definition(context.Context, string, int, int) ([]string, bool) { return f.refs, f.ready }

func TestReferencesResolvesByName(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	_ = ts.db.SaveSymbols(ts.projectID, []db.Symbol{{FilePath: "a.go", Kind: "function", Name: "Foo", LineStart: 5, LineEnd: 7, Source: "lsp"}})
	ts.lsp = fakeLSPQ{refs: []string{"a.go:5", "b.go:9"}, ready: true}

	res, err := ts.referencesTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Foo"}`)})
	if err != nil || !strings.Contains(res.Content, "b.go:9") {
		t.Fatalf("res=%q err=%v", res.Content, err)
	}
}

func TestReferencesUnknownSymbol(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	ts.lsp = fakeLSPQ{ready: true}
	res, _ := ts.referencesTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Nope"}`)})
	if !strings.Contains(res.Content, "no indexed symbol") {
		t.Fatalf("res=%q", res.Content)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native/ -run TestReferences -v`
Expected: FAIL — `ts.referencesTool undefined`.

- [ ] **Step 3: Write minimal implementation**

Add the `lsp LSPQuerier` field + `LSPQuerier` interface to `native.go`. Create `internal/tools/native/lsp_tools.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

type LSPQuerier interface {
	Definition(ctx context.Context, filePath string, line, col int) ([]string, bool)
	References(ctx context.Context, filePath string, line, col int) ([]string, bool)
	Hover(ctx context.Context, filePath string, line, col int) (string, bool)
}

type symbolQueryArgs struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path"`
}

// resolveSymbol finds the single matching symbol, or returns a ToolResult
// describing the miss (not-found / ambiguous) with ok=false.
func (t *toolSet) resolveSymbol(name, path string) (db.Symbol, registry.ToolResult, bool) {
	matches, err := t.db.FindSymbols(t.projectID, name, "", 50)
	if err != nil {
		return db.Symbol{}, registry.ToolResult{}, false
	}
	var filtered []db.Symbol
	for _, s := range matches {
		if s.Name != name {
			continue
		}
		if path != "" && s.FilePath != path {
			continue
		}
		filtered = append(filtered, s)
	}
	switch {
	case len(filtered) == 0:
		return db.Symbol{}, registry.ToolResult{Summary: "not found",
			Content: fmt.Sprintf("no indexed symbol named %q", name)}, false
	case len(filtered) > 1:
		var b strings.Builder
		b.WriteString("ambiguous symbol; pass `path` to disambiguate:\n")
		for _, s := range filtered {
			fmt.Fprintf(&b, "  %s:%d\n", s.FilePath, s.LineStart)
		}
		return db.Symbol{}, registry.ToolResult{Summary: "ambiguous", Content: b.String()}, false
	default:
		return filtered[0], registry.ToolResult{}, true
	}
}

func symbolSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"symbol":{"type":"string"},"path":{"type":"string"}},"required":["symbol"],"additionalProperties":false}`)
}

func (t *toolSet) lspLocationsTool(name, desc string, call func(q LSPQuerier, ctx context.Context, path string, line int) ([]string, bool)) registry.Tool {
	tool := registry.Tool{Name: name, Description: desc, Schema: symbolSchema(), Risk: registry.RiskReadOnly}
	tool.Handler = func(ctx context.Context, tc registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[symbolQueryArgs](tool, tc.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		sym, miss, ok := t.resolveSymbol(args.Symbol, args.Path)
		if !ok {
			return miss, nil
		}
		if t.lsp == nil {
			return registry.ToolResult{Summary: "no lsp", Content: "no language server available for this symbol"}, nil
		}
		locs, ready := call(t.lsp, ctx, sym.FilePath, sym.LineStart-1)
		if !ready {
			return registry.ToolResult{Summary: "no lsp", Content: "no language server available for this symbol"}, nil
		}
		if len(locs) == 0 {
			return registry.ToolResult{Summary: "none", Content: "no results"}, nil
		}
		return registry.ToolResult{Summary: fmt.Sprintf("%d results", len(locs)), Content: strings.Join(locs, "\n")}, nil
	}
	return tool
}

func (t *toolSet) referencesTool() registry.Tool {
	return t.lspLocationsTool("references", "Find all references to a symbol (by name).",
		func(q LSPQuerier, ctx context.Context, path string, line int) ([]string, bool) {
			return q.References(ctx, path, line, 0)
		})
}

func (t *toolSet) definitionTool() registry.Tool {
	return t.lspLocationsTool("definition", "Find where a symbol is defined (by name).",
		func(q LSPQuerier, ctx context.Context, path string, line int) ([]string, bool) {
			return q.Definition(ctx, path, line, 0)
		})
}

func (t *toolSet) hoverTool() registry.Tool {
	tool := registry.Tool{Name: "hover", Description: "Show a symbol's type signature and documentation (by name).", Schema: symbolSchema(), Risk: registry.RiskReadOnly}
	tool.Handler = func(ctx context.Context, tc registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[symbolQueryArgs](tool, tc.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		sym, miss, ok := t.resolveSymbol(args.Symbol, args.Path)
		if !ok {
			return miss, nil
		}
		if t.lsp == nil {
			return registry.ToolResult{Summary: "no lsp", Content: "no language server available for this symbol"}, nil
		}
		text, ready := t.lsp.Hover(ctx, sym.FilePath, sym.LineStart-1, 0)
		if !ready {
			return registry.ToolResult{Summary: "no lsp", Content: "no language server available for this symbol"}, nil
		}
		return registry.ToolResult{Summary: "hover", Content: text}, nil
	}
	return tool
}
```

Register the three tools in `native.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/native/ -run 'TestReferences|TestHover|TestDefinition' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tools/native/ && go vet ./internal/tools/native/
git add internal/tools/native/lsp_tools.go internal/tools/native/native.go internal/tools/native/lsp_tools_test.go
git commit -m "feat(native): add symbol-name-addressed definition/references/hover tools"
```

---

### Task 8: LSP diagnostics behind the Checker

**Files:**
- Modify: `internal/diagnostics/checkers.go` (add an optional LSP source consulted first)
- Test: `internal/diagnostics/checkers_test.go`

**Interfaces:**
- Consumes: an injected source: `type LSPSource interface { Diagnostics(lang, filePath string) (string, bool) }` (`bool` = a ready server produced it).
- Produces: `Checker` prefers the LSP source when it returns `ok`, else runs configured commands.

- [ ] **Step 1: Write the failing test**

Append to `internal/diagnostics/checkers_test.go`:

```go
type fakeLSPSource struct{ out string; ok bool }

func (f fakeLSPSource) Diagnostics(string, string) (string, bool) { return f.out, f.ok }

func TestCheckerPrefersLSP(t *testing.T) {
	c := NewChecker(nil) // no command checkers configured
	c.SetLSPSource(fakeLSPSource{out: "a.go:1: oops", ok: true})
	out, err := c.Check([]string{"a.go"}, "go")
	if err != nil || out != "a.go:1: oops" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestCheckerFallsBackWhenNoLSP(t *testing.T) {
	c := NewChecker(nil)
	c.SetLSPSource(fakeLSPSource{ok: false})
	out, _ := c.Check([]string{"a.go"}, "go")
	if out != "" { // no commands configured, no lsp → empty (caller renders "none")
		t.Fatalf("expected empty fallback, got %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/diagnostics/ -run TestChecker -v`
Expected: FAIL — `c.SetLSPSource undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/diagnostics/checkers.go`, add the source hook to `Checker`:

```go
type LSPSource interface {
	Diagnostics(lang, filePath string) (string, bool)
}

// SetLSPSource installs an LSP diagnostics source consulted before the
// configured command checkers.
func (c *Checker) SetLSPSource(src LSPSource) { c.lsp = src }
```

Add an `lsp LSPSource` field to `Checker`, and at the top of `Check(paths, lang)`:

```go
	if c.lsp != nil && len(paths) > 0 {
		if out, ok := c.lsp.Diagnostics(lang, paths[0]); ok {
			return out, nil
		}
	}
```

(then the existing command-checker logic runs as the fallback).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/diagnostics/ -run TestChecker -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/diagnostics/ && go vet ./internal/diagnostics/
git add internal/diagnostics/checkers.go internal/diagnostics/checkers_test.go
git commit -m "feat(diagnostics): consult an LSP source before command checkers"
```

---

### Task 9: app wiring + adapters

**Files:**
- Create: `internal/lsp/adapters.go` (adapters implementing `index.LSPSymbols`, `native.LSPQuerier`, `diagnostics.LSPSource` over `*lsp.Manager`)
- Modify: `internal/app/app.go` (build manager from config, start as worker, inject adapters)
- Test: `internal/lsp/adapters_test.go`

**Interfaces:**
- Consumes: `lsp.Manager`, `lsp.MapSymbols`, `config.LSP`, `worker` start helper (#3), the three consumer interfaces.
- Produces: `func NewSymbolAdapter(m *Manager) *SymbolAdapter` (etc.), wired in `app.Run`.

- [ ] **Step 1: Write the failing test**

Create `internal/lsp/adapters_test.go` — a unit test that the symbol adapter maps through `MapSymbols` when the manager reports a ready server. Use a manager whose `state` is seeded with a fake client is awkward (client needs a server); instead test the adapter's language/readiness gating with a manager that has no servers (returns `ok=false`) and assert graceful `(nil,false)`:

```go
package lsp

import (
	"context"
	"testing"
)

func TestSymbolAdapterNoServer(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{}, nil)
	a := NewSymbolAdapter(m)
	syms, ok := a.DocumentSymbols(context.Background(), "go", "a.go", []byte("package p"))
	if ok || syms != nil {
		t.Fatalf("expected (nil,false) with no server, got %v %v", syms, ok)
	}
}
```

(Full round-trip through a real server is covered by the manual integration path; unit tests assert the gating.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestSymbolAdapter -v`
Expected: FAIL — `undefined: NewSymbolAdapter`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/lsp/adapters.go`:

```go
package lsp

import (
	"context"
	"fmt"
	"strings"

	"marshal/internal/db"
)

// SymbolAdapter implements index.LSPSymbols.
type SymbolAdapter struct{ m *Manager }

func NewSymbolAdapter(m *Manager) *SymbolAdapter { return &SymbolAdapter{m: m} }

func (a *SymbolAdapter) DocumentSymbols(ctx context.Context, lang, filePath string, content []byte) ([]db.Symbol, bool) {
	client, ok := a.m.ServerFor(lang)
	if !ok {
		return nil, false
	}
	uri := "file://" + filePath
	_ = client.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": lang, "version": 1, "text": string(content)},
	})
	raw, err := client.Request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	_ = client.Notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
	if err != nil {
		return nil, false
	}
	var docs []DocumentSymbol
	if err := jsonUnmarshal(raw, &docs); err != nil || len(docs) == 0 {
		return nil, false
	}
	return MapSymbols(filePath, docs), true
}

// QueryAdapter implements native.LSPQuerier.
type QueryAdapter struct{ m *Manager }

func NewQueryAdapter(m *Manager) *QueryAdapter { return &QueryAdapter{m: m} }

func (a *QueryAdapter) References(ctx context.Context, filePath string, line, col int) ([]string, bool) {
	return a.locations(ctx, filePath, line, col, "textDocument/references", map[string]any{"includeDeclaration": true})
}
func (a *QueryAdapter) Definition(ctx context.Context, filePath string, line, col int) ([]string, bool) {
	return a.locations(ctx, filePath, line, col, "textDocument/definition", nil)
}
func (a *QueryAdapter) Hover(ctx context.Context, filePath string, line, col int) (string, bool) {
	client, ok := a.m.ServerFor(langFor(filePath))
	if !ok {
		return "", false
	}
	raw, err := client.Request(ctx, "textDocument/hover", posParams(filePath, line, col, nil))
	if err != nil {
		return "", false
	}
	var h Hover
	if jsonUnmarshal(raw, &h) != nil {
		return "", false
	}
	return string(h.Contents), true
}

func (a *QueryAdapter) locations(ctx context.Context, filePath string, line, col int, method string, extra map[string]any) ([]string, bool) {
	client, ok := a.m.ServerFor(langFor(filePath))
	if !ok {
		return nil, false
	}
	raw, err := client.Request(ctx, method, posParams(filePath, line, col, extra))
	if err != nil {
		return nil, false
	}
	var locs []Location
	if jsonUnmarshal(raw, &locs) != nil {
		// definition may return a single Location, not an array
		var one Location
		if jsonUnmarshal(raw, &one) == nil && one.URI != "" {
			locs = []Location{one}
		}
	}
	out := make([]string, 0, len(locs))
	for _, l := range locs {
		out = append(out, fmt.Sprintf("%s:%d", strings.TrimPrefix(l.URI, "file://"), l.Range.Start.Line+1))
	}
	return out, true
}

// DiagnosticsAdapter implements diagnostics.LSPSource.
type DiagnosticsAdapter struct{ m *Manager }

func NewDiagnosticsAdapter(m *Manager) *DiagnosticsAdapter { return &DiagnosticsAdapter{m: m} }

func (a *DiagnosticsAdapter) Diagnostics(lang, filePath string) (string, bool) {
	client, ok := a.m.ServerFor(lang)
	if !ok {
		return "", false
	}
	diags := client.Diagnostics("file://" + filePath)
	if len(diags) == 0 {
		return "", true // ready server, no problems
	}
	var b strings.Builder
	for _, d := range diags {
		fmt.Fprintf(&b, "%s:%d: %s\n", filePath, d.Range.Start.Line+1, d.Message)
	}
	return strings.TrimSpace(b.String()), true
}
```

Add small helpers (`jsonUnmarshal` wrapping `encoding/json`, `posParams`, `langFor` reusing `repo.DetectLanguage`) in `adapters.go` or `client.go`.

In `internal/app/app.go`, after config is loaded:

```go
	servers := lsp.DetectServers(toServerSpecs(cfg.LSP.Servers), disabledLangs(cfg.LSP.Servers))
	var mgr *lsp.Manager
	if lspEnabled(cfg.LSP) && len(servers) > 0 {
		mgr = lsp.NewManager(workingDir, servers, logger)
		workers = append(workers, mgr) // started by startWorker (subsystem #3)
	}
```

Inject the adapters where those consumers are built: `index.Deps.LSP = lsp.NewSymbolAdapter(mgr)` (in both the watcher's `runPass` and the `repo.index` tool deps), `toolSet.lsp = lsp.NewQueryAdapter(mgr)`, and `checker.SetLSPSource(lsp.NewDiagnosticsAdapter(mgr))` — each guarded so a nil `mgr` leaves the consumer in its graceful-off state.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestSymbolAdapter -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/lsp/ internal/app/ && go vet ./internal/lsp/ ./internal/app/
git add internal/lsp/adapters.go internal/lsp/adapters_test.go internal/app/app.go
git commit -m "feat(lsp): wire manager + adapters into index, tools, diagnostics"
```

---

## Final verification

- [ ] `go test ./...` — Expected: PASS.
- [ ] `go vet ./...` — Expected: no errors.
- [ ] `gofmt -l internal/db/ internal/lsp/ internal/index/ internal/app/config/ internal/tools/native/ internal/diagnostics/ internal/app/` — Expected: no files listed.
- [ ] Manual smoke (optional, needs gopls): `go run ./cmd/marshal` in a Go repo, run `repo.index`, confirm symbols show `source='lsp'` (via `symbols.find`), then `references` for a known symbol returns usages.

## Spec coverage map

- `source` column on symbols → Task 1
- JSON-RPC/stdio client (handshake, request/notify, publishDiagnostics) → Task 2
- documentSymbol → db.Symbol mapping → Task 3
- server manager (defaults, PATH detect, config, lifecycle, worker.Worker) → Tasks 4, 5, 9
- `[lsp]` config → Task 5
- layered symbol persistence in index.Run → Task 6
- symbol-name-addressed definition/references/hover → Task 7
- LSP diagnostics behind the Checker → Task 8
- adapters + app wiring + graceful degradation → Task 9

## Notes for the implementer

- Crash-restart with bounded backoff and a circuit breaker (spec open-question) is intentionally not in Task 4's minimal manager; add it as a supervised loop around `startServer` once the happy path is green.
- The `readMessage`/`writeMessage` framing is the crux — get Task 2's fake-server round-trip solid before building anything on top.
- Every consumer (index, tools, diagnostics) must keep working with a nil manager/adapter; the Task 9 guards are not optional.
- `didOpen` before `documentSymbol`/`hover`/`references` is required by most servers; the adapters open-query(-close) per call. A small open-document cache is a later optimization (spec open-question).
