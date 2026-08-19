package agent

import (
	"context"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
	"marshal/internal/strutil"
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
	res, err := provider.ChatText(ctx, t.provider, schema.ChatRequest{Model: t.model, Messages: req})
	if err != nil || strings.TrimSpace(res) == "" {
		return
	}
	title := strings.TrimSpace(res)
	title = strings.Join(strings.Fields(title), " ")
	title = strings.Trim(title, "\"'`.,;:!?")
	title = strutil.Truncate(title, titleMaxChars, false)
	// SetTitleIfNotManual atomically checks the manual-title guard and
	// persists the title under one lock, closing the TOCTOU window where
	// a /rename between the check and the set could be overwritten.
	if !t.state.SetTitleIfNotManual(title) {
		return
	}
	if db := t.state.DB(); db != nil {
		_ = db.UpdateSessionTitle(t.state.SessionID(), title)
	}
}
