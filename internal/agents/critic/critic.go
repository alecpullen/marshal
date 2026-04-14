// Package critic provides the code-review agent.
// The critic receives diffs from the Marshal and returns structured verdicts.
package critic

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/prompts"
)

// Critic reviews code diffs and returns structured verdicts.
// It is commanded by the Marshal to evaluate Executor output.
type Critic struct {
	backend backend.Backend
	cfg     config.AgentConfig
}

// Verdict represents the structured critic response.
type Verdict struct {
	Verdict  string   `json:"verdict"` // "PASS" or "FAIL"
	Summary  string   `json:"summary"`
	Issue    string   `json:"issue"`
	Fix      string   `json:"fix"`
	Concerns []string `json:"concerns"`
}

// IsPass returns true if the verdict is a PASS.
func (v *Verdict) IsPass() bool {
	return strings.ToUpper(v.Verdict) == "PASS"
}

// Result contains the verdict and token usage.
type Result struct {
	Verdict          *Verdict
	PromptTokens     int
	CompletionTokens int
	RawResponse      string // Full raw response for think-block extraction
}

// New creates a new critic agent.
func New(be backend.Backend, cfg config.AgentConfig) *Critic {
	return &Critic{
		backend: be,
		cfg:     cfg,
	}
}

// Review sends the diff to the LLM and parses the verdict.
// This is the primary method the Marshal uses to command the Critic.
func (c *Critic) Review(ctx context.Context, diff string, task string) (*Result, error) {
	messages := c.buildMessages(diff, task)

	resp, err := c.backend.Complete(ctx, c.cfg.Model, messages)
	if err != nil {
		return nil, fmt.Errorf("backend complete: %w", err)
	}

	// Parse the verdict from the response
	verdict, err := ParseVerdict(resp.Content)
	if err != nil {
		// Return a default FAIL verdict on parse error
		return &Result{
			Verdict: &Verdict{
				Verdict:  "FAIL",
				Summary:  "Failed to parse critic response",
				Issue:    fmt.Sprintf("Parse error: %v", err),
				Fix:      "Ensure the critic outputs valid JSON",
				Concerns: []string{"Original response: " + truncate(resp.Content, 200)},
			},
			PromptTokens:     resp.PromptTokens,
			CompletionTokens: resp.CompletionTokens,
		}, nil
	}

	return &Result{
		Verdict:          verdict,
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
		RawResponse:      resp.Content,
	}, nil
}

// ReviewStreaming sends the diff to the LLM with streaming callback support.
// If the backend supports streaming, it streams chunks via onChunk.
// Otherwise it falls back to non-streaming Review.
func (c *Critic) ReviewStreaming(ctx context.Context, diff string, task string, onChunk func(string) error) (*Result, error) {
	messages := c.buildMessages(diff, task)

	if sb := backend.AsStreaming(c.backend); sb != nil {
		var full strings.Builder
		err := sb.CompleteStreaming(ctx, c.cfg.Model, messages, func(chunk backend.StreamResponse) {
			full.WriteString(chunk.Content)
			if onChunk != nil {
				_ = onChunk(chunk.Content)
			}
		})
		if err != nil {
			return nil, fmt.Errorf("streaming complete: %w", err)
		}
		content := full.String()
		verdict, parseErr := ParseVerdict(content)
		if parseErr != nil {
			verdict = &Verdict{
				Verdict:  "FAIL",
				Summary:  "Failed to parse critic response",
				Issue:    fmt.Sprintf("Parse error: %v", parseErr),
				Fix:      "Ensure the critic outputs valid JSON",
				Concerns: []string{"Original response: " + truncate(content, 200)},
			}
		}
		return &Result{
			Verdict:          verdict,
			PromptTokens:     0,
			CompletionTokens: 0,
			RawResponse:      content,
		}, nil
	}

	// Fallback to non-streaming.
	// Call onChunk with the full response so callers can extract think blocks and update the UI.
	result, err := c.Review(ctx, diff, task)
	if err != nil {
		return nil, err
	}
	if onChunk != nil && result.RawResponse != "" {
		_ = onChunk(result.RawResponse)
	}
	return result, nil
}

// buildMessages constructs the messages for the critic review.
func (c *Critic) buildMessages(diff string, task string) []backend.Message {
	systemPrompt := c.buildSystemPrompt()

	userContent := fmt.Sprintf(`Task: %s

Git diff to review:
%s

Provide your verdict as JSON matching the schema.`, task, diff)

	return []backend.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userContent},
	}
}

// buildSystemPrompt constructs the full system prompt for the critic.
func (c *Critic) buildSystemPrompt() string {
	prompt := prompts.SecurityInstructions + "\n\n" + prompts.CriticBaseInstructions + "\n\n"

	prompt += `Respond ONLY with valid JSON matching this exact schema:
{
  "verdict": "PASS" or "FAIL",
  "summary": "Brief summary of the changes",
  "issue": "If FAIL, describe the specific issue",
  "fix": "If FAIL, describe what needs to be fixed",
  "concerns": ["Any non-blocking concerns or warnings"]
}

The verdict field must be exactly "PASS" or "FAIL" (uppercase).
Do not include any text outside the JSON object.`

	return prompt
}

// IntegrationResult contains the outcome of a cross-task coherence review.
type IntegrationResult struct {
	Verdict         string   // "PASS" or "FAIL"
	Summary         string   `json:"summary"`
	CrossTaskIssues []string `json:"cross_task_issues"`
}

// ReviewIntegration reviews combined changes from multiple tasks for cross-task coherence.
// It checks for naming conflicts, API contract violations, and other inter-task issues.
func (c *Critic) ReviewIntegration(ctx context.Context, feature string, taskDescriptions []string, combinedDiff string) (*IntegrationResult, error) {
	systemPrompt := `You are an integration critic reviewing the combined output of a multi-task pipeline.

Check for cross-task coherence issues:
- Naming conflicts (two tasks define the same identifier differently)
- API contract violations (task A defines an interface task B implements incorrectly)
- Duplicate functionality introduced by independent tasks
- Inconsistent error handling across tasks
- Import cycles introduced by changes in different tasks

Respond ONLY with valid JSON matching this schema:
{
  "verdict": "PASS" or "FAIL",
  "summary": "brief overview of the combined changes",
  "cross_task_issues": ["description of issue", ...]
}

If no cross-task issues are found, return "PASS" with an empty cross_task_issues array.
Do not include any text outside the JSON object.`

	taskList := strings.Join(taskDescriptions, "\n")
	userContent := fmt.Sprintf(`Feature: %s

Tasks completed:
%s

Combined diff across all tasks:
%s

Review for cross-task coherence issues.`, feature, taskList, combinedDiff)

	messages := []backend.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userContent},
	}

	resp, err := c.backend.Complete(ctx, c.cfg.Model, messages)
	if err != nil {
		return nil, fmt.Errorf("integration critic backend: %w", err)
	}

	// Parse the JSON response
	content := extractJSONFromContent(resp.Content)
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || end <= start {
		return &IntegrationResult{Verdict: "PASS", Summary: "Could not parse integration review; assuming PASS"}, nil
	}

	var result struct {
		Verdict         string   `json:"verdict"`
		Summary         string   `json:"summary"`
		CrossTaskIssues []string `json:"cross_task_issues"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &result); err != nil {
		return &IntegrationResult{Verdict: "PASS", Summary: "Could not parse integration review; assuming PASS"}, nil
	}

	return &IntegrationResult{
		Verdict:         strings.ToUpper(result.Verdict),
		Summary:         result.Summary,
		CrossTaskIssues: result.CrossTaskIssues,
	}, nil
}

// ParseVerdict extracts a JSON verdict from content.
// Handles both pure JSON and JSON embedded in <thinking> tags.
func ParseVerdict(content string) (*Verdict, error) {
	// First, try to extract from <thinking> tags if present
	content = extractJSONFromContent(content)

	// Find JSON object boundaries
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")

	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON object found in response")
	}

	jsonStr := content[start : end+1]

	var verdict Verdict
	if err := json.Unmarshal([]byte(jsonStr), &verdict); err != nil {
		return nil, fmt.Errorf("failed to unmarshal verdict: %w", err)
	}

	// Normalize verdict
	verdict.Verdict = strings.ToUpper(verdict.Verdict)
	if verdict.Verdict != "PASS" && verdict.Verdict != "FAIL" {
		return nil, fmt.Errorf("invalid verdict value: %s", verdict.Verdict)
	}

	return &verdict, nil
}

// extractJSONFromContent removes think blocks and returns the remaining content.
// Handles both <think>...</think> (DeepSeek/Qwen) and <thinking>...</thinking> (Claude).
func extractJSONFromContent(content string) string {
	re := regexp.MustCompile(`(?s)<think(?:ing)?>.*?</think(?:ing)?>`)
	return strings.TrimSpace(re.ReplaceAllString(content, ""))
}

// truncate returns a string truncated to max length with ellipsis.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
