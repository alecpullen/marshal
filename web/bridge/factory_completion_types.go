package bridge

import (
	"context"

	"marshal/web/bridge/contract"
)

// The completion types are contract types (F6 decision 1); the bridge aliases
// them here so its code and the journal's bytes are unchanged.
type (
	// FactoryPRLink is contract.FactoryPRLink.
	FactoryPRLink = contract.FactoryPRLink
	// FactoryAssessment is contract.FactoryAssessment.
	FactoryAssessment = contract.FactoryAssessment
	// FactoryCompletionRecord is contract.FactoryCompletionRecord.
	FactoryCompletionRecord = contract.FactoryCompletionRecord
	// FactoryMergeStep is contract.FactoryMergeStep.
	FactoryMergeStep = contract.FactoryMergeStep
	// FactoryMergeBatch is contract.FactoryMergeBatch.
	FactoryMergeBatch = contract.FactoryMergeBatch
	// FactoryCompletion is contract.FactoryCompletion.
	FactoryCompletion = contract.FactoryCompletion
)

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
