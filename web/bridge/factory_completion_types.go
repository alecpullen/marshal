package bridge

import (
	"context"
	"time"
)

type FactoryPRLink struct {
	RepoID     string    `json:"repoId"`
	PR         FactoryPR `json:"pr"`
	Origin     string    `json:"origin"` // factory or user
	ObservedAt time.Time `json:"observedAt"`
	Fresh      bool      `json:"fresh"`
}
type FactoryAssessment struct {
	Status      string    `json:"status"` // pending, available, failed, unavailable
	Summary     string    `json:"summary,omitempty"`
	Source      string    `json:"source,omitempty"`
	InputDigest string    `json:"inputDigest"`
	ObservedAt  time.Time `json:"observedAt"`
	// Fresh is derived, never stored: the server recomputes it on every item
	// it serves by comparing InputDigest with the item's current input digest,
	// so a result computed against inputs that have since moved is shown with
	// its summary intact and fresh false. The journalled value is never
	// trusted on read.
	Fresh bool `json:"fresh"`
}
type FactoryCompletionRecord struct {
	ID         string             `json:"id"`
	At         time.Time          `json:"at"`
	Actor      string             `json:"actor"`
	Goal       string             `json:"goal"`
	Criteria   string             `json:"criteria"`
	Assessment *FactoryAssessment `json:"assessment,omitempty"`
	Links      []FactoryPRLink    `json:"links"`
	RunChoice  string             `json:"runChoice,omitempty"` // continue or request_stop
}
type FactoryMergeStep struct {
	RepoID   string `json:"repoId"`
	Number   int    `json:"number"`
	NodeID   string `json:"nodeId"`
	HeadSHA  string `json:"headSha"`
	BaseRef  string `json:"baseRef"`
	Strategy string `json:"strategy"`
	State    string `json:"state"` // unattempted, prepared, dispatched, confirmed, failed, uncertain
	Result   string `json:"result,omitempty"`
}
type FactoryMergeBatch struct {
	ID            string             `json:"id"`
	RequestID     string             `json:"requestId"`
	State         string             `json:"state"` // active, stopped, complete
	CompleteAfter bool               `json:"completeAfter"`
	RunChoice     string             `json:"runChoice,omitempty"`
	Steps         []FactoryMergeStep `json:"steps"`
}
type FactoryCompletion struct {
	Status     string                    `json:"status"` // open or complete; empty means open
	Links      []FactoryPRLink           `json:"links,omitempty"`
	Assessment *FactoryAssessment        `json:"assessment,omitempty"`
	Records    []FactoryCompletionRecord `json:"records,omitempty"`
	Batch      *FactoryMergeBatch        `json:"batch,omitempty"`
	StopState  string                    `json:"stopState,omitempty"` // requested, stopped, failed, unknown
}
type FactoryPRMergeRequest struct {
	Number   int
	NodeID   string
	HeadSHA  string
	BaseRef  string
	Strategy string
}
type FactoryPRMerger interface {
	MergeFactoryPR(context.Context, Repo, FactoryPRMergeRequest, Credential) error
}
type FactoryCompletionAssessor interface {
	AssessFactoryCompletion(context.Context, FactoryItem, []FactoryPRLink) (FactoryAssessment, error)
}
