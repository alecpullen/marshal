package contextpack

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const truncationMarker = "\n\n...[truncated]"

// trimSectionContent trims surrounding whitespace and reports whether
// the result is non-empty. Used as the shared skip/empty rule for
// sections built by PinFiles.
func trimSectionContent(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

func EstimateTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}

// PinFiles adds the given snippets as pinned sections that bypass the
// token-budget gate (F18 R2: accepted @file references are injected as
// context, budget permitting — but pinning means they are not dropped by
// the greedy rebudget pass). Each pinned snippet is appended to
// pack.Sections with Priority 100 (higher than the 30/40 of normal
// file-snippet/tool-output sections).
func PinFiles(pack Pack, snippets []FileSnippet) Pack {
	for _, snip := range snippets {
		content, ok := trimSectionContent(snip.Content)
		if !ok {
			continue
		}
		source := snip.Path
		if snip.StartLine > 0 && snip.EndLine > 0 {
			source = fmt.Sprintf("%s:%d-%d", snip.Path, snip.StartLine, snip.EndLine)
		}
		pack.Sections = append(pack.Sections, Section{
			Kind:            SectionFileSnippet,
			Title:           snip.Path,
			Content:         content,
			Source:          source,
			Priority:        100,
			EstimatedTokens: EstimateTokens(content),
		})
	}
	return pack
}

// resolvePackParams applies the shared maxTokens/generatedAt defaulting
// used by every pack-mutation entry point.
func resolvePackParams(pack Pack, maxTokens int, now func() time.Time) (int, time.Time) {
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
	return maxTokens, generatedAt
}

// replaceSection returns sections with every dropKind section removed and
// sec inserted immediately before the first section whose kind is in
// beforeKinds (appended when none match). When ok is false, sections are
// only filtered.
func replaceSection(sections []Section, dropKind SectionKind, sec Section, ok bool, beforeKinds ...SectionKind) []Section {
	out := make([]Section, 0, len(sections)+1)
	inserted := false
	for _, s := range sections {
		if s.Kind == dropKind {
			continue
		}
		if ok && !inserted && slices.Contains(beforeKinds, s.Kind) {
			out = append(out, sec)
			inserted = true
		}
		out = append(out, s)
	}
	if ok && !inserted {
		out = append(out, sec)
	}
	return out
}

func RefreshPlan(pack Pack, plan []string, now func() time.Time) Pack {
	maxTokens := pack.TokenUsage.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	return RefreshPlanWithBudget(pack, plan, maxTokens, now)
}

func RefreshPlanWithBudget(pack Pack, plan []string, maxTokens int, now func() time.Time) Pack {
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	planSection, hasPlan := newPlanSection(plan)
	sections := replaceSection(pack.Sections, SectionPlan, planSection, hasPlan, SectionFileSnippet, SectionToolOutput)
	return buildPackFromSections(sections, maxTokens, generatedAt)
}

func Rebudget(pack Pack, maxTokens int, now func() time.Time) Pack {
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	return buildPackFromSections(pack.Clone().Sections, maxTokens, generatedAt)
}

func buildPackFromSections(sections []Section, maxTokens int, generatedAt time.Time) Pack {
	pack := Pack{
		TokenUsage:  TokenUsage{MaxTokens: maxTokens},
		GeneratedAt: generatedAt,
	}

	// Split pinned (Priority >= 100) from regular sections. Pinned
	// sections are processed first so the greedy pass cannot starve
	// them. Regular sections keep their original order (RepoCard=10,
	// Plan=20, FileSnippet=30, ToolOutput=40 — lower number first,
	// meaning more foundational content gets budget priority).
	var pinned, regular []Section
	for _, s := range sections {
		s.EstimatedTokens = EstimateTokens(s.Content)
		if s.EstimatedTokens == 0 {
			continue
		}
		if s.Priority >= 100 {
			pinned = append(pinned, s)
		} else {
			regular = append(regular, s)
		}
	}

	remaining := maxTokens
	for _, s := range slices.Concat(pinned, regular) {
		if s.EstimatedTokens <= remaining {
			pack.Sections = append(pack.Sections, s)
			pack.TokenUsage.EstimatedTokens += s.EstimatedTokens
			remaining -= s.EstimatedTokens
			continue
		}
		truncated, ok := truncateToTokens(s.Content, remaining)
		if !ok {
			pack.TokenUsage.Truncated = true
			continue
		}
		s.Content = truncated
		s.EstimatedTokens = EstimateTokens(s.Content)
		pack.Sections = append(pack.Sections, s)
		pack.TokenUsage.EstimatedTokens += s.EstimatedTokens
		pack.TokenUsage.Truncated = true
		remaining -= s.EstimatedTokens
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
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	memorySection, hasMemory := newMemorySection(memories)
	sections := replaceSection(pack.Sections, SectionMemory, memorySection, hasMemory, SectionPlan, SectionFileSnippet, SectionToolOutput)
	return buildPackFromSections(sections, maxTokens, generatedAt)
}

func newSemanticSection(snippets []FileSnippet) (Section, bool) {
	var parts []string
	for _, s := range snippets {
		content, ok := trimSectionContent(s.Content)
		if !ok {
			continue
		}
		src := s.Path
		if s.StartLine > 0 && s.EndLine > 0 {
			src = fmt.Sprintf("%s:%d-%d", s.Path, s.StartLine, s.EndLine)
		}
		parts = append(parts, fmt.Sprintf("%s\n%s", src, content))
	}
	if len(parts) == 0 {
		return Section{}, false
	}
	return Section{
		Kind:     SectionSemantic,
		Title:    "Relevant Code",
		Priority: 35,
		Content:  strings.Join(parts, "\n\n"),
	}, true
}

// newScratchpadSection builds a projection section from scratchpad
// entries. Each entry is rendered as a single line showing the key,
// format, estimated token count, and a one-line preview of the content
// (truncated to 120 chars). This keeps the projection compact so it
// stays within the context budget while giving the agent enough
// information to decide whether to call scratchpad.read for full content.
func newScratchpadSection(entries []ScratchpadEntry) (Section, bool) {
	if len(entries) == 0 {
		return Section{}, false
	}
	var lines []string
	for _, e := range entries {
		preview := strings.ReplaceAll(e.Content, "\n", " ")
		preview = strings.TrimSpace(preview)
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		tokens := EstimateTokens(e.Content)
		format := e.Format
		if format == "" {
			format = "text"
		}
		lines = append(lines, fmt.Sprintf("%s (%s, ~%d tokens): %s", e.Key, format, tokens, preview))
	}
	return Section{
		Kind:     SectionScratchpad,
		Title:    "Scratchpad",
		Priority: 50,
		Content:  strings.Join(lines, "\n"),
	}, true
}

// MergeScratchpad replaces any existing scratchpad section with a
// projection built from entries, inserted before file-snippet/tool-output
// sections, then rebudgets within maxTokens. Empty entries removes the
// section.
func MergeScratchpad(pack Pack, entries []ScratchpadEntry, maxTokens int, now func() time.Time) Pack {
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	sec, ok := newScratchpadSection(entries)
	sections := replaceSection(pack.Sections, SectionScratchpad, sec, ok, SectionFileSnippet, SectionToolOutput)
	return buildPackFromSections(sections, maxTokens, generatedAt)
}

// MergeSemanticContext replaces any existing semantic section with one built
// from snippets, inserted before file-snippet/tool-output sections, then
// rebudgets within maxTokens. Empty snippets removes the section.
func MergeSemanticContext(pack Pack, snippets []FileSnippet, maxTokens int, now func() time.Time) Pack {
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	sec, ok := newSemanticSection(snippets)
	sections := replaceSection(pack.Sections, SectionSemantic, sec, ok, SectionFileSnippet, SectionToolOutput)
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
