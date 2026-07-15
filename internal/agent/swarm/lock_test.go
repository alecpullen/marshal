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
