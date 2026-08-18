package provider

import (
	"context"
	"fmt"
	"strings"

	"marshal/internal/llm/schema"
)

// ChatText issues a one-shot Chat request and drains the stream to a single
// string. It passes no tools and returns the first error event as a Go
// error. Shared by the title generator and the knowledge extractor, which
// previously each carried their own copy of this loop.
func ChatText(ctx context.Context, p Provider, req schema.ChatRequest) (string, error) {
	ch, err := p.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	var done bool
	for ev := range ch {
		switch ev.Type {
		case schema.ChatEventDelta:
			b.WriteString(ev.Delta)
		case schema.ChatEventError:
			return b.String(), ev.Err
		case schema.ChatEventDone:
			done = true
		}
	}
	if !done {
		return b.String(), fmt.Errorf("stream ended without Done signal (%d bytes received)", b.Len())
	}
	return b.String(), nil
}
