package actor

import (
	"context"

	"marshal/test/usability/screen"
)

// ActionType values for Action.Type.
const (
	ActionType = "type"
	ActionKey  = "key"
	ActionDone = "done"
	ActionNoOp = "noop"
)

// Action is one input decision from an actor.
type Action struct {
	Type    string // "type", "key", "done", "noop"
	Text    string // for "type"
	Key     string // for "key"
	Success bool   // for "done"
	Notes   string // for "done" or debugging
}

// Actor decides the next input given the current screen.
type Actor interface {
	Act(ctx context.Context, s screen.Screen) (Action, error)
}
