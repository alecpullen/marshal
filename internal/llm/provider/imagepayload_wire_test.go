package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"marshal/internal/imagepayload"
	"marshal/internal/llm/schema"
)

// onePixelPNG is a 1×1 PNG: the smallest fixture that is a real image.
var onePixelPNG = mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

func mustDecodeBase64(s string) []byte {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic("provider test: bad base64 fixture: " + err.Error())
	}
	return data
}

// fakeSource is an in-memory ImageSource over a hash → bytes map.
type fakeSource map[string][]byte

func (f fakeSource) Image(_ context.Context, hash string) ([]byte, string, error) {
	if b, ok := f[hash]; ok {
		return b, "image/png", nil
	}
	return nil, "", imagepayload.ErrNotFound
}

// testImageRef is the ref every encoder test resolves through fakeSource.
var testImageRef = schema.ImageRef{
	Hash:   "sha256:0f1e2d",
	MIME:   "image/png",
	Width:  1,
	Height: 1,
	Bytes:  len(onePixelPNG),
	Label:  "screenshot of /tmp/ui.png",
}

func newImageSource() fakeSource { return fakeSource{testImageRef.Hash: onePixelPNG} }

// decodedAnthropicBlock reads one converted block generically: a tool_result
// that carries images holds a nested block array in Content.
type decodedAnthropicBlock struct {
	Type         string                  `json:"type"`
	Text         string                  `json:"text"`
	ToolUseID    string                  `json:"tool_use_id"`
	CacheControl *anthropicCacheControl  `json:"cache_control"`
	Source       *decodedAnthropicSource `json:"source"`
	Content      decodedAnthropicBlocks  `json:"content"`
}

// decodedAnthropicBlocks decodes a block's content field, which is a block
// array on a tool_result that carries images and a plain string on any other
// tool_result. The string form decodes to no blocks.
type decodedAnthropicBlocks []decodedAnthropicBlock

func (b *decodedAnthropicBlocks) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		*b = nil
		return nil
	}
	var blocks []decodedAnthropicBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	*b = blocks
	return nil
}

type decodedAnthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type decodedAnthropicMessage struct {
	Role    string                  `json:"role"`
	Content []decodedAnthropicBlock `json:"content"`
}

func decodedAnthropicMessages(t *testing.T, req schema.ChatRequest) []decodedAnthropicMessage {
	t.Helper()
	raw, err := buildAnthropicRequestBody(t.Context(), req, 0, 0)
	if err != nil {
		t.Fatalf("buildAnthropicRequestBody: %v", err)
	}
	var body struct {
		Messages []decodedAnthropicMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body.Messages
}

func toolImageChatRequest(src schema.ImageSource) schema.ChatRequest {
	return schema.ChatRequest{
		Model:       "test-model",
		ImageSource: src,
		Messages: []schema.ChatMessage{
			{Role: schema.RoleUser, Content: "look at this"},
			{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
				ID: "call_1", Name: "screen.read", Args: json.RawMessage(`{"path":"/tmp/ui.png"}`),
			}}},
			{Role: schema.RoleTool, ToolCallID: "call_1", Content: "captured", Images: []schema.ImageRef{testImageRef}},
		},
	}
}

func userImageChatRequest(src schema.ImageSource) schema.ChatRequest {
	return schema.ChatRequest{
		Model:       "test-model",
		ImageSource: src,
		Messages: []schema.ChatMessage{
			{Role: schema.RoleUser, Content: "what is this?", Images: []schema.ImageRef{testImageRef}},
		},
	}
}

// A tool message's images live inside its tool_result, whose content becomes
// a block array: the tool text first, the image blocks after.
func TestAnthropicToolResultCarriesImages(t *testing.T) {
	msgs := decodedAnthropicMessages(t, toolImageChatRequest(newImageSource()))

	last := msgs[len(msgs)-1]
	if last.Role != "user" || len(last.Content) != 1 {
		t.Fatalf("last message = %+v, want one user message holding the tool result", last)
	}
	result := last.Content[0]
	if result.Type != "tool_result" || result.ToolUseID != "call_1" {
		t.Fatalf("block = %+v, want a tool_result for call_1", result)
	}
	if len(result.Content) != 2 {
		t.Fatalf("tool_result content = %+v, want a text block and an image block", result.Content)
	}
	if result.Content[0].Type != "text" || result.Content[0].Text != "captured" {
		t.Fatalf("first content block = %+v, want the tool text", result.Content[0])
	}
	image := result.Content[1]
	if image.Type != "image" || image.Source == nil || image.Source.Type != "base64" {
		t.Fatalf("second content block = %+v, want a base64 image block", image)
	}
	if image.Source.MediaType != testImageRef.MIME {
		t.Errorf("media_type = %q, want %q", image.Source.MediaType, testImageRef.MIME)
	}
	if want := base64.StdEncoding.EncodeToString(onePixelPNG); image.Source.Data != want {
		t.Errorf("image data = %q, want the stored bytes base64-encoded", image.Source.Data)
	}
}

// A user message's images follow its text block, in one message.
func TestAnthropicUserMessageCarriesImages(t *testing.T) {
	msgs := decodedAnthropicMessages(t, userImageChatRequest(newImageSource()))

	if len(msgs) != 1 || len(msgs[0].Content) != 2 {
		t.Fatalf("messages = %+v, want one message with a text block and an image block", msgs)
	}
	if msgs[0].Content[0].Type != "text" || msgs[0].Content[0].Text != "what is this?" {
		t.Fatalf("first block = %+v, want the message text", msgs[0].Content[0])
	}
	image := msgs[0].Content[1]
	if image.Type != "image" || image.Source == nil || image.Source.Type != "base64" {
		t.Fatalf("second block = %+v, want a base64 image block", image)
	}
}

// An image-only user message sends its image blocks alone. The Messages API
// rejects an empty text block, and the 400 that comes back talks about
// "text" rather than images, so the send-time retry never recognises it.
func TestAnthropicImageOnlyUserMessageHasNoTextBlock(t *testing.T) {
	req := schema.ChatRequest{
		Model:       "test-model",
		ImageSource: newImageSource(),
		Messages:    []schema.ChatMessage{{Role: schema.RoleUser, Images: []schema.ImageRef{testImageRef}}},
	}
	msgs := decodedAnthropicMessages(t, req)

	if len(msgs) != 1 || len(msgs[0].Content) != 1 {
		t.Fatalf("messages = %+v, want one user message holding only the image", msgs)
	}
	if block := msgs[0].Content[0]; block.Type != "image" {
		t.Fatalf("block = %+v, want the image block alone", block)
	}
}

// countDecodedBreakpoints counts cache_control marks on converted messages,
// including one on a tool_result that carries images.
func countDecodedBreakpoints(msgs []decodedAnthropicMessage) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				n++
			}
		}
	}
	return n
}

// The rolling breakpoint lands on the last stable block, which for a tool
// turn is the tool_result. An image-bearing tool result is a block array
// rather than a flat text block, so it must still take the breakpoint: a
// dropped one would quietly send an uncached transcript every image read.
func TestAnthropicCacheBreakpointOnImageBearingToolResult(t *testing.T) {
	t.Run("images", func(t *testing.T) {
		msgs := decodedAnthropicMessages(t, toolImageChatRequest(newImageSource()))
		if n := countDecodedBreakpoints(msgs); n != 1 {
			t.Fatalf("breakpoints = %d, want exactly one", n)
		}
		last := msgs[len(msgs)-1].Content[0]
		if last.Type != "tool_result" || last.CacheControl == nil {
			t.Fatalf("last block = %+v, want the tool_result carrying the breakpoint", last)
		}
	})

	t.Run("text only", func(t *testing.T) {
		req := toolImageChatRequest(newImageSource())
		req.Messages[len(req.Messages)-1].Images = nil
		msgs := decodedAnthropicMessages(t, req)
		if n := countDecodedBreakpoints(msgs); n != 1 {
			t.Fatalf("breakpoints = %d, want exactly one", n)
		}
		last := msgs[len(msgs)-1].Content[0]
		if last.Type != "tool_result" || last.CacheControl == nil {
			t.Fatalf("last block = %+v, want the text tool_result carrying the breakpoint", last)
		}
	})
}

// A message with no images serializes exactly as it did before images
// existed, empty text included. The Responses API requires the field on a
// text part, so omitempty on the part's Text would 400 every empty prompt.
func TestResponsesTextPartsKeepTheirTextField(t *testing.T) {
	req := schema.ChatRequest{Model: "test-model", Messages: []schema.ChatMessage{
		{Role: schema.RoleUser},
		{Role: schema.RoleUser, Content: "hi"},
		{Role: schema.RoleAssistant, Content: "hello"},
	}}
	items := responsesInputItems(t, req)

	want := []string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`,
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}`,
	}
	if len(items) != len(want) {
		t.Fatalf("input items = %d, want %d: %s", len(items), len(want), items)
	}
	for i, w := range want {
		if got := string(items[i]); got != w {
			t.Errorf("input[%d] = %s, want %s", i, got, w)
		}
	}
}

// An image-only prompt has no text to send: it goes as image parts alone,
// and the image part itself carries no text field.
func TestResponsesImageOnlyMessageDropsTheTextPart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "image only",
			want: `{"type":"message","role":"user","content":[{"type":"input_image","image_url":"` +
				imageDataURLFixture() + `"}]}`,
		},
		{
			name:    "text and image",
			content: "what is this?",
			want: `{"type":"message","role":"user","content":[{"type":"input_text","text":"what is this?"},` +
				`{"type":"input_image","image_url":"` + imageDataURLFixture() + `"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := schema.ChatRequest{
				Model:       "test-model",
				ImageSource: newImageSource(),
				Messages: []schema.ChatMessage{{
					Role: schema.RoleUser, Content: tc.content, Images: []schema.ImageRef{testImageRef},
				}},
			}
			items := responsesInputItems(t, req)
			if len(items) != 1 {
				t.Fatalf("input items = %d, want 1: %s", len(items), items)
			}
			if got := string(items[0]); got != tc.want {
				t.Errorf("input[0] = %s, want %s", got, tc.want)
			}
		})
	}
}

// imageDataURLFixture is onePixelPNG as the data URL the encoder emits.
func imageDataURLFixture() string {
	return "data:" + testImageRef.MIME + ";base64," + base64.StdEncoding.EncodeToString(onePixelPNG)
}

// responsesInputItems builds a Responses body and returns the input array
// as raw JSON, so a test can pin the exact wire shape of each item.
func responsesInputItems(t *testing.T, req schema.ChatRequest) []json.RawMessage {
	t.Helper()
	raw, err := buildResponsesRequestBody(t.Context(), req, false)
	if err != nil {
		t.Fatalf("buildResponsesRequestBody: %v", err)
	}
	var body struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body.Input
}

// OpenAI's role:"tool" content is text-only, so a tool message's images ride
// a synthetic user parts message immediately after it.
func TestOpenAIToolImagesRideASyntheticUserMessage(t *testing.T) {
	raw, err := buildChatRequestBody(t.Context(), toolImageChatRequest(newImageSource()), false, "")
	if err != nil {
		t.Fatalf("buildChatRequestBody: %v", err)
	}
	var body chatCompletionRequestBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (user, assistant, tool, synthetic user)", len(body.Messages))
	}
	tool := body.Messages[2]
	if tool.Role != string(schema.RoleTool) || tool.contentText() != "captured" {
		t.Fatalf("tool message = %+v, want plain text content %q", tool, "captured")
	}
	imageMessage := body.Messages[3]
	if imageMessage.Role != string(schema.RoleUser) {
		t.Fatalf("message after the tool result = %q, want role user", imageMessage.Role)
	}
	urls := imageURLsIn(t, imageMessage.Content)
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "data:image/png;base64,") {
		t.Fatalf("image urls = %v, want one png data URL", urls)
	}
}

// A user message with images is one parts array instead of a string.
func TestOpenAIUserMessageBecomesParts(t *testing.T) {
	raw, err := buildChatRequestBody(t.Context(), userImageChatRequest(newImageSource()), false, "")
	if err != nil {
		t.Fatalf("buildChatRequestBody: %v", err)
	}
	var body chatCompletionRequestBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(body.Messages))
	}
	var parts []openAIContentPart
	if err := json.Unmarshal(body.Messages[0].Content, &parts); err != nil {
		t.Fatalf("content is not a parts array: %v (%s)", err, body.Messages[0].Content)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "what is this?" {
		t.Fatalf("parts = %+v, want the text part first", parts)
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil ||
		!strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("second part = %+v, want an image_url data URL", parts[1])
	}
}

// imageURLsIn collects the image_url parts of one encoded content value.
func imageURLsIn(t *testing.T, content json.RawMessage) []string {
	t.Helper()
	var parts []openAIContentPart
	if err := json.Unmarshal(content, &parts); err != nil {
		t.Fatalf("content is not a parts array: %v (%s)", err, content)
	}
	var urls []string
	for _, part := range parts {
		if part.Type == "image_url" && part.ImageURL != nil {
			urls = append(urls, part.ImageURL.URL)
		}
	}
	return urls
}

// The native API takes images on the message itself, on any role.
func TestOllamaImagesOnTheMessage(t *testing.T) {
	raw, err := (&OllamaNative{}).buildChatRequestBody(t.Context(), userImageChatRequest(newImageSource()))
	if err != nil {
		t.Fatalf("buildChatRequestBody: %v", err)
	}
	var body ollamaChatRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Messages) != 1 || len(body.Messages[0].Images) != 1 {
		t.Fatalf("messages = %+v, want one message carrying one image", body.Messages)
	}
	if want := base64.StdEncoding.EncodeToString(onePixelPNG); body.Messages[0].Images[0] != want {
		t.Errorf("image = %q, want the stored bytes base64-encoded", body.Messages[0].Images[0])
	}
}

// An encoder meeting images with no ImageSource fails rather than sending
// text alone, which would ask the model about an image it never received.
func TestNilImageSourceFailsLoudly(t *testing.T) {
	for _, builder := range []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"anthropic", func(t *testing.T) error {
			_, err := buildAnthropicRequestBody(t.Context(), userImageChatRequest(nil), 0, 0)
			return err
		}},
		{"openai-compatible", func(t *testing.T) error {
			_, err := buildChatRequestBody(t.Context(), userImageChatRequest(nil), false, "")
			return err
		}},
		{"openai-responses", func(t *testing.T) error {
			_, err := buildResponsesRequestBody(t.Context(), userImageChatRequest(nil), false)
			return err
		}},
		{"ollama-native", func(t *testing.T) error {
			_, err := (&OllamaNative{}).buildChatRequestBody(t.Context(), userImageChatRequest(nil))
			return err
		}},
	} {
		t.Run(builder.name, func(t *testing.T) {
			err := builder.run(t)
			if !errors.Is(err, schema.ErrVisionUnsupported) {
				t.Fatalf("error = %v, want schema.ErrVisionUnsupported", err)
			}
		})
	}
}

// A store error surfaces with errors.Is; it is never swallowed into a
// text-only request.
func TestImageSourceErrorPropagates(t *testing.T) {
	missing := fakeSource{} // resolves nothing
	for _, builder := range []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"anthropic", func(t *testing.T) error {
			_, err := buildAnthropicRequestBody(t.Context(), toolImageChatRequest(missing), 0, 0)
			return err
		}},
		{"openai-compatible", func(t *testing.T) error {
			_, err := buildChatRequestBody(t.Context(), userImageChatRequest(missing), false, "")
			return err
		}},
		{"openai-responses", func(t *testing.T) error {
			_, err := buildResponsesRequestBody(t.Context(), userImageChatRequest(missing), false)
			return err
		}},
		{"ollama-native", func(t *testing.T) error {
			_, err := (&OllamaNative{}).buildChatRequestBody(t.Context(), userImageChatRequest(missing))
			return err
		}},
	} {
		t.Run(builder.name, func(t *testing.T) {
			err := builder.run(t)
			if !errors.Is(err, imagepayload.ErrNotFound) {
				t.Fatalf("error = %v, want imagepayload.ErrNotFound", err)
			}
		})
	}
}
