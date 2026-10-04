package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"marshal/internal/llm/schema"
	"marshal/internal/llm/streaming"
)

// --- wire types (OpenAI Responses API) ---

// responsesContentPart is one content part inside a role item's content
// array: input_text for user/system items, output_text for assistant
// items.
type responsesContentPart struct {
	Type string `json:"type"` // input_text | output_text
	Text string `json:"text"`
}

// responsesInputItem is one item in the top-level input array. Role
// items are typed "message" and carry a content parts array;
// function_call / function_call_output items carry their own fields.
type responsesInputItem struct {
	Type    string                 `json:"type"` // message | function_call | function_call_output
	Role    string                 `json:"role,omitempty"`
	Content []responsesContentPart `json:"content,omitempty"`
	// function_call fields
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output fields
	Output string `json:"output,omitempty"`
}

// responsesTool is FLAT — {type, name, description, parameters} — not
// the chat-completions nested {function: {…}} shape. Key wire difference
// of the Responses API.
type responsesTool struct {
	Type        string          `json:"type"` // always "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// responsesReasoning unifies the chat path's reasoning_effort and
// reasoning.summary fields into the Responses reasoning object.
type responsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// responsesText carries req.ResponseFormat through as text.format
// (json_schema passthrough); nil when unset.
type responsesText struct {
	Format *schema.ResponseFormat `json:"format,omitempty"`
}

type responsesRequestBody struct {
	Model           string               `json:"model"`
	Input           []responsesInputItem `json:"input"`
	Instructions    string               `json:"instructions,omitempty"`
	Tools           []responsesTool      `json:"tools,omitempty"`
	ToolChoice      string               `json:"tool_choice,omitempty"`
	Stream          bool                 `json:"stream"`
	Temperature     *float64             `json:"temperature,omitempty"`
	TopP            *float64             `json:"top_p,omitempty"`
	MaxOutputTokens *int                 `json:"max_output_tokens,omitempty"`
	Reasoning       *responsesReasoning  `json:"reasoning,omitempty"`
	Store           bool                 `json:"store"` // always false: stateless client
	Text            *responsesText       `json:"text,omitempty"`
	// Include lists extra response fields to return. The codex backend
	// requires ["reasoning.encrypted_content"] on every request (it 400s
	// without it); the openai_compatible path leaves it empty.
	Include []string `json:"include,omitempty"`
}

// --- usage ---

type responsesUsageDetails struct {
	CachedTokens    int `json:"cached_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
}

type responsesUsage struct {
	InputTokens         int                    `json:"input_tokens"`
	OutputTokens        int                    `json:"output_tokens"`
	TotalTokens         int                    `json:"total_tokens"`
	InputTokensDetails  *responsesUsageDetails `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *responsesUsageDetails `json:"output_tokens_details,omitempty"`
}

// responsesUsageFrom maps Responses usage to schema semantics. The Go
// table bills cached reads for every /responses family, so the cached
// and reasoning details matter for cost display.
func responsesUsageFrom(u *responsesUsage) *schema.TokenUsage {
	if u == nil {
		return nil
	}
	out := &schema.TokenUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.InputTokensDetails != nil {
		out.CacheReadTokens = u.InputTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	return out
}

// --- response decoding ---

// responsesOutputItem is one item in the non-streaming output array
// (and the item field of output_item stream events).
type responsesOutputItem struct {
	Type    string `json:"type"` // message | function_call | reasoning
	Role    string `json:"role,omitempty"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// reasoning items carry summary parts
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary,omitempty"`
}

type responsesResponse struct {
	Output []responsesOutputItem `json:"output"`
	Status string                `json:"status,omitempty"` // completed | incomplete | failed
	// IncompleteDetails explains a status == "incomplete" response. Its
	// reason (max_output_tokens, content_filter, …) is surfaced in the
	// empty-content error so a truncated turn is diagnosable instead of
	// reading as a generic empty response.
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Usage *responsesUsage `json:"usage,omitempty"`
	Error *apiError       `json:"error,omitempty"`
}

// responsesFinishReason maps the Responses status to the OpenAI-style
// finish reasons marshal's agent loop understands: incomplete → length
// (the response was cut short, max_output_tokens being the documented
// reason), any function_call items → tool_calls, else stop.
func responsesFinishReason(resp *responsesResponse) string {
	if resp.Status == "incomplete" {
		return "length"
	}
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			return "tool_calls"
		}
	}
	return "stop"
}

// --- request building ---

// buildResponsesInput converts marshal messages into Responses input
// items. The leading run of system messages concatenates into the
// top-level instructions field (matching @ai-sdk/openai, which OpenCode
// itself uses); any mid-conversation system message (skill hints,
// steering) becomes a role item at its position — order preserved.
func buildResponsesInput(msgs []schema.ChatMessage) (instructions string, items []responsesInputItem) {
	return buildResponsesInputWithSystemRole(msgs, "system")
}

// buildResponsesInputWithSystemRole keeps the leading harness prompt in
// instructions and adapts subsequent system messages to the provider's role.
func buildResponsesInputWithSystemRole(msgs []schema.ChatMessage, systemRole string) (instructions string, items []responsesInputItem) {
	var sysParts []string
	leading := true
	for _, m := range msgs {
		if leading && m.Role == schema.RoleSystem {
			if m.Content != "" {
				sysParts = append(sysParts, m.Content)
			}
			continue
		}
		leading = false
		switch m.Role {
		case schema.RoleSystem:
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    systemRole,
				Content: []responsesContentPart{{Type: "input_text", Text: m.Content}},
			})
		case schema.RoleUser:
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    "user",
				Content: []responsesContentPart{{Type: "input_text", Text: m.Content}},
			})
		case schema.RoleAssistant:
			// The message item is emitted only when there is text: an
			// assistant turn that is purely tool calls is represented by
			// its function_call items alone.
			if m.Content != "" {
				items = append(items, responsesInputItem{
					Type:    "message",
					Role:    "assistant",
					Content: []responsesContentPart{{Type: "output_text", Text: m.Content}},
				})
			}
			for _, call := range m.ToolCalls {
				// The Responses API requires an arguments string on
				// function_call items, so an empty or malformed recorded
				// call would otherwise drop the field (omitempty) and 400
				// on replay. Same defense as toAnthropicMessages.
				args := string(call.Args)
				if len(call.Args) == 0 || !json.Valid(call.Args) {
					args = "{}"
				}
				items = append(items, responsesInputItem{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      call.Name,
					Arguments: args,
				})
			}
		case schema.RoleTool:
			items = append(items, responsesInputItem{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: m.Content,
			})
		}
	}
	return strings.Join(sysParts, "\n\n"), items
}

func buildResponsesRequestBody(req schema.ChatRequest, reasoningSummary bool) ([]byte, error) {
	return buildResponsesRequestBodyWithInclude(req, reasoningSummary, nil)
}

// buildResponsesRequestBodyWithInclude is buildResponsesRequestBody with an
// explicit include list. The codex backend needs
// ["reasoning.encrypted_content"] on every request; every other caller
// passes nil and gets the previous wire shape byte for byte.
func buildResponsesRequestBodyWithInclude(req schema.ChatRequest, reasoningSummary bool, include []string) ([]byte, error) {
	return buildResponsesRequestBodyWithSystemRole(req, reasoningSummary, include, "system")
}

func buildResponsesRequestBodyWithSystemRole(req schema.ChatRequest, reasoningSummary bool, include []string, systemRole string) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("chat request: model is required")
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("chat request: at least one message is required")
	}
	instructions, input := buildResponsesInputWithSystemRole(req.Messages, systemRole)

	var tools []responsesTool
	for _, tool := range req.Tools {
		tools = append(tools, responsesTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}

	// Explicit off encodes none; default omits the override. Models that
	// cannot disable reasoning do not offer off in discovered options.
	effort := openAIThinkingEffort(req)
	var reasoning *responsesReasoning
	if effort != "" || reasoningSummary {
		reasoning = &responsesReasoning{Effort: effort}
		if reasoningSummary {
			reasoning.Summary = "auto"
		}
	}

	var text *responsesText
	if req.ResponseFormat != nil {
		text = &responsesText{Format: req.ResponseFormat}
	}

	// req.Stop is intentionally dropped: the Responses API has no stop
	// parameter. previous_response_id is never sent (stateless client).
	return json.Marshal(responsesRequestBody{
		Model:           req.Model,
		Input:           input,
		Instructions:    instructions,
		Tools:           tools,
		ToolChoice:      req.ToolChoice,
		Stream:          req.Stream,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		MaxOutputTokens: req.MaxTokens,
		Reasoning:       reasoning,
		Store:           false,
		Text:            text,
		Include:         include,
	})
}

// --- Chat ---

// responsesChat posts to the OpenAI Responses API (/responses) — the
// wire protocol OpenCode Go serves the grok/gpt/muse-spark families
// on. Same error contract as chat(): HTTP-level failures return
// synchronously; in-stream failures arrive as one ChatEventError
// followed by channel close. The strict-template demote/retry never
// applies here (that is llama.cpp/LM Studio behavior).
func (p *OpenAICompatible) responsesChat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	body, err := buildResponsesRequestBody(req, p.reasoningSummary)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.name, err)
	}
	writeRequestCapture(p.name, body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+endpointResponses, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider %q: build chat request: %w", p.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, &RequestError{Provider: p.name, Op: "chat request failed", Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, HTTPError(p.name, resp)
	}

	events := make(chan schema.ChatEvent)
	capture := newWireCapture(p.name)
	if req.Stream {
		go streamResponsesEvents(capture.wrap(resp.Body), capture, events)
	} else {
		go readResponsesResponse(capture.wrap(resp.Body), events)
	}
	return events, nil
}

func readResponsesResponse(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	var parsed responsesResponse
	if err := json.NewDecoder(body).Decode(&parsed); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode chat response: %w", err)}
		return
	}
	if parsed.Error != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(parsed.Error.Message)}
		return
	}

	var thinking, answer strings.Builder
	var toolCalls []schema.ToolCall
	for _, item := range parsed.Output {
		switch item.Type {
		case "reasoning":
			for _, s := range item.Summary {
				thinking.WriteString(s.Text)
			}
		case "message":
			for _, part := range item.Content {
				answer.WriteString(part.Text)
			}
		case "function_call":
			toolCalls = append(toolCalls, schema.ToolCall{
				ID:   item.CallID,
				Name: item.Name,
				Args: json.RawMessage(item.Arguments),
			})
		}
	}
	toolCalls, _ = repairToolCalls(toolCalls)

	if thinking.Len() > 0 {
		events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: thinking.String()}
	}
	if answer.Len() > 0 {
		events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: answer.String()}
	}
	if thinking.Len() == 0 && answer.Len() == 0 && len(toolCalls) == 0 {
		msg := "responses endpoint returned empty content"
		if parsed.IncompleteDetails != nil && parsed.IncompleteDetails.Reason != "" {
			msg += ": " + parsed.IncompleteDetails.Reason
		}
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(msg)}
		return
	}
	events <- schema.ChatEvent{
		Type:         schema.ChatEventDone,
		FinishReason: responsesFinishReason(&parsed),
		Usage:        responsesUsageFrom(parsed.Usage),
		ToolCalls:    toolCalls,
	}
}

// --- streaming ---

// responsesStreamEvent is the union of every SSE data payload the
// Responses API emits; Type discriminates. There is no [DONE] sentinel
// — the stream ends at response.completed + EOF, which the shared
// decoder already handles.
type responsesStreamEvent struct {
	Type        string               `json:"type"`
	OutputIndex int                  `json:"output_index"`
	Item        *responsesOutputItem `json:"item"`
	Delta       string               `json:"delta"`
	Message     string               `json:"message"` // top-level error event text
	Response    *responsesResponse   `json:"response"`
	Error       *apiError            `json:"error"`
}

// responsesToolBuffer accumulates one function_call item's streamed
// state, keyed by output_index.
type responsesToolBuffer struct {
	callID    string
	name      string
	arguments strings.Builder
}

// streamResponsesEvents consumes the Responses API SSE stream.
func streamResponsesEvents(body io.ReadCloser, capture *wireCapture, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	dec := streaming.NewDecoder(body)
	buffers := make(map[int]*responsesToolBuffer)
	var usage *schema.TokenUsage
	var finishReason string
	var incompleteReason string
	var hasContent bool

	for dec.Next() {
		data := strings.TrimSpace(dec.Event().Data)
		if data == "" {
			continue
		}
		// The Responses API has no [DONE] sentinel, but a proxy in front
		// of it may append one; skip it rather than failing the decode
		// (which would end the turn with a spurious stream error).
		if data == "[DONE]" {
			continue
		}
		var ev responsesStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode stream event: %w", err)}
			return
		}
		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				hasContent = true
				events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: ev.Delta}
			}
		case "response.refusal.delta":
			// A refusal is user-visible text: the non-streaming reader
			// concatenates every content part, so it already surfaces
			// refusals. Map the stream delta to answer text as well
			// rather than dropping it and ending on "empty content".
			if ev.Delta != "" {
				hasContent = true
				events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: ev.Delta}
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if ev.Delta != "" {
				hasContent = true
				events <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: ev.Delta}
			}
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				buffers[ev.OutputIndex] = &responsesToolBuffer{callID: ev.Item.CallID, name: ev.Item.Name}
				hasContent = true
			}
		case "response.function_call_arguments.delta":
			if buf := buffers[ev.OutputIndex]; buf != nil {
				buf.arguments.WriteString(ev.Delta)
			}
		case "response.output_item.done":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				buf := buffers[ev.OutputIndex]
				if buf == nil {
					buf = &responsesToolBuffer{}
					buffers[ev.OutputIndex] = buf
				}
				// The done item is authoritative: overwrite partial state.
				if ev.Item.CallID != "" {
					buf.callID = ev.Item.CallID
				}
				if ev.Item.Name != "" {
					buf.name = ev.Item.Name
				}
				if ev.Item.Arguments != "" {
					buf.arguments.Reset()
					buf.arguments.WriteString(ev.Item.Arguments)
				}
			}
		case "response.completed", "response.incomplete":
			if ev.Response != nil {
				usage = responsesUsageFrom(ev.Response.Usage)
				finishReason = responsesFinishReason(ev.Response)
				incompleteReason = ""
				if ev.Type == "response.incomplete" && ev.Response.IncompleteDetails != nil {
					incompleteReason = ev.Response.IncompleteDetails.Reason
				}
			}
		case "response.failed", "error":
			msg := ev.Message
			if msg == "" && ev.Error != nil {
				msg = ev.Error.Message
			}
			if msg == "" && ev.Response != nil && ev.Response.Error != nil {
				msg = ev.Response.Error.Message
			}
			if msg == "" {
				msg = "stream error event: " + ev.Type
			}
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(msg)}
			return
		case "response.created", "response.queued", "response.in_progress",
			"response.content_part.added", "response.content_part.done",
			"response.output_text.done",
			"response.output_text.annotation.added", "response.output_text.annotation.done",
			"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
			"response.reasoning_summary_text.done", "response.reasoning_text.done",
			"response.function_call_arguments.done":
			// Known no-op events: nothing to do, nothing to flag. Keeping
			// them out of the capture's [unrecognized-chunk] markers
			// preserves the signal for genuinely unknown events.
		default:
			// Anything a gateway adds later: never fatal, flagged in the
			// wire capture.
			capture.annotate("[unrecognized-chunk]", data)
		}
	}
	if err := dec.Err(); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("read stream: %w", err)}
		return
	}

	indexes := make([]int, 0, len(buffers))
	for index := range buffers {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	toolCalls := make([]schema.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		buf := buffers[index]
		toolCalls = append(toolCalls, schema.ToolCall{
			ID:   buf.callID,
			Name: buf.name,
			Args: json.RawMessage(buf.arguments.String()),
		})
	}
	toolCalls, _ = repairToolCalls(toolCalls)

	if !hasContent && len(toolCalls) == 0 {
		msg := "responses endpoint returned empty content (stream)"
		if incompleteReason != "" {
			msg += ": " + incompleteReason
		}
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(msg)}
		return
	}
	// A gateway whose completed event omits the output array would leave
	// finishReason empty despite tool calls; derive it rather than emit
	// an empty finish.
	if finishReason == "" && len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	events <- schema.ChatEvent{
		Type:         schema.ChatEventDone,
		FinishReason: finishReason,
		Usage:        usage,
		ToolCalls:    toolCalls,
	}
}
