package session

// ForceVision records that the user chose to send images on this model
// anyway. It is in-memory and session-scoped: the user is answering a
// question about this image on this model, not editing their setup mid-turn.
func (s *State) ForceVision(providerName, model string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.visionForced == nil {
		s.visionForced = map[string]bool{}
	}
	s.visionForced[visionForcedKey(providerName, model)] = true
}

// VisionForced reports whether the user chose to send images on this model
// anyway, for this session only.
func (s *State) VisionForced(providerName, model string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visionForced[visionForcedKey(providerName, model)]
}

func visionForcedKey(providerName, model string) string {
	return providerName + "/" + model
}
