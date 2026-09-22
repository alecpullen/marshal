package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
)

// newTaskPrefix is the deterministic re-titling trigger: a user message that
// begins with it (case-insensitive, followed by whitespace, ":", or "-")
// asks for a fresh session title.
const newTaskPrefix = "new task"

// TitleManager runs at most one small LLM call at the start of a user turn,
// before the main request: turn 1 generates the initial title; later turns
// re-title only when the user explicitly starts a new task (see
// newTaskPrefix). Ordinary turns make no call at all, so titling never adds
// turn-start latency. The call is synchronous (safe on single-model backends)
// and never fails the turn: errors and timeouts keep the current title.
type TitleManager interface {
	OnUserTurn(ctx context.Context, goal string)
}

type titleManager struct {
	state *session.State
	gen   *titleGenerator
}

// NewTitleManager wires the turn-start titler. timeout caps the title call;
// values <= 0 fall back to the title-call default.
func NewTitleManager(p provider.Provider, model string, state *session.State, timeout time.Duration) TitleManager {
	if timeout <= 0 {
		timeout = titleCallTimeout
	}
	return &titleManager{
		state: state,
		gen:   &titleGenerator{provider: p, model: model, state: state, timeout: timeout},
	}
}

// OnUserTurn runs the turn-start titling. Untitled sessions get the initial
// title (titleGenerator.generate already derives it from the user message
// alone); titled sessions keep their title unless the message explicitly
// starts a new task.
func (m *titleManager) OnUserTurn(ctx context.Context, goal string) {
	if m.state.Title() == "" && !m.state.TitleManuallySet() {
		m.gen.generate(ctx, goal)
		return
	}
	if _, isNew := parseNewTaskMessage(goal); !isNew {
		return
	}
	m.regenerate(ctx, goal)
}

// parseNewTaskMessage reports whether the user message explicitly starts a
// new task and returns the candidate title text following the prefix. The
// prefix must start the message (case-insensitive) and be followed by
// whitespace, ":", or "-", so ordinary mentions of "a new task" and
// words like "new taskforce" never trigger.
func parseNewTaskMessage(goal string) (candidate string, isNew bool) {
	s := strings.ToLower(strings.TrimSpace(goal))
	rest, ok := strings.CutPrefix(s, newTaskPrefix)
	if !ok || rest == "" {
		return "", false
	}
	if c := rest[0]; c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != ':' && c != '-' {
		return "", false
	}
	rest = strings.TrimSpace(strings.TrimLeft(rest, " \t\n\r:-"))
	if rest == "" {
		return "", false
	}
	return rest, true
}

// regenerate asks the title model for a fresh title from the new-task
// message and applies it. Failures keep the current title silently.
func (m *titleManager) regenerate(ctx context.Context, goal string) {
	title, ok := m.gen.request(ctx, goal)
	if !ok {
		return
	}
	m.applyDrift(title)
}

// applyDrift applies a new title: auto-named sessions update silently;
// manually named sessions get a proposal note instead of an overwrite.
func (m *titleManager) applyDrift(candidate string) {
	if m.state.TitleManuallySet() {
		m.state.AddMessage(session.RoleSystem,
			fmt.Sprintf("Suggested title: %q — run /rename %q to apply, or /rename with no args to re-enable auto-titling.", candidate, candidate),
			session.ContentTypePlain)
		return
	}
	// SetTitleIfNotManual atomically re-checks the manual guard under the
	// state lock, closing the TOCTOU window with a concurrent /rename.
	if !m.state.SetTitleIfNotManual(candidate) {
		return
	}
	if db := m.state.DB(); db != nil {
		_ = db.UpdateSessionTitle(m.state.SessionID(), candidate, false)
	}
}
