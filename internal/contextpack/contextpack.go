package contextpack

import "time"

const DefaultMaxTokens = 12000

type SectionKind string

const (
	SectionRepoCard    SectionKind = "repo_card"
	SectionMemory      SectionKind = "memory"
	SectionPlan        SectionKind = "plan"
	SectionFileSnippet SectionKind = "file_snippet"
	SectionToolOutput  SectionKind = "tool_output"
)

type Pack struct {
	Sections    []Section
	TokenUsage  TokenUsage
	GeneratedAt time.Time

	// Pinned tracks @file references accepted by the F18 editor
	// completion popup. The runner extracts these from the user's goal
	// and adds them via PinFiles; the pinned sections are appended to
	// pack.Sections with a high Priority so they survive a token-budget
	// rebudget (the user explicitly asked for the file's content).
	Pinned []FileSnippet
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

type BuildInput struct {
	RepoCard         string
	FileSnippets     []FileSnippet
	RecentToolOutput []ToolOutput
	Plan             []string
	MaxTokens        int
	Now              func() time.Time
}

type FileSnippet struct {
	Path      string
	StartLine int
	EndLine   int
	Content   string
}

type ToolOutput struct {
	ToolName string
	Summary  string
	Content  string
}

// MemoryNote is contextpack's own view of a durable memory — just enough
// to render a section. It is declared here (not imported from
// internal/knowledge) so that internal/agent, which already depends on
// contextpack, never needs to depend on internal/knowledge (the two
// packages must not import each other — see the Milestone N design doc).
type MemoryNote struct {
	Kind    string
	Content string
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
