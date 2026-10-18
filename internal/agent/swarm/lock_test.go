// internal/agent/swarm/lock_test.go
package swarm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestAcquireReleaseIdempotent(t *testing.T) {
	var lock WriteLock

	release := lock.Acquire()
	release()
	// Second call must not panic and must be a no-op.
	release()

	// The lock must actually be released by the first call: a regression that
	// made release a no-op would silently pass the double-call above, so
	// re-acquire with a non-blocking path and assert it succeeds.
	acquired := make(chan struct{})
	go func() {
		l := lock.Acquire()
		close(acquired)
		l()
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("WriteLock was not released by a prior release(); re-acquire blocked forever")
	}
}

func newLockTestState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func TestWriteLockSerialisesConcurrentWriters(t *testing.T) {
	var active int32
	var overlapped atomic.Bool

	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "fs.touch", Description: "write", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			if atomic.AddInt32(&active, 1) > 1 {
				overlapped.Store(true)
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&active, -1)
			return registry.ToolResult{Summary: "touched"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	script := []string{
		`{"rationale": "write", "action": {"type": "tool_call", "tool": "fs.touch", "args": {}}}`,
		`{"rationale": "done", "action": {"type": "final", "content": "done"}}`,
	}
	state := newLockTestState(t)
	lock := &WriteLock{}

	newWriter := func() *agent.Runner {
		r := agent.NewRunner(&agenttest.ScriptedProvider{Responses: script}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.SetForceClass("question")
		r.WriteGate = lock
		return r
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := newWriter().Run(context.Background(), "touch"); err != nil {
				t.Errorf("Run: %v", err)
			}
		}()
	}
	wg.Wait()

	if overlapped.Load() {
		t.Fatal("two write-tool executions overlapped; WriteLock must serialise them")
	}
}

func TestWriteLockCanceledWaitDoesNotAcquire(t *testing.T) {
	var lock WriteLock
	held := lock.Acquire()

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 1)
	go func() {
		release, err := lock.AcquireContext(ctx)
		if err == nil {
			release()
		}
		results <- err
	}()

	// Let the waiter block on the held token, then cancel it.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-results:
		if err == nil {
			t.Fatal("blocked AcquireContext returned nil error after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not wake")
	}

	held()

	acquired := make(chan struct{})
	go func() {
		r := lock.Acquire()
		close(acquired)
		r()
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("token leaked: no admission available after a canceled wait")
	}
}

func TestWriteLockCancelAcquireRaceDoesNotLeakToken(t *testing.T) {
	for i := 0; i < 200; i++ {
		var lock WriteLock
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := lock.AcquireContext(ctx)
			if err == nil {
				release()
			}
		}()
		cancel()
		wg.Wait()

		// However the race resolved, exactly one token must remain.
		done := make(chan struct{})
		go func() {
			r := lock.Acquire()
			r()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: token leaked", i)
		}
	}
}

func TestWriteLockMixedLegacyAndContextSerialization(t *testing.T) {
	var lock WriteLock
	var active int32
	var overlapped atomic.Bool

	run := func(release func()) {
		if atomic.AddInt32(&active, 1) > 1 {
			overlapped.Store(true)
		}
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		release()
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(legacy bool) {
			defer wg.Done()
			if legacy {
				run(lock.Acquire())
				return
			}
			release, err := lock.AcquireContext(context.Background())
			if err != nil {
				t.Errorf("AcquireContext: %v", err)
				return
			}
			run(release)
		}(i%2 == 0)
	}
	wg.Wait()

	if overlapped.Load() {
		t.Fatal("legacy and context acquisitions overlapped; serialization broken")
	}
}

func TestWriteLockReleaseIsIdempotent(t *testing.T) {
	var lock WriteLock
	release, err := lock.AcquireContext(context.Background())
	if err != nil {
		t.Fatalf("AcquireContext: %v", err)
	}
	release()
	// Extra releases must be no-ops, not extra tokens.
	release()
	release()

	// The single token is available again.
	first, err := lock.AcquireContext(context.Background())
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}

	// A double release must not have minted a second token: a concurrent
	// acquire must block until first is released.
	got := make(chan struct{})
	go func() {
		r := lock.Acquire()
		close(got)
		r()
	}()

	select {
	case <-got:
		t.Fatal("release was not idempotent: a second token was minted")
	case <-time.After(100 * time.Millisecond):
	}

	first()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("first release did not return the token")
	}
}
