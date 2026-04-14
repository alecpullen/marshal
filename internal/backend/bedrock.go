package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/alecpullen/marshal/internal/tools"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// BedrockBackend implements the Backend interface for AWS Bedrock.
// It provides access to models hosted on AWS Bedrock (Claude, Llama, etc.)
type BedrockBackend struct {
	name        string
	region      string
	temperature float64
	maxTokens   int
	client      *bedrockruntime.Client
}

// NewBedrockBackend creates a new AWS Bedrock backend.
// Credentials are loaded from the environment (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION).
func NewBedrockBackend(name, region string) *BedrockBackend {
	return &BedrockBackend{
		name:   name,
		region: region,
	}
}

// WithTemperature sets the temperature for generation.
func (b *BedrockBackend) WithTemperature(t float64) *BedrockBackend {
	b.temperature = t
	return b
}

// WithMaxTokens sets the maximum tokens to generate.
func (b *BedrockBackend) WithMaxTokens(n int) *BedrockBackend {
	b.maxTokens = n
	return b
}

// Name returns the backend identifier.
func (b *BedrockBackend) Name() string { return b.name }

// initClient initializes the Bedrock runtime client if not already done.
func (b *BedrockBackend) initClient(ctx context.Context) error {
	if b.client != nil {
		return nil
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(b.regionOrDefault()),
	)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}

	b.client = bedrockruntime.NewFromConfig(cfg)
	return nil
}

// Complete sends messages to a model via Bedrock and returns the response.
func (b *BedrockBackend) Complete(
	ctx context.Context,
	model string,
	messages []Message,
) (Response, error) {
	if err := b.initClient(ctx); err != nil {
		return Response{}, err
	}

	// Bedrock uses different request formats depending on the model provider
	// For Anthropic Claude on Bedrock, we use the same format as native Claude
	// For other models, we may need different formats

	// Determine model provider from the model ID
	provider := b.inferProvider(model)

	switch provider {
	case "anthropic":
		return b.completeWithAnthropicFormat(ctx, model, messages)
	case "amazon":
		return b.completeWithAmazonFormat(ctx, model, messages)
	default:
		return b.completeWithAnthropicFormat(ctx, model, messages) // Default to Claude format
	}
}

func (b *BedrockBackend) inferProvider(model string) string {
	// Model IDs on Bedrock follow patterns like:
	// - anthropic.claude-3-5-sonnet-20241022-v2:0
	// - amazon.titan-text-express-v1
	// - meta.llama3-70b-instruct-v1:0
	// - mistral.mistral-large-2402-v1:0

	if len(model) > 9 && model[:9] == "anthropic" {
		return "anthropic"
	}
	if len(model) > 6 && model[:6] == "amazon" {
		return "amazon"
	}
	return "unknown"
}

// completeWithAnthropicFormat uses the Anthropic Claude format for Bedrock.
func (b *BedrockBackend) completeWithAnthropicFormat(
	ctx context.Context,
	model string,
	messages []Message,
) (Response, error) {
	var claudeMsgs []map[string]interface{}
	var systemPrompt string

	for _, m := range messages {
		if m.Role == "system" {
			systemPrompt = m.Content
			continue
		}

		role := m.Role
		if role == "tool" {
			role = "user"
		}

		claudeMsgs = append(claudeMsgs, map[string]interface{}{
			"role":    role,
			"content": m.Content,
		})
	}

	body := map[string]interface{}{
		"model":      model,
		"max_tokens": b.maxTokensOrDefault(),
		"messages":   claudeMsgs,
	}

	if b.temperature > 0 {
		body["temperature"] = b.temperature
	}
	if systemPrompt != "" {
		body["system"] = systemPrompt
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := b.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(model),
		Body:        bodyBytes,
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return Response{}, fmt.Errorf("bedrock invoke: %w", err)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}

	var content string
	for _, block := range result.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	return Response{
		Content:          content,
		PromptTokens:     result.Usage.InputTokens,
		CompletionTokens: result.Usage.OutputTokens,
	}, nil
}

// completeWithAmazonFormat uses the Amazon Titan format for Bedrock.
func (b *BedrockBackend) completeWithAmazonFormat(
	ctx context.Context,
	model string,
	messages []Message,
) (Response, error) {
	// Amazon Titan format is different
	// Combine messages into a single prompt
	var prompt string
	for _, m := range messages {
		if m.Role == "system" {
			prompt += fmt.Sprintf("System: %s\n\n", m.Content)
		} else if m.Role == "assistant" {
			prompt += fmt.Sprintf("Assistant: %s\n\n", m.Content)
		} else {
			prompt += fmt.Sprintf("Human: %s\n\n", m.Content)
		}
	}
	prompt += "Assistant: "

	body := map[string]interface{}{
		"inputText": prompt,
		"textGenerationConfig": map[string]interface{}{
			"temperature":   b.temperature,
			"maxTokenCount": b.maxTokensOrDefault(),
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := b.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(model),
		Body:        bodyBytes,
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return Response{}, fmt.Errorf("bedrock invoke: %w", err)
	}

	var result struct {
		Results []struct {
			OutputText      string `json:"outputText"`
			TokenCount      int    `json:"tokenCount"`
			InputTokenCount int    `json:"inputTokenCount,omitempty"`
		} `json:"results"`
	}

	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Results) == 0 {
		return Response{}, fmt.Errorf("no results in response")
	}

	return Response{
		Content:          result.Results[0].OutputText,
		PromptTokens:     result.Results[0].InputTokenCount,
		CompletionTokens: result.Results[0].TokenCount,
	}, nil
}

// CompleteWithTools sends messages with tool definitions via Bedrock.
func (b *BedrockBackend) CompleteWithTools(
	ctx context.Context,
	model string,
	messages []Message,
	toolDefs []tools.Definition,
) (tools.Response, error) {
	if err := b.initClient(ctx); err != nil {
		return tools.Response{}, err
	}

	provider := b.inferProvider(model)

	switch provider {
	case "anthropic":
		return b.completeWithToolsAnthropicFormat(ctx, model, messages, toolDefs)
	default:
		return tools.Response{}, fmt.Errorf("tool use not supported for %s models on Bedrock", provider)
	}
}

func (b *BedrockBackend) completeWithToolsAnthropicFormat(
	ctx context.Context,
	model string,
	messages []Message,
	toolDefs []tools.Definition,
) (tools.Response, error) {
	// Build tool definitions
	claudeTools := make([]map[string]interface{}, 0, len(toolDefs))
	for _, d := range toolDefs {
		claudeTools = append(claudeTools, map[string]interface{}{
			"name":         d.Name,
			"description":  d.Description,
			"input_schema": d.Parameters,
		})
	}

	// Convert messages
	var claudeMsgs []map[string]interface{}
	var systemPrompt string

	for _, m := range messages {
		if m.Role == "system" {
			systemPrompt = m.Content
			continue
		}

		role := m.Role
		if role == "tool" {
			role = "user"
		}

		claudeMsgs = append(claudeMsgs, map[string]interface{}{
			"role":    role,
			"content": m.Content,
		})
	}

	body := map[string]interface{}{
		"model":      model,
		"max_tokens": b.maxTokensOrDefault(),
		"messages":   claudeMsgs,
		"tools":      claudeTools,
	}

	if b.temperature > 0 {
		body["temperature"] = b.temperature
	}
	if systemPrompt != "" {
		body["system"] = systemPrompt
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return tools.Response{}, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := b.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(model),
		Body:        bodyBytes,
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return tools.Response{}, fmt.Errorf("bedrock invoke: %w", err)
	}

	respBody, _ := io.ReadAll(bytes.NewReader(resp.Body))

	var result struct {
		Content []struct {
			Type  string                 `json:"type"`
			Text  string                 `json:"text,omitempty"`
			ID    string                 `json:"id,omitempty"`
			Name  string                 `json:"name,omitempty"`
			Input map[string]interface{} `json:"input,omitempty"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return tools.Response{}, fmt.Errorf("decode response: %w", err)
	}

	var content string
	var calls []tools.Call
	stopReason := result.StopReason

	for _, block := range result.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "tool_use":
			calls = append(calls, tools.Call{
				ID:        block.ID,
				ToolName:  block.Name,
				Arguments: block.Input,
			})
			if stopReason == "" {
				stopReason = "tool_calls"
			}
		}
	}

	return tools.Response{
		Content:    content,
		ToolCalls:  calls,
		StopReason: stopReason,
	}, nil
}

func (b *BedrockBackend) regionOrDefault() string {
	if b.region == "" {
		return "us-east-1"
	}
	return b.region
}

func (b *BedrockBackend) maxTokensOrDefault() int {
	if b.maxTokens == 0 {
		return 4096
	}
	return b.maxTokens
}
