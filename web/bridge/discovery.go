package bridge

import (
	"errors"
	"fmt"
	"net/http"
)

// Discovery is the bridge's read-only view of an existing project: what a
// registered forge and credential can access, what a linked checkout reveals,
// and what is missing. It never writes, clones, or starts anything.

// DiscoveryCapabilityState is the outcome of one read-only capability probe.
// The set is frozen at five values; the studio client switches on these exact
// strings.
type DiscoveryCapabilityState string

const (
	DiscoveryCapabilitySupported              DiscoveryCapabilityState = "supported"
	DiscoveryCapabilityUnconfigured           DiscoveryCapabilityState = "unconfigured"
	DiscoveryCapabilityInsufficientPermission DiscoveryCapabilityState = "insufficient_permission"
	DiscoveryCapabilityTemporarilyUnavailable DiscoveryCapabilityState = "temporarily_unavailable"
	DiscoveryCapabilityUnsupported            DiscoveryCapabilityState = "unsupported"
)

// DiscoveryCapability is one entry of the report's capability list.
type DiscoveryCapability struct {
	Key    string                   `json:"key"`
	State  DiscoveryCapabilityState `json:"state"`
	Detail string                   `json:"detail,omitempty"`
}

// DiscoveryFact is something detected in the checkout. Origin distinguishes a
// detected fact from a recommendation.
type DiscoveryFact struct {
	Origin   string `json:"origin"`
	Section  string `json:"section"`
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	Source   string `json:"source"`
	Revision string `json:"revision,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// DiscoveryRecommendation is an advisory setup option. It is never applied by
// discovery.
type DiscoveryRecommendation struct {
	Origin  string `json:"origin"`
	Section string `json:"section"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Reason  string `json:"reason"`
}

// DiscoveryGap explains a capability that is not supported, so the UI can turn
// it into an actionable next step.
type DiscoveryGap struct {
	Key     string                   `json:"key"`
	Section string                   `json:"section"`
	State   DiscoveryCapabilityState `json:"state"`
	Detail  string                   `json:"detail"`
}

// DiscoveryWorktree is the locally observable Git state of a linked checkout.
// Dirty is a pointer: nil means not determined, which differs from a known
// clean checkout.
type DiscoveryWorktree struct {
	Path       string   `json:"path"`
	Branch     string   `json:"branch,omitempty"`
	Head       string   `json:"head,omitempty"`
	Dirty      *bool    `json:"dirty"`
	DirtyFiles []string `json:"dirtyFiles,omitempty"`
	Detail     string   `json:"detail,omitempty"`
}

// DiscoveryReport is the single JSON document the discovery endpoint returns.
// Status is "ok", "degraded", or "unavailable"; the report slices are always
// non-nil so the wire shape stays stable.
type DiscoveryReport struct {
	RepoID       string                    `json:"repoId"`
	Forge        string                    `json:"forge"`
	Status       string                    `json:"status"`
	Error        string                    `json:"error,omitempty"`
	Revision     string                    `json:"revision,omitempty"`
	Branch       string                    `json:"branch,omitempty"`
	Worktree     *DiscoveryWorktree        `json:"worktree,omitempty"`
	Capabilities []DiscoveryCapability     `json:"capabilities"`
	Facts        []DiscoveryFact           `json:"facts"`
	Recommended  []DiscoveryRecommendation `json:"recommended"`
	Gaps         []DiscoveryGap            `json:"gaps"`
}

// DiscoveryRepositoryMetadata is the normalised repository description a forge
// probe returns.
type DiscoveryRepositoryMetadata struct {
	DefaultBranch string
	Visibility    string
	Description   string
	Topics        []string
	Language      string
	SizeBytes     int64
}

// DiscoveryBranch is one branch and, when the forge reports it, its head.
type DiscoveryBranch struct {
	Name string
	SHA  string
}

// DiscoveryPullRequest is an open pull/merge request, normalised across forges.
type DiscoveryPullRequest struct {
	Number  int
	State   string
	Title   string
	HeadSHA string
}

// DiscoveryCheck is one CI check/workflow run, normalised across forges.
type DiscoveryCheck struct {
	Name       string
	Conclusion string
	Status     string
	URL        string
	AppID      int64
	Context    string
	JobID      int64
}

// discoveryCapabilityOrder is the frozen capability list: every report carries
// all ten keys, always in this order. Later tasks build the report from it, so
// it must not be reordered or extended.
var discoveryCapabilityOrder = []string{
	"repository_metadata",
	"branches",
	"pull_requests",
	"ci_checks",
	"ci_logs",
	"checkout_instructions",
	"checkout_docs",
	"checkout_tooling",
	"checkout_environment",
	"checkout_ci_definition",
}

// discoveryCapabilityKeys returns a copy of the frozen capability order, so a
// caller cannot mutate the contract for the whole process.
func discoveryCapabilityKeys() []string {
	return append([]string(nil), discoveryCapabilityOrder...)
}

// Discovery probes report these sentinels when the state is known before any
// HTTP exchange happens: no usable credential, or a resource the adapter does
// not implement for this forge.
var (
	errDiscoveryUnconfigured = errors.New("bridge: discovery capability is unconfigured")
	errDiscoveryUnsupported  = errors.New("bridge: discovery capability is unsupported")
)

// discoveryCapabilityState classifies one capability probe outcome.
//
// The mapping is contextual rather than a blanket HTTP table: a 404 can mean
// "this instance does not serve the endpoint" or "this credential cannot see
// the resource", and only a prior successful metadata read tells them apart
// (metadataReadable). The sentinels are honoured before any HTTP
// classification; a non-HTTP failure is treated as a transport failure.
//
// Details are meant for the report's gaps list. They describe the state and
// never echo request headers, the credential, or the forge's raw error body:
// those end up in logs and on screen.
func discoveryCapabilityState(err error, metadataReadable bool) (DiscoveryCapabilityState, string) {
	if err == nil {
		return DiscoveryCapabilitySupported, ""
	}
	if errors.Is(err, errDiscoveryUnconfigured) {
		return DiscoveryCapabilityUnconfigured, "no usable credential is configured for this forge"
	}
	if errors.Is(err, errDiscoveryUnsupported) {
		return DiscoveryCapabilityUnsupported, "this forge does not expose the capability"
	}

	var apiErr *forgeAPIError
	if !errors.As(err, &apiErr) {
		// Transport failure (DNS, connection refused, timeout) or an
		// undecodable response: it says nothing durable about permission or
		// support, so the caller may retry later.
		return DiscoveryCapabilityTemporarilyUnavailable, "the forge could not be reached; retry later"
	}

	switch code := apiErr.statusCode; {
	case code == http.StatusUnauthorized:
		return DiscoveryCapabilityUnconfigured,
			fmt.Sprintf("the forge rejected the credential (HTTP %d); configure a valid token", code)
	case code == http.StatusForbidden && apiErr.retryAfter != "":
		return DiscoveryCapabilityTemporarilyUnavailable,
			fmt.Sprintf("the forge asked to retry later (HTTP %d)", code)
	case code == http.StatusForbidden:
		return DiscoveryCapabilityInsufficientPermission,
			fmt.Sprintf("the credential lacks permission to read this resource (HTTP %d)", code)
	case code == http.StatusNotFound && metadataReadable:
		return DiscoveryCapabilityUnsupported,
			fmt.Sprintf("the forge does not expose this endpoint (HTTP %d)", code)
	case code == http.StatusNotFound:
		return DiscoveryCapabilityInsufficientPermission,
			fmt.Sprintf("the credential cannot see this resource (HTTP %d) and repository metadata is not readable with it", code)
	case code == http.StatusTooManyRequests:
		return DiscoveryCapabilityTemporarilyUnavailable,
			fmt.Sprintf("the forge is rate limiting requests (HTTP %d); retry later", code)
	case code >= http.StatusInternalServerError:
		return DiscoveryCapabilityTemporarilyUnavailable,
			fmt.Sprintf("the forge is temporarily unavailable (HTTP %d); retry later", code)
	case code >= http.StatusBadRequest:
		// An unexpected client error: this instance does not serve the request
		// the adapter makes, and retrying it unchanged will not help.
		return DiscoveryCapabilityUnsupported,
			fmt.Sprintf("the forge rejected the request as this adapter sends it (HTTP %d)", code)
	default:
		return DiscoveryCapabilityTemporarilyUnavailable,
			fmt.Sprintf("the forge returned an unexpected status (HTTP %d); retry later", code)
	}
}
