package session

import (
	"fmt"

	"marshal/internal/activity"
	"marshal/internal/tools/registry"
)

const maxEvidenceAliases = 256

// EvidenceRecord is an immutable copy of one audited result delivered to an
// actor. It deliberately contains facts reported by the tool, not conclusions
// inferred from its prose.
type EvidenceRecord struct {
	Alias        string
	Owner        activity.Ref
	SourceViewID string
	ToolName     string
	Summary      string
	Content      string
	Error        string
	Denied       bool
	ExitCode     *int
	FilesChanged []string
	Retention    string
	Notice       *registry.ToolNotice
}

// IssueEvidenceReceipt binds a completed audit to the explicit runtime call
// identity and returns a compact receipt for the caller's result message.
// No latest-call or timestamp matching is used.
func (s *State) IssueEvidenceReceipt(owner activity.Ref) (EvidenceRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issueEvidenceReceiptLocked(owner)
}

func (s *State) issueEvidenceReceiptLocked(owner activity.Ref) (EvidenceRecord, bool) {
	if owner.RunID == "" || owner.ActorID == "" || owner.CallID == "" || owner.RunID != s.activityRun.RunID || owner.ActorID != s.activityRun.ActorID || !s.activityBoundaryOnPathLocked() {
		return EvidenceRecord{}, false
	}
	activeNarration := false
	for _, narration := range s.activityNarrations {
		if narration.ID == owner.NarrationID && narration.RunID == owner.RunID && narration.ActorID == owner.ActorID && narration.BoundaryMessageID == s.activityBoundary {
			activeNarration = narration.Source == activity.SourceRuntimeFallback && narration.SourceMessageID == 0
			if narration.SourceMessageID != 0 {
				for _, message := range s.messages {
					if message.ID == narration.SourceMessageID {
						activeNarration = true
						break
					}
				}
			}
			if narration.Source == activity.SourceStructuredProgress {
				for _, revision := range s.activityProgress {
					if revision.NarrationID == narration.ID {
						for _, message := range s.messages {
							if message.ID == revision.SourceMessageID {
								activeNarration = true
								break
							}
						}
					}
				}
			}
			break
		}
	}
	if !activeNarration {
		return EvidenceRecord{}, false
	}
	for i := len(s.evidenceRecords) - 1; i >= 0; i-- {
		if s.evidenceRecords[i].Owner == owner {
			return cloneEvidenceRecord(s.evidenceRecords[i]), true
		}
	}
	if s.evidenceIssuedCalls[owner.CallID] {
		return EvidenceRecord{}, false
	}
	for i := len(s.auditLog) - 1; i >= 0; i-- {
		ev := s.auditLog[i]
		if ev.Activity != owner || ev.ToolName == "agent.run" {
			continue
		}
		alias := fmt.Sprintf("e1-%d", s.evidenceNextAlias+1)
		s.evidenceNextAlias++
		record := EvidenceRecord{
			Alias: alias, Owner: ev.Activity,
			SourceViewID: ordinalViewID(s.scopePrefix(viewIDAudit), i),
			ToolName:     ev.ToolName, Summary: ev.ResultSummary, Content: ev.ResultContent,
			Error: ev.Error, Denied: ev.Approval == registry.ApprovalDenied,
			FilesChanged: append([]string(nil), ev.FilesChanged...),
		}
		if ev.CommandExitCode != nil {
			code := *ev.CommandExitCode
			record.ExitCode = &code
		}
		if ev.Notice != nil {
			record.Retention = ev.Notice.Kind
			if ev.Notice.Text != "" {
				record.Retention += ": " + ev.Notice.Text
			}
			record.Notice = cloneEvidenceNotice(ev.Notice)
		}
		s.evidenceRecords = append(s.evidenceRecords, record)
		if s.evidenceIssuedCalls == nil {
			s.evidenceIssuedCalls = make(map[string]bool)
		}
		s.evidenceIssuedCalls[owner.CallID] = true
		if len(s.evidenceRecords) > maxEvidenceAliases {
			s.evidenceRecords = append([]EvidenceRecord(nil), s.evidenceRecords[len(s.evidenceRecords)-maxEvidenceAliases:]...)
		}
		return cloneEvidenceRecord(record), true
	}
	return EvidenceRecord{}, false
}

// ResolveEvidenceRefs resolves only aliases issued to this active actor/run.
// It returns detached records so callers cannot mutate session state.
func (s *State) ResolveEvidenceRefs(owner activity.Ref, refs []string) ([]EvidenceRecord, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveEvidenceRefsLocked(owner, refs)
}

func (s *State) resolveEvidenceRefsLocked(owner activity.Ref, refs []string) ([]EvidenceRecord, []string) {
	if owner.RunID == "" || owner.RunID != s.activityRun.RunID || owner.ActorID != s.activityRun.ActorID {
		return nil, warningForUnresolvedRefs(len(refs))
	}
	seen := make(map[string]bool, len(refs))
	records := make([]EvidenceRecord, 0, len(refs))
	bad := false
	for _, alias := range refs {
		if seen[alias] {
			continue
		}
		seen[alias] = true
		found := false
		for i := len(s.evidenceRecords) - 1; i >= 0; i-- {
			record := s.evidenceRecords[i]
			if record.Alias == alias && record.Owner.RunID == owner.RunID && record.Owner.ActorID == owner.ActorID {
				records = append(records, cloneEvidenceRecord(record))
				found = true
				break
			}
		}
		if !found {
			bad = true
		}
	}
	if bad {
		return records, []string{"some evidence references are unavailable"}
	}
	return records, nil
}

func warningForUnresolvedRefs(n int) []string {
	if n == 0 {
		return nil
	}
	return []string{"some evidence references are unavailable"}
}

func cloneEvidenceRecord(record EvidenceRecord) EvidenceRecord {
	record.FilesChanged = append([]string(nil), record.FilesChanged...)
	if record.ExitCode != nil {
		code := *record.ExitCode
		record.ExitCode = &code
	}
	if record.Notice != nil {
		record.Notice = cloneEvidenceNotice(record.Notice)
	}
	return record
}

func cloneEvidenceNotice(notice *registry.ToolNotice) *registry.ToolNotice {
	if notice == nil {
		return nil
	}
	clone := &registry.ToolNotice{Kind: notice.Kind, Text: notice.Text}
	if len(notice.Data) > 0 {
		clone.Data = make(map[string]any, len(notice.Data))
		for key, value := range notice.Data {
			clone.Data[key] = cloneEvidenceValue(value)
		}
	}
	return clone
}

func cloneEvidenceValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, value := range typed {
			clone[key] = cloneEvidenceValue(value)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for i, value := range typed {
			clone[i] = cloneEvidenceValue(value)
		}
		return clone
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
