package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
)

// Critic reviews code diffs and returns structured verdicts.
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

// ReviewResult contains the verdict and token usage.
type ReviewResult struct {
	Verdict          *Verdict
	PromptTokens     int
	CompletionTokens int
}

// NewCritic creates a new critic agent.
func NewCritic(be backend.Backend, cfg config.AgentConfig) *Critic {
	return &Critic{
		backend: be,
		cfg:     cfg,
	}
}

// Review sends the diff to the LLM and parses the verdict.
func (c *Critic) Review(ctx context.Context, diff string, task string) (*ReviewResult, error) {
	systemPrompt := c.buildSystemPrompt()

	userContent := fmt.Sprintf(`Task: %s

Git diff to review:
%s

Provide your verdict as JSON matching the schema.`, task, diff)

	messages := []backend.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userContent},
	}

	resp, err := c.backend.Complete(ctx, c.cfg.Model, messages)
	if err != nil {
		return nil, fmt.Errorf("backend complete: %w", err)
	}

	// Parse the verdict from the response
	verdict, err := ParseVerdict(resp.Content)
	if err != nil {
		// Return a default FAIL verdict on parse error
		return &ReviewResult{
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

	return &ReviewResult{
		Verdict:          verdict,
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
	}, nil
}

// buildSystemPrompt constructs the full system prompt for the critic.
func (c *Critic) buildSystemPrompt() string {
	prompt := SecurityInstructions + "\n\n" + CriticBaseInstructions + "\n\n"

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

// ParseVerdict extracts a JSON verdict from content.
// Handles both pure JSON and JSON embedded in  tags.
func ParseVerdict(content string) (*Verdict, error) {
	// First, try to extract from  tags if present
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

// extractJSONFromContent removes  blocks and returns the remaining content.
func extractJSONFromContent(content string) string {
	// Remove  blocks (multiline, non-greedy)
	// Pattern matches  ...  with any content in between
	re := regexp.MustCompile(`(?s)<thinking>.*?</thinking>`)
	content = re.ReplaceAllString(content, "")

	// Clean up extra whitespace
	return strings.TrimSpace(content)
}

// truncate returns a string truncated to max length with ellipsis.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
