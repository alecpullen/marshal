package loop_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/loop"
	"github.com/alecpullen/marshal/internal/session"
)

// callbackHandler wraps a function that returns response content per call.
func callbackHandler(fn func(call int) string) http.HandlerFunc {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		call := int(n.Add(1))
		jsonResponse(fn(call))(w, r)
	}
}

// newTestRegistryFull builds a registry with distinct backends for all four
// roles, using separate test servers for executor, critic, and compactor.
func newTestRegistryFull(t *testing.T, execSrv, criticSrv, compactorSrv *httptest.Server) *backend.Registry {
	t.Helper()
	mk := func(srv *httptest.Server, model string) backend.Backend {
		return backend.NewOpenAICompat(backend.OpenAICompatConfig{
			BaseURL:    srv.URL,
			ModelName:  model,
			SupTools:   false,
			HTTPClient: srv.Client(),
		}, nil)
	}
	return backend.NewRegistryFromBackends(map[string]backend.Backend{
		"marshal":   mk(execSrv, "test-exec"),
		"executor":  mk(execSrv, "test-exec"),
		"critic":    mk(criticSrv, "test-critic"),
		"compactor": mk(compactorSrv, "test-compactor"),
	})
}

// TestEngine_CompactorCalledAfterThreshold verifies the compactor backend is
// invoked once compact_after consecutive FAIL rounds have accumulated and that
// the synthesized issue/fix is used in subsequent rounds.
func TestEngine_CompactorCalledAfterThreshold(t *testing.T) {
	repo := newTestGitRepo(t)

	// Executor always writes a different Hello function (net diff vs staging).
	execResp := "main.go\n```go\npackage main\n\nfunc Hello() string { return \"hello\" }\n\nfunc main() {}\n```"
	execSrv := httptest.NewServer(muxBackend(sseResponse(execResp), jsonResponse("unused")))
	defer execSrv.Close()

	// Critic: FAIL for rounds 1 & 2, PASS from round 3 on.
	var criticCalls atomic.Int32
	criticSrv := httptest.NewServer(callbackHandler(func(call int) string {
		criticCalls.Add(1)
		if call <= 2 {
			return `{"verdict":"FAIL","summary":"not done","issue":"still missing","fix":"add it","concerns":[]}`
		}
		return `{"verdict":"PASS","summary":"done","issue":"","fix":"","concerns":[]}`
	}))
	defer criticSrv.Close()

	// Compactor: returns a synthesised issue/fix; track whether it was called.
	var compactorCalled atomic.Int32
	compactorSrv := httptest.NewServer(callbackHandler(func(_ int) string {
		compactorCalled.Add(1)
		return `{"issue":"synthesized issue","fix":"synthesized fix"}`
	}))
	defer compactorSrv.Close()

	store := newTestStore(t)
	gitSess := git.NewSession(repo, git.SessionOptions{})
	_ = gitSess.Start()
	mainSHA := headSHAOf(t, repo.Root())
	sessID := "test-compact"
	_ = store.InsertSession(&session.Session{
		ID: sessID, TargetBranch: "main", TargetStartSHA: mainSHA,
		StagingBranch: gitSess.StagingBranch, StartedAt: time.Now(),
	})

	reg := newTestRegistryFull(t, execSrv, criticSrv, compactorSrv)
	eng := loop.New(
		loop.Config{
			MaxRounds:    4,
			CompactAfter: 2, // compact after 2 consecutive FAILs
			SessionID:    sessID,
			GitEnabled:   true,
		},
		repo, gitSess, store, reg, loop.DiscardSink{},
	)

	if err := eng.Run(context.Background(), "implement feature"); err != nil {
		t.Fatalf("expected eventual PASS, got: %v", err)
	}
	if compactorCalled.Load() == 0 {
		t.Error("expected compactor to be called after compact_after failures")
	}
}

// TestEngine_CompactorNotCalledBelowThreshold verifies the compactor is not
// invoked when the failure count is below the compact_after threshold.
func TestEngine_CompactorNotCalledBelowThreshold(t *testing.T) {
	repo := newTestGitRepo(t)
	execResp := "main.go\n```go\npackage main\n\nfunc Hello() string { return \"hi\" }\n\nfunc main() {}\n```"
	execSrv := httptest.NewServer(muxBackend(sseResponse(execResp), jsonResponse("unused")))
	defer execSrv.Close()

	// One FAIL then PASS — below the threshold of 2.
	var criticCalls atomic.Int32
	criticSrv := httptest.NewServer(callbackHandler(func(call int) string {
		criticCalls.Add(1)
		if call < 2 {
			return `{"verdict":"FAIL","summary":"bad","issue":"x","fix":"y","concerns":[]}`
		}
		return `{"verdict":"PASS","summary":"ok","issue":"","fix":"","concerns":[]}`
	}))
	defer criticSrv.Close()

	var compactorCalled atomic.Int32
	compactorSrv := httptest.NewServer(callbackHandler(func(_ int) string {
		compactorCalled.Add(1)
		return `{"issue":"x","fix":"y"}`
	}))
	defer compactorSrv.Close()

	store := newTestStore(t)
	gitSess := git.NewSession(repo, git.SessionOptions{})
	_ = gitSess.Start()
	mainSHA := headSHAOf(t, repo.Root())
	sessID := "test-no-compact"
	_ = store.InsertSession(&session.Session{
		ID: sessID, TargetBranch: "main", TargetStartSHA: mainSHA,
		StagingBranch: gitSess.StagingBranch, StartedAt: time.Now(),
	})

	reg := newTestRegistryFull(t, execSrv, criticSrv, compactorSrv)
	eng := loop.New(
		loop.Config{
			MaxRounds:    3,
			CompactAfter: 2, // need 2 failures; we only have 1
			SessionID:    sessID,
			GitEnabled:   true,
		},
		repo, gitSess, store, reg, loop.DiscardSink{},
	)

	if err := eng.Run(context.Background(), "task"); err != nil {
		t.Fatalf("expected PASS, got: %v", err)
	}
	if compactorCalled.Load() != 0 {
		t.Error("compactor should not be called when threshold not reached")
	}
}

// TestEngine_CompactorDisabled verifies compact_after=0 disables compaction.
func TestEngine_CompactorDisabled(t *testing.T) {
	repo := newTestGitRepo(t)
	execResp := "main.go\n```go\npackage main\n\nfunc Hello() string { return \"hi\" }\n\nfunc main() {}\n```"
	execSrv := httptest.NewServer(muxBackend(sseResponse(execResp), jsonResponse("unused")))
	defer execSrv.Close()

	criticSrv := httptest.NewServer(callbackHandler(func(call int) string {
		if call < 3 {
			return `{"verdict":"FAIL","summary":"bad","issue":"x","fix":"y","concerns":[]}`
		}
		return `{"verdict":"PASS","summary":"ok","issue":"","fix":"","concerns":[]}`
	}))
	defer criticSrv.Close()

	var compactorCalled atomic.Int32
	compactorSrv := httptest.NewServer(callbackHandler(func(_ int) string {
		compactorCalled.Add(1)
		return `{"issue":"x","fix":"y"}`
	}))
	defer compactorSrv.Close()

	store := newTestStore(t)
	gitSess := git.NewSession(repo, git.SessionOptions{})
	_ = gitSess.Start()
	mainSHA := headSHAOf(t, repo.Root())
	sessID := "test-compact-disabled"
	_ = store.InsertSession(&session.Session{
		ID: sessID, TargetBranch: "main", TargetStartSHA: mainSHA,
		StagingBranch: gitSess.StagingBranch, StartedAt: time.Now(),
	})

	reg := newTestRegistryFull(t, execSrv, criticSrv, compactorSrv)
	eng := loop.New(
		loop.Config{
			MaxRounds:    4,
			CompactAfter: 0, // disabled
			SessionID:    sessID,
			GitEnabled:   true,
		},
		repo, gitSess, store, reg, loop.DiscardSink{},
	)

	if err := eng.Run(context.Background(), "task"); err != nil {
		t.Fatalf("expected PASS, got: %v", err)
	}
	if compactorCalled.Load() != 0 {
		t.Error("compactor should not be called when compact_after=0")
	}
}
