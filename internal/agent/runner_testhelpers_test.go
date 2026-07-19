package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/hooks"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// scriptedRouteResolver returns pre-canned routes and providers in call order.
type scriptedRouteResolver struct {
	routes    []routing.Route
	providers []provider.Provider
	errs      []error
	tasks     []routing.TaskProfile
}

func (r *scriptedRouteResolver) Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error) {
	r.tasks = append(r.tasks, task)
	if len(r.errs) > 0 {
		err := r.errs[0]
		r.errs = r.errs[1:]
		if err != nil {
			return routing.Route{}, nil, err
		}
	}
	if len(r.routes) == 0 {
		return routing.Route{}, nil, routing.ErrNoRoute
	}
	route := r.routes[0]
	r.routes = r.routes[1:]
	var p provider.Provider
	if len(r.providers) > 0 {
		p = r.providers[0]
		r.providers = r.providers[1:]
	}
	return route, p, nil
}

// fakeMemoryProvider returns a fixed memory slice for testing.
type fakeMemoryProvider struct {
	memories []contextpack.MemoryNote
	err      error
}

func (f *fakeMemoryProvider) Memories(projectID int64) ([]contextpack.MemoryNote, error) {
	return f.memories, f.err
}

// blockingProvider blocks until the context is cancelled. Used by timeout tests.
type blockingProvider struct{}

func (p *blockingProvider) Name() string { return "blocking" }

func (p *blockingProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) { return nil, nil }

func (p *blockingProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *blockingProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	events := make(chan schema.ChatEvent)
	go func() {
		defer close(events)
		<-ctx.Done()
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: ctx.Err()}
	}()
	return events, nil
}

// recordingGate records how many times Acquire() is called.
type recordingGate struct {
	mu           sync.Mutex
	acquisitions int
}

func (g *recordingGate) Acquire() (release func()) {
	g.mu.Lock()
	g.acquisitions++
	return g.mu.Unlock
}

// staticResolver returns a fixed route and provider. Used by route-resolution tests.
type staticResolver struct {
	route    routing.Route
	provider provider.Provider
}

func (s *staticResolver) Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error) {
	return s.route, s.provider, nil
}

// fakeHookRunner returns canned outputs for pre_tool_use and turn_end hooks.
type fakeHookRunner struct {
	preOut  hooks.Output
	turnOut hooks.Output
	preErr  error
}

func (f fakeHookRunner) RunPreToolUse(ctx context.Context, in hooks.PreToolUseInput) (hooks.Output, error) {
	return f.preOut, f.preErr
}

func (f fakeHookRunner) RunTurnEnd(ctx context.Context, in hooks.TurnEndInput) (hooks.Output, error) {
	return f.turnOut, nil
}

// onceRewriteHookRunner rewrites args exactly once, then passes through
// on subsequent calls. Used by TestAuditEventRecordsOriginalArgs.
type onceRewriteHookRunner struct {
	hasRewritten bool
}

func (r *onceRewriteHookRunner) RunPreToolUse(ctx context.Context, in hooks.PreToolUseInput) (hooks.Output, error) {
	if !r.hasRewritten {
		r.hasRewritten = true
		return hooks.Output{Rewrite: json.RawMessage(`{"command":"git --no-pager log"}`)}, nil
	}
	return hooks.Output{}, nil
}

func (r *onceRewriteHookRunner) RunTurnEnd(ctx context.Context, in hooks.TurnEndInput) (hooks.Output, error) {
	return hooks.Output{}, nil
}

func newTestState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func scriptRepeats(n int, resp string) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, resp)
	}
	return out
}

func answerPendingQuestion(state *session.State, answer string) <-chan string {
	questionCh := make(chan string, 1)
	go func() {
		for {
			if q := state.PendingQuestion(); q != nil && len(q.Questions) > 0 {
				canonical := q.Questions[0].Question
				questionCh <- canonical
				q.ResponseChan <- []session.Answer{{Question: canonical, Answer: answer}}
				state.SetPendingQuestion(nil)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return questionCh
}
