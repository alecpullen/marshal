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

// titleGenerator is the default implementation. request() is the shared
// synchronous core; generate() guards it for the initial title.
type titleGenerator struct {
	provider provider.Provider
	model    string
	state    *session.State
	timeout  time.Duration
}

// generate stores the initial title for a still-untitled session, derived
// from the first user message. Manual titles and existing titles are left
// untouched.
func (t *titleGenerator) generate(ctx context.Context, firstUserMessage string) {
	if t.state.TitleManuallySet() || t.state.Title() != "" {
		return
	}
	title, ok := t.request(ctx, firstUserMessage)
	if !ok {
		return
	}
	// SetTitleIfNotManual atomically checks the manual-title guard and
	// persists the title under one lock, closing the TOCTOU window where
	// a /rename between the check and the set could be overwritten.
	if !t.state.SetTitleIfNotManual(title) {
		return
	}
	if db := t.state.DB(); db != nil {
		_ = db.UpdateSessionTitle(t.state.SessionID(), title, false)
	}
}

// request asks the title model for a title for the given message and
// returns it cleaned (whitespace collapse, quote/punctuation trim, 50-char
// cap). ok is false on error, timeout, or empty reply.
func (t *titleGenerator) request(ctx context.Context, message string) (title string, ok bool) {
	timeout := t.timeout
	if timeout <= 0 {
		timeout = titleCallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: titleDirectiveText},
		{Role: schema.RoleUser, Content: message},
	}
	res, err := provider.ChatText(ctx, t.provider, schema.ChatRequest{Model: t.model, Messages: req})
	if err != nil || strings.TrimSpace(res) == "" {
		return "", false
	}
	title = strings.TrimSpace(res)
	title = strings.Join(strings.Fields(title), " ")
	title = strings.Trim(title, "\"'`.,;:!?")
	return strutil.Truncate(title, titleMaxChars, false), true
}
