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

	// RoleSDDPlanAuthor is an internal child role for the SDD plan-authoring
	// runner. It is deliberately excluded from AllRoles: it is not a
	// user-editable role and must not appear in /agents settings. It
	// resolves through the existing implementer fallback when no dedicated
	// binding exists.
	RoleSDDPlanAuthor AgentRole = "sdd_plan_author"

	// RoleEmbedding selects the text-embedding provider+model. It is
	// deliberately excluded from AllRoles: embedding is not a chat role, so
	// onboarding/settings that enumerate AllRoles must not list it. Resolved
	// via StaticRouter.ResolveEmbedding, not ResolveRole.
	RoleEmbedding AgentRole = "embedding"

	// RoleFast is a fallback source, not a dispatchable role: the low-stakes
	// roles in FastRoles resolve through it before falling back to
	// RoleImplementer. Excluded from AllRoles for the same reason
	// RoleEmbedding is — nothing dispatches it directly.
	RoleFast AgentRole = "fast"
)

// FastRoles are the low-stakes roles that resolve through RoleFast before
// falling back to RoleImplementer. Setting a profile's fast model makes
// these cheaper without forcing the user to bind each one.
var FastRoles = map[AgentRole]bool{
	RoleRouter:     true,
	RoleTitle:      true,
	RoleSummarizer: true,
	RoleRepoScout:  true,
}

// EffectiveBinding resolves role through the profile's inheritance chain:
// the role's own binding, then RoleFast for roles in FastRoles, then
// RoleImplementer. from reports which rung supplied the binding, which is
// what the settings screen renders as provenance. ok is false when no rung
// is bound.
//
// RoleEmbedding is exempt: it has no fallback at all, because a chat model
// cannot produce vectors.
func (p AgentProfile) EffectiveBinding(role AgentRole) (RoleBinding, AgentRole, bool) {
	bound := func(r AgentRole) (RoleBinding, bool) {
		b, ok := p.Roles[r]
		if !ok || (b.Preset == "" && b.CustomAgent == "") {
			return RoleBinding{}, false
		}
		return b, true
	}
	if b, ok := bound(role); ok {
		return b, role, true
	}
	if role == RoleEmbedding {
		return RoleBinding{}, "", false
	}
	if FastRoles[role] {
		if b, ok := bound(RoleFast); ok {
			return b, RoleFast, true
		}
	}
	if role != RoleImplementer {
		if b, ok := bound(RoleImplementer); ok {
			return b, RoleImplementer, true
		}
	}
	return RoleBinding{}, "", false
}

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

// ModelPreset is a configured provider/model pair plus optional overrides.
// Preset map keys are always "<provider>/<model>"; Name stores the key.
type ModelPreset struct {
	Name            string `toml:"-"`
	Provider        string `toml:"provider"`
	Model           string `toml:"model"`
	ContextWindow   int    `toml:"context_window"`
	MaxOutputTokens int    `toml:"max_output_tokens"`
	ToolCalling     string `toml:"tool_calling"`
	LocalOnly       bool   `toml:"local_only"`
	// Thinking controls reasoning effort: "" = provider default (nothing is
	// sent on the wire), "off" disables thinking where the provider allows
	// it, "low"/"medium"/"high" map to reasoning_effort on OpenAI-compatible
	// endpoints and to budget_tokens on Anthropic. Ignored when the provider
	// reports no reasoning capability.
	Thinking string `toml:"thinking,omitempty"`
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
	CustomAgent   *CustomAgent // nil unless resolved from a RoleBinding.CustomAgent
	// Window is the resolved model context window for this route (from
	// preset or catalog). Set per-turn by resolveRoute so the per-turn
	// threshold derivation tracks the model actually in use. 0 means
	// unknown.
	Window int
	// MaxOutput is the resolved max output tokens for this route (from
	// preset or catalog). 0 means unknown.
	MaxOutput int
}

type Config struct {
	DefaultProfile   string
	RemoteAllowed    bool
	Presets          map[string]ModelPreset
	Profiles         map[string]AgentProfile
	CustomAgents     map[string]CustomAgent
	ContextBudgets   map[AgentRole]ContextBudget
	ProviderBaseURLs map[string]string // provider name -> base URL for default-preset synthesis
	// EmbeddingPreset names the preset used for embeddings, from
	// [indexing] embedding_preset. Preferred over the legacy per-profile
	// embedding role binding, which ResolveEmbedding still consults as a
	// fallback for configs built without the load-time migration.
	EmbeddingPreset string
}
