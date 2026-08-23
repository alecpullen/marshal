package app

import (
	"context"
	"sync"

	"marshal/internal/knowledge"
)

// knowledgeExtractionTurnInterval is how often (in completed turns) the
// periodic knowledge pass fires (AI-15). Compactions fire it immediately.
const knowledgeExtractionTurnInterval = 5

// knowledgeScheduler runs knowledge.Extract periodically so a crash loses at
// most a few turns of learnings instead of the whole session. At most one
// extraction is in flight; triggers arriving during a run are dropped (the
// next interval catches up).
type knowledgeScheduler struct {
	mu       sync.Mutex
	turns    int
	inFlight bool
	input    func() knowledge.ExtractInput
	run      func(ctx context.Context, in knowledge.ExtractInput)
}

func (s *knowledgeScheduler) OnTurn() {
	s.mu.Lock()
	s.turns++
	due := s.turns%knowledgeExtractionTurnInterval == 0
	s.mu.Unlock()
	if due {
		s.trigger()
	}
}

func (s *knowledgeScheduler) OnCompaction() { s.trigger() }

func (s *knowledgeScheduler) trigger() {
	s.mu.Lock()
	if s.inFlight {
		s.mu.Unlock()
		return
	}
	s.inFlight = true
	s.mu.Unlock()
	in := s.input()
	go func() {
		defer func() {
			s.mu.Lock()
			s.inFlight = false
			s.mu.Unlock()
		}()
		s.run(context.Background(), in)
	}()
}
