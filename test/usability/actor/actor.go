package actor

import (
	"context"

	"marshal/test/usability/screen"
)

// ActionType values for Action.Type.
const (
	ActionType   = "type"
	ActionKey    = "key"
	ActionPaste  = "paste"
	ActionResize = "resize"
	ActionDone   = "done"
	ActionNoOp   = "noop"
)

// Size is a terminal viewport in character cells.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Action is one input decision from an actor.
type Action struct {
	Type    string // "type", "key", "paste", "resize", "done", "noop"
	Text    string // for "type" and "paste"
	Key     string // for "key"
	Width   int    // for "resize"
	Height  int    // for "resize"
	Success bool   // for "done"
	Notes   string // for "done" or debugging
}

// Actor decides the next input given the current screen.
type Actor interface {
	Act(ctx context.Context, s screen.Screen) (Action, error)
}
