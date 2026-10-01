package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/activity"
)

var ErrProgressScope = errors.New("public progress response is stale or outside the active scope")
var ErrProgressConflict = errors.New("response already has a different public progress update")
var ErrProgressNoNarration = errors.New("no current public narration to revise")

type PublicProgressReceipt struct {
	Owner    activity.Ref
	Revision uint64
	Warnings []string
}

type progressResponseRecord struct {
	Payload []byte
	Receipt PublicProgressReceipt
}

// ApplyPublicProgress validates one update against the current run, actor,
// response and visible boundary, then appends an immutable public revision.
func (s *State) ApplyPublicProgress(response activity.Ref, update activity.ProgressUpdate) (PublicProgressReceipt, error) {
	if err := activity.ValidateProgress(update); err != nil {
		return PublicProgressReceipt{}, err
	}
	payload, err := json.Marshal(update)
	if err != nil {
		return PublicProgressReceipt{}, fmt.Errorf("encode progress update: %w", err)
	}
	s.progressMu.Lock()
	progressLocked := true
	defer func() {
		if progressLocked {
			s.progressMu.Unlock()
		}
	}()

	s.mu.Lock()
	if response.ResponseID == "" || response.RunID == "" || response.RunID != s.activityRun.RunID ||
		response.ActorID != s.activityRun.ActorID || response.ResponseID != s.activityResponse.ResponseID ||
		response.RunID != s.activityResponse.RunID || response.ActorID != s.activityResponse.ActorID ||
		!s.activityBoundaryOnPathLocked() {
		s.mu.Unlock()
		return PublicProgressReceipt{}, ErrProgressScope
	}
	if s.activityProgressResponses == nil {
		s.activityProgressResponses = make(map[string]progressResponseRecord)
	}
	if previous, ok := s.activityProgressResponses[response.ResponseID]; ok {
		if bytes.Equal(previous.Payload, payload) {
			receipt := cloneProgressReceipt(previous.Receipt)
			if receipt.Owner.NarrationID == "" {
				receipt.Owner.NarrationID = response.NarrationID
			}
			s.mu.Unlock()
			return receipt, nil
		}
		s.mu.Unlock()
		return PublicProgressReceipt{}, ErrProgressConflict
	}

	var current *activity.Narration
	if response.NarrationID != "" {
		for i := range s.activityNarrations {
			if s.activityNarrations[i].ID == response.NarrationID {
				current = &s.activityNarrations[i]
				break
			}
		}
	}
	if current == nil && update.Mode == activity.ProgressRevise {
		for i := len(s.activityNarrations) - 1; i >= 0; i-- {
			n := &s.activityNarrations[i]
			if n.Source == activity.SourceStructuredProgress && n.RunID == response.RunID && n.ActorID == response.ActorID && n.BoundaryMessageID == s.activityBoundary && !n.Closed {
				current = n
				break
			}
		}
	}
	if update.Mode == activity.ProgressRevise && (current == nil || current.Closed || current.RunID != response.RunID || current.ActorID != response.ActorID || current.BoundaryMessageID != s.activityBoundary) {
		s.mu.Unlock()
		return PublicProgressReceipt{}, ErrProgressNoNarration
	}

	sections := cloneProgressSections(progressSectionsValue(update))
	warnings := []string(nil)
	for i := range sections {
		if len(sections[i].EvidenceRefs) > 0 {
			sections[i].EvidenceRefs = nil // Task 2 supplies the resolver.
			warnings = []string{"some evidence references are unavailable"}
		}
	}
	var narrationID string
	var headline, body, action string
	var rev uint64
	if update.Mode == activity.ProgressBegin {
		for i := range s.activityNarrations {
			n := &s.activityNarrations[i]
			if n.RunID == response.RunID && n.ActorID == response.ActorID && !n.Closed {
				n.Closed = true
			}
		}
		narrationID = s.nextActivityIDLocked("narration")
		s.activitySequence++
		s.activityNarrations = append(s.activityNarrations, activity.Narration{ID: narrationID, RunID: response.RunID, ActorID: response.ActorID, ResponseID: response.ResponseID, BoundaryMessageID: s.activityBoundary, Source: activity.SourceStructuredProgress, Sequence: s.activitySequence})
		headline = *update.Headline
		if update.Body != nil {
			body = *update.Body
		}
		if update.CurrentAction != nil {
			action = *update.CurrentAction
		}
		rev = 1
	} else {
		narrationID = current.ID
		headline, body, action = currentProgressFields(s.activityProgress, narrationID)
		rev = latestProgressRevision(s.activityProgress, narrationID) + 1
		if update.Headline != nil {
			headline = *update.Headline
		}
		if update.Body != nil {
			body = *update.Body
		}
		if update.CurrentAction != nil {
			action = *update.CurrentAction
		}
		if update.Sections != nil { /* supplied below */
		} else {
			sections = currentProgressSections(s.activityProgress, narrationID)
		}
	}
	if update.Mode == activity.ProgressBegin && update.Sections == nil {
		sections = nil
	}
	if update.Mode == activity.ProgressRevise && update.Sections == nil {
		sections = currentProgressSections(s.activityProgress, narrationID)
	}
	if update.Mode == activity.ProgressRevise && update.Headline == nil && update.Body == nil && update.CurrentAction == nil && update.Sections == nil {
		response.NarrationID = narrationID
		receipt := PublicProgressReceipt{Owner: response, Revision: latestProgressRevision(s.activityProgress, narrationID)}
		s.setActivityResponseNarrationLocked(response.ResponseID, narrationID)
		s.activityResponse = response
		s.activityProgressResponses[response.ResponseID] = progressResponseRecord{Payload: append([]byte(nil), payload...), Receipt: cloneProgressReceipt(receipt)}
		s.mu.Unlock()
		return receipt, nil
	}
	// Evidence aliases are unavailable until task 2. Keep model-authored prose,
	// but compare materialized public values to make unchanged revisions no-ops.
	if update.Mode == activity.ProgressRevise && progressContentEqual(s.activityProgress, narrationID, headline, body, action, sections) {
		response.NarrationID = narrationID
		receipt := PublicProgressReceipt{Owner: response, Revision: latestProgressRevision(s.activityProgress, narrationID), Warnings: warnings}
		s.setActivityResponseNarrationLocked(response.ResponseID, narrationID)
		s.activityResponse = response
		s.activityProgressResponses[response.ResponseID] = progressResponseRecord{Payload: append([]byte(nil), payload...), Receipt: cloneProgressReceipt(receipt)}
		s.mu.Unlock()
		return receipt, nil
	}
	response.NarrationID = narrationID
	s.setActivityResponseNarrationLocked(response.ResponseID, narrationID)
	s.activityResponse = response
	s.mu.Unlock()

	content := formatPublicProgress(headline, body, action, sections)
	messageID, message := s.appendMessageActivityDeferred(RoleAssistant, content, ContentTypeNarration, false, false, "", 0, "", response)
	s.mu.Lock()
	var sequence uint64
	for _, msg := range s.messages {
		if msg.ID == messageID {
			sequence = msg.Sequence
			break
		}
	}
	for i := range s.activityNarrations {
		if s.activityNarrations[i].ID == narrationID {
			s.activityNarrations[i].SourceMessageID = messageID
			break
		}
	}
	s.activityProgress = append(s.activityProgress, activity.ProgressRevision{NarrationID: narrationID, Revision: rev, Headline: headline, Body: body, CurrentAction: action, Sections: cloneProgressSections(sections), SourceMessageID: messageID, Sequence: sequence})
	receipt := PublicProgressReceipt{Owner: response, Revision: rev, Warnings: warnings}
	s.activityProgressResponses[response.ResponseID] = progressResponseRecord{Payload: append([]byte(nil), payload...), Receipt: cloneProgressReceipt(receipt)}
	s.mu.Unlock()
	s.progressMu.Unlock()
	progressLocked = false
	s.publishEvent(EventMessageAdded, Event{Message: &message})
	return receipt, nil
}

func progressSectionsValue(u activity.ProgressUpdate) []activity.ProgressSection {
	if u.Sections == nil {
		return nil
	}
	return *u.Sections
}

func (s *State) activityBoundaryOnPathLocked() bool {
	if s.activityBoundary == 0 {
		return false
	}
	for _, msg := range s.messages {
		if msg.ID == s.activityBoundary {
			return true
		}
	}
	return false
}

func cloneProgressSections(in []activity.ProgressSection) []activity.ProgressSection {
	if in == nil {
		return nil
	}
	out := make([]activity.ProgressSection, len(in))
	for i, section := range in {
		out[i] = section
		out[i].EvidenceRefs = append([]string(nil), section.EvidenceRefs...)
	}
	return out
}
func cloneProgressReceipt(r PublicProgressReceipt) PublicProgressReceipt {
	r.Warnings = append([]string(nil), r.Warnings...)
	return r
}
func latestProgressRevision(rs []activity.ProgressRevision, id string) uint64 {
	var n uint64
	for _, r := range rs {
		if r.NarrationID == id && r.Revision > n {
			n = r.Revision
		}
	}
	return n
}
func currentProgressFields(rs []activity.ProgressRevision, id string) (string, string, string) {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].NarrationID == id {
			return rs[i].Headline, rs[i].Body, rs[i].CurrentAction
		}
	}
	return "", "", ""
}
func currentProgressSections(rs []activity.ProgressRevision, id string) []activity.ProgressSection {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].NarrationID == id {
			return cloneProgressSections(rs[i].Sections)
		}
	}
	return nil
}
func progressContentEqual(rs []activity.ProgressRevision, id, h, b, a string, sections []activity.ProgressSection) bool {
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if r.NarrationID == id {
			x, _ := json.Marshal(r.Sections)
			y, _ := json.Marshal(sections)
			return r.Headline == h && r.Body == b && r.CurrentAction == a && bytes.Equal(x, y)
		}
	}
	return false
}
func formatPublicProgress(headline, body, action string, sections []activity.ProgressSection) string {
	var b strings.Builder
	b.WriteString(headline)
	if body != "" {
		b.WriteString("\n\n")
		b.WriteString(body)
	}
	if action != "" {
		b.WriteString("\n\nCurrent action: ")
		b.WriteString(action)
	}
	for _, section := range sections {
		b.WriteString("\n\n")
		b.WriteString(strings.ToUpper(string(section.Kind)))
		b.WriteString(": ")
		b.WriteString(section.Text)
	}
	return b.String()
}
