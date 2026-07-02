package contextpack

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const truncationMarker = "\n\n...[truncated]"

type Builder struct{}

func NewBuilder() Builder {
	return Builder{}
}

func EstimateTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}

func (b Builder) Build(input BuildInput) Pack {
	maxTokens := input.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	now := time.Now
	if input.Now != nil {
		now = input.Now
	}

	candidates := buildCandidateSections(input)
	pack := Pack{
		TokenUsage:  TokenUsage{MaxTokens: maxTokens},
		GeneratedAt: now().UTC(),
	}

	remaining := maxTokens
	for _, section := range candidates {
		section.EstimatedTokens = EstimateTokens(section.Content)
		if section.EstimatedTokens == 0 {
			continue
		}
		if section.EstimatedTokens <= remaining {
			pack.Sections = append(pack.Sections, section)
			pack.TokenUsage.EstimatedTokens += section.EstimatedTokens
			remaining -= section.EstimatedTokens
			continue
		}

		truncated, ok := truncateToTokens(section.Content, remaining)
		if !ok {
			pack.TokenUsage.Truncated = true
			continue
		}
		section.Content = truncated
		section.EstimatedTokens = EstimateTokens(section.Content)
		pack.Sections = append(pack.Sections, section)
		pack.TokenUsage.EstimatedTokens += section.EstimatedTokens
		pack.TokenUsage.Truncated = true
		remaining -= section.EstimatedTokens
	}

	return pack
}

func buildCandidateSections(input BuildInput) []Section {
	var sections []Section
	if strings.TrimSpace(input.RepoCard) != "" {
		sections = append(sections, Section{
			Kind:     SectionRepoCard,
			Title:    "Repo Card",
			Source:   "repo.card",
			Priority: 10,
			Content:  strings.TrimSpace(input.RepoCard),
		})
	}
	if len(input.Plan) > 0 {
		sections = append(sections, Section{
			Kind:     SectionPlan,
			Title:    "Current Plan",
			Priority: 20,
			Content:  strings.Join(input.Plan, "\n"),
		})
	}
	for _, snippet := range input.FileSnippets {
		content := strings.TrimSpace(snippet.Content)
		if content == "" {
			continue
		}
		source := snippet.Path
		if snippet.StartLine > 0 && snippet.EndLine > 0 {
			source = fmt.Sprintf("%s:%d-%d", snippet.Path, snippet.StartLine, snippet.EndLine)
		}
		sections = append(sections, Section{
			Kind:     SectionFileSnippet,
			Title:    snippet.Path,
			Source:   source,
			Priority: 30,
			Content:  content,
		})
	}
	for _, output := range input.RecentToolOutput {
		content := strings.TrimSpace(output.Summary)
		if strings.TrimSpace(output.Content) != "" {
			content += "\n\n" + strings.TrimSpace(output.Content)
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		sections = append(sections, Section{
			Kind:     SectionToolOutput,
			Title:    output.ToolName,
			Source:   output.ToolName,
			Priority: 40,
			Content:  content,
		})
	}
	return sections
}

func truncateToTokens(content string, maxTokens int) (string, bool) {
	if maxTokens <= 0 {
		return "", false
	}
	markerTokens := EstimateTokens(truncationMarker)
	if maxTokens <= markerTokens {
		return "", false
	}
	maxRunes := (maxTokens - markerTokens) * 4
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content, true
	}
	truncated := strings.TrimRight(string(runes[:maxRunes]), "\n\t ")
	return truncated + truncationMarker, true
}
