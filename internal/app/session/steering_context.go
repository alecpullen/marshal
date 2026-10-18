package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

type steeringContextKey struct{}

type steeringOperation struct {
	owner         *State
	ref           OperationRef
	kind          OperationKind
	parent        context.Context
	cancel        context.CancelCauseFunc
	requested     atomic.Bool
	interruptOnce sync.Once
}

func OperationFromContext(ctx context.Context) (OperationRef, bool) {
	if ctx == nil {
		return OperationRef{}, false
	}
	op, ok := ctx.Value(steeringContextKey{}).(*steeringOperation)
	if !ok || op == nil {
		return OperationRef{}, false
	}
	return op.ref, true
}

func SteeringRequested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	op, _ := ctx.Value(steeringContextKey{}).(*steeringOperation)
	return op != nil && op.requested.Load()
}

func CheckSteering(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := SteeringTurnContext(ctx).Err(); err != nil {
		return err
	}
	if SteeringRequested(ctx) {
		return ErrSendNow
	}
	return ctx.Err()
}

func IsSteeringCancellation(ctx context.Context, err error) bool {
	if err == nil || ctx == nil || SteeringTurnContext(ctx).Err() != nil {
		return false
	}
	return SteeringRequested(ctx) &&
		(errors.Is(err, ErrSendNow) || errors.Is(err, context.Canceled))
}

func SteeringTurnContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	op, _ := ctx.Value(steeringContextKey{}).(*steeringOperation)
	if op != nil && op.parent != nil {
		return op.parent
	}
	return ctx
}

func BeginSteeringMutation(ctx context.Context) (func(), error) {
	if ctx == nil {
		return func() {}, nil
	}
	op, _ := ctx.Value(steeringContextKey{}).(*steeringOperation)
	if op == nil || op.owner == nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return func() {}, nil
	}
	return op.owner.beginSteeringMutation(ctx)
}
