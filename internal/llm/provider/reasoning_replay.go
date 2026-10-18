package provider

import "regexp"

// ReasoningField values for Options.ReasoningField / CodexOptions.ReasoningField.
const (
	ReasoningFieldAuto      = "auto"
	ReasoningFieldContent   = "reasoning_content"
	ReasoningFieldReasoning = "reasoning"
	ReasoningFieldOff       = "off"
)

// resolveReplayField maps the configured setting to the wire field the
// chat-completions encoder writes; "" means do not replay.
func resolveReplayField(setting string) string {
	switch setting {
	case "", ReasoningFieldAuto:
		return ReasoningFieldContent
	case ReasoningFieldOff:
		return ""
	default:
		return setting
	}
}

// replayFieldRejected reports whether an error body names field as a whole
// word, so "reasoning" does not match "reasoning_effort".
func replayFieldRejected(body []byte, field string) bool {
	if field == "" {
		return false
	}
	re := regexp.MustCompile(`(^|[^a-z_])` + regexp.QuoteMeta(field) + `([^a-z_]|$)`)
	return re.Match(body)
}
