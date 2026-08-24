package contextpack

import "time"

const DefaultMaxTokens = 12000

// sectionShareDenominator bounds how much of the pack budget any single
// non-pinned section may claim: at most maxTokens/sectionShareDenominator.
//
// Without it the first large section wins everything. A repo directory
// map grows with the repo and is unbounded in practice — on a mid-size
// Go repo it alone exceeds the whole default budget — so it would
// otherwise evict the memory, session-summary, plan, and todo sections
// that carry everything the agent is supposed to remember. Those
// sections are small; reserving half the budget is enough to fit all of
// them with room to spare, and the map degrades gracefully because it is
// orientation material that truncates without losing its shape.
//
// Pinned sections (Priority >= pinnedPriority) are exempt: the user asked
// for those by name.
const sectionShareDenominator = 2

// pinnedPriority is the Priority at or above which a section is pinned:
// exempt from the fairness cap, allocated before everything else, and
// rendered first. PinFiles assigns it.
const pinnedPriority = 100

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
	Kind    SectionKind
	Title   string
	Content string
	// Full is the section's untruncated text. buildPackFromSections
	// restores Content from it before every budget pass, so a pack
	// truncated under a small budget can be rebudgeted upward without
	// having permanently lost the dropped text. Empty on a section that
	// has never been through a budget pass, in which case Content is
	// already the full text.
	Full            string
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
