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
	return buildPackFromSections(candidates, maxTokens, now().UTC())
}

func RefreshPlan(pack Pack, plan []string, now func() time.Time) Pack {
	maxTokens := pack.TokenUsage.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	return RefreshPlanWithBudget(pack, plan, maxTokens, now)
}

func RefreshPlanWithBudget(pack Pack, plan []string, maxTokens int, now func() time.Time) Pack {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	generatedAt := pack.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	if now != nil {
		generatedAt = now().UTC()
	}

	planSection, hasPlan := newPlanSection(plan)
	sections := make([]Section, 0, len(pack.Sections)+1)
	insertedPlan := false
	for _, section := range pack.Sections {
		if section.Kind == SectionPlan {
			continue
		}
		if hasPlan && !insertedPlan && (section.Kind == SectionFileSnippet || section.Kind == SectionToolOutput) {
			sections = append(sections, planSection)
			insertedPlan = true
		}
		sections = append(sections, section)
	}
	if hasPlan && !insertedPlan {
		sections = append(sections, planSection)
	}

	return buildPackFromSections(sections, maxTokens, generatedAt)
}

func Rebudget(pack Pack, maxTokens int, now func() time.Time) Pack {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	generatedAt := pack.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	if now != nil {
		generatedAt = now().UTC()
	}

	return buildPackFromSections(pack.Clone().Sections, maxTokens, generatedAt)
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

func buildPackFromSections(sections []Section, maxTokens int, generatedAt time.Time) Pack {
	pack := Pack{
		TokenUsage:  TokenUsage{MaxTokens: maxTokens},
		GeneratedAt: generatedAt,
	}

	remaining := maxTokens
	for _, section := range sections {
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

func newPlanSection(plan []string) (Section, bool) {
	content := strings.TrimSpace(strings.Join(plan, "\n"))
	if content == "" {
		return Section{}, false
	}
	return Section{
		Kind:     SectionPlan,
		Title:    "Current Plan",
		Priority: 20,
		Content:  content,
	}, true
}

func newMemorySection(memories []MemoryNote) (Section, bool) {
	var lines []string
	for _, m := range memories {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if m.Kind != "" {
			lines = append(lines, fmt.Sprintf("[%s] %s", m.Kind, content))
		} else {
			lines = append(lines, content)
		}
	}
	if len(lines) == 0 {
		return Section{}, false
	}
	return Section{
		Kind:     SectionMemory,
		Title:    "Project Memories",
		Priority: 15,
		Content:  strings.Join(lines, "\n"),
	}, true
}

// MergeMemories replaces any existing memory section in pack with a single
// new section built from memories (joined newline-separated), inserted
// immediately before the first plan/file-snippet/tool-output section (or
// appended if none exist), then rebuilds the pack within maxTokens. Mirrors
// RefreshPlanWithBudget's replace-and-rebuild shape.
func MergeMemories(pack Pack, memories []MemoryNote, maxTokens int, now func() time.Time) Pack {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	generatedAt := pack.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	if now != nil {
		generatedAt = now().UTC()
	}

	memorySection, hasMemory := newMemorySection(memories)
	sections := make([]Section, 0, len(pack.Sections)+1)
	insertedMemory := false
	for _, section := range pack.Sections {
		if section.Kind == SectionMemory {
			continue
		}
		if hasMemory && !insertedMemory &&
			(section.Kind == SectionPlan || section.Kind == SectionFileSnippet || section.Kind == SectionToolOutput) {
			sections = append(sections, memorySection)
			insertedMemory = true
		}
		sections = append(sections, section)
	}
	if hasMemory && !insertedMemory {
		sections = append(sections, memorySection)
	}

	return buildPackFromSections(sections, maxTokens, generatedAt)
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
