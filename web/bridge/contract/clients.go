package contract

// ApprovalAuthority is how much approval power a factory grant carries.
type ApprovalAuthority string

const (
	// ApprovalAuthorityNone may queue work but not approve it.
	ApprovalAuthorityNone ApprovalAuthority = "none"
	// ApprovalAuthorityUserDirected approves only what a person directed.
	ApprovalAuthorityUserDirected ApprovalAuthority = "user_directed"
	// ApprovalAuthorityStanding approves within its capabilities.
	ApprovalAuthorityStanding ApprovalAuthority = "standing"
)

// MCPClient is one registered MCP client: GET /api/clients' answer
// (web/bridge/client.go). Its token is never here; it is shown once, when the
// client is created.
type MCPClient struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Autonomous    bool   `json:"autonomous,omitempty"`
	MaxConcurrent int    `json:"maxConcurrent,omitempty"`
	MaxPerDay     int    `json:"maxPerDay,omitempty"`
	// AllowedRepos restricts which registered repos the client may target.
	// Empty means every one.
	AllowedRepos []string `json:"allowedRepos,omitempty"`
	OwnerID      string   `json:"ownerId"`
	CreatedAt    string   `json:"createdAt"`
}

// CreateClientRequest is the body POST /api/clients takes.
type CreateClientRequest struct {
	Name          string   `json:"name"`
	Autonomous    bool     `json:"autonomous,omitempty"`
	MaxConcurrent int      `json:"maxConcurrent,omitempty"`
	MaxPerDay     int      `json:"maxPerDay,omitempty"`
	AllowedRepos  []string `json:"allowedRepos,omitempty"`
}

// CreateClientResult is what creating a client answers. It is the one place
// the token appears.
type CreateClientResult struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Token      string `json:"token"`
	Autonomous bool   `json:"autonomous"`
}

// MCPFactoryProjectGrant is one client's factory authority over one project.
// Every capability is independent of the others, and the approval authority
// is independent of them all.
type MCPFactoryProjectGrant struct {
	ProjectID         string            `json:"projectId"`
	Read              bool              `json:"read"`
	CreateDraft       bool              `json:"createDraft"`
	EditDraft         bool              `json:"editDraft"`
	AnswerQuestion    bool              `json:"answerQuestion"`
	ApproveAndQueue   bool              `json:"approveAndQueue"`
	ApprovalAuthority ApprovalAuthority `json:"approvalAuthority"`
	// AssessPlanChanges and ApproveRoutineRevision are optional so a grant
	// written before they existed round trips unchanged: absent grants
	// nothing.
	AssessPlanChanges      bool `json:"assessPlanChanges,omitempty"`
	ApproveRoutineRevision bool `json:"approveRoutineRevision,omitempty"`
}

// FactoryGrantState is one (client, project) pair's stored grant and the
// opaque version a write must name.
type FactoryGrantState struct {
	ClientID  string                 `json:"clientId"`
	ProjectID string                 `json:"projectId"`
	Grant     MCPFactoryProjectGrant `json:"grant"`
	Version   string                 `json:"version"`
}

// PutGrantRequest is the body PUT
// /api/clients/{clientId}/factory-grants/{projectId} takes: the whole grant
// and the version it was edited from.
type PutGrantRequest struct {
	Grant   MCPFactoryProjectGrant `json:"grant"`
	Version string                 `json:"version"`
}
