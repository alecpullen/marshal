package contextpack

import "time"

const DefaultMaxTokens = 12000

type SectionKind string

const (
	SectionRepoCard         SectionKind = "repo_card"
	SectionRepoMap          SectionKind = "repo_map"
	SectionMemory           SectionKind = "memory"
	SectionPlan             SectionKind = "plan"
	SectionScratchpad       SectionKind = "scratchpad"
	SectionTodos            SectionKind = "todos"
	SectionFileSnippet      SectionKind = "file_snippet"
	SectionToolOutput       SectionKind = "tool_output"
	SectionSemantic         SectionKind = "semantic"
	SectionSessionSummaries SectionKind = "session_summaries"
)

type Pack struct {
	Sections    []Section
	TokenUsage  TokenUsage
	GeneratedAt time.Time
}

type Section struct {
	Kind            SectionKind
	Title           string
	Content         string
	Source          string
	Priority        int
	EstimatedTokens int
}

type TokenUsage struct {
	MaxTokens       int
	EstimatedTokens int
	Truncated       bool
}

type FileSnippet struct {
	Path      string
	StartLine int
	EndLine   int
	Content   string
}

// MemoryNote is contextpack's own view of a durable memory — just enough
// to render a section. It is declared here (not imported from
// internal/knowledge) so that internal/agent, which already depends on
// contextpack, never needs to depend on internal/knowledge (the two
// packages must not import each other — see the Milestone N design doc).
type MemoryNote struct {
	Kind       string
	Content    string
	Confidence string    // mirrors db confidence values; "stale" is dropped at render
	UpdatedAt  time.Time // zero sorts oldest within a kind/confidence rung
}

// ScratchpadEntry is contextpack's own view of a scratchpad entry —
// just enough to render a projection section. Declared here (not
// imported from internal/db) to avoid a circular dependency.
type ScratchpadEntry struct {
	Key     string
	Content string
	Format  string
	Updated int64 // unix timestamp in milliseconds
}

// TodoItem is contextpack's own view of a todo entry — just enough to
// render a projection section. Declared here (not imported from
// internal/db) to avoid a circular dependency.
type TodoItem struct {
	Content string
	Status  string
}

func (p Pack) IsEmpty() bool {
	return len(p.Sections) == 0
}

func (p Pack) Clone() Pack {
	clone := p
	if p.Sections != nil {
		clone.Sections = append([]Section(nil), p.Sections...)
	}
	return clone
}
