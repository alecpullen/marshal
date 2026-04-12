package loop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/git"
	"github.com/alec/marshal/internal/loop"
	"github.com/alec/marshal/internal/session"
)

// --- Test helpers ------------------------------------------------------------

func newTestGitRepo(t *testing.T) *git.Repo {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@marshal"},
		{"git", "config", "user.name", "Test"},
	} {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	writeTestFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	gitAddCommit(t, dir, "initial")

	repo, err := git.New(dir, git.RepoConfig{CoAuthoredBy: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitAddCommit(t *testing.T, dir, msg string) {
	t.Helper()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_COMMITTER_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@marshal", "GIT_COMMITTER_EMAIL=test@marshal",
	)
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", msg},
	} {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		c.Env = env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
}

func headSHAOf(t *testing.T, dir string) string {
	t.Helper()
	c := exec.Command("git", "rev-parse", "HEAD")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func currentBranchOf(t *testing.T, dir string) string {
	t.Helper()
	c := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// newTestStore opens an in-memory SQLite database.
func newTestStore(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// --- Mock server builders ----------------------------------------------------

// sseResponse formats a chat.completion.chunk SSE stream with the given text,
// streaming in 8-byte chunks to simulate real streaming without destroying
// newlines (which are required for ParseWhole to find filenames).
func sseResponse(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		const chunkSize = 8
		for i := 0; i < len(text); i += chunkSize {
			end := i + chunkSize
			if end > len(text) {
				end = len(text)
			}
			chunk := map[string]any{
				"choices": []map[string]any{{
					"delta": map[string]any{"content": text[i:end]},
				}},
			}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}
}

// jsonResponse returns a static non-streaming chat completion response.
func jsonResponse(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		}
		json.NewEncoder(w).Encode(resp)
	}
}

// muxBackend routes POST to /chat/completions either as SSE or JSON based on
// the "stream" field in the request body.
func muxBackend(streamHandler, completeHandler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Stream {
			streamHandler(w, r)
		} else {
			completeHandler(w, r)
		}
	}
}

func newTestRegistry(t *testing.T, execSrv, criticSrv *httptest.Server) *backend.Registry {
	t.Helper()
	// Build a config that points all four roles at the same server for
	// simplicity, but we differentiate executor vs critic by server param.
	execBackend := backend.NewOpenAICompat(backend.OpenAICompatConfig{
		BaseURL:    execSrv.URL,
		ModelName:  "test-exec",
		SupTools:   false,
		HTTPClient: execSrv.Client(),
	}, nil)
	criticBackend := backend.NewOpenAICompat(backend.OpenAICompatConfig{
		BaseURL:    criticSrv.URL,
		ModelName:  "test-critic",
		SupTools:   false,
		HTTPClient: criticSrv.Client(),
	}, nil)
	// We can't call NewRegistry directly (it builds from config), so build a
	// minimal registry via a test shim.
	return backend.NewRegistryFromBackends(map[string]backend.Backend{
		"marshal":   execBackend,
		"executor":  execBackend,
		"critic":    criticBackend,
		"compactor": execBackend,
	})
}

// --- Tests -------------------------------------------------------------------

func TestEngine_Pass(t *testing.T) {
	repo := newTestGitRepo(t)
	mainSHABefore := headSHAOf(t, repo.Root())

	// Executor returns a file change.
	execResp := "I'll update main.go:\n\nmain.go\n```go\npackage main\n\nfunc Hello() string { return \"hello\" }\n\nfunc main() {}\n```"
	passVerdict := `{"verdict":"PASS","summary":"Hello function added","issue":"","fix":"","concerns":[]}`

	execSrv := httptest.NewServer(muxBackend(
		sseResponse(execResp),
		jsonResponse("unused"),
	))
	criticSrv := httptest.NewServer(jsonResponse(passVerdict))
	defer execSrv.Close()
	defer criticSrv.Close()

	store := newTestStore(t)
	gitSess := git.NewSession(repo, git.SessionOptions{})
	if err := gitSess.Start(); err != nil {
		t.Fatal(err)
	}

	sessID := "test-session"
	_ = store.InsertSession(&session.Session{
		ID: sessID, TargetBranch: "main", TargetStartSHA: mainSHABefore,
		StagingBranch: gitSess.StagingBranch, StartedAt: time.Now(),
	})

	reg := newTestRegistry(t, execSrv, criticSrv)
	eng := loop.New(
		loop.Config{MaxRounds: 3, SessionID: sessID, GitEnabled: true},
		repo, gitSess, store, reg, loop.DiscardSink{},
	)

	if err := eng.Run(context.Background(), "add Hello function"); err != nil {
		t.Fatalf("expected PASS, got: %v", err)
	}

	// Staging HEAD advanced; main unchanged.
	stagingSHA, _ := gitSess.StagingHEAD()
	if stagingSHA == mainSHABefore {
		t.Error("staging HEAD should have advanced")
	}
	if headSHAOf(t, repo.Root()) == mainSHABefore {
		// We're on staging now, main should still be the old SHA.
	}
	if currentBranchOf(t, repo.Root()) != gitSess.StagingBranch {
		t.Errorf("should be on staging branch after PASS, got %s",
			currentBranchOf(t, repo.Root()))
	}

	// Hello function should exist in the repo.
	content, err := os.ReadFile(filepath.Join(repo.Root(), "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Hello()") {
		t.Errorf("Hello() not in main.go:\n%s", content)
	}

	// Task row in DB should be passed.
	tasks, err := store.TasksForSession(sessID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Status != session.StatusPassed {
		t.Errorf("expected status=passed, got %q", tasks[0].Status)
	}
	if tasks[0].StagingSHA == nil {
		t.Error("StagingSHA should be set on passed task")
	}
}

func TestEngine_FailAfterRetries(t *testing.T) {
	repo := newTestGitRepo(t)
	mainSHABefore := headSHAOf(t, repo.Root())

	execResp := "main.go\n```go\npackage main\n\nfunc main() {}\n```"
	failVerdict := `{"verdict":"FAIL","summary":"missing feature","issue":"no feature","fix":"add feature","concerns":[]}`

	execSrv := httptest.NewServer(muxBackend(sseResponse(execResp), jsonResponse("")))
	criticSrv := httptest.NewServer(jsonResponse(failVerdict))
	defer execSrv.Close()
	defer criticSrv.Close()

	store := newTestStore(t)
	gitSess := git.NewSession(repo, git.SessionOptions{})
	_ = gitSess.Start()

	sessID := "test-fail"
	_ = store.InsertSession(&session.Session{
		ID: sessID, TargetBranch: "main", TargetStartSHA: mainSHABefore,
		StagingBranch: gitSess.StagingBranch, StartedAt: time.Now(),
	})

	reg := newTestRegistry(t, execSrv, criticSrv)
	eng := loop.New(
		loop.Config{MaxRounds: 2, SessionID: sessID, GitEnabled: true},
		repo, gitSess, store, reg, loop.DiscardSink{},
	)

	err := eng.Run(context.Background(), "add missing feature")
	if err == nil {
		t.Fatal("expected ErrTaskFailed")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("unexpected error: %v", err)
	}

	// Staging HEAD unchanged; main unchanged.
	stagingSHA, _ := gitSess.StagingHEAD()
	if stagingSHA != mainSHABefore {
		t.Error("staging HEAD must not advance on FAIL")
	}

	// Task row in DB should be failed.
	tasks, err2 := store.TasksForSession(sessID)
	if err2 != nil {
		t.Fatal(err2)
	}
	if len(tasks) != 1 || tasks[0].Status != session.StatusFailed {
		t.Errorf("expected 1 failed task, got %+v", tasks)
	}
}

func TestEngine_RetryOnFail_PassOnSecond(t *testing.T) {
	repo := newTestGitRepo(t)

	execResp := "main.go\n```go\npackage main\n\nfunc Hello() {}\n\nfunc main() {}\n```"

	var criticCalls atomic.Int32
	criticSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := criticCalls.Add(1)
		verdict := `{"verdict":"FAIL","summary":"bad","issue":"issue","fix":"fix","concerns":[]}`
		if n >= 2 {
			verdict = `{"verdict":"PASS","summary":"looks good","issue":"","fix":"","concerns":[]}`
		}
		jsonResponse(verdict)(w, r)
	}))
	execSrv := httptest.NewServer(muxBackend(sseResponse(execResp), jsonResponse("")))
	defer execSrv.Close()
	defer criticSrv.Close()

	store := newTestStore(t)
	gitSess := git.NewSession(repo, git.SessionOptions{})
	_ = gitSess.Start()
	mainSHA := headSHAOf(t, repo.Root())

	sessID := "test-retry"
	_ = store.InsertSession(&session.Session{
		ID: sessID, TargetBranch: "main", TargetStartSHA: mainSHA,
		StagingBranch: gitSess.StagingBranch, StartedAt: time.Now(),
	})

	reg := newTestRegistry(t, execSrv, criticSrv)
	eng := loop.New(
		loop.Config{MaxRounds: 3, SessionID: sessID, GitEnabled: true},
		repo, gitSess, store, reg, loop.DiscardSink{},
	)

	if err := eng.Run(context.Background(), "add Hello"); err != nil {
		t.Fatalf("expected PASS on second attempt, got: %v", err)
	}

	if criticCalls.Load() != 2 {
		t.Errorf("expected 2 critic calls (fail+pass), got %d", criticCalls.Load())
	}

	tasks, _ := store.TasksForSession(sessID)
	if len(tasks) != 1 || tasks[0].Status != session.StatusPassed {
		t.Errorf("expected 1 passed task, got %+v", tasks)
	}
}

func TestEngine_MainUnchangedAfterPass(t *testing.T) {
	repo := newTestGitRepo(t)
	mainSHABefore := headSHAOf(t, repo.Root())

	execResp := "main.go\n```go\npackage main\n\nfunc main() {}\n```"
	passVerdict := `{"verdict":"PASS","summary":"ok","issue":"","fix":"","concerns":[]}`

	execSrv := httptest.NewServer(muxBackend(sseResponse(execResp), jsonResponse("")))
	criticSrv := httptest.NewServer(jsonResponse(passVerdict))
	defer execSrv.Close()
	defer criticSrv.Close()

	store := newTestStore(t)
	gitSess := git.NewSession(repo, git.SessionOptions{})
	_ = gitSess.Start()

	sessID := "test-main-unchanged"
	_ = store.InsertSession(&session.Session{
		ID: sessID, TargetBranch: "main", TargetStartSHA: mainSHABefore,
		StagingBranch: gitSess.StagingBranch, StartedAt: time.Now(),
	})

	reg := newTestRegistry(t, execSrv, criticSrv)
	eng := loop.New(
		loop.Config{MaxRounds: 3, SessionID: sessID, GitEnabled: true},
		repo, gitSess, store, reg, loop.DiscardSink{},
	)
	_ = eng.Run(context.Background(), "no-op")

	// The main branch must not have moved.
	mainSHAAfter := func() string {
		c := exec.Command("git", "rev-parse", "main")
		c.Dir = repo.Root()
		out, _ := c.CombinedOutput()
		return strings.TrimSpace(string(out))
	}()
	if mainSHAAfter != mainSHABefore {
		t.Errorf("main branch moved: %s → %s", mainSHABefore, mainSHAAfter)
	}
}
