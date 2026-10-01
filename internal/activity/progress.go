package activity

// ProgressMode describes whether a response starts a public narration or
// updates the current narration for its actor and run.
type ProgressMode string

const (
	ProgressBegin  ProgressMode = "begin"
	ProgressRevise ProgressMode = "revise"
)

type SectionKind string

const (
	SectionChange   SectionKind = "change"
	SectionEvidence SectionKind = "evidence"
	SectionChecking SectionKind = "checking"
	SectionNext     SectionKind = "next"
	SectionWork     SectionKind = "work"
)

type ProgressSection struct {
	Kind         SectionKind `json:"kind"`
	Text         string      `json:"text"`
	EvidenceRefs []string    `json:"evidence_refs,omitempty"`
	// EvidenceSources captures canonical source identity at acceptance time.
	// It is runtime-owned and never part of the model-authored JSON contract.
	EvidenceSources []EvidenceSource `json:"-"`
}

// EvidenceSource is the immutable transcript identity resolved for one
// accepted alias. It survives alias authorization eviction so historical
// progress can still project a visible source in its original scope.
type EvidenceSource struct {
	Alias        string
	Owner        Ref
	SourceViewID string
	ToolName     string
}

// ProgressUpdate uses pointers to distinguish omitted values from explicit
// empty strings/slices, which clear fields when applying a revision.
type ProgressUpdate struct {
	Mode          ProgressMode       `json:"mode"`
	Headline      *string            `json:"headline,omitempty"`
	Body          *string            `json:"body,omitempty"`
	CurrentAction *string            `json:"current_action,omitempty"`
	Sections      *[]ProgressSection `json:"sections,omitempty"`
}

const (
	MaxProgressBytes    = 12 * 1024
	MaxHeadlineRunes    = 160
	MaxBodyRunes        = 2000
	MaxActionRunes      = 160
	MaxSectionRunes     = 600
	MaxProgressSections = 5
	MaxSectionRefs      = 8
)

// ProgressRevision is an immutable public snapshot. Each changed update
// stores a complete materialized view so callers never have to replay patches.
type ProgressRevision struct {
	NarrationID     string
	Revision        uint64
	Headline        string
	Body            string
	CurrentAction   string
	Sections        []ProgressSection
	SourceMessageID int64
	Sequence        uint64
}
