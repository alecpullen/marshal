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
func structuredNarrationSections(parent conversation.Block, narration activity.Narration, snapshot session.ActivitySnapshot, items, visibleItems []session.TranscriptItem) conversation.Block {
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

	itemsByView := make(map[string]session.TranscriptItem, len(visibleItems))
	for _, item := range visibleItems {
		itemsByView[item.ViewID] = item
	}
	revisionBySource := make(map[int64]activity.ProgressRevision)
	for _, revision := range snapshot.ProgressRevisions {
		if revision.NarrationID == narration.ID && revision.SourceMessageID != 0 {
			revisionBySource[revision.SourceMessageID] = revision
		}
	}
	allHaveSequence := true
	for _, item := range items {
		if !eligibleForOwner(item, narration) {
			continue
		}
		source, ok := conversationBlock(transcriptEntry{Item: &item})
		if !ok {
			continue
		}
		source.EventOrderSequence = item.Sequence
		if item.Kind == session.KindMessage && item.Message != nil && item.Message.ContentType == session.ContentTypeNarration {
			if revision, found := revisionBySource[item.Message.ID]; found {
				source.SourceRevision = revision.Revision
				if revision.Sequence != 0 {
					source.EventOrderSequence = revision.Sequence
				}
			}
		}
		if source.EventOrderSequence == 0 {
			allHaveSequence = false
		}
		parent.EventOrderAlternatives = append(parent.EventOrderAlternatives, source)
	}
	// Transcript order is the stable fallback for older/fixture records with no
	// sequence. When every event has a recorded sequence, use that exact source
	// order instead of the semantic section grouping.
	if allHaveSequence {
		sort.SliceStable(parent.EventOrderAlternatives, func(i, j int) bool {
			return parent.EventOrderAlternatives[i].EventOrderSequence < parent.EventOrderAlternatives[j].EventOrderSequence
		})
	}
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
			source, hasCanonicalSource := evidenceSourceForAlias(section.EvidenceSources, alias)
			if !hasCanonicalSource && len(section.EvidenceSources) == 0 {
				record, exists := aliases[alias]
				if exists {
					source = activity.EvidenceSource{Alias: record.Alias, Owner: record.Owner, SourceViewID: record.SourceViewID, ToolName: record.ToolName}
					hasCanonicalSource = true
				}
			}
			if !hasCanonicalSource {
				sectionBlock.Children = append(sectionBlock.Children, conversation.Block{Kind: conversation.BlockReference, Text: "Evidence unavailable", PresentationOnly: true, ID: conversation.BlockID("unavailable:" + narration.ID + ":" + string(section.Kind) + ":" + alias)})
				continue
			}
			item, retained := itemsByView[source.SourceViewID]
			if !retained || item.Kind != session.KindAudit || item.Activity != source.Owner || source.Owner.RunID != narration.RunID || source.Owner.ActorID != narration.ActorID {
				sectionBlock.Children = append(sectionBlock.Children, unavailableEvidence(narration.ID, section.Kind, alias))
				continue
			}
			if source.Owner.NarrationID != narration.ID {
				sectionBlock.Children = append(sectionBlock.Children, conversation.Block{Kind: conversation.BlockReference, Text: fmt.Sprintf("See %s", source.ToolName), ReferenceTarget: source.SourceViewID, PresentationOnly: true, ID: conversation.BlockID("reference:" + narration.ID + ":" + alias)})
				continue
			}
			if seenResult[source.SourceViewID] {
				sectionBlock.Children = append(sectionBlock.Children, conversation.Block{Kind: conversation.BlockReference, Text: fmt.Sprintf("See %s", source.ToolName), ReferenceTarget: source.SourceViewID, PresentationOnly: true, ID: conversation.BlockID("reference:" + narration.ID + ":" + alias)})
				continue
			}
			result, ok := conversationBlock(transcriptEntry{Item: &item})
			if !ok {
				sectionBlock.Children = append(sectionBlock.Children, unavailableEvidence(narration.ID, section.Kind, alias))
				continue
			}
			sectionBlock.Children = append(sectionBlock.Children, result)
			seenResult[source.SourceViewID] = true
			seenSource[source.SourceViewID] = true
		}
		children = append(children, notebookBlockRevision(sectionBlock))
	}
	if latest.Body != "" {
		children = append(children, blockRevision(conversation.Block{ID: conversation.BlockID("section:" + narration.ID + ":body"), Kind: conversation.BlockSection, Text: latest.Body, Source: conversation.SourceAnswer, SectionLabel: "Summary", CopyTargets: []conversation.CopyTarget{{Source: conversation.SourceAnswer, Text: latest.Body, Label: "Copy summary"}}}))
	}
	var unreferencedWork []conversation.Block
	for _, child := range parent.Children {
		if child.Kind == conversation.BlockMessage {
			if len(child.Members) > 0 {
				if item, ok := itemsByView[child.Members[0]]; ok && item.Message != nil && item.Message.ContentType == session.ContentTypeNarration {
					continue
				}
			}
		}
		if child.Kind == conversation.BlockThinking || len(child.Members) == 0 || !seenSource[child.Members[0]] {
			if child.Kind != conversation.BlockThinking && len(child.Members) > 0 {
				unreferencedWork = append(unreferencedWork, child)
			} else {
				children = append(children, child)
			}
		}
	}
	if len(unreferencedWork) > 0 {
		children = append(children, conversation.Block{ID: conversation.BlockID("section:" + narration.ID + ":work"), Kind: conversation.BlockSection, SectionLabel: string(activity.SectionWork), Children: unreferencedWork})
	}
	parent.Children = children
	return notebookBlockRevision(parent)
}

func evidenceSourceForAlias(sources []activity.EvidenceSource, alias string) (activity.EvidenceSource, bool) {
	for _, source := range sources {
		if source.Alias == alias {
			return source, true
		}
	}
	return activity.EvidenceSource{}, false
}

func unavailableEvidence(narration string, kind activity.SectionKind, alias string) conversation.Block {
	return conversation.Block{Kind: conversation.BlockReference, Text: "Evidence unavailable", PresentationOnly: true, ID: conversation.BlockID("unavailable:" + narration + ":" + string(kind) + ":" + alias)}
}
