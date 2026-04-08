package backend

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ReasoningParser extracts reasoning and final content from models like DeepSeek-R1
// that output  blocks followed by the actual response.
type ReasoningParser struct {
	// ExtractContent removes  tags and returns just the final content
	ExtractContent func(string) string
	// ExtractReasoning returns just the content of  tags
	ExtractReasoning func(string) string
	// HasReasoning checks if content contains  tags
	HasReasoning func(string) bool
}

// DeepSeekR1Parser handles DeepSeek-R1 style output:
//
// ... chain of thought ...
//
// Final response here (JSON, answer, etc.)
type DeepSeekR1Parser struct{}

var thinkingTagRegex = regexp.MustCompile(`(?s)  *(.+?)  *`)

// ExtractContent removes  blocks and returns the remaining content
func (p *DeepSeekR1Parser) ExtractContent(s string) string {
	// Remove all  blocks
	content := thinkingTagRegex.ReplaceAllString(s, "")
	// Clean up extra whitespace
	return strings.TrimSpace(content)
}

// ExtractReasoning returns the concatenated content of all  blocks
func (p *DeepSeekR1Parser) ExtractReasoning(s string) string {
	matches := thinkingTagRegex.FindAllStringSubmatch(s, -1)
	var reasoning []string
	for _, match := range matches {
		if len(match) > 1 {
			reasoning = append(reasoning, strings.TrimSpace(match[1]))
		}
	}
	return strings.Join(reasoning, "\n\n")
}

// HasReasoning returns true if the content contains  tags
func (p *DeepSeekR1Parser) HasReasoning(s string) bool {
	return thinkingTagRegex.MatchString(s)
}

// ParseCriticVerdict attempts to parse a JSON verdict from critic output
// Handles both pure JSON and JSON embedded after  reasoning blocks
func ParseCriticVerdict(content string) (*Verdict, error) {
	parser := &DeepSeekR1Parser{}

	// Extract just the final content (removes  blocks)
	finalContent := parser.ExtractContent(content)

	// Try to find JSON in the content
	// Look for JSON object between braces
	jsonStart := strings.Index(finalContent, "{")
	jsonEnd := strings.LastIndex(finalContent, "}")

	if jsonStart == -1 || jsonEnd == -1 || jsonEnd <= jsonStart {
		return nil, fmt.Errorf("no JSON object found in response")
	}

	jsonStr := finalContent[jsonStart : jsonEnd+1]

	var verdict Verdict
	if err := json.Unmarshal([]byte(jsonStr), &verdict); err != nil {
		return nil, fmt.Errorf("failed to parse verdict JSON: %w", err)
	}

	return &verdict, nil
}

// Verdict represents the structured critic response
type Verdict struct {
	Verdict  string   `json:"verdict"` // "PASS" or "FAIL"
	Summary  string   `json:"summary"`
	Issue    string   `json:"issue"`
	Fix      string   `json:"fix"`
	Concerns []string `json:"concerns"`
}

// IsPass returns true if the verdict is a PASS
func (v *Verdict) IsPass() bool {
	return strings.ToUpper(v.Verdict) == "PASS"
}
