package agent

import (
	"context"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

const (
	titleMaxChars      = 50
	titleCallTimeout   = 5 * time.Second
	titleDirectiveText = `Generate a concise title for this conversation, at most 50 characters. No quotes, no markdown, no trailing punctuation. Respond with the title text only.`
)

// TitleGenerator forks a background one-shot LLM call to name a session
// (crush's title prompt / opencode session/prompt.ts fork, docs/12 F13). It is
// fire-and-forget: failures and timeouts leave the existing fallback name and
// never block the turn.
type TitleGenerator interface {
	Generate(ctx context.Context, firstUserMessage string)
}

// titleGenerator is the default implementation. generate() is the synchronous
// core; the runner wraps it in a goroutine with its own timeout.
type titleGenerator struct {
	provider provider.Provider
	model    string
	state    *session.State
	timeout  time.Duration
}

func NewTitleGenerator(p provider.Provider, model string, state *session.State) TitleGenerator {
	return &titleGenerator{provider: p, model: model, state: state, timeout: titleCallTimeout}
}

func (t *titleGenerator) Generate(ctx context.Context, firstUserMessage string) {
	go t.generate(ctx, firstUserMessage)
}

func (t *titleGenerator) generate(ctx context.Context, firstUserMessage string) {
	if t.state.TitleManuallySet() || t.state.Title() != "" {
		return
	}
	timeout := t.timeout
	if timeout <= 0 {
		timeout = titleCallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: firstUserMessage},
		{Role: schema.RoleSystem, Content: titleDirectiveText},
	}
	res, err := chatNoTools(ctx, t.provider, t.model, req)
	if err != nil || strings.TrimSpace(res) == "" {
		return
	}
	title := strings.TrimSpace(res)
	title = strings.Join(strings.Fields(title), " ")
	title = strings.Trim(title, "\"'`.,;:!?")
	if runes := []rune(title); len(runes) > titleMaxChars {
		title = string(runes[:titleMaxChars])
	}
	// Re-check the manual-title guard immediately before persisting.
	// A /rename issued after the early-return check at the top of
	// generate() but before the LLM call returns must not be silently
	// overwritten by the auto-title. Use the same mutex-locked check as
	// SetTitleManual so the read is race-free with concurrent renames.
	if t.state.TitleManuallySet() {
		return
	}
	t.state.SetTitle(title)
	if db := t.state.DB(); db != nil {
		_ = db.UpdateSessionTitle(t.state.SessionID(), title)
	}
}

// chatNoTools is a thin wrapper that drains a Chat stream to a single text
// string. It deliberately passes no tools — the title call must never call
// tools. Reuses the provider streaming protocol already used elsewhere
// (events are ChatEventDelta/Done/Error — see internal/llm/schema/event.go).
func chatNoTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage) (string, error) {
	ch, err := p.Chat(ctx, schema.ChatRequest{Model: model, Messages: messages})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for ev := range ch {
		if ev.Type == schema.ChatEventDelta {
			b.WriteString(ev.Delta)
		}
		if ev.Type == schema.ChatEventError {
			return "", ev.Err
		}
	}
	return b.String(), nil
}
