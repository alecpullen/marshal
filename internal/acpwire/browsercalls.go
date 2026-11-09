package acpwire

// The notes, command, browser and project methods (P3 plan 7b).
const (
	MethodNote                  = "session/note"
	MethodCommand               = "session/command"
	MethodCommandList           = "session/command_list"
	MethodCommandAction         = "session/command_action"
	MethodPromptHistory         = "session/prompt_history"
	MethodPromptHistorySave     = "session/prompt_history_save"
	MethodMessages              = "session/messages"
	MethodFileIndex             = "session/file_index"
	MethodIntelligence          = "session/intelligence"
	MethodPlans                 = "session/plans"
	MethodSDDDiscard            = "session/sdd_discard"
	MethodSDDScaffold           = "session/sdd_scaffold"
	MethodDiff                  = "session/diff"
	MethodFile                  = "session/file"
	MethodMemoryList            = "session/memory_list"
	MethodMemoryDelete          = "session/memory_delete"
	MethodPluginsList           = "session/plugins_list"
	MethodPluginsInstallScan    = "session/plugins_install_scan"
	MethodPluginsInstallConfirm = "session/plugins_install_confirm"
	MethodPluginsInstallDiscard = "session/plugins_install_discard"
	MethodPluginsRemove         = "session/plugins_remove"
	MethodSkillsList            = "session/skills_list"
	MethodSkillsLoad            = "session/skills_load"
	MethodSkillsInstall         = "session/skills_install"
	MethodSkillsRemove          = "session/skills_remove"
	MethodSkillsAutoload        = "session/skills_autoload"
	MethodSkillGateSet          = "session/skill_gate_set"
	MethodSkillGateClear        = "session/skill_gate_clear"
)

// NoteParams is session/note's params.
type NoteParams struct {
	SessionID string `json:"sessionId"`
	Note      Note   `json:"note"`
}

// CommandCallParams is session/command's params from a marshal client; ACP's
// carry the same keys.
type CommandCallParams struct {
	SessionID string   `json:"sessionId"`
	Name      string   `json:"name"`
	Args      []string `json:"args,omitempty"`
}

// CommandActionParams is session/command_action's params: a row action's ID.
type CommandActionParams struct {
	SessionID string `json:"sessionId"`
	ID        string `json:"id"`
}

// PromptHistoryParams is session/prompt_history's params.
type PromptHistoryParams struct {
	SessionID string `json:"sessionId"`
	Limit     int    `json:"limit,omitempty"`
}

// PromptHistoryResult is session/prompt_history's result, newest first.
type PromptHistoryResult struct {
	Prompts []string `json:"prompts,omitempty"`
}

// PromptSaveParams is session/prompt_history_save's params.
type PromptSaveParams struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

// MessagesParams is session/messages' params.
type MessagesParams struct {
	SessionID string       `json:"sessionId"`
	Query     MessageQuery `json:"query"`
}

// FileIndexResult is session/file_index's result.
type FileIndexResult struct {
	Paths []string `json:"paths,omitempty"`
}

// IntelligenceResult is session/intelligence's result.
type IntelligenceResult struct {
	Checks []IntelligenceCheck `json:"checks,omitempty"`
}

// PlansResult is session/plans' result.
type PlansResult struct {
	Plans []PlanCandidate `json:"plans,omitempty"`
}

// PathParams is the params of a call that names one path: session/sdd_discard
// and a marshal client's session/file.
type PathParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

// DiffCallParams is a marshal client's session/diff params: the range
// Base..Head.
type DiffCallParams struct {
	SessionID string `json:"sessionId"`
	Base      string `json:"base"`
	Head      string `json:"head"`
}

// DiffResult is a marshal client's session/diff result.
type DiffResult struct {
	Diff string `json:"diff"`
}

// MemoriesResult is a marshal client's session/memory_list result.
type MemoriesResult struct {
	Memories []MemoryEntry `json:"memories,omitempty"`
}

// MemoryDeleteParams is session/memory_delete's params; ACP's carry the same
// keys.
type MemoryDeleteParams struct {
	SessionID string `json:"sessionId"`
	ID        int64  `json:"id"`
}

// PluginsResult is a marshal client's session/plugins_list result.
type PluginsResult struct {
	Plugins []PluginEntry `json:"plugins,omitempty"`
}

// PluginScanParams is a marshal client's session/plugins_install_scan params.
type PluginScanParams struct {
	SessionID string `json:"sessionId"`
	Source    string `json:"source"`
}

// PluginInstallParams is a marshal client's session/plugins_install_confirm
// params; ACP's carry the same keys.
type PluginInstallParams struct {
	SessionID string `json:"sessionId"`
	ScanToken string `json:"scanToken"`
	Scope     string `json:"scope"`
}

// PluginDiscardParams is a marshal client's session/plugins_install_discard
// params; ACP's carry the same keys.
type PluginDiscardParams struct {
	SessionID string `json:"sessionId"`
	ScanToken string `json:"scanToken"`
}

// PluginRemoveParams is a marshal client's session/plugins_remove params;
// ACP's carry the same keys.
type PluginRemoveParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
}

// SkillsResult is a marshal client's session/skills_list result.
type SkillsResult struct {
	Skills []SkillEntry `json:"skills,omitempty"`
}

// SkillParams names one listed skill by its store and source: a marshal
// client's session/skills_load and session/skills_remove params.
type SkillParams struct {
	SessionID string `json:"sessionId"`
	Scope     string `json:"scope"`
	Source    string `json:"source"`
}

// SkillNameResult is session/skills_load's and session/skills_install's
// result for a marshal client: the skill's name.
type SkillNameResult struct {
	Name string `json:"name"`
}

// SkillInstallParams is session/skills_install's params.
type SkillInstallParams struct {
	SessionID string `json:"sessionId"`
	Source    string `json:"source"`
	Scope     string `json:"scope"`
}

// SkillAutoloadParams is session/skills_autoload's params.
type SkillAutoloadParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	On        bool   `json:"on"`
}

// SkillGateParams is session/skill_gate_set's params.
type SkillGateParams struct {
	SessionID string `json:"sessionId"`
	On        bool   `json:"on"`
}

// SkillGateClearParams is session/skill_gate_clear's params.
type SkillGateClearParams struct {
	SessionID string `json:"sessionId"`
	Skill     string `json:"skill"`
}
