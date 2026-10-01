package tui

import (
	"hash/fnv"
	"sort"
	"strconv"

	"marshal/internal/activity"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
)

// conversationProjectionOptions is intentionally TUI-local. The preference
// will be supplied by config in a later task; production stays on legacy until
// that wiring exists.
type conversationProjectionOptions struct {
	Notebook        bool
	FollowingLatest bool
	ScopeID         string
}

// projectConversation is the common semantic projection seam. Both modes
// consume the exact same filtered source records, so copy/search cannot expose
// an item omitted from the rendered transcript.
func projectConversation(items []session.TranscriptItem, snapshot session.ActivitySnapshot, options conversationProjectionOptions) *conversation.Document {
	if !options.Notebook {
		return legacyConversationDocument(items)
	}
	return notebookConversationDocument(items, snapshot, options)
}

func legacyConversationDocument(items []session.TranscriptItem) *conversation.Document {
	entries := groupTranscript(items)
	blocks := make([]conversation.Block, 0, len(entries))
	for _, entry := range entries {
		if block, ok := conversationBlock(entry); ok {
			blocks = append(blocks, block)
		}
	}
	return conversation.NewDocument(blocks)
}

func notebookConversationDocument(items []session.TranscriptItem, snapshot session.ActivitySnapshot, options conversationProjectionOptions) *conversation.Document {
	// Ownership metadata is available only for live attributed records. Build
	// each section inside one genuine user segment, and accept only record
	// kinds/owners that the activity contract can account for.
	owners := map[string]activity.Narration{}
	for _, narration := range snapshot.Narrations {
		owners[narration.ID] = narration
	}
	blocks := make([]conversation.Block, 0, len(items))
	segment := make([]session.TranscriptItem, 0)
	flush := func() {
		blocks = append(blocks, projectNotebookSegment(segment, owners, snapshot, options.FollowingLatest, items)...)
		segment = segment[:0]
	}
	unavailable := false
	for _, item := range items {
		if item.Activity.NarrationID == "" && (item.Kind == session.KindAudit || item.Kind == session.KindThinking || (item.Kind == session.KindMessage && item.Message != nil && item.Message.Role == session.RoleAssistant && item.Message.ContentType != session.ContentTypeSteering)) {
			unavailable = true
		}
		if isUserTurn(item) && len(segment) > 0 {
			flush()
		}
		segment = append(segment, item)
	}
	flush()
	if unavailable {
		scope := options.ScopeID
		if scope == "" {
			scope = "conversation"
		}
		blocks = append([]conversation.Block{{ID: conversation.BlockID("note:ownership-unavailable:" + scope), Kind: conversation.BlockOwnershipNote, Text: "Ownership unavailable for earlier conversation history", PresentationOnly: true}}, blocks...)
	}
	return conversation.NewDocument(blocks)
}

func projectNotebookSegment(items []session.TranscriptItem, owners map[string]activity.Narration, snapshot session.ActivitySnapshot, followingLatest bool, allItems []session.TranscriptItem) []conversation.Block {
	if len(items) == 0 {
		return nil
	}
	byNarration := map[string][]session.TranscriptItem{}
	sourceIndex := map[string]int{}
	fallbackBoundaryIndex := map[string]int{}
	boundaryFound := map[string]bool{}
	for id, owner := range owners {
		if owner.BoundaryMessageID == 0 {
			boundaryFound[id] = true // legacy fixtures and sources without a recorded boundary
			continue
		}
		for _, item := range items {
			if item.Kind == session.KindMessage && item.Message != nil && item.Message.ID == owner.BoundaryMessageID && isUserTurn(item) {
				boundaryFound[id] = true
				break
			}
		}
		if boundaryFound[id] && owner.Source == activity.SourceRuntimeFallback {
			for i, item := range items {
				if item.Kind == session.KindMessage && item.Message != nil && item.Message.ID == owner.BoundaryMessageID && isUserTurn(item) {
					fallbackBoundaryIndex[id] = i
					break
				}
			}
		}
	}
	for i, item := range items {
		id := item.Activity.NarrationID
		owner, known := owners[id]
		if id == "" || !known || !boundaryFound[id] || !eligibleNarrationRecord(item) || item.Activity.RunID != owner.RunID || item.Activity.ActorID != owner.ActorID {
			continue
		}
		byNarration[id] = append(byNarration[id], item)
		if item.Kind == session.KindMessage && item.Message != nil && item.Message.ContentType == session.ContentTypeNarration && item.Message.ID == owner.SourceMessageID {
			sourceIndex[id] = i
		}
	}
	ids := make([]string, 0, len(byNarration))
	for id := range byNarration {
		_, hasSource := sourceIndex[id]
		_, hasFallbackBoundary := fallbackBoundaryIndex[id]
		if hasSource || (owners[id].Source == activity.SourceRuntimeFallback && hasFallbackBoundary) {
			ids = append(ids, id)
		}
	}
	sectionIndex := func(id string) int {
		if idx, ok := sourceIndex[id]; ok {
			return idx
		}
		return fallbackBoundaryIndex[id]
	}
	sort.SliceStable(ids, func(i, j int) bool { return sectionIndex(ids[i]) < sectionIndex(ids[j]) })
	allSequenced := len(ids) > 1
	for _, id := range ids {
		if owners[id].Sequence == 0 {
			allSequenced = false
			break
		}
	}
	if allSequenced {
		sort.SliceStable(ids, func(i, j int) bool { return owners[ids[i]].Sequence < owners[ids[j]].Sequence })
	}
	sections := map[string]conversation.Block{}
	for _, id := range ids {
		records := byNarration[id]
		complete := len(records) > 1
		for _, item := range records {
			if item.Sequence == 0 {
				complete = false
				break
			}
		}
		if complete {
			sort.SliceStable(records, func(i, j int) bool { return records[i].Sequence < records[j].Sequence })
		}
		children := make([]conversation.Block, 0, len(records))
		members := make([]string, 0, len(records))
		seenMembers := map[string]bool{}
		for _, item := range records {
			b, ok := conversationBlock(transcriptEntry{Item: &item})
			if !ok {
				continue
			}
			children = append(children, b)
			for _, member := range b.Members {
				if !seenMembers[member] {
					members = append(members, member)
					seenMembers[member] = true
				}
			}
		}
		if len(children) == 0 {
			continue
		}
		text := ""
		fallback := owners[id].Source == activity.SourceRuntimeFallback && owners[id].SourceMessageID == 0
		if fallback {
			// The runtime explicitly owns this work, though there was no public
			// prose or transcript source message to use as a headline.
			text = "Tool activity"
		} else {
			// The narration source, not the earliest child currently present,
			// permanently names its parent section.
			sourceID := items[sourceIndex[id]].ViewID
			if len(members) > 0 && members[0] != sourceID {
				for i, m := range members {
					if m == sourceID {
						copy(members[1:i+1], members[0:i])
						members[0] = sourceID
						break
					}
				}
			}
			for _, child := range children {
				if child.Kind == conversation.BlockMessage {
					text = child.Text
					break
				}
			}
		}
		block := conversation.Block{Kind: conversation.BlockNarration, Members: members, Children: children, Text: text}
		if fallback {
			block.ID = conversation.BlockID("notebook:fallback:" + id)
			block.PresentationOnly = true
			block.Members = nil
		}
		if owners[id].Source == activity.SourceStructuredProgress {
			block = structuredNarrationSections(block, owners[id], snapshot, items, allItems)
		}
		sections[id] = notebookBlockRevision(block)
	}
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := sections[id]; ok {
			ordered = append(ordered, id)
		}
	}
	if followingLatest && len(ordered) > 1 {
		latest := ordered[len(ordered)-1]
		ordered = append([]string{latest}, ordered[:len(ordered)-1]...)
	}
	normalOrdered := make([]string, 0, len(ordered))
	fallbackOrdered := make([]string, 0)
	for _, id := range ordered {
		if owners[id].Source == activity.SourceRuntimeFallback && owners[id].SourceMessageID == 0 {
			fallbackOrdered = append(fallbackOrdered, id)
		} else {
			normalOrdered = append(normalOrdered, id)
		}
	}
	fallbacksAtBoundary := map[int][]string{}
	for _, id := range fallbackOrdered {
		fallbacksAtBoundary[fallbackBoundaryIndex[id]] = append(fallbacksAtBoundary[fallbackBoundaryIndex[id]], id)
	}
	out := make([]conversation.Block, 0, len(items))
	nextSection := 0
	emitted := map[string]bool{}
	for itemIndex, item := range items {
		id := item.Activity.NarrationID
		if idx, ok := sourceIndex[id]; ok && idx >= 0 && item.Kind == session.KindMessage && item.Message != nil && item.Message.ContentType == session.ContentTypeNarration && item.Message.ID == owners[id].SourceMessageID {
			if nextSection < len(normalOrdered) {
				selected := normalOrdered[nextSection]
				out = append(out, sections[selected])
				emitted[selected] = true
				nextSection++
			}
			continue
		}
		if _, grouped := sections[id]; grouped && eligibleForOwner(item, owners[id]) {
			continue
		}
		if b, ok := conversationBlock(transcriptEntry{Item: &item}); ok {
			out = append(out, b)
		}
		for _, selected := range fallbacksAtBoundary[itemIndex] {
			out = append(out, sections[selected])
			emitted[selected] = true
		}
	}
	// Defensive completion: a malformed duplicate source record should not
	// cause an owned section to disappear, though normal transcript IDs are
	// unique and the source occurrence above emits each section exactly once.
	for _, id := range append(normalOrdered, fallbackOrdered...) {
		if !emitted[id] {
			out = append(out, sections[id])
		}
	}
	return out
}

func eligibleNarrationRecord(item session.TranscriptItem) bool {
	switch item.Kind {
	case session.KindAudit, session.KindThinking:
		return true
	case session.KindMessage:
		return item.Message != nil && item.Message.Role == session.RoleAssistant && (item.Message.ContentType == session.ContentTypeNarration || item.Message.Final)
	default:
		return false
	}
}

func eligibleForOwner(item session.TranscriptItem, owner activity.Narration) bool {
	return item.Activity.NarrationID == owner.ID && item.Activity.RunID == owner.RunID && item.Activity.ActorID == owner.ActorID && eligibleNarrationRecord(item)
}

func notebookBlockRevision(b conversation.Block) conversation.Block {
	b = blockRevision(b)
	h := fnv.New64a()
	writeRevisionField(h, strconv.Itoa(b.Revision))
	for _, member := range b.Members {
		writeRevisionField(h, member)
	}
	for _, child := range b.Children {
		writeRevisionField(h, string(child.ID))
		writeRevisionField(h, strconv.Itoa(child.Revision))
		writeRevisionField(h, child.ReferenceTarget)
		writeRevisionField(h, child.Text)
		writeRevisionField(h, strconv.Itoa(int(child.Kind)))
		for _, member := range child.Members {
			writeRevisionField(h, member)
		}
	}
	for _, alternative := range b.EventOrderAlternatives {
		writeRevisionField(h, string(alternative.ID))
		writeRevisionField(h, strconv.Itoa(alternative.Revision))
		writeRevisionField(h, strconv.FormatUint(alternative.EventOrderSequence, 10))
		writeRevisionField(h, strconv.FormatUint(alternative.SourceRevision, 10))
		for _, member := range alternative.Members {
			writeRevisionField(h, member)
		}
	}
	b.Revision = int(h.Sum64() & 0x7fffffff)
	return b
}
