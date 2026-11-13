package bridge

import "strings"

// This file freezes the preview-environment report contract: the frozen
// capability keys, the four wire types, and this surface's own fix rules.
//
// The vocabulary is the discovery contract's, reused unchanged: the same five
// DiscoveryCapabilityState values, the same DiscoveryCapability and
// DiscoveryGap shapes, and the same classifier and severity order. Only the
// key list and the fix text are this surface's own, because the discovery
// fixes reason about a checkout and about declaring a forge, neither of which
// is the remedy here.

// PreviewReport is the single JSON document the preview-environments endpoint
// returns. The four arrays are always non-nil in the Go value, so they
// serialise as [] and never as null.
type PreviewReport struct {
	RepoID string `json:"repoId"`
	Forge  string `json:"forge"`
	Status string `json:"status"`
	// Error is present only when Status is "unavailable".
	Error        string                `json:"error,omitempty"`
	Ref          string                `json:"ref"`
	SHA          string                `json:"sha"`
	Capabilities []DiscoveryCapability `json:"capabilities"`
	Environments []PreviewEnvironment  `json:"environments"`
	Deployments  []PreviewDeployment   `json:"deployments"`
	Gaps         []DiscoveryGap        `json:"gaps"`
}

// PreviewFilter is the revision scope a deployments read is filtered by. Ref
// and SHA are applied conjunctively when both are set; an empty dimension is
// omitted from the request rather than sent as an empty value.
type PreviewFilter struct {
	Ref string
	SHA string
}

// PreviewEnvironment is one configured deployment environment, as the forge
// reports it. Every field is a provider value.
type PreviewEnvironment struct {
	Name                string `json:"name"`
	ID                  string `json:"id"`
	HTMLURL             string `json:"htmlUrl"`
	BranchPolicy        string `json:"branchPolicy"`
	ProtectionRuleCount int    `json:"protectionRuleCount"`
}

// PreviewDeployment is one deployment record with its newest status folded in.
// Its own fields come from the deployment; State, EnvironmentURL, TargetURL,
// LogURL and Description come from the newest status only.
type PreviewDeployment struct {
	ID                  string `json:"id"`
	Environment         string `json:"environment"`
	OriginalEnvironment string `json:"originalEnvironment"`
	Ref                 string `json:"ref"`
	SHA                 string `json:"sha"`
	Task                string `json:"task"`
	State               string `json:"state"`
	EnvironmentURL      string `json:"environmentUrl"`
	TargetURL           string `json:"targetUrl"`
	LogURL              string `json:"logUrl"`
	Description         string `json:"description"`
	Transient           bool   `json:"transient"`
	Production          bool   `json:"production"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
}

// PreviewDeploymentStatus is one deployment status record, as the forge
// reports it. It is never normalised into Marshal's own vocabulary.
type PreviewDeploymentStatus struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	EnvironmentURL string `json:"environmentUrl"`
	TargetURL      string `json:"targetUrl"`
	LogURL         string `json:"logUrl"`
	Description    string `json:"description"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// Gap sections, as they appear in a preview gap's Section.
const (
	previewSectionEnvironments = "environments"
	previewSectionDeployments  = "deployments"
	previewSectionStatuses     = "statuses"
)

// previewNamedDeploymentLimit is how many deployments the report names: the
// newest deployment of each environment, capped at this many environments.
const previewNamedDeploymentLimit = 20

// previewCapabilityOrder is the frozen capability list for this surface. Every
// report carries all three keys, always in this order, and gaps follow it. It
// is deliberately separate from discoveryCapabilityOrder so the discovery
// report's ten keys and their order cannot be touched.
var previewCapabilityOrder = []string{
	"preview_environments",
	"preview_deployments",
	"preview_statuses",
}

// previewCapabilityKeys returns a copy of the frozen capability order, so a
// caller cannot mutate the contract for the whole process.
func previewCapabilityKeys() []string {
	return append([]string(nil), previewCapabilityOrder...)
}

// previewFix names what would resolve a preview capability in this state. It
// is appended to a gap's detail, never to a supported capability's.
// forgeDeclared distinguishes a repository that names a forge (whose instance
// answered, or did not serve the resource) from the Git-only path, which needs
// a forge declared before any instance can be asked.
func previewFix(state DiscoveryCapabilityState, forgeDeclared bool) string {
	switch state {
	case DiscoveryCapabilityUnconfigured:
		return "this repository needs a personal access token credential to read preview environments; register or repair one and retry"
	case DiscoveryCapabilityInsufficientPermission:
		return "grant the credential read access to the repository's deployments and environments, and retry"
	case DiscoveryCapabilityTemporarilyUnavailable:
		return "the forge could not answer right now; retry later"
	case DiscoveryCapabilityUnsupported:
		if !forgeDeclared {
			return "declare github or gitea for this repository"
		}
		// An instance fact, not an adapter gap: the adapter exists and the
		// instance answered. Never promise that adapter support would help.
		return "this forge instance does not expose this resource; no adapter can read what the instance does not serve"
	}
	return ""
}

// previewGapDetail joins a cause with the fix that would remove it.
func previewGapDetail(detail string, state DiscoveryCapabilityState, forgeDeclared bool) string {
	fix := previewFix(state, forgeDeclared)
	switch {
	case fix == "":
		return detail
	case strings.Contains(detail, fix):
		return detail
	case detail == "":
		return fix
	}
	return detail + "; " + fix
}
