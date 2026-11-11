package bridge

import (
	"encoding/json"
	"time"
)

type FactoryConfig struct {
	RepoID                   string            `json:"repoId"`
	ProjectRoot              string            `json:"projectRoot"`
	HostID                   string            `json:"hostId"`
	TargetBranch             string            `json:"targetBranch"`
	Workspace                string            `json:"workspace"`
	Native                   bool              `json:"native"`
	ImplementerRouting       json.RawMessage   `json:"implementerRouting"`
	ReviewerRouting          json.RawMessage   `json:"reviewerRouting"`
	ReviewerInstructions     string            `json:"reviewerInstructions"`
	ImplementerAuto          bool              `json:"implementerAuto"`
	ExpectedChecks           []FactoryCheckKey `json:"expectedChecks"`
	NoRequiredCIAcknowledged bool              `json:"noRequiredCiAcknowledged"`
	MaxRounds                int               `json:"maxRounds"`
	CIPollSeconds            int               `json:"ciPollSeconds"`
	CIWaitSeconds            int               `json:"ciWaitSeconds"`
}

type FactoryCreate struct {
	RequestID string        `json:"requestId"`
	Title     string        `json:"title"`
	Plan      string        `json:"plan"`
	Spec      string        `json:"spec"`
	Config    FactoryConfig `json:"config"`
}

type FactoryApproval struct {
	Revision   uint64        `json:"revision"`
	Plan       string        `json:"plan"`
	Spec       string        `json:"spec"`
	PlanHash   string        `json:"planHash"`
	Config     FactoryConfig `json:"config"`
	ApprovedAt time.Time     `json:"approvedAt"`
}

type FactoryGrant struct {
	ID               string    `json:"id"`
	Reason           string    `json:"reason"`
	AdditionalRounds int       `json:"additionalRounds"`
	At               time.Time `json:"at"`
}

type FactoryWorker struct {
	AgentID       string `json:"agentId"`
	SessionID     string `json:"sessionId"`
	Role          string `json:"role"`
	SpawnEffectID string `json:"spawnEffectId"`
	Parked        bool   `json:"parked"`
}

type FactoryPR struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	NodeID  string `json:"nodeId"`
	HeadRef string `json:"headRef"`
	BaseRef string `json:"baseRef"`
	HeadSHA string `json:"headSha"`
	BaseSHA string `json:"baseSha"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
}

type FactoryCheckKey struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	AppID int64  `json:"appId"`
}

type FactoryCheck struct {
	Key        FactoryCheckKey `json:"key"`
	ID         string          `json:"id"`
	SHA        string          `json:"sha"`
	Status     string          `json:"status"`
	Conclusion string          `json:"conclusion"`
	URL        string          `json:"url"`
	LogTail    string          `json:"logTail"`
	ObservedAt time.Time       `json:"observedAt"`
}

type FactoryCI struct {
	HeadSHA    string         `json:"headSha"`
	Checks     []FactoryCheck `json:"checks"`
	Complete   bool           `json:"complete"`
	ObservedAt time.Time      `json:"observedAt"`
}

type FactoryReport struct {
	OperationID      string          `json:"operationId"`
	Outcome          string          `json:"outcome"`
	Summary          string          `json:"summary"`
	Question         string          `json:"question"`
	Reason           string          `json:"reason"`
	BaseSHA          string          `json:"baseSha"`
	HeadSHA          string          `json:"headSha"`
	InputsAccessible bool            `json:"inputsAccessible"`
	Findings         []ReviewFinding `json:"findings"`
}

type FactoryRound struct {
	Number            int            `json:"number"`
	ApprovalRevision  uint64         `json:"approvalRevision"`
	ImplementEffectID string         `json:"implementEffectId"`
	BaseSHA           string         `json:"baseSha"`
	HeadSHA           string         `json:"headSha"`
	TreeSHA           string         `json:"treeSha"`
	CI                *FactoryCI     `json:"ci"`
	Implementation    *FactoryReport `json:"implementation"`
	Review            *FactoryReport `json:"review"`
	StartedAt         time.Time      `json:"startedAt"`
	FinishedAt        time.Time      `json:"finishedAt"`
}

type FactoryEffect struct {
	// Outcome retains accepted stop reasons and confirmed adapter evidence.
	Outcome          *FactoryOutcome `json:"outcome"`
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	Status           string          `json:"status"`
	WorkerRole       string          `json:"workerRole"`
	Round            int             `json:"round"`
	ApprovalRevision uint64          `json:"approvalRevision"`
	Worker           *FactoryWorker  `json:"worker"`
	ExpectedHead     string          `json:"expectedHead"`
	ExpectedBase     string          `json:"expectedBase"`
	CreatedAt        time.Time       `json:"createdAt"`
}

type FactoryOutcome struct {
	EffectID   string         `json:"effectId"`
	StopReason string         `json:"stopReason"`
	Error      string         `json:"error"`
	HeadSHA    string         `json:"headSha"`
	TreeSHA    string         `json:"treeSha"`
	BaseSHA    string         `json:"baseSha"`
	Worker     *FactoryWorker `json:"worker"`
	PR         *FactoryPR     `json:"pr"`
	Report     *FactoryReport `json:"report"`
	Confirmed  bool           `json:"confirmed"`
}

type FactoryObservation struct {
	Workers         []FactoryWorker `json:"workers"`
	RuntimeKnown    bool            `json:"runtimeKnown"`
	TurnIdle        bool            `json:"turnIdle"`
	ActiveWorkKnown bool            `json:"activeWorkKnown"`
	WorkIdle        bool            `json:"workIdle"`
	HeadSHA         string          `json:"headSha"`
	TreeSHA         string          `json:"treeSha"`
	BaseSHA         string          `json:"baseSha"`
	RemoteHead      string          `json:"remoteHead"`
	Dirty           bool            `json:"dirty"`
	PR              *FactoryPR      `json:"pr"`
	CI              *FactoryCI      `json:"ci"`
	Error           string          `json:"error"`
	ObservedAt      time.Time       `json:"observedAt"`
}

type FactoryDecision struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Question  string    `json:"question"`
	Reason    string    `json:"reason"`
	EffectID  string    `json:"effectId"`
	CreatedAt time.Time `json:"createdAt"`
}

type FactoryEvent struct {
	Sequence uint64    `json:"sequence"`
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Message  string    `json:"message"`
	EffectID string    `json:"effectId"`
}

type FactoryItem struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	State       string            `json:"state"`
	Stage       string            `json:"stage"`
	RepoID      string            `json:"repoId"`
	ProjectRoot string            `json:"projectRoot"`
	HostID      string            `json:"hostId"`
	Branch      string            `json:"branch"`
	BaseSHA     string            `json:"baseSha"`
	Version     uint64            `json:"version"`
	Draft       FactoryCreate     `json:"draft"`
	Approvals   []FactoryApproval `json:"approvals"`
	Grants      []FactoryGrant    `json:"grants"`
	Rounds      []FactoryRound    `json:"rounds"`
	Workers     []FactoryWorker   `json:"workers"`
	PRs         []FactoryPR       `json:"prs"`
	Effects     []FactoryEffect   `json:"effects"`
	Decision    *FactoryDecision  `json:"decision"`
	History     []FactoryEvent    `json:"history"`
	CreatedAt   time.Time         `json:"createdAt"`
	ApprovedAt  time.Time         `json:"approvedAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

type FactoryCommand struct {
	RequestID                    string          `json:"requestId"`
	Kind                         string          `json:"kind"`
	DecisionID                   string          `json:"decisionId"`
	Answer                       string          `json:"answer"`
	Reason                       string          `json:"reason"`
	ExpectedVersion              uint64          `json:"expectedVersion"`
	AdditionalRounds             int             `json:"additionalRounds"`
	Replacement                  *FactoryCreate  `json:"replacement"`
	ImplementerRouting           json.RawMessage `json:"implementerRouting"`
	ReviewerRouting              json.RawMessage `json:"reviewerRouting"`
	ConfirmPreviousWorkerStopped bool            `json:"confirmPreviousWorkerStopped"`
}

// FactoryPlanHash binds exact plan and spec bytes, including whitespace.
func FactoryPlanHash(plan, spec string) string { return factoryPlanHash(plan, spec) }
