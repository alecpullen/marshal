package rpc

import (
	"context"

	"marshal/internal/acpwire"
)

// ForceVision records that the user chose to send images on this route anyway.
// It covers the session only, and the next prompt's images are not refused.
func (s *Session) ForceVision(ctx context.Context, provider, model string) error {
	params := acpwire.VisionForceParams{SessionID: s.id, Provider: provider, Model: model}
	return s.conn.Call(ctx, acpwire.MethodVisionForce, params, nil)
}
