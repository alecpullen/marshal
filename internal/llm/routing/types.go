package routing

type AgentRole string

const (
	RoleRouter            AgentRole = "router"
	RoleKnowledge         AgentRole = "knowledge"
	RoleSummarizer        AgentRole = "summarizer"
	RoleRepoScout         AgentRole = "repo_scout"
	RoleTester            AgentRole = "tester"
	RolePlanner           AgentRole = "planner"
	RoleImplementer       AgentRole = "implementer"
	RoleReviewer          AgentRole = "reviewer"
	RoleSecurityReviewer  AgentRole = "security_reviewer"
	RoleSubtask           AgentRole = "subtask"
	RoleTitle             AgentRole = "title"
	RoleSDDImplementer    AgentRole = "sdd_implementer"
	RoleSDDReviewer       AgentRole = "sdd_reviewer"
	RoleSDDBranchReviewer AgentRole = "sdd_branch_reviewer"
)

type ModelPreset struct {
	Name            string
	Provider        string
	Model           string
	ContextWindow   int
	MaxOutputTokens int
	ToolCalling     string
	LocalOnly       bool
}

type AgentProfile struct {
	Name  string
	Roles map[AgentRole]string
}

type ContextBudget struct {
	MaxRepoContextTokens  int
	MaxConversationTokens int
	IncludeRawCode        bool
	IncludeSummaries      bool
	IncludeSymbols        bool
	IncludeDiff           bool
	IncludeTests          bool
}

type TaskProfile struct {
	Class string
}

type Route struct {
	Role          AgentRole
	Profile       string
	Preset        ModelPreset
	ContextBudget ContextBudget
	Legacy        bool
}

type Config struct {
	DefaultProfile string
	RemoteAllowed  bool
	Presets        map[string]ModelPreset
	Profiles       map[string]AgentProfile
	ContextBudgets map[AgentRole]ContextBudget
	LegacyProvider string
	LegacyModel    string
}
