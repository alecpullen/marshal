package viewmodel

import (
	"marshal/internal/acpwire"
	"marshal/internal/progress"
)

// WorkSnapshotOf is s on the wire (C3): times as Unix milliseconds, every
// other field copied, and fresh values throughout.
func WorkSnapshotOf(s WorkSnapshot) acpwire.WorkSnapshot {
	out := acpwire.WorkSnapshot{
		Version:             s.Version,
		ConversationID:      s.ConversationID,
		RuntimeEpoch:        s.RuntimeEpoch,
		Revision:            s.Revision,
		HistoryRevision:     s.HistoryRevision,
		ObservedAt:          acpwire.Millis(s.ObservedAt),
		ActiveCount:         s.ActiveCount,
		AttentionCount:      s.AttentionCount,
		SuccessReceiptCount: s.SuccessReceiptCount,
		StoppedReceiptCount: s.StoppedReceiptCount,
	}
	if s.Items != nil {
		out.Items = make([]acpwire.WorkItem, len(s.Items))
		for i, it := range s.Items {
			out.Items[i] = WorkItemOf(it)
		}
	}
	return out
}

// WorkItemOf is it on the wire.
func WorkItemOf(it WorkItem) acpwire.WorkItem {
	out := acpwire.WorkItem{
		TaskKey:        it.TaskKey,
		DisplayID:      it.DisplayID,
		ConversationID: it.ConversationID,
		RuntimeEpoch:   it.RuntimeEpoch,
		SourceID:       it.SourceID,
		SourceRevision: it.SourceRevision,
		Kind:           it.Kind,
		Label:          it.Label,
		Owner:          it.Owner,
		Origin: acpwire.WorkOrigin{
			TurnID:     it.Origin.TurnID,
			StepID:     it.Origin.StepID,
			ToolCallID: it.Origin.ToolCallID,
		},
		State:          it.State,
		Outcome:        it.Outcome,
		Attachment:     it.Attachment,
		StartedAt:      acpwire.Millis(it.StartedAt),
		EndedAt:        acpwire.Millis(it.EndedAt),
		ResultRevision: it.ResultRevision,
		Activity:       it.Activity,
		OutputTail:     it.OutputTail,
		Warning:        it.Warning,
		Attention:      it.Attention,
		Reference: acpwire.WorkReference{
			MessageID:      it.Reference.MessageID,
			NodeKey:        it.Reference.NodeKey,
			ChildSessionID: it.Reference.ChildSessionID,
		},
		Prompt: workPromptOf(it.Prompt),
		Capabilities: acpwire.WorkCapabilities{
			Inspect:       it.Capabilities.Inspect,
			Stop:          it.Capabilities.Stop,
			Acknowledge:   it.Capabilities.Acknowledge,
			ResolvePrompt: it.Capabilities.ResolvePrompt,
		},
		ProgressConfig: workProgressConfigOf(it.ProgressConfig),
		Progress:       WorkProgressOf(it.Progress),
	}
	if it.ExitCode != nil {
		code := *it.ExitCode
		out.ExitCode = &code
	}
	return out
}

// WorkProgressOf is p on the wire with its frame flattened, or nil for nil.
func WorkProgressOf(p *progress.Snapshot) *acpwire.WorkProgress {
	if p == nil {
		return nil
	}
	out := &acpwire.WorkProgress{
		Version:         p.Version,
		Sequence:        p.Sequence,
		PrimaryMetricID: p.PrimaryMetricID,
		Detail:          p.Detail,
		ObservedAt:      acpwire.Millis(p.ObservedAt),
		MeasuredAt:      acpwire.Millis(p.MeasuredAt),
		Partial:         p.Partial,
		Warning:         p.Warning,
		ReportedError:   p.ReportedError,
	}
	if p.Phase != nil {
		out.Phase = &acpwire.WorkPhase{ID: p.Phase.ID, Label: p.Phase.Label, Index: p.Phase.Index, Total: p.Phase.Total}
	}
	if p.Metrics != nil {
		out.Metrics = make([]acpwire.WorkMetric, len(p.Metrics))
		for i, m := range p.Metrics {
			wm := acpwire.WorkMetric{ID: m.ID, Label: m.Label, Unit: m.Unit, Current: m.Current}
			if m.Total != nil {
				total := *m.Total
				wm.Total = &total
			}
			if m.Rate != nil {
				rate := *m.Rate
				wm.Rate = &rate
			}
			out.Metrics[i] = wm
		}
	}
	if p.Rows != nil {
		out.Rows = make([]acpwire.WorkRow, len(p.Rows))
		for i, r := range p.Rows {
			out.Rows[i] = acpwire.WorkRow{ID: r.ID, Label: r.Label, State: r.State}
		}
	}
	return out
}

// WorkPageOf is p on the wire, its items converted one by one.
func WorkPageOf(p WorkPage) acpwire.WorkPage {
	out := acpwire.WorkPage{RuntimeEpoch: p.RuntimeEpoch, Revision: p.Revision, NextCursor: p.NextCursor}
	if p.Items != nil {
		out.Items = make([]acpwire.WorkItem, len(p.Items))
		for i, it := range p.Items {
			out.Items[i] = WorkItemOf(it)
		}
	}
	return out
}

// WorkDetailOf is d on the wire.
func WorkDetailOf(d WorkDetail) acpwire.WorkDetail {
	return acpwire.WorkDetail{
		Item:        WorkItemOf(d.Item),
		Output:      d.Output,
		Raw:         d.Raw,
		Report:      d.Report,
		Unavailable: d.Unavailable,
	}
}

// WorkStopResultOf is r on the wire.
func WorkStopResultOf(r WorkStopResult) acpwire.WorkStopResult {
	return acpwire.WorkStopResult{
		TaskKey:  r.TaskKey,
		State:    r.State,
		Summary:  r.Summary,
		Accepted: r.Accepted,
	}
}

// workProgressConfigOf is c on the wire, or nil for nil.
func workProgressConfigOf(c *progress.Config) *acpwire.WorkProgressConfig {
	if c == nil {
		return nil
	}
	return &acpwire.WorkProgressConfig{Profile: c.Profile, Source: c.Source, Title: c.Title}
}

// workPromptOf is p on the wire, or nil for nil.
func workPromptOf(p *WorkPrompt) *acpwire.WorkPrompt {
	if p == nil {
		return nil
	}
	return &acpwire.WorkPrompt{Kind: p.Kind, RequestID: p.RequestID, AgentID: p.AgentID}
}
