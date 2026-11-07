package acpwire

// The roles a transcript message carries, as the session stores them.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// The content types a transcript message carries, as the session stores them.
const (
	ContentTypePlain          = "plain"
	ContentTypeMarkdown       = "markdown"
	ContentTypeCode           = "code"
	ContentTypePlan           = "plan"
	ContentTypeDiff           = "diff"
	ContentTypeToolResult     = "tool_result"
	ContentTypeSkill          = "skill"
	ContentTypeSkillBody      = "skill_body"
	ContentTypeNarration      = "narration"
	ContentTypeCompaction     = "compaction"
	ContentTypeSubagentReport = "subagent_report"
	ContentTypeSubagentNotice = "subagent_notice"
	ContentTypeWatchReport    = "watch_report"
	ContentTypeSteering       = "steering"
	ContentTypeSkillAuto      = "skill_auto"
)

// Note is a message a client appends to the transcript without starting a
// turn (C9). Role is RoleSystem when empty, and only RoleSystem or RoleUser is
// accepted: a user note records text the user typed while no turn could run.
// ContentType is ContentTypePlain when empty.
type Note struct {
	Text        string `json:"text"`
	ContentType string `json:"contentType,omitempty"`
	Role        string `json:"role,omitempty"`
}

// The categories a notice belongs to. Clearing is by category, so a success in
// one subsystem never dismisses another's notice.
const (
	NoticeProvider = "provider"
	NoticeConfig   = "config"
	NoticeTool     = "tool"
	NoticeInternal = "internal"
)

// The severities a notice carries.
const (
	NoticeError = "error"
	NoticeWarn  = "warn"
)

// Notice is the session's banner: one user-facing notice at a time. SetAt is
// Unix milliseconds; SetNotice stamps a zero one with the agent's clock.
type Notice struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
	Source   string `json:"source,omitempty"`
	SetAt    int64  `json:"setAt,omitempty"`
}

// ErrorData is what an Error's Data carries. Notice is the banner a failed
// run's error calls for, classified agent side (C9). Plan 7 adds the provider's
// fields for CodeProviderError.
type ErrorData struct {
	Notice *Notice `json:"notice,omitempty"`
}

// NoticeSetParams is session/notice_set's params.
type NoticeSetParams struct {
	SessionID string `json:"sessionId"`
	Notice    Notice `json:"notice"`
}

// NoticeClearParams is session/notice_clear's params.
type NoticeClearParams struct {
	SessionID string `json:"sessionId"`
	Category  string `json:"category"`
}
