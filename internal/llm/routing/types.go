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

// AllRoles lists every AgentRole in declaration order. Callers that need
// to enumerate roles (onboarding, settings) iterate this instead of
// hardcoding role strings so the list cannot drift from the constants.
var AllRoles = []AgentRole{
	RoleRouter,
	RoleKnowledge,
	RoleSummarizer,
	RoleRepoScout,
	RoleTester,
	RolePlanner,
	RoleImplementer,
	RoleReviewer,
	RoleSecurityReviewer,
	RoleSubtask,
	RoleTitle,
	RoleSDDImplementer,
	RoleSDDReviewer,
	RoleSDDBranchReviewer,
}

type ModelPreset struct {
	Name            string `toml:"-"`
	Provider        string `toml:"provider"`
	Model           string `toml:"model"`
	ContextWindow   int    `toml:"context_window"`
	MaxOutputTokens int    `toml:"max_output_tokens"`
	ToolCalling     string `toml:"tool_calling"`
	LocalOnly       bool   `toml:"local_only"`
	// Pricing is an optional per-preset override for the built-in pricing
	// table in the pricing package. Nil means "use the built-in table by
	// Model name (or zero if the model is unpriced)". Set to a non-nil
	// *pricing.ModelPricing to override input/output/reasoning/cache rates
	// for a specific preset, e.g. for a self-hosted deployment with custom
	// rates. Typed as `any` (rather than `*pricing.ModelPricing`) to avoid
	// an import cycle: routing is consumed by pricing, not the other way
	// around. pricing.Lookup type-asserts this back to *pricing.ModelPricing.
	Pricing any `toml:"pricing,omitempty"`
}

type AgentProfile struct {
	Name  string
	Roles map[AgentRole]string
}

type ContextBudget struct {
	MaxRepoContextTokens int `toml:"max_repo_context_tokens"`
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
