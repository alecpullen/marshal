package contract

// SkillEntry is one installed skill: GET /api/library/skills' answer, one
// entry per scope (web/bridge/library.go).
type SkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
	Scope       string `json:"scope"`
}

// SkillPreview is what staging a skill answers: what it would install, and
// the token that confirms or discards it.
type SkillPreview struct {
	StagingToken string `json:"stagingToken"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Risk         string `json:"risk"`
	Source       string `json:"source"`
}

// SkillList is GET /api/library/skills' body.
type SkillList struct {
	Skills []SkillEntry `json:"skills"`
}

// SkillStage and SkillConfirm are the bodies the skills routes take. The
// confirm must name the same scope and project the preview was staged on.
type SkillStage struct {
	Source  string `json:"source"`
	Scope   string `json:"scope,omitempty"`
	Project string `json:"project,omitempty"`
}

type SkillConfirm struct {
	StagingToken string `json:"stagingToken"`
	Scope        string `json:"scope,omitempty"`
	Project      string `json:"project,omitempty"`
}

type SkillDiscard struct {
	StagingToken string `json:"stagingToken"`
}

// PluginEntry is one installed plugin: GET /api/library/plugins' answer.
type PluginEntry struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Ref         string `json:"ref,omitempty"`
	Commit      string `json:"commit"`
	ContentHash string `json:"contentHash"`
	InstalledAt string `json:"installedAt"`
	Scope       string `json:"scope"`
}

// PluginContents is what a scanned plugin ships.
type PluginContents struct {
	HasManifest    bool `json:"hasManifest"`
	SkillCount     int  `json:"skillCount"`
	CommandCount   int  `json:"commandCount"`
	HookCount      int  `json:"hookCount"`
	MCPServerCount int  `json:"mcpServerCount"`
	MCPPolicyCount int  `json:"mcpPolicyCount"`
}

// PluginScan is what scanning a plugin answers: what it ships, and the token
// that confirms or discards it.
type PluginScan struct {
	ScanToken string         `json:"scanToken"`
	Name      string         `json:"name"`
	Source    string         `json:"source"`
	Ref       string         `json:"ref,omitempty"`
	Commit    string         `json:"commit"`
	Contents  PluginContents `json:"contents"`
}

// PluginList is GET /api/library/plugins' body.
type PluginList struct {
	Plugins []PluginEntry `json:"plugins"`
}

// PluginStage, PluginConfirm and PluginDiscard are the bodies the plugin
// routes take.
type PluginStage struct {
	Source  string `json:"source"`
	Ref     string `json:"ref,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Project string `json:"project,omitempty"`
}

type PluginConfirm struct {
	ScanToken string `json:"scanToken"`
	Scope     string `json:"scope,omitempty"`
	Project   string `json:"project,omitempty"`
}

type PluginDiscard struct {
	ScanToken string `json:"scanToken"`
}

// MemoryEntry is one memory: GET /api/library/memory's answer, one entry per
// project's memory store.
type MemoryEntry struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Content    string `json:"content"`
	Confidence string `json:"confidence"`
	// SourceSessionID is the session that learned it, when the store kept it.
	SourceSessionID string `json:"sourceSessionId,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	Scope           string `json:"scope,omitempty"`
	ScopeKey        string `json:"scopeKey,omitempty"`
	// LearnedProjectRoot, LearnedAgent and LearnedStep are its provenance.
	LearnedProjectRoot string   `json:"learnedProjectRoot,omitempty"`
	LearnedAgent       string   `json:"learnedAgent,omitempty"`
	LearnedStep        int      `json:"learnedStep,omitempty"`
	ConfirmedBy        []string `json:"confirmedBy,omitempty"`
}

// MemoryList is GET /api/library/memory's body.
type MemoryList struct {
	Entries []MemoryEntry `json:"entries"`
}

// MemorySuggestion is a project memory another project holds too, with the
// scope it could be promoted to. Promoting it is the person's decision.
type MemorySuggestion struct {
	MemoryID         int64  `json:"memoryId"`
	MatchProjectRoot string `json:"matchProjectRoot"`
	SuggestedScope   string `json:"suggestedScope"`
}

// MemorySuggestions is GET /api/library/memory/suggestions' body.
type MemorySuggestions struct {
	Suggestions []MemorySuggestion `json:"suggestions"`
}

// MemoryPromote and MemoryConfidence are the bodies the memory routes take.
type MemoryPromote struct {
	Scope    string `json:"scope"`
	ScopeKey string `json:"scopeKey,omitempty"`
}

type MemoryConfidence struct {
	Confidence string `json:"confidence"`
}
