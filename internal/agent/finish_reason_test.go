package agent

import (
	"sync"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestTurnFinishReasonConcurrentAccess(t *testing.T) {
	state := newTestState(t)
	r := NewRunner(
		&agenttest.ScriptedProvider{Responses: []string{"ok"}},
		registry.New(),
		policy.NewEngine(&config.Config{}, nil),
		state,
		"test-model",
	)

	// Concurrent writes and reads should not race.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.setTurnFinishReason("stop")
		}()
		go func() {
			defer wg.Done()
			_ = r.getTurnFinishReason()
		}()
	}
	wg.Wait()
}
