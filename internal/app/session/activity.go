package session

import (
	"strconv"

	"marshal/internal/activity"
)

// ActivitySnapshot is a defensive copy of the live ownership state. Its
// narration list is scoped to the active message path; older restored records
// are intentionally absent because phase 1 does not persist ownership.
type ActivitySnapshot struct {
	Narrations         []activity.Narration
	ProgressRevisions  []activity.ProgressRevision
	EvidenceRecords    []EvidenceRecord
	ResponseNarrations []ResponseNarration
	Run                activity.Ref
	Response           activity.Ref
}

type ResponseNarration struct{ ResponseID, NarrationID string }

// NarrationForResponse resolves a completed thinking/tool response without
// relying on timestamps or whichever narration happens to be current now.
func (s ActivitySnapshot) NarrationForResponse(responseID string) (activity.Narration, bool) {
	for _, link := range s.ResponseNarrations {
		if link.ResponseID == responseID {
			for _, n := range s.Narrations {
				if n.ID == link.NarrationID {
					return n, true
				}
			}
		}
	}
	return activity.Narration{}, false
}

func (s *State) nextActivityIDLocked(kind string) string {
	s.activityNextID++
	// ScopeID is process-local and deliberately not a durable/replay identity.
	return kind + "@" + s.ScopeID() + ":" + strconv.FormatUint(s.activityNextID, 10)
}

// BeginActivityRun starts fresh ownership for one invocation. Runtime IDs
// are in-process only and are never persisted or used as message IDs.
func (s *State) BeginActivityRun(boundaryMessageID int64) activity.Ref {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	run := activity.Ref{RunID: s.nextActivityIDLocked("run"), ActorID: "main"}
	s.activityRun, s.activityResponse, s.activityBoundary = run, activity.Ref{}, boundaryMessageID
	// Response idempotency payloads are scoped to the active run. Old response
	// identities cannot be accepted after this boundary, so release their
	// canonical payload bytes here instead of retaining them for the session.
	s.activityProgressResponses = nil
	s.evidenceRecords = nil
	s.evidenceNextAlias = 0
	s.evidenceIssuedCalls = make(map[string]bool)
	return run
}

func (s *State) BeginActivityResponse() activity.Ref {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activityRun.RunID == "" {
		s.activityRun = activity.Ref{RunID: s.nextActivityIDLocked("run"), ActorID: "main"}
		s.activityProgressResponses = nil
	}
	activeNarration := s.activityResponse.NarrationID
	s.activityResponse = s.activityRun
	s.activityResponse.NarrationID = activeNarration
	s.activityResponse.ResponseID = s.nextActivityIDLocked("response")
	if activeNarration != "" {
		s.setActivityResponseNarrationLocked(s.activityResponse.ResponseID, activeNarration)
	}
	return s.activityResponse
}

// BeginActivityCall allocates a unique runtime call identity while retaining
// the provider's call ID when one was supplied.
func (s *State) BeginActivityCall(owner activity.Ref, providerCallID string) activity.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner.CallID = s.nextActivityIDLocked("call")
	owner.ProviderCallID = providerCallID
	return owner
}

// BindActivityFallback binds tool-only output to the current runtime context.
func (s *State) BindActivityFallback(owner activity.Ref) activity.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner.NarrationID != "" {
		return owner
	}
	owner.NarrationID = s.nextActivityIDLocked("narration")
	s.activitySequence++
	s.activityNarrations = append(s.activityNarrations, activity.Narration{
		ID: owner.NarrationID, RunID: owner.RunID, ActorID: owner.ActorID,
		ResponseID: owner.ResponseID, BoundaryMessageID: s.activityBoundary,
		Source: activity.SourceRuntimeFallback, Sequence: s.activitySequence,
	})
	s.setActivityResponseNarrationLocked(owner.ResponseID, owner.NarrationID)
	s.activityResponse = owner
	return owner
}

// BindActivityNarration records public model prose by response identity.
// The returned ref is a new value and can be safely captured by dispatchers.
func (s *State) BindActivityNarration(response activity.Ref, publicText string) activity.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	response.NarrationID = s.nextActivityIDLocked("narration")
	s.activitySequence++
	s.activityNarrations = append(s.activityNarrations, activity.Narration{
		ID: response.NarrationID, RunID: response.RunID, ActorID: response.ActorID,
		ResponseID: response.ResponseID, BoundaryMessageID: s.activityBoundary,
		Text: publicText, Source: activity.SourceModelProse, Sequence: s.activitySequence,
	})
	s.setActivityResponseNarrationLocked(response.ResponseID, response.NarrationID)
	s.activityResponse = response
	return response
}

// AddNarrationMessage appends the normal non-final narration source message
// after binding it to its response. The database stores that source message
// using existing behavior, while ownership remains in memory only.
func (s *State) AddNarrationMessage(response activity.Ref, publicText string) int64 {
	if response.NarrationID == "" {
		response = s.BindActivityNarration(response, publicText)
	}
	messageID := s.appendMessageActivity(RoleAssistant, publicText, ContentTypeNarration, false, false, "", 0, "", response)
	s.mu.Lock()
	for i := range s.activityNarrations {
		if s.activityNarrations[i].ID == response.NarrationID {
			s.activityNarrations[i].SourceMessageID = messageID
			break
		}
	}
	s.mu.Unlock()
	return messageID
}

func (s *State) setActivityResponseNarrationLocked(responseID, narrationID string) {
	for i := range s.activityResponseLinks {
		if s.activityResponseLinks[i].ResponseID == responseID {
			s.activityResponseLinks[i].NarrationID = narrationID
			return
		}
	}
	s.activityResponseLinks = append(s.activityResponseLinks, ResponseNarration{ResponseID: responseID, NarrationID: narrationID})
}

// SetActivityBoundary starts a new visible user segment inside a run and
// drops the active response so subsequent tool-only output cannot inherit it.
func (s *State) SetActivityBoundary(messageID int64) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.mu.Lock()
	s.activityBoundary = messageID
	s.activityResponse = activity.Ref{}
	s.mu.Unlock()
}

func (s *State) EndActivityRun(run activity.Ref) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.mu.Lock()
	if run.RunID == s.activityRun.RunID {
		for i := range s.activityNarrations {
			if s.activityNarrations[i].RunID == run.RunID {
				s.activityNarrations[i].Closed = true
			}
		}
		s.activityRun, s.activityResponse = activity.Ref{}, activity.Ref{}
		s.activityProgressResponses = nil
		s.evidenceRecords = nil
		s.evidenceNextAlias = 0
		s.evidenceIssuedCalls = nil
	}
	s.mu.Unlock()
}

func (s *State) resetActivityLocked() {
	s.activityRun, s.activityResponse = activity.Ref{}, activity.Ref{}
	s.activityBoundary = 0
}

func (s *State) ActivitySnapshot() ActivitySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := make(map[int64]bool, len(s.messages))
	for _, msg := range s.messages {
		path[msg.ID] = true
	}
	snapshot := ActivitySnapshot{Run: s.activityRun, Response: s.activityResponse}
	for _, n := range s.activityNarrations {
		// Model-prose narrations are backed by a transcript message. Runtime
		// fallback narrations intentionally have no synthetic message: their
		// ownership begins at the active user boundary and is carried by the
		// tool records themselves.
		sourceOnPath := n.SourceMessageID != 0 && path[n.SourceMessageID]
		if n.Source == activity.SourceStructuredProgress {
			for _, revision := range s.activityProgress {
				if revision.NarrationID == n.ID && path[revision.SourceMessageID] {
					sourceOnPath = true
					break
				}
			}
		}
		fallback := n.Source == activity.SourceRuntimeFallback && n.SourceMessageID == 0
		if n.BoundaryMessageID != 0 && path[n.BoundaryMessageID] && (sourceOnPath || fallback) {
			if n.Source == activity.SourceStructuredProgress {
				for _, revision := range s.activityProgress {
					if revision.NarrationID == n.ID && path[revision.SourceMessageID] {
						n.SourceMessageID = revision.SourceMessageID
						break
					}
				}
			}
			snapshot.Narrations = append(snapshot.Narrations, n)
		}
	}
	for _, revision := range s.activityProgress {
		if path[revision.SourceMessageID] {
			revision.Sections = cloneProgressSections(revision.Sections)
			snapshot.ProgressRevisions = append(snapshot.ProgressRevisions, revision)
		}
	}
	for _, record := range s.evidenceRecords {
		snapshot.EvidenceRecords = append(snapshot.EvidenceRecords, cloneEvidenceRecord(record))
	}
	known := make(map[string]bool, len(snapshot.Narrations))
	for _, n := range snapshot.Narrations {
		known[n.ID] = true
	}
	for _, link := range s.activityResponseLinks {
		if known[link.NarrationID] {
			snapshot.ResponseNarrations = append(snapshot.ResponseNarrations, link)
		}
	}
	return snapshot
}
