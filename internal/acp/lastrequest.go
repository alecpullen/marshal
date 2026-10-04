package acp

import (
	"context"
	"encoding/json"
	"time"

	"marshal/internal/app/session"
)

// RequestJSON is the wire form of session.RequestInspection.
type RequestJSON struct {
	AttemptID       uint64        `json:"attemptId"`
	At              int64         `json:"at,omitempty"`
	Provider        string        `json:"provider,omitempty"`
	Model           string        `json:"model,omitempty"`
	Generation      string        `json:"generation,omitempty"`
	LeafID          int64         `json:"leafId,omitempty"`
	Messages        []MessageJSON `json:"messages"`
	Tools           []ToolJSON    `json:"tools"`
	Options         OptionsJSON   `json:"options"`
	Outcome         OutcomeJSON   `json:"outcome"`
	Truncated       bool          `json:"truncated,omitempty"`
	OmittedBytes    int           `json:"omittedBytes,omitempty"`
	MessagesOmitted int           `json:"messagesOmitted,omitempty"`
	ToolsOmitted    int           `json:"toolsOmitted,omitempty"`
	PackTokens      int           `json:"packTokens,omitempty"`
	PackWindow      int           `json:"packWindow,omitempty"`
	PackTruncated   bool          `json:"packTruncated,omitempty"`
	PackSections    int           `json:"packSections,omitempty"`
	PackKnown       bool          `json:"packKnown,omitempty"`
}

// MessageJSON is one message of the request.
type MessageJSON struct {
	Role         string         `json:"role"`
	Content      string         `json:"content"`
	ToolCalls    []ToolCallJSON `json:"toolCalls,omitempty"`
	ToolCallID   string         `json:"toolCallId,omitempty"`
	Truncated    bool           `json:"truncated,omitempty"`
	OmittedBytes int            `json:"omittedBytes,omitempty"`
}

// ToolCallJSON is a tool call attached to a message.
type ToolCallJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Args         string `json:"args"`
	Truncated    bool   `json:"truncated,omitempty"`
	OmittedBytes int    `json:"omittedBytes,omitempty"`
}

// ToolJSON is a tool definition offered on the request.
type ToolJSON struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Parameters   string `json:"parameters,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	OmittedBytes int    `json:"omittedBytes,omitempty"`
}

// OptionsJSON records the options that actually went out.
type OptionsJSON struct {
	Thinking       string   `json:"thinking,omitempty"`
	Streaming      bool     `json:"streaming"`
	MaxTokens      *int     `json:"maxTokens,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
	ResponseFormat string   `json:"responseFormat,omitempty"`
	ToolChoice     string   `json:"toolChoice,omitempty"`
}

// OutcomeJSON is what became of the attempt.
type OutcomeJSON struct {
	Status string `json:"status"`
	Err    string `json:"err,omitempty"`
	At     int64  `json:"at,omitempty"`
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func toRequestJSON(r session.RequestInspection) RequestJSON {
	out := RequestJSON{
		AttemptID: r.AttemptID, At: ms(r.At), Provider: r.Provider, Model: r.Model,
		Generation: r.Generation, LeafID: r.LeafID,
		Messages: make([]MessageJSON, 0, len(r.Messages)),
		Tools:    make([]ToolJSON, 0, len(r.Tools)),
		Options: OptionsJSON{
			Thinking: r.Options.Thinking, Streaming: r.Options.Streaming, MaxTokens: r.Options.MaxTokens,
			Temperature: r.Options.Temperature, ResponseFormat: r.Options.ResponseFormat, ToolChoice: r.Options.ToolChoice,
		},
		Outcome:   OutcomeJSON{Status: string(r.Outcome.Status), Err: r.Outcome.Err, At: ms(r.Outcome.At)},
		Truncated: r.Truncated, OmittedBytes: r.OmittedBytes,
		MessagesOmitted: r.MessagesOmitted, ToolsOmitted: r.ToolsOmitted,
		PackTokens: r.PackTokens, PackWindow: r.PackWindow, PackTruncated: r.PackTruncated,
		PackSections: r.PackSections, PackKnown: r.PackKnown,
	}
	for _, m := range r.Messages {
		mj := MessageJSON{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Truncated: m.Truncated, OmittedBytes: m.OmittedBytes}
		for _, tc := range m.ToolCalls {
			mj.ToolCalls = append(mj.ToolCalls, ToolCallJSON{ID: tc.ID, Name: tc.Name, Args: tc.Args, Truncated: tc.Truncated, OmittedBytes: tc.OmittedBytes})
		}
		out.Messages = append(out.Messages, mj)
	}
	for _, t := range r.Tools {
		out.Tools = append(out.Tools, ToolJSON{Name: t.Name, Description: t.Description, Parameters: t.Parameters, Truncated: t.Truncated, OmittedBytes: t.OmittedBytes})
	}
	return out
}

// LastRequest handles session/last_request: the newest model request, or
// {"request": null} when the session has not made one.
func (m *TurnManager) LastRequest(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if err := decodeParams(params, &p, "session/last_request"); err != nil {
		return nil, err
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok || rt.State == nil {
		return nil, serverErrorf("unknown session: %s", p.SessionID)
	}
	if r, ok := rt.State.RequestInspection(); ok {
		return map[string]any{"request": toRequestJSON(r)}, nil
	}
	return map[string]any{"request": nil}, nil
}
