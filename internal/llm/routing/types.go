package routing

import "github.com/pelletier/go-toml/v2"

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
	RoleSDDOrchestrator   AgentRole = "sdd_orchestrator"
	RoleSDDAuditor        AgentRole = "sdd_auditor"
	RoleSDDInvestigator   AgentRole = "sdd_investigator"
	RoleSDDRescue         AgentRole = "sdd_rescue"

	// RoleEmbedding selects the text-embedding provider+model. It is
	// deliberately excluded from AllRoles: embedding is not a chat role, so
	// onboarding/settings that enumerate AllRoles must not list it. Resolved
	// via StaticRouter.ResolveEmbedding, not ResolveRole.
	RoleEmbedding AgentRole = "embedding"
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
	RoleSDDOrchestrator,
	RoleSDDAuditor,
	RoleSDDInvestigator,
	RoleSDDRescue,
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

// RoleBinding is a oneOf: exactly one of Preset or CustomAgent is set.
// A bare TOML string ("reasoning") decodes as Preset (see UnmarshalTOML).
type RoleBinding struct {
	Preset      string `toml:"preset,omitempty"`
	CustomAgent string `toml:"custom_agent,omitempty"`
}

// UnmarshalTOML accepts a bare string as Preset, or a table with
// preset/custom_agent. This preserves the pre-custom-agents TOML shape.
func (b *RoleBinding) UnmarshalTOML(v any) error {
	switch raw := v.(type) {
	case string:
		b.Preset = raw
		return nil
	default:
		// Use the standard struct decoder by re-marshalling/unmarshalling.
		data, err := toml.Marshal(map[string]any{"__rb": v})
		if err != nil {
			return err
		}
		var wrap struct {
			RB RoleBinding `toml:"__rb"`
		}
		if err := toml.Unmarshal(data, &wrap); err != nil {
			return err
		}
		*b = wrap.RB
		return nil
	}
}

type AgentProfile struct {
	Name  string
	Roles map[AgentRole]RoleBinding
}

// CustomAgent is a user-defined, named agent that layers prompt, tool
// denylist, approval mode, context budget, and iteration cap on top of a
// referenced ModelPreset. It can fill a role slot (via RoleBinding) or be
// dispatched ad-hoc by name (agent.run / /agents Run now).
type CustomAgent struct {
	Name          string        `toml:"name"`
	Preset        string        `toml:"preset"`
	SystemPrompt  string        `toml:"system_prompt,omitempty"`
	ToolDenylist  []string      `toml:"tool_denylist,omitempty"`
	ApprovalMode  string        `toml:"approval_mode,omitempty"`
	MaxIterations int           `toml:"max_iterations,omitempty"`
	Context       ContextBudget `toml:"context,omitempty"`
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
	CustomAgent   *CustomAgent // nil unless resolved from a RoleBinding.CustomAgent
}

type Config struct {
	DefaultProfile string
	RemoteAllowed  bool
	Presets        map[string]ModelPreset
	Profiles       map[string]AgentProfile
	CustomAgents   map[string]CustomAgent
	ContextBudgets map[AgentRole]ContextBudget
	LegacyProvider string
	LegacyModel    string
}
