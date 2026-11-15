package bridge

import (
	"errors"
	"time"
)

// Working drafts are the bridge's durable copy of an incomplete manual factory
// handoff. They are not factory items: saving one performs no factory
// validation, starts no run and inherits no project defaults.
//
// Content is the raw editor input, never the effective factory configuration.
// A blank override means "follow project settings when converted", and an
// absent checks mode means the same. Empty and temporarily invalid values are
// accepted while editing; conversion is what resolves and validates them.
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

// Wire requests for the working-draft API. The JSON names are the contract with
// the Studio client: it sends and reads exactly these envelopes.
type workingDraftCreateBody struct {
	ProjectRoot string              `json:"projectRoot"`
	RequestID   string              `json:"requestId"`
	Content     WorkingDraftContent `json:"content"`
}

type workingDraftSaveBody struct {
	RequestID       string              `json:"requestId"`
	ExpectedVersion uint64              `json:"expectedVersion"`
	Content         WorkingDraftContent `json:"content"`
}

type workingDraftDeleteBody struct {
	RequestID       string `json:"requestId"`
	ExpectedVersion uint64 `json:"expectedVersion"`
}

type workingDraftConvertBody struct {
	RequestID       string         `json:"requestId"`
	ExpectedVersion uint64         `json:"expectedVersion"`
	FactoryRequest  *FactoryCreate `json:"factoryRequest"`
	SettingsVersion string         `json:"settingsVersion"`
}

// Working draft lifecycle states. A draft is only ever edited while editing;
// converting and converted are read-only, and deleted is a tombstone that List
// excludes and no mutation resurrects.
const (
	workingDraftEditing    = "editing"
	workingDraftConverting = "converting"
	workingDraftConverted  = "converted"
	workingDraftDeleted    = "deleted"
)

// Working draft checks modes, as stored in WorkingDraftContent.ChecksMode:
// inherit, none or custom. An unset mode is treated as inherit.
const (
	workingDraftChecksInherit = "inherit"
	workingDraftChecksNone    = "none"
	workingDraftChecksCustom  = "custom"
)

var (
	ErrWorkingDraftConflict    = errors.New("working draft conflict")
	ErrWorkingDraftNotFound    = errors.New("working draft not found")
	ErrWorkingDraftUnavailable = errors.New("working draft store unavailable")
	// ErrWorkingDraftUncertain means the outcome of a conversion cannot be
	// established; it is never reported as saved, approved or failed.
	ErrWorkingDraftUncertain = errors.New("working draft conversion outcome uncertain")
)
