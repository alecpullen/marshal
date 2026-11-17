package service

import "context"

// ForceVision records that the user chose to send images on this session's
// model anyway, so the route's refusal yields to the next prompt.
func (s *Session) ForceVision(ctx context.Context, provider, model string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.state.ForceVision(provider, model)
	return nil
}
