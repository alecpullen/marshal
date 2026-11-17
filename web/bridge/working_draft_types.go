package bridge

import (
	"errors"

	"marshal/web/bridge/contract"
)

// Working drafts are the bridge's durable copy of an incomplete manual factory
// handoff. They are not factory items: saving one performs no factory
// validation, starts no run and inherits no project defaults.

// Content is the raw editor input, never the effective factory configuration.
// A blank override means "follow project settings when converted", and an
// absent checks mode means the same. Empty and temporarily invalid values are
// accepted while editing; conversion is what resolves and validates them.

// The wire shapes are contract/drafts.go's (F6 decision 1), aliased here so no
// bridge code changed with the move.
type (
	// WorkingDraftContent is one draft's editor state.
	WorkingDraftContent = contract.WorkingDraftContent
	// WorkingDraft is one durable draft.
	WorkingDraft = contract.WorkingDraft
	// workingDraftCreateBody is the POST /api/factory/working-drafts body.
	workingDraftCreateBody = contract.WorkingDraftCreateRequest
	// workingDraftSaveBody is the PUT .../working-drafts/{id} body.
	workingDraftSaveBody = contract.WorkingDraftSaveRequest
	// workingDraftDeleteBody is the DELETE .../working-drafts/{id} body.
	workingDraftDeleteBody = contract.WorkingDraftDeleteRequest
)

// workingDraftConvertBody is the POST .../working-drafts/{id}/convert body. It
// names a factory draft and the settings version it was reviewed against, so
// the conversion the bridge performs is the one the person approved.
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
