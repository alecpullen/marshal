# Embedding Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a local-first embedding capability to Marshal — an `Embedder` interface with Ollama-native and OpenAI-compatible backends, resolved via a dedicated `embedding` routing role.

**Architecture:** A new `internal/llm/embedding/` package defines the `Embedder` interface plus shared batching/retry/dimension helpers, two HTTP backends, and a factory that selects a backend by the provider entry's `Type`. Routing gains a `RoleEmbedding` constant (deliberately excluded from `AllRoles`) and a no-fallback `ResolveEmbedding()` resolver. Nothing consumes embeddings yet — this is the foundation spec #2 (semantic index) will depend on.

**Tech Stack:** Go, standard library only (`net/http`, `encoding/json`). No new dependencies. Reuses existing `internal/app/config` and `internal/llm/routing`.

**Spec:** [docs/superpowers/specs/2026-07-24-embedding-foundation-design.md](../specs/2026-07-24-embedding-foundation-design.md)

## Global Constraints

- **Language/build:** Go; project builds with `CGO_ENABLED=1` (tree-sitter). The embedding package itself is pure-Go and needs no CGO.
- **No new dependencies:** standard library only.
- **Local-first:** default config has `remote_providers_allowed = false`; embedding resolution MUST honor it (`ErrRemoteProviderBlocked` for a remote preset).
- **No implementer fallback for embedding:** a chat model cannot produce vectors — resolution must fail cleanly, never fall back to `RoleImplementer`.
- **Format/vet before commit:** `gofmt -w .` and `go vet ./...` must pass.
- **`"ollama"` Type is embedding-only:** the chat factory `provider.NewFromConfig` must continue to reject it.
- Embedders are **not** safe for concurrent use (mutable cached `dims`); the future index engine serializes or constructs one per worker.

---

### Task 1: Embedder interface + shared helpers

**Files:**
- Create: `internal/llm/embedding/embedding.go`
- Test: `internal/llm/embedding/embedding_test.go`

**Interfaces:**
- Consumes: nothing (first task).
- Produces:
  - `type Embedder interface { Embed(ctx context.Context, texts []string) ([][]float32, error); Model() string; Dims() int }`
  - `func splitBatches(texts []string, size int) [][]string`
  - `func withRetry(ctx context.Context, fn func() error) error`
  - `func retryable(err error) error` and `func isRetryable(err error) bool`
  - `func checkDims(vecs [][]float32, want int) (int, error)`
  - `func Probe(ctx context.Context, e Embedder) (int, error)`
  - `var ErrDimMismatch error`, `const defaultBatchSize = 64`, `const maxRetryAttempts = 3`, `var retryBackoff time.Duration`

- [ ] **Step 1: Write the failing test**

Create `internal/llm/embedding/embedding_test.go`:

```go
package embedding

import (
	"context"
	"errors"
	"testing"
)

type fakeEmbedder struct {
	vecs [][]float32
	err  error
}

func (f fakeEmbedder) Embed(context.Context, []string) ([][]float32, error) { return f.vecs, f.err }
func (f fakeEmbedder) Model() string                                        { return "fake" }
func (f fakeEmbedder) Dims() int                                            { return 0 }

func TestSplitBatches(t *testing.T) {
	got := splitBatches([]string{"a", "b", "c", "d", "e"}, 2)
	if len(got) != 3 || len(got[0]) != 2 || len(got[2]) != 1 {
		t.Fatalf("splitBatches = %#v", got)
	}
	if len(splitBatches(nil, 2)) != 0 {
		t.Fatal("empty input should yield no batches")
	}
	if len(splitBatches([]string{"a"}, 0)) != 1 {
		t.Fatal("size<=0 should fall back to default and yield one batch")
	}
}

func TestCheckDims(t *testing.T) {
	got, err := checkDims([][]float32{{1, 2, 3}, {4, 5, 6}}, 0)
	if err != nil || got != 3 {
		t.Fatalf("discover dims = %d, err = %v", got, err)
	}
	if _, err := checkDims([][]float32{{1, 2}}, 3); !errors.Is(err, ErrDimMismatch) {
		t.Fatalf("want ErrDimMismatch, got %v", err)
	}
}

func TestWithRetryRetriesTransient(t *testing.T) {
	retryBackoff = 0
	calls := 0
	err := withRetry(context.Background(), func() error {
		calls++
		if calls < 3 {
			return retryable(errors.New("boom"))
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
}

func TestWithRetryStopsOnNonRetryable(t *testing.T) {
	retryBackoff = 0
	calls := 0
	sentinel := errors.New("fatal")
	err := withRetry(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
}

func TestProbe(t *testing.T) {
	dims, err := Probe(context.Background(), fakeEmbedder{vecs: [][]float32{{1, 2, 3, 4}}})
	if err != nil || dims != 4 {
		t.Fatalf("Probe dims = %d, err = %v", dims, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/embedding/ -run 'TestSplitBatches|TestCheckDims|TestWithRetry|TestProbe' -v`
Expected: FAIL — build error, `undefined: splitBatches` (and the other symbols).

- [ ] **Step 3: Write minimal implementation**

Create `internal/llm/embedding/embedding.go`:

```go
// Package embedding provides Marshal's local-first text embedding capability:
// an Embedder abstraction plus Ollama-native and OpenAI-compatible backends.
// Embedders are NOT safe for concurrent use (they cache the vector dimension
// after the first embed); callers serialize or construct one per worker.
package embedding

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Embedder is implemented by every embedding backend Marshal can talk to.
type Embedder interface {
	// Embed returns one vector per input text, in input order. An empty
	// input slice returns an empty result and no error.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Model returns the embedding model name (stored alongside vectors by
	// the semantic index so a model change marks vectors stale).
	Model() string
	// Dims returns the embedding dimension, discovered on the first
	// successful Embed and cached; 0 before the first embed.
	Dims() int
}

// ErrDimMismatch is returned when a backend yields a vector whose dimension
// differs from the dimension established by the first successful embed.
var ErrDimMismatch = errors.New("embedding: vector dimension mismatch")

const (
	// defaultBatchSize bounds how many inputs are sent per request.
	defaultBatchSize = 64
	// maxRetryAttempts bounds retry attempts on transient errors.
	maxRetryAttempts = 3
)

// retryBackoff is the fixed delay between retry attempts. It is a var (not a
// const) so tests can set it to 0.
var retryBackoff = 200 * time.Millisecond

// splitBatches splits texts into consecutive slices of at most size.
func splitBatches(texts []string, size int) [][]string {
	if size <= 0 {
		size = defaultBatchSize
	}
	var batches [][]string
	for start := 0; start < len(texts); start += size {
		end := start + size
		if end > len(texts) {
			end = len(texts)
		}
		batches = append(batches, texts[start:end])
	}
	return batches
}

// transientErr marks an error as retryable so withRetry will retry it.
type transientErr struct{ err error }

func (t transientErr) Error() string { return t.err.Error() }
func (t transientErr) Unwrap() error { return t.err }

// retryable wraps err so withRetry treats it as transient. Returns nil for nil.
func retryable(err error) error {
	if err == nil {
		return nil
	}
	return transientErr{err}
}

func isRetryable(err error) bool {
	var t transientErr
	return errors.As(err, &t)
}

// withRetry calls fn up to maxRetryAttempts times, retrying only errors marked
// retryable(), with a fixed backoff, honoring ctx cancellation.
func withRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		err = fn()
		if err == nil || !isRetryable(err) {
			return err
		}
		if attempt == maxRetryAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryBackoff):
		}
	}
	return err
}

// checkDims verifies every vector in vecs has dimension want (when want > 0),
// or discovers and returns the dimension from the first vector (when want ==
// 0). Returns ErrDimMismatch on any inconsistency.
func checkDims(vecs [][]float32, want int) (int, error) {
	for _, v := range vecs {
		if want == 0 {
			want = len(v)
		}
		if len(v) != want {
			return 0, fmt.Errorf("%w: got %d, want %d", ErrDimMismatch, len(v), want)
		}
	}
	return want, nil
}

// Probe embeds a short fixed string and returns the discovered dimension. Used
// by a future "test embedding connection" affordance and by tests.
func Probe(ctx context.Context, e Embedder) (int, error) {
	vecs, err := e.Embed(ctx, []string{"ping"})
	if err != nil {
		return 0, err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return 0, errors.New("embedding: probe returned no vector")
	}
	return len(vecs[0]), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/embedding/ -v`
Expected: PASS (all Task 1 tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm/embedding/
go vet ./internal/llm/embedding/
git add internal/llm/embedding/embedding.go internal/llm/embedding/embedding_test.go
git commit -m "feat(embedding): add Embedder interface and shared batch/retry helpers"
```

---

### Task 2: OpenAI-compatible backend

**Files:**
- Create: `internal/llm/embedding/openai.go`
- Test: `internal/llm/embedding/openai_test.go`

**Interfaces:**
- Consumes: `splitBatches`, `withRetry`, `retryable`, `checkDims`, `defaultBatchSize` (Task 1).
- Produces: `func newOpenAIEmbedder(baseURL, apiKey, model string) *openAIEmbedder` implementing `Embedder`, hitting `POST {baseURL}/v1/embeddings`.

- [ ] **Step 1: Write the failing test**

Create `internal/llm/embedding/openai_test.go`:

```go
package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIEmbedderEmbedPreservesOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, ok := req["input"].([]any); !ok {
			t.Errorf("input not an array: %#v", req["input"])
		}
		w.Header().Set("Content-Type", "application/json")
		// Deliberately out of order to prove index-based sorting.
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.3,0.4],"index":1},{"embedding":[0.1,0.2],"index":0}]}`))
	}))
	defer server.Close()

	e := newOpenAIEmbedder(server.URL, "", "nomic-embed-text")
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][0] != 0.3 {
		t.Fatalf("vecs = %#v", vecs)
	}
	if e.Dims() != 2 || e.Model() != "nomic-embed-text" {
		t.Fatalf("Dims = %d, Model = %q", e.Dims(), e.Model())
	}
}

func TestOpenAIEmbedderEmptyInput(t *testing.T) {
	e := newOpenAIEmbedder("http://unused.invalid", "", "m")
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil || len(vecs) != 0 {
		t.Fatalf("empty input: vecs=%#v err=%v", vecs, err)
	}
}

func TestOpenAIEmbedderRetriesOn503(t *testing.T) {
	retryBackoff = 0
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,2,3],"index":0}]}`))
	}))
	defer server.Close()

	e := newOpenAIEmbedder(server.URL, "", "m")
	vecs, err := e.Embed(context.Background(), []string{"x"})
	if err != nil || len(vecs) != 1 || calls != 2 {
		t.Fatalf("calls=%d vecs=%#v err=%v", calls, vecs, err)
	}
}

func TestOpenAIEmbedderSetsAuthHeader(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1],"index":0}]}`))
	}))
	defer server.Close()

	e := newOpenAIEmbedder(server.URL, "secret", "m")
	if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/embedding/ -run TestOpenAIEmbedder -v`
Expected: FAIL — `undefined: newOpenAIEmbedder`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/llm/embedding/openai.go`:

```go
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// openAIEmbedder talks to an OpenAI-compatible /v1/embeddings endpoint. This
// path also works against Ollama's /v1 endpoint, LM Studio, and llama.cpp
// servers.
type openAIEmbedder struct {
	client  *http.Client
	baseURL string
	apiKey  string
	model   string
	dims    int
}

func newOpenAIEmbedder(baseURL, apiKey, model string) *openAIEmbedder {
	return &openAIEmbedder{
		client:  &http.Client{Timeout: 60 * time.Second},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
	}
}

func (e *openAIEmbedder) Model() string { return e.model }
func (e *openAIEmbedder) Dims() int     { return e.dims }

type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	out := make([][]float32, 0, len(texts))
	for _, batch := range splitBatches(texts, defaultBatchSize) {
		var vecs [][]float32
		err := withRetry(ctx, func() error {
			v, e2 := e.embedOnce(ctx, batch)
			vecs = v
			return e2
		})
		if err != nil {
			return nil, err
		}
		dims, err := checkDims(vecs, e.dims)
		if err != nil {
			return nil, err
		}
		e.dims = dims
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *openAIEmbedder) embedOnce(ctx context.Context, batch []string) ([][]float32, error) {
	body, err := json.Marshal(openAIEmbedRequest{Model: e.model, Input: batch})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, retryable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, retryable(fmt.Errorf("embeddings: status %d", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: status %d", resp.StatusCode)
	}
	var decoded openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Data) != len(batch) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(decoded.Data), len(batch))
	}
	sort.Slice(decoded.Data, func(i, j int) bool { return decoded.Data[i].Index < decoded.Data[j].Index })
	vecs := make([][]float32, len(decoded.Data))
	for i, d := range decoded.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/embedding/ -run TestOpenAIEmbedder -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm/embedding/
go vet ./internal/llm/embedding/
git add internal/llm/embedding/openai.go internal/llm/embedding/openai_test.go
git commit -m "feat(embedding): add OpenAI-compatible /v1/embeddings backend"
```

---

### Task 3: Ollama-native backend

**Files:**
- Create: `internal/llm/embedding/ollama.go`
- Test: `internal/llm/embedding/ollama_test.go`

**Interfaces:**
- Consumes: `splitBatches`, `withRetry`, `retryable`, `checkDims`, `defaultBatchSize` (Task 1).
- Produces: `func newOllamaEmbedder(baseURL, apiKey, model string) *ollamaEmbedder` implementing `Embedder`, hitting `POST {baseURL}/api/embed`.

- [ ] **Step 1: Write the failing test**

Create `internal/llm/embedding/ollama_test.go`:

```go
package embedding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbedderEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		// Native endpoint returns embeddings in input order (no index field).
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2],[0.3,0.4]]}`))
	}))
	defer server.Close()

	e := newOllamaEmbedder(server.URL, "", "nomic-embed-text")
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][0] != 0.3 {
		t.Fatalf("vecs = %#v", vecs)
	}
	if e.Dims() != 2 || e.Model() != "nomic-embed-text" {
		t.Fatalf("Dims = %d, Model = %q", e.Dims(), e.Model())
	}
}

func TestOllamaEmbedderCountMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2]]}`)) // 1 vec for 2 inputs
	}))
	defer server.Close()

	e := newOllamaEmbedder(server.URL, "", "m")
	if _, err := e.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("expected error on vector/input count mismatch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/embedding/ -run TestOllamaEmbedder -v`
Expected: FAIL — `undefined: newOllamaEmbedder`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/llm/embedding/ollama.go`:

```go
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ollamaEmbedder talks to Ollama's native /api/embed endpoint, which accepts
// an array input and returns embeddings in input order.
type ollamaEmbedder struct {
	client  *http.Client
	baseURL string
	apiKey  string
	model   string
	dims    int
}

func newOllamaEmbedder(baseURL, apiKey, model string) *ollamaEmbedder {
	return &ollamaEmbedder{
		client:  &http.Client{Timeout: 60 * time.Second},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
	}
}

func (e *ollamaEmbedder) Model() string { return e.model }
func (e *ollamaEmbedder) Dims() int     { return e.dims }

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (e *ollamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	out := make([][]float32, 0, len(texts))
	for _, batch := range splitBatches(texts, defaultBatchSize) {
		var vecs [][]float32
		err := withRetry(ctx, func() error {
			v, e2 := e.embedOnce(ctx, batch)
			vecs = v
			return e2
		})
		if err != nil {
			return nil, err
		}
		dims, err := checkDims(vecs, e.dims)
		if err != nil {
			return nil, err
		}
		e.dims = dims
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *ollamaEmbedder) embedOnce(ctx context.Context, batch []string) ([][]float32, error) {
	body, err := json.Marshal(ollamaEmbedRequest{Model: e.model, Input: batch})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, retryable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, retryable(fmt.Errorf("embed: status %d", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: status %d", resp.StatusCode)
	}
	var decoded ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Embeddings) != len(batch) {
		return nil, fmt.Errorf("embed: got %d vectors for %d inputs", len(decoded.Embeddings), len(batch))
	}
	return decoded.Embeddings, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/embedding/ -run TestOllamaEmbedder -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm/embedding/
go vet ./internal/llm/embedding/
git add internal/llm/embedding/ollama.go internal/llm/embedding/ollama_test.go
git commit -m "feat(embedding): add Ollama-native /api/embed backend"
```

---

### Task 4: Factory (backend selection by provider Type)

**Files:**
- Create: `internal/llm/embedding/factory.go`
- Test: `internal/llm/embedding/factory_test.go`

**Interfaces:**
- Consumes: `newOpenAIEmbedder`, `newOllamaEmbedder` (Tasks 2/3), `config.ProviderConfig` (`internal/app/config`).
- Produces: `func NewFromConfig(name string, pc config.ProviderConfig, model string) (Embedder, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/llm/embedding/factory_test.go`:

```go
package embedding

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func TestNewFromConfigSelectsBackendByType(t *testing.T) {
	ollama, err := NewFromConfig("ollama", config.ProviderConfig{Type: "ollama", BaseURL: "http://localhost:11434"}, "nomic-embed-text")
	if err != nil {
		t.Fatalf("ollama: %v", err)
	}
	if _, ok := ollama.(*ollamaEmbedder); !ok {
		t.Fatalf("type=ollama built %T, want *ollamaEmbedder", ollama)
	}

	for _, typ := range []string{"", "openai_compatible"} {
		e, err := NewFromConfig("oai", config.ProviderConfig{Type: typ, BaseURL: "http://localhost:1234"}, "m")
		if err != nil {
			t.Fatalf("type=%q: %v", typ, err)
		}
		if _, ok := e.(*openAIEmbedder); !ok {
			t.Fatalf("type=%q built %T, want *openAIEmbedder", typ, e)
		}
	}
}

func TestNewFromConfigRejectsUnknownType(t *testing.T) {
	_, err := NewFromConfig("x", config.ProviderConfig{Type: "weird"}, "m")
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("want unsupported type error, got %v", err)
	}
}

func TestNewFromConfigRequiresModel(t *testing.T) {
	_, err := NewFromConfig("x", config.ProviderConfig{Type: "ollama"}, "")
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("want model-required error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/embedding/ -run TestNewFromConfig -v`
Expected: FAIL — `undefined: NewFromConfig`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/llm/embedding/factory.go`:

```go
package embedding

import (
	"fmt"
	"os"

	"marshal/internal/app/config"
)

// NewFromConfig builds an Embedder from a single provider entry and a model
// name. The backend is selected by pc.Type: "ollama" -> native /api/embed;
// "" or "openai_compatible" -> OpenAI-compatible /v1/embeddings.
//
// API-key resolution mirrors provider.NewFromConfig: a literal api_key wins
// over api_key_env; absent auth is normal for local endpoints.
func NewFromConfig(name string, pc config.ProviderConfig, model string) (Embedder, error) {
	if model == "" {
		return nil, fmt.Errorf("embedding provider %q: model is required", name)
	}
	apiKey, err := resolveAPIKey(pc)
	if err != nil {
		return nil, fmt.Errorf("embedding provider %q: %w", name, err)
	}
	switch pc.Type {
	case "ollama":
		return newOllamaEmbedder(pc.BaseURL, apiKey, model), nil
	case "", "openai_compatible":
		return newOpenAIEmbedder(pc.BaseURL, apiKey, model), nil
	default:
		return nil, fmt.Errorf("embedding provider %q: unsupported type %q", name, pc.Type)
	}
}

// resolveAPIKey mirrors provider.resolveAPIKey (literal key wins over env
// lookup; empty is allowed for local endpoints).
func resolveAPIKey(pc config.ProviderConfig) (string, error) {
	if pc.APIKey != "" {
		return pc.APIKey, nil
	}
	if pc.APIKeyEnv != "" {
		v, ok := os.LookupEnv(pc.APIKeyEnv)
		if !ok || v == "" {
			return "", fmt.Errorf("environment variable %q (from api_key_env) is not set", pc.APIKeyEnv)
		}
		return v, nil
	}
	return "", nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/embedding/ -v`
Expected: PASS (all embedding-package tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm/embedding/
go vet ./internal/llm/embedding/
git add internal/llm/embedding/factory.go internal/llm/embedding/factory_test.go
git commit -m "feat(embedding): add factory selecting backend by provider type"
```

---

### Task 5: Routing role + no-fallback resolver

**Files:**
- Modify: `internal/llm/routing/types.go` (add `RoleEmbedding` constant; do NOT add to `AllRoles`)
- Modify: `internal/llm/routing/router.go` (add `ErrEmbeddingNotConfigured` and `ResolveEmbedding`)
- Test: `internal/llm/routing/router_test.go` (append tests)

**Interfaces:**
- Consumes: `resolveProfileRole`, `isNoConfiguredRoute`, `ErrRemoteProviderBlocked`, `Route`, `Config`, `AgentProfile`, `ModelPreset` (existing routing).
- Produces: `const RoleEmbedding AgentRole = "embedding"`; `var ErrEmbeddingNotConfigured error`; `func (r *StaticRouter) ResolveEmbedding() (Route, error)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/routing/router_test.go`:

```go
func TestResolveEmbedding(t *testing.T) {
	cfg := Config{
		DefaultProfile: "local",
		RemoteAllowed:  false,
		Presets: map[string]ModelPreset{
			"nomic": {Provider: "ollama", Model: "nomic-embed-text", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"local": {Name: "local", Roles: map[AgentRole]string{RoleEmbedding: "nomic"}},
		},
	}
	route, err := NewStaticRouter(cfg).ResolveEmbedding()
	if err != nil {
		t.Fatalf("ResolveEmbedding: %v", err)
	}
	if route.Preset.Provider != "ollama" || route.Preset.Model != "nomic-embed-text" {
		t.Fatalf("preset = %+v", route.Preset)
	}
}

func TestResolveEmbeddingNotConfigured(t *testing.T) {
	cfg := Config{
		DefaultProfile: "local",
		Profiles:       map[string]AgentProfile{"local": {Name: "local", Roles: map[AgentRole]string{}}},
	}
	if _, err := NewStaticRouter(cfg).ResolveEmbedding(); !errors.Is(err, ErrEmbeddingNotConfigured) {
		t.Fatalf("want ErrEmbeddingNotConfigured, got %v", err)
	}
}

func TestResolveEmbeddingRemoteBlocked(t *testing.T) {
	cfg := Config{
		DefaultProfile: "local",
		RemoteAllowed:  false,
		Presets: map[string]ModelPreset{
			"remote": {Provider: "openai", Model: "text-embedding-3-small", LocalOnly: false},
		},
		Profiles: map[string]AgentProfile{
			"local": {Name: "local", Roles: map[AgentRole]string{RoleEmbedding: "remote"}},
		},
	}
	if _, err := NewStaticRouter(cfg).ResolveEmbedding(); !errors.Is(err, ErrRemoteProviderBlocked) {
		t.Fatalf("want ErrRemoteProviderBlocked, got %v", err)
	}
}

func TestRoleEmbeddingExcludedFromAllRoles(t *testing.T) {
	for _, role := range AllRoles {
		if role == RoleEmbedding {
			t.Fatal("RoleEmbedding must not appear in AllRoles")
		}
	}
}
```

Note: `router_test.go` is `package routing` and already imports `errors` (used by existing tests). If a build error reports `errors` unused/missing, add it to the import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/routing/ -run 'ResolveEmbedding|RoleEmbedding' -v`
Expected: FAIL — `undefined: RoleEmbedding` / `ResolveEmbedding` / `ErrEmbeddingNotConfigured`.

- [ ] **Step 3: Write minimal implementation**

In `internal/llm/routing/types.go`, add the constant inside the existing `const (...)` block (after `RoleSDDBranchReviewer`), with a comment. Do **not** add it to `AllRoles`:

```go
	RoleSDDBranchReviewer AgentRole = "sdd_branch_reviewer"

	// RoleEmbedding selects the text-embedding provider+model. It is
	// deliberately excluded from AllRoles: embedding is not a chat role, so
	// onboarding/settings that enumerate AllRoles must not list it. Resolved
	// via StaticRouter.ResolveEmbedding, not ResolveRole.
	RoleEmbedding AgentRole = "embedding"
```

In `internal/llm/routing/router.go`, add the sentinel error next to the existing `var (...)` error block:

```go
// ErrEmbeddingNotConfigured is returned by ResolveEmbedding when the active
// profile has no embedding role. Callers use it to gracefully disable
// embedding-dependent features rather than fail.
var ErrEmbeddingNotConfigured = errors.New("routing: embedding role not configured")
```

And add the resolver method (e.g. after `ResolveRole`):

```go
// ResolveEmbedding resolves the embedding provider+model from the active
// profile's embedding role. Unlike ResolveRole it has NO implementer fallback
// — a chat model cannot produce vectors. Returns ErrEmbeddingNotConfigured
// when the role is unset (or no profile exists), and ErrRemoteProviderBlocked
// when the resolved preset is remote under remote_providers_allowed = false.
func (r *StaticRouter) ResolveEmbedding() (Route, error) {
	route, err := r.resolveProfileRole(RoleEmbedding)
	if err != nil {
		if isNoConfiguredRoute(err) {
			return Route{}, ErrEmbeddingNotConfigured
		}
		return Route{}, err
	}
	return route, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/routing/ -v`
Expected: PASS (new tests plus all existing routing tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm/routing/
go vet ./internal/llm/routing/
git add internal/llm/routing/types.go internal/llm/routing/router.go internal/llm/routing/router_test.go
git commit -m "feat(routing): add embedding role and no-fallback ResolveEmbedding"
```

---

### Task 6: Formalize "ollama" Type + config-reuse verification

**Files:**
- Modify: `internal/app/config/types.go:294` (update `Type` field doc comment)
- Test: `internal/llm/provider/factory_test.go` (append chat-factory boundary test)
- Test: `internal/app/config/config_test.go` (append embedding-role round-trip test)

**Interfaces:**
- Consumes: `provider.NewFromConfig` (existing), `config.Load` / `LoadOptions` / `writeFile` test helper (existing), `routing.RoleEmbedding` (Task 5).
- Produces: no new code contract — documents `"ollama"` as a recognized `Type` and pins two boundary behaviors.

- [ ] **Step 1: Write the failing tests**

Append to `internal/llm/provider/factory_test.go` (package `provider`, already imports `config` and `strings`):

```go
func TestNewFromConfigRejectsOllamaType(t *testing.T) {
	// "ollama" is an embedding-only Type; the chat factory must reject it.
	_, err := NewFromConfig("ollama", config.ProviderConfig{Type: "ollama", BaseURL: "http://localhost:11434"})
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("want unsupported type error, got %v", err)
	}
}
```

Append to `internal/app/config/config_test.go` (package `config`, already imports `marshal/internal/llm/routing` and uses the `writeFile` helper):

```go
func TestLoadEmbeddingRoleSurvives(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[agent_profiles.local.roles]
embedding = "nomic"
`)
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.AgentProfiles["local"].Roles[routing.RoleEmbedding]; got != "nomic" {
		t.Fatalf("embedding role = %q, want nomic", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/provider/ -run TestNewFromConfigRejectsOllamaType -v && go test ./internal/app/config/ -run TestLoadEmbeddingRoleSurvives -v`
Expected: the provider test may already PASS (the factory already rejects unknown types) — that is fine, it pins the behavior. The config test FAILS only if Task 5 is not yet merged (`undefined: routing.RoleEmbedding`); with Task 5 in place it should PASS once the config path preserves the key. If the config test fails with `embedding role = ""`, investigate merge/decoding before proceeding.

- [ ] **Step 3: Update the Type field doc comment**

In `internal/app/config/types.go`, replace the `Type` field line (currently line 294):

```go
	Type        string `toml:"type"` // "openai_compatible" (default when empty) or "ollama"; "ollama" selects the native embedding backend and is not a chat provider type
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/llm/provider/ ./internal/app/config/ -v`
Expected: PASS (including the two new tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/config/ internal/llm/provider/
go vet ./internal/app/config/ ./internal/llm/provider/
git add internal/app/config/types.go internal/llm/provider/factory_test.go internal/app/config/config_test.go
git commit -m "docs(config): recognize ollama Type; pin embedding-role and chat-factory boundaries"
```

---

### Task 7: Live integration tests (gated)

**Files:**
- Create: `internal/llm/embedding/integration_test.go` (build tag `integration`)

**Interfaces:**
- Consumes: `newOllamaEmbedder`, `newOpenAIEmbedder` (Tasks 2/3).
- Produces: manual, skipped-by-default contract tests against a live endpoint.

- [ ] **Step 1: Write the test**

Create `internal/llm/embedding/integration_test.go`:

```go
//go:build integration

package embedding

// Run manually against a local Ollama server:
//   MARSHAL_TEST_OLLAMA_URL=http://localhost:11434 \
//     go test -tags=integration ./internal/llm/embedding/... -run Native -v
//
// Run against the OpenAI-compatible path (e.g. Ollama's /v1 or LM Studio):
//   MARSHAL_TEST_EMBED_V1_URL=http://localhost:11434/v1 \
//     go test -tags=integration ./internal/llm/embedding/... -run OpenAICompat -v
//
// Override the model with MARSHAL_TEST_EMBED_MODEL (default nomic-embed-text).

import (
	"context"
	"os"
	"testing"
	"time"
)

func embedModel() string {
	if m := os.Getenv("MARSHAL_TEST_EMBED_MODEL"); m != "" {
		return m
	}
	return "nomic-embed-text"
}

func TestOllamaNativeEmbedIntegration(t *testing.T) {
	baseURL := os.Getenv("MARSHAL_TEST_OLLAMA_URL")
	if baseURL == "" {
		t.Skip("set MARSHAL_TEST_OLLAMA_URL to run against a local Ollama server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	e := newOllamaEmbedder(baseURL, "", embedModel())
	vecs, err := e.Embed(ctx, []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || e.Dims() == 0 || len(vecs[0]) != e.Dims() {
		t.Fatalf("vecs=%d dims=%d", len(vecs), e.Dims())
	}
}

func TestOpenAICompatEmbedIntegration(t *testing.T) {
	baseURL := os.Getenv("MARSHAL_TEST_EMBED_V1_URL")
	if baseURL == "" {
		t.Skip("set MARSHAL_TEST_EMBED_V1_URL to run against an OpenAI-compatible endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// baseURL already ends in /v1; the backend appends /v1/embeddings, so
	// strip a trailing /v1 to avoid duplication.
	e := newOpenAIEmbedder(trimV1(baseURL), os.Getenv("MARSHAL_TEST_EMBED_API_KEY"), embedModel())
	vecs, err := e.Embed(ctx, []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || e.Dims() == 0 {
		t.Fatalf("vecs=%d dims=%d", len(vecs), e.Dims())
	}
}

func trimV1(s string) string {
	for _, suffix := range []string{"/v1/", "/v1"} {
		if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
			return s[:len(s)-len(suffix)]
		}
	}
	return s
}
```

- [ ] **Step 2: Verify it compiles under the build tag**

Run: `go vet -tags=integration ./internal/llm/embedding/`
Expected: no errors (the tests are skipped without env vars).

- [ ] **Step 3: (Optional) Run against a live server**

Run (only if Ollama is running): `MARSHAL_TEST_OLLAMA_URL=http://localhost:11434 go test -tags=integration ./internal/llm/embedding/ -run Native -v`
Expected: PASS or SKIP.

- [ ] **Step 4: Full package test still green**

Run: `go test ./internal/llm/embedding/ -v`
Expected: PASS (non-integration tests; integration file excluded without the tag).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/llm/embedding/
git add internal/llm/embedding/integration_test.go
git commit -m "test(embedding): add gated live integration tests for both backends"
```

---

## Final verification

- [ ] Run the full suite: `go test ./...` — Expected: PASS.
- [ ] Vet everything: `go vet ./...` — Expected: no errors.
- [ ] Confirm formatting: `gofmt -l internal/llm/embedding/ internal/llm/routing/ internal/app/config/` — Expected: no files listed.

## Spec coverage map

- `Embedder` interface + `Probe` → Task 1
- OpenAI-compatible backend (order via `index`, batching, retry) → Task 2
- Ollama-native backend → Task 3
- Factory + Type-based backend selection + api-key resolution → Task 4
- `RoleEmbedding` (excluded from `AllRoles`) + `ResolveEmbedding` (no fallback, `ErrEmbeddingNotConfigured`, remote-block) → Task 5
- Formalized `"ollama"` Type comment + chat-factory rejection test + config round-trip test → Task 6
- Gated live contract tests → Task 7

## Out of scope (later specs)

Chunking, `chunks`/`embeddings` tables, the index engine, context-pack wiring, any TUI/settings surface, and the managed local inference service lifecycle.
