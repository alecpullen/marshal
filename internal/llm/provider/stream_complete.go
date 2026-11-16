package provider

import (
	"fmt"

	"marshal/internal/llm/schema"
)

// streamCompletion tracks whether a streaming adapter observed its protocol's
// terminal marker. EOF is a clean completion only when it did: AUD-08's
// recorded contract is that a stream is complete only when the provider's
// protocol announced completion, so an unterminated stream must surface as an
// interruption rather than a successful turn (and never as a repaired tool
// call). See .docs-archive/backend-tui-audit/plans/aud-08-stream-completion.md.
type streamCompletion struct {
	seen bool
}

// mark records that the protocol's terminal marker was observed.
func (s *streamCompletion) mark() { s.seen = true }

// interrupted builds the event an adapter emits when its stream ended without
// the protocol's terminal marker. markers names them for the user-visible
// error ("[DONE] or finish_reason", "message_stop or stop_reason", …). The
// event carries usage when the provider reported token counts before the cut,
// and never carries ToolCalls: a partially accumulated call must not be
// repaired into an apparently complete one.
func (s *streamCompletion) interrupted(markers string, usage *schema.TokenUsage) schema.ChatEvent {
	return schema.ChatEvent{
		Type:  schema.ChatEventInterrupted,
		Err:   fmt.Errorf("provider stream ended without a %s completion marker", markers),
		Usage: usage,
	}
}
