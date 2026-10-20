//go:build integration

package provider

// Opt-in live tests for reasoning replay. These are excluded from the default
// suite by the integration build tag and skip unless their environment switch
// is set; running them is a manual step.
//
// Ollama Cloud codeword probe:
//   MARSHAL_TEST_OLLAMA_CLOUD_KEY=... MARSHAL_TEST_OLLAMA_CLOUD_MODEL=... \
//     CGO_ENABLED=1 go test -tags=integration ./internal/llm/provider/ -run TestLiveOllamaCloudReplayReachesModel -v
//
// Codex item replay (reads the real keychain):
//   MARSHAL_TEST_CODEX=1 \
//     CGO_ENABLED=1 go test -tags=integration ./internal/llm/provider/ -run TestLiveCodexReasoningItemsAccepted -v

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
)

// ollamaCloudBaseURL is the OpenAI-compatible Ollama Cloud route (see the
// feature spec's live-probe table).
const ollamaCloudBaseURL = "https://ollama.com/v1"

// replayCodeword is planted only in the replayed assistant message's
// reasoning. It never appears in the user prompt, so the model can only
// echo it back if the reasoning field reached it.
const replayCodeword = "ZEBRA-7731"

// codewordProbePrompt asks the model to recall the codeword it "picked"
// earlier, or answer UNKNOWN.
const codewordProbePrompt = "Silently pick a secret codeword (do not say it yet), then call lookup with query 'weather'. After the tool returns, reply with ONLY the codeword you picked, or UNKNOWN if you cannot recall it."

// codewordProbeMessages is the probe history: the user instruction, the
// assistant's tool call carrying the codeword in its reasoning, and the tool
// result. The codeword appears only in Reasoning.Text.
func codewordProbeMessages() []schema.ChatMessage {
	return []schema.ChatMessage{
		{Role: schema.RoleUser, Content: codewordProbePrompt},
		{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_probe_1",
				Name: "lookup",
				Args: json.RawMessage(`{"query":"weather"}`),
			}},
			Reasoning: &schema.Reasoning{Text: "The secret codeword I picked is " + replayCodeword + "."},
		},
		{Role: schema.RoleTool, ToolCallID: "call_probe_1", Content: "sunny"},
	}
}

// liveStepResult is one drained chat call: the non-thinking text the model
// produced, the thinking text, and the terminal Done event.
type liveStepResult struct {
	answer   string
	thinking string
	done     schema.ChatEvent
}

// liveRunStep sends req, drains the event channel, and returns the result.
// A stream error or a missing Done event fails the test.
func liveRunStep(t *testing.T, p Provider, req schema.ChatRequest) liveStepResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	events, err := p.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var res liveStepResult
	var sawDone bool
	for ev := range events {
		switch ev.Type {
		case schema.ChatEventError:
			t.Fatalf("stream error: %v", ev.Err)
		case schema.ChatEventDelta:
			if ev.Kind == schema.DeltaThinking {
				res.thinking += ev.Delta
			} else {
				res.answer += ev.Delta
			}
		case schema.ChatEventDone:
			res.done = ev
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatal("stream closed without a Done event")
	}
	return res
}

// TestLiveOllamaCloudReplayReachesModel proves the OpenAI-compatible replay
// field reaches the model: the codeword planted in the replayed assistant
// reasoning is echoed back inside the same tool loop with replay on, and not
// at all with replay off.
func TestLiveOllamaCloudReplayReachesModel(t *testing.T) {
	key := os.Getenv("MARSHAL_TEST_OLLAMA_CLOUD_KEY")
	model := os.Getenv("MARSHAL_TEST_OLLAMA_CLOUD_MODEL")
	if key == "" || model == "" {
		t.Skip("set MARSHAL_TEST_OLLAMA_CLOUD_KEY and MARSHAL_TEST_OLLAMA_CLOUD_MODEL to run this live test")
	}

	run := func(reasoningField string, attempts int) int {
		t.Helper()
		p, err := NewOpenAICompatible(Options{
			Name:           "ollama-cloud",
			BaseURL:        ollamaCloudBaseURL,
			APIKey:         key,
			ReasoningField: reasoningField,
		})
		if err != nil {
			t.Fatalf("NewOpenAICompatible: %v", err)
		}

		hits := 0
		for i := 0; i < attempts; i++ {
			res := liveRunStep(t, p, schema.ChatRequest{
				Model:    model,
				Messages: codewordProbeMessages(),
				Stream:   true,
			})
			hit := strings.Contains(res.answer, replayCodeword)
			if hit {
				hits++
			}
			t.Logf("reasoning_field=%q attempt %d/%d: codeword=%v answer=%q", reasoningField, i+1, attempts, hit, res.answer)
		}
		return hits
	}

	on := run(ReasoningFieldAuto, 3)
	if on < 2 {
		t.Errorf("replay on: codeword reached the model in %d/3 attempts, want at least 2", on)
	}

	off := run(ReasoningFieldOff, 3)
	if off != 0 {
		t.Errorf("replay off: codeword leaked into %d/3 answers, want 0", off)
	}
}

// lookupToolDefinition is the tool the codex tool loop calls.
func lookupToolDefinition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        "lookup",
		Description: "Look up a value by query.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	}
}

// TestLiveCodexReasoningItemsAccepted runs a two-step tool loop against the
// configured Codex backend, replaying the reasoning items step 1 returned.
// It deliberately reads the real keychain through DefaultAuthResolver, which
// is why it sits behind both the build tag and MARSHAL_TEST_CODEX=1.
func TestLiveCodexReasoningItemsAccepted(t *testing.T) {
	if os.Getenv("MARSHAL_TEST_CODEX") != "1" {
		t.Skip("set MARSHAL_TEST_CODEX=1 to run this live test")
	}

	pc := config.ProviderConfig{
		Type:        "openai_codex",
		BaseURL:     "https://chatgpt.com/backend-api",
		Auth:        "oauth",
		ToolCalling: true,
		Template:    "openai-codex",
	}
	p, err := NewFromConfigWithAuth("codex", pc, "", false, "reasoning-replay-live", DefaultAuthResolver())
	if err != nil {
		t.Fatalf("NewFromConfigWithAuth: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	models, err := p.Models(ctx)
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("codex provider returned no models")
	}
	model := models[0].ID

	logUsage := func(label string, done schema.ChatEvent) {
		if done.Usage == nil {
			t.Logf("%s: no usage reported", label)
			return
		}
		t.Logf("%s: reasoning_tokens=%d cache_read_tokens=%d prompt_tokens=%d completion_tokens=%d",
			label, done.Usage.ReasoningTokens, done.Usage.CacheReadTokens,
			done.Usage.PromptTokens, done.Usage.CompletionTokens)
	}

	tools := []schema.ToolDefinition{lookupToolDefinition()}
	prompt := "Call the lookup tool with query 'weather', then reply with its result."

	step1 := liveRunStep(t, p, schema.ChatRequest{
		Model:    model,
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: prompt}},
		Tools:    tools,
		Stream:   true,
	})
	logUsage("step 1", step1.done)

	if len(step1.done.ToolCalls) == 0 {
		t.Fatalf("step 1 returned no tool call; answer=%q", step1.answer)
	}
	call := step1.done.ToolCalls[0]

	// Stamp the captured items exactly as chatOnceAttempt does, so replay
	// can match them to the provider and model that produced them.
	items := make([]schema.ReasoningItem, 0, len(step1.done.ReasoningItems))
	for _, it := range step1.done.ReasoningItems {
		it.Provider = p.Name()
		it.Model = model
		items = append(items, it)
	}
	if len(items) == 0 {
		t.Fatal("step 1 returned no reasoning items; nothing to replay")
	}
	t.Logf("replaying %d reasoning item(s) in step 2", len(items))

	step2 := liveRunStep(t, p, schema.ChatRequest{
		Model: model,
		Messages: []schema.ChatMessage{
			{Role: schema.RoleUser, Content: prompt},
			{
				Role:      schema.RoleAssistant,
				Content:   step1.answer,
				ToolCalls: step1.done.ToolCalls,
				Reasoning: &schema.Reasoning{Text: step1.thinking, Items: items},
			},
			{Role: schema.RoleTool, ToolCallID: call.ID, Content: `{"weather":"sunny"}`},
		},
		Tools:  tools,
		Stream: true,
	})
	logUsage("step 2", step2.done)

	if strings.TrimSpace(step2.answer) == "" && len(step2.done.ToolCalls) == 0 {
		t.Fatal("step 2 was accepted but produced no answer and no tool call")
	}
}
