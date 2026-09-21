package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
	"marshal/internal/strutil"
)

// driftDirectiveText drives the turn-start drift classification.
const driftDirectiveText = `You manage this session's title. Given the current session title and the user's new message, decide whether the message continues the current task or starts a new task.
Reply with exactly CONTINUES if the message continues the current task.
Reply with exactly NEW_TASK: followed by a new title of at most 50 characters if the message starts a new task.
No quotes, no markdown, no trailing punctuation. Respond with nothing else.`

// TitleManager runs one small LLM call at the start of every user turn,
// before the main request: turn 1 generates the initial title; turn 2+
// classifies task drift and proposes a replacement title on drift. The call
// is synchronous (safe on single-model backends) and never fails the turn:
// errors, timeouts, and unrecognized replies keep the existing title.
type TitleManager interface {
	OnUserTurn(ctx context.Context, goal string)
}

type titleManager struct {
	provider provider.Provider
	model    string
	state    *session.State
	gen      *titleGenerator
	timeout  time.Duration
}

// NewTitleManager wires the turn-start titler. timeout caps the drift call;
// values <= 0 fall back to the title-call default.
func NewTitleManager(p provider.Provider, model string, state *session.State, timeout time.Duration) TitleManager {
	if timeout <= 0 {
		timeout = titleCallTimeout
	}
	return &titleManager{
		provider: p,
		model:    model,
		state:    state,
		gen:      &titleGenerator{provider: p, model: model, state: state, timeout: timeout},
		timeout:  timeout,
	}
}

// OnUserTurn runs the turn-start call. Untitled sessions get the initial
// title (titleGenerator.generate already derives it from the user message
// alone); titled sessions get the drift check.
func (m *titleManager) OnUserTurn(ctx context.Context, goal string) {
	if m.state.Title() != "" || m.state.TitleManuallySet() {
		m.classifyDrift(ctx, goal)
		return
	}
	m.gen.generate(ctx, goal)
}

func (m *titleManager) classifyDrift(ctx context.Context, goal string) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	req := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: fmt.Sprintf("Current title: %s\n\nNew user message:\n%s", m.state.Title(), goal)},
		{Role: schema.RoleSystem, Content: driftDirectiveText},
	}
	res, err := provider.ChatText(ctx, m.provider, schema.ChatRequest{Model: m.model, Messages: req})
	if err != nil {
		m.state.Logger().Warn("title drift check failed; keeping current title", "error", err)
		return
	}
	candidate, drifted := parseDriftReply(res)
	if !drifted {
		return
	}
	m.applyDrift(candidate)
}

// parseDriftReply strictly parses the classifier reply: CONTINUES keeps the
// current title, "NEW_TASK: <title>" proposes a candidate, and anything else
// (including an empty candidate) is treated as CONTINUES.
func parseDriftReply(reply string) (candidate string, drifted bool) {
	s := strings.TrimSpace(reply)
	if s == "CONTINUES" {
		return "", false
	}
	if rest, ok := strings.CutPrefix(s, "NEW_TASK:"); ok {
		rest = cleanTitleCandidate(rest)
		if rest == "" {
			return "", false
		}
		return rest, true
	}
	return "", false
}

// cleanTitleCandidate normalises a model-proposed title the same way the
// initial title path does (whitespace collapse, quote/punct trim, 50-char cap).
func cleanTitleCandidate(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'`.,;:!?")
	return strutil.Truncate(s, titleMaxChars, false)
}

// applyDrift applies a drift candidate: auto-named sessions update silently;
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
