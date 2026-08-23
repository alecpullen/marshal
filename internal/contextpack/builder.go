package contextpack

import (
	"fmt"
	"slices"
	"sort"
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

// maxMemoryNotes bounds how many memory lines reach the pack; overflow is
// dropped whole-entry, never truncated mid-line.
const maxMemoryNotes = 20

// Rank values mirror db memory kinds/confidences. contextpack must not import
// internal/db (circular dependency), so the strings are literals here.
func memoryKindRank(kind string) int {
	switch kind {
	case "architecture":
		return 0
	case "decision":
		return 1
	case "fact":
		return 2
	}
	return 3
}

func memoryConfidenceRank(confidence string) int {
	switch confidence {
	case "confirmed":
		return 0
	case "tentative":
		return 1
	}
	return 2
}

// rankMemories filters stale notes, orders by kind priority then confidence
// then recency, and caps the result. Deterministic: sort is stable and the
// comparators fully order distinct inputs by UpdatedAt.
func rankMemories(memories []MemoryNote) []MemoryNote {
	out := make([]MemoryNote, 0, len(memories))
	for _, m := range memories {
		if m.Confidence == "stale" {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ki, kj := memoryKindRank(out[i].Kind), memoryKindRank(out[j].Kind); ki != kj {
			return ki < kj
		}
		if ci, cj := memoryConfidenceRank(out[i].Confidence), memoryConfidenceRank(out[j].Confidence); ci != cj {
			return ci < cj
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if len(out) > maxMemoryNotes {
		out = out[:maxMemoryNotes]
	}
	return out
}

func newMemorySection(memories []MemoryNote) (Section, bool) {
	memories = rankMemories(memories)
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

// newRepoSections builds the repo-orientation sections from rendered card
// and directory-map content. Empty content yields no section.
func newRepoSections(card, dirMap string) (cardSec, mapSec Section, hasCard, hasMap bool) {
	if content, ok := trimSectionContent(card); ok {
		cardSec = Section{Kind: SectionRepoCard, Title: "Repo Card", Priority: 10, Content: content}
		hasCard = true
	}
	if content, ok := trimSectionContent(dirMap); ok {
		mapSec = Section{Kind: SectionRepoMap, Title: "Directory Map", Priority: 11, Content: content}
		hasMap = true
	}
	return cardSec, mapSec, hasCard, hasMap
}

// MergeRepoSections replaces any existing repo-card / directory-map sections
// with fresh ones and rebuilds the pack within maxTokens. The repo sections
// lead the section list: they are the most foundational context and
// buildPackFromSections budgets regular sections in slice order, so the card
// starves last. Idempotent — re-seeding overwrites rather than duplicates.
func MergeRepoSections(pack Pack, card, dirMap string, maxTokens int, now func() time.Time) Pack {
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	cardSec, mapSec, hasCard, hasMap := newRepoSections(card, dirMap)
	sections := make([]Section, 0, len(pack.Sections)+2)
	if hasCard {
		sections = append(sections, cardSec)
	}
	if hasMap {
		sections = append(sections, mapSec)
	}
	for _, s := range pack.Sections {
		if s.Kind == SectionRepoCard || s.Kind == SectionRepoMap {
			continue
		}
		sections = append(sections, s)
	}
	return buildPackFromSections(sections, maxTokens, generatedAt)
}

// newSessionSummariesSection builds the prior-work section. Priority 16:
// just below memories (15), so it starves first under budget pressure.
func newSessionSummariesSection(content string) (Section, bool) {
	content, ok := trimSectionContent(content)
	if !ok {
		return Section{}, false
	}
	return Section{
		Kind:     SectionSessionSummaries,
		Title:    "Recent Sessions",
		Priority: 16,
		Content:  content,
	}, true
}

// MergeSessionSummaries replaces any existing session-summaries section with
// one built from content, then rebuilds the pack within maxTokens. Mirrors
// MergeMemories' replace-and-rebuild shape.
func MergeSessionSummaries(pack Pack, content string, maxTokens int, now func() time.Time) Pack {
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	sec, ok := newSessionSummariesSection(content)
	sections := replaceSection(pack.Sections, SectionSessionSummaries, sec, ok, SectionPlan, SectionFileSnippet, SectionToolOutput)
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

func newScratchpadSection(entries []ScratchpadEntry, projectionMaxTokens int, now time.Time) (Section, bool) {
	if len(entries) == 0 {
		return Section{}, false
	}

	const header = "Keys parked in working memory (use scratchpad.read for full content):"
	lines := []string{header}
	used := EstimateTokens(header)

	for _, e := range entries {
		preview := strings.ReplaceAll(e.Content, "\n", " ")
		preview = strings.TrimSpace(preview)
		runes := []rune(preview)
		if len(runes) > 80 {
			preview = string(runes[:80]) + "..."
		}
		tokens := EstimateTokens(e.Content)
		format := e.Format
		if format == "" {
			format = "text"
		}
		age := formatAge(e.Updated, now)
		line := fmt.Sprintf("- %s (%s, ~%d tokens, %s): %s", e.Key, format, tokens, age, preview)
		lineTokens := EstimateTokens(line)

		if used+lineTokens > projectionMaxTokens && len(lines) > 1 {
			lines = append(lines, "...")
			break
		}
		lines = append(lines, line)
		used += lineTokens
	}

	return Section{
		Kind:     SectionScratchpad,
		Title:    "Scratchpad",
		Priority: 50,
		Content:  strings.Join(lines, "\n"),
	}, true
}

func formatAge(updated int64, now time.Time) string {
	if updated <= 0 {
		return "unknown age"
	}
	d := now.Sub(time.UnixMilli(updated))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// MergeScratchpad replaces any existing scratchpad section with a
// projection built from entries, inserted before file-snippet/tool-output
// sections, then rebudgets within maxTokens. Empty entries removes the
// section. projectionMaxTokens caps the size of the scratchpad projection;
// when zero or negative it defaults to one eighth of the pack's current
// max-token budget.
func MergeScratchpad(pack Pack, entries []ScratchpadEntry, maxTokens, projectionMaxTokens int, now func() time.Time) Pack {
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	if projectionMaxTokens <= 0 {
		projectionMaxTokens = pack.TokenUsage.MaxTokens / 8
	}
	sec, ok := newScratchpadSection(entries, projectionMaxTokens, generatedAt)
	sections := replaceSection(pack.Sections, SectionScratchpad, sec, ok, SectionFileSnippet, SectionToolOutput)
	return buildPackFromSections(sections, maxTokens, generatedAt)
}

func newTodosSection(todos []TodoItem) (Section, bool) {
	if len(todos) == 0 {
		return Section{}, false
	}
	var lines []string
	for _, item := range todos {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		var marker string
		switch item.Status {
		case "completed":
			marker = "x"
		case "in_progress":
			marker = "~"
		default:
			marker = " "
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", marker, content))
	}
	if len(lines) == 0 {
		return Section{}, false
	}
	return Section{
		Kind:     SectionTodos,
		Title:    "Current Todos",
		Priority: 45,
		Content:  strings.Join(lines, "\n"),
	}, true
}

// MergeTodos replaces any existing todos section with a projection of
// the session's current todo list, inserted before file-snippet/
// tool-output sections, then rebudgets. Empty todos removes the
// section. todo.write is full-replace, so the model must see the
// current list every turn or a follow-up write obliterates it.
func MergeTodos(pack Pack, todos []TodoItem, maxTokens int, now func() time.Time) Pack {
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	sec, ok := newTodosSection(todos)
	sections := replaceSection(pack.Sections, SectionTodos, sec, ok, SectionFileSnippet, SectionToolOutput)
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
