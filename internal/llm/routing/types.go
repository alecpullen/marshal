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
	Name            string `toml:"-"`
	Provider        string `toml:"provider"`
	Model           string `toml:"model"`
	ContextWindow   int    `toml:"context_window"`
	MaxOutputTokens int    `toml:"max_output_tokens"`
	ToolCalling     string `toml:"tool_calling"`
	LocalOnly       bool   `toml:"local_only"`
}

type AgentProfile struct {
	Name  string
	Roles map[AgentRole]string
}

type ContextBudget struct {
	MaxRepoContextTokens int `toml:"max_repo_context_tokens"`
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
