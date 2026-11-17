package contract

import "time"

// The working drafts' wire types (F6 decision 10): the bridge's durable copy
// of an incomplete manual factory handoff, under
// `/api/factory/working-drafts`.
//
// The declarations are the bridge's own (web/bridge/working_draft_types.go),
// moved here so both clients are generated from one source; the bridge aliases
// each name to the type below. The four request envelopes are declared here
// because the bridge's own are unexported — the generators skip an unexported
// type — and their JSON is the handlers' own, transcribed unchanged.
//
// Content is the raw editor input, never the effective factory configuration.
// A blank override means "follow project settings when converted", and an
// absent checks mode means the same. Empty and temporarily invalid values are
// accepted while editing; conversion is what resolves and validates them.

// WorkingDraftContent is one draft's editor state.
type WorkingDraftContent struct {
	Title                    string `json:"title"`
	Plan                     string `json:"plan"`
	Spec                     string `json:"spec"`
	RepoID                   string `json:"repoId"`
	TargetBranchOverride     string `json:"targetBranchOverride"`
	WorkspaceOverride        string `json:"workspaceOverride"`
	ImplementerOverride      string `json:"implementerOverride"`
	ReviewerOverride         string `json:"reviewerOverride"`
	ReviewerInstructions     string `json:"reviewerInstructions"`
	MaxRoundsOverride        string `json:"maxRoundsOverride"`
	CIPollOverride           string `json:"ciPollOverride"`
	CIWaitOverride           string `json:"ciWaitOverride"`
	ChecksMode               string `json:"checksMode"`
	ExpectedChecksText       string `json:"expectedChecksText"`
	Native                   bool   `json:"native"`
	NativeChosen             bool   `json:"nativeChosen"`
	ImplementerAuto          bool   `json:"implementerAuto"`
	NoRequiredCIAcknowledged bool   `json:"noRequiredCiAcknowledged"`
}

// WorkingDraft is one durable draft. ProjectRoot is the store's identity for
// the draft's project and is never derived from content; the browser cannot
// move a draft between projects.
type WorkingDraft struct {
	ID            string              `json:"id"`
	ProjectRoot   string              `json:"projectRoot"`
	Version       uint64              `json:"version"`
	State         string              `json:"state"`
	Content       WorkingDraftContent `json:"content"`
	UpdatedAt     time.Time           `json:"updatedAt"`
	FactoryItemID string              `json:"factoryItemId,omitempty"`
	Error         string              `json:"error,omitempty"`
}

// WorkingDraftCreateRequest is the POST /api/factory/working-drafts body.
type WorkingDraftCreateRequest struct {
	ProjectRoot string              `json:"projectRoot"`
	RequestID   string              `json:"requestId"`
	Content     WorkingDraftContent `json:"content"`
}

// WorkingDraftSaveRequest is the PUT /api/factory/working-drafts/{id} body:
// the version the editor last read, so a save made against a draft that has
// moved is refused rather than overwriting it.
type WorkingDraftSaveRequest struct {
	RequestID       string              `json:"requestId"`
	ExpectedVersion uint64              `json:"expectedVersion"`
	Content         WorkingDraftContent `json:"content"`
}

// WorkingDraftDeleteRequest is the DELETE /api/factory/working-drafts/{id}
// body: a tombstone is a versioned write like any other.
type WorkingDraftDeleteRequest struct {
	RequestID       string `json:"requestId"`
	ExpectedVersion uint64 `json:"expectedVersion"`
}
