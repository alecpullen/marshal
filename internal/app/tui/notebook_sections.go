package tui

import (
	"fmt"
	"sort"

	"marshal/internal/activity"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
)

// structuredNarrationSections replaces the source-message body with the
// latest immutable public revision. Every section keeps its exact source
// members; reference labels are presentation text and never become identities.
func structuredNarrationSections(parent conversation.Block, narration activity.Narration, snapshot session.ActivitySnapshot, items []session.TranscriptItem) conversation.Block {
	var latest *activity.ProgressRevision
	for i := range snapshot.ProgressRevisions {
		revision := &snapshot.ProgressRevisions[i]
		if revision.NarrationID == narration.ID && (latest == nil || revision.Revision > latest.Revision) {
			latest = revision
		}
	}
	if latest == nil {
		return parent
	}
	parent.ID = conversation.BlockID("narration:" + narration.ID)
	parent.Text = latest.Headline
	parent.Source = conversation.SourceAnswer
	for _, item := range items {
		if item.Kind == session.KindMessage && item.Message != nil && item.Message.ID == latest.SourceMessageID {
			if source, ok := conversationBlock(transcriptEntry{Item: &item}); ok {
				parent.CopyTargets = source.CopyTargets
			}
			break
		}
	}
	children := make([]conversation.Block, 0, len(latest.Sections)+len(parent.Children))

	itemsByView := make(map[string]session.TranscriptItem, len(items))
	for _, item := range items {
		itemsByView[item.ViewID] = item
	}
	for _, revision := range snapshot.ProgressRevisions {
		if revision.NarrationID != narration.ID {
			continue
		}
		for _, item := range items {
			if item.Kind != session.KindMessage || item.Message == nil || item.Message.ID != revision.SourceMessageID || item.Message.ContentType != session.ContentTypeNarration {
				continue
			}
			source, ok := conversationBlock(transcriptEntry{Item: &item})
			if !ok {
				break
			}
			source.SourceRevision = revision.Revision
			source.EventOrderSequence = revision.Sequence
			if source.EventOrderSequence == 0 {
				source.EventOrderSequence = item.Sequence
			}
			parent.EventOrderAlternatives = append(parent.EventOrderAlternatives, source)
			break
		}
	}
	sort.SliceStable(parent.EventOrderAlternatives, func(i, j int) bool {
		left, right := parent.EventOrderAlternatives[i], parent.EventOrderAlternatives[j]
		if left.EventOrderSequence == 0 || right.EventOrderSequence == 0 {
			return left.SourceRevision < right.SourceRevision
		}
		return left.EventOrderSequence < right.EventOrderSequence
	})
	aliases := make(map[string]session.EvidenceRecord, len(snapshot.EvidenceRecords))
	for _, record := range snapshot.EvidenceRecords {
		aliases[record.Alias] = record
	}
	seenResult := make(map[string]bool)
	seenSource := make(map[string]bool)
	for _, section := range latest.Sections {
		sectionBlock := conversation.Block{ID: conversation.BlockID("section:" + narration.ID + ":" + string(section.Kind)), Kind: conversation.BlockSection, Text: section.Text, Source: conversation.SourceAnswer, SectionLabel: string(section.Kind), CopyTargets: []conversation.CopyTarget{{Source: conversation.SourceAnswer, Text: section.Text, Label: "Copy " + string(section.Kind)}}}
		localAliases := map[string]bool{}
		for _, alias := range section.EvidenceRefs {
			if localAliases[alias] {
				continue
			}
			localAliases[alias] = true
			record, exists := aliases[alias]
			if !exists {
				sectionBlock.Children = append(sectionBlock.Children, conversation.Block{Kind: conversation.BlockReference, Text: "Evidence unavailable", PresentationOnly: true, ID: conversation.BlockID("unavailable:" + narration.ID + ":" + string(section.Kind) + ":" + alias)})
				continue
			}
			item, retained := itemsByView[record.SourceViewID]
			if !retained || item.Kind != session.KindAudit || item.Activity.RunID != narration.RunID || item.Activity.ActorID != narration.ActorID {
				sectionBlock.Children = append(sectionBlock.Children, unavailableEvidence(narration.ID, section.Kind, alias))
				continue
			}
			if item.Activity.NarrationID != narration.ID {
				sectionBlock.Children = append(sectionBlock.Children, conversation.Block{Kind: conversation.BlockReference, Text: fmt.Sprintf("See %s", record.ToolName), ReferenceTarget: record.SourceViewID, PresentationOnly: true, ID: conversation.BlockID("reference:" + narration.ID + ":" + alias)})
				continue
			}
			if seenResult[record.SourceViewID] {
				sectionBlock.Children = append(sectionBlock.Children, conversation.Block{Kind: conversation.BlockReference, Text: fmt.Sprintf("See %s", record.ToolName), ReferenceTarget: record.SourceViewID, PresentationOnly: true, ID: conversation.BlockID("reference:" + narration.ID + ":" + alias)})
				continue
			}
			result, ok := conversationBlock(transcriptEntry{Item: &item})
			if !ok {
				sectionBlock.Children = append(sectionBlock.Children, unavailableEvidence(narration.ID, section.Kind, alias))
				continue
			}
			sectionBlock.Children = append(sectionBlock.Children, result)
			seenResult[record.SourceViewID] = true
			seenSource[record.SourceViewID] = true
		}
		children = append(children, notebookBlockRevision(sectionBlock))
	}
	if latest.Body != "" {
		children = append(children, blockRevision(conversation.Block{ID: conversation.BlockID("section:" + narration.ID + ":body"), Kind: conversation.BlockSection, Text: latest.Body, Source: conversation.SourceAnswer, SectionLabel: "Summary", CopyTargets: []conversation.CopyTarget{{Source: conversation.SourceAnswer, Text: latest.Body, Label: "Copy summary"}}}))
	}
	for _, child := range parent.Children {
		if child.Kind == conversation.BlockMessage {
			if len(child.Members) > 0 {
				if item, ok := itemsByView[child.Members[0]]; ok && item.Message != nil && item.Message.ContentType == session.ContentTypeNarration {
					continue
				}
			}
		}
		if child.Kind == conversation.BlockThinking || len(child.Members) == 0 || !seenSource[child.Members[0]] {
			children = append(children, child)
		}
	}
	parent.Children = children
	return notebookBlockRevision(parent)
}

func unavailableEvidence(narration string, kind activity.SectionKind, alias string) conversation.Block {
	return conversation.Block{Kind: conversation.BlockReference, Text: "Evidence unavailable", PresentationOnly: true, ID: conversation.BlockID("unavailable:" + narration + ":" + string(kind) + ":" + alias)}
}
