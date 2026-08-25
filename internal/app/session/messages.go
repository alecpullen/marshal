package session

import (
	"sort"
	"strings"
	"time"

	"marshal/internal/db"
)

// Role identifies the sender of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ContentType describes the rendering format of a message's Content field.
type ContentType string

const (
	ContentTypePlain      ContentType = "plain"
	ContentTypeMarkdown   ContentType = "markdown"
	ContentTypeCode       ContentType = "code"
	ContentTypePlan       ContentType = "plan"
	ContentTypeDiff       ContentType = "diff"
	ContentTypeToolResult ContentType = "tool_result"
	ContentTypeSkill      ContentType = "skill"
	// ContentTypeSkillBody marks a message carrying a loaded skill's
	// reference body. It exists so the body reaches the model and persists
	// with the session, but it is not rendered in the transcript — the
	// compact ContentTypeSkill tag is the user-facing trace of the load.
	ContentTypeSkillBody ContentType = "skill_body"
	// ContentTypeNarration marks the prose a model emitted alongside its
	// tool calls — what it says it is about to do, as distinct from the
	// final answer and from its private reasoning. Stored non-final, which
	// is what keeps it out of history replay (internal/agent/history.go)
	// while still rendering and persisting with the session.
	ContentTypeNarration ContentType = "narration"
	// ContentTypeCompaction marks the point where the transcript was
	// compacted. It renders as a compact one-line marker (like
	// ContentTypeSkill) so a long session shows where context was
	// handed off rather than appearing to silently forget.
	//
	// Stored under RoleSystem, which is what keeps it out of the model's
	// context: buildHistoryMessages (internal/agent/history.go:59) switches
	// only on RoleUser and RoleAssistant, so system-role messages never
	// replay. The marker is for the user, not the model — the model already
	// gets the digest, which history.go injects separately as
	// "Previous generation summary: ...". This is the same separation
	// ContentTypeNarration achieves by storing non-final.
	ContentTypeCompaction ContentType = "compaction"
	// ContentTypeSubagentReport marks a background subagent's completion
	// report, persisted under RoleUser so buildHistoryMessages replays it
	// and it survives restart. It renders nothing: the user already gets a
	// RoleSystem notice from the same completion path, and rendering the
	// RoleUser copy put a turn separator and a ❯ prompt in the transcript
	// for something the user never typed.
	//
	// Same split as ContentTypeSkillBody: reaches the model, invisible to
	// the reader.
	ContentTypeSubagentReport ContentType = "subagent_report"
	// ContentTypeSkillAuto records the skills the ranker auto-loaded at the
	// start of one turn. Content is the newline-separated skill names.
	//
	// Legacy: the ranker no longer auto-loads skills (it emits hints
	// instead — see internal/agent/skillrank.go). This content type is
	// retained so old session transcripts still render correctly. No new
	// messages with this type are produced.
	//
	// Distinct from ContentTypeSkill, which is the tag an explicit
	// skill.load tool call still posts.
	ContentTypeSkillAuto ContentType = "skill_auto"
)

// Message is a single turn in the session transcript. Messages form an
// append-only tree: each message has a ParentID (0 for the root), and the
// active branch is the path from root to leafID.
type Message struct {
	ID       int64
	ParentID int64
	// DBID is the persisted SQLite row id for this message. It is non-zero
	// for messages that were written to (or loaded from) the database and
	// zero for transient in-memory-only messages.
	DBID          int64
	Role          Role
	Content       string
	ContentType   ContentType
	Reasoning     string
	ThinkDuration time.Duration
	CreatedAt     time.Time
	Final         bool
	Salvaged      bool
	SalvageReason string
	// ToolCallCount is how many real tool calls the runner executed during
	// the turn that produced this message. In-memory only (not persisted
	// to SQLite) — it survives across session/prompt calls within a live
	// process but resets to 0 after a session/load replay from disk.
	ToolCallCount int
	// Usage is a human-readable per-turn token-usage line (e.g. "12k
	// prompt + 3k completion tokens") recorded on final assistant messages
	// when the provider reported usage. In-memory only, like
	// ToolCallCount — it feeds the session export's .usage block.
	Usage string
}

// loadFromDB reconstructs the in-memory message tree and scratchpad state
// for the current session from the persisted rows. It is called once from
// New when persistence is enabled. Message state and scratchpad state are
// loaded independently, so a session with scratchpad data but no messages
// (or vice versa) still cold-starts correctly.
//
// Concurrency: takes s.mu for the entire load. Idempotency: refuses to
// run if the in-memory tree is already populated, so calling New twice
// on the same State (e.g. accidental double-init) is a no-op rather than
// a duplicate-load.
func (s *State) loadFromDB() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.msgByID) > 0 {
		// Already loaded (or messages were added in-memory). Avoid
		// clobbering live state.
		return
	}

	leafDBID, err := s.db.GetLeafMessageID(s.sessionID)
	if err != nil {
		s.logger.Error("loadFromDB: get leaf", "error", err, "session_id", s.sessionID)
		s.loadErr = err
		return
	}

	if leafDBID != 0 {
		dbMessages, err := s.db.MessagesOnBranch(s.sessionID, leafDBID)
		if err != nil {
			s.logger.Error("loadFromDB: messages on branch", "error", err, "session_id", s.sessionID, "leaf", leafDBID)
			s.loadErr = err
			return
		}
		if len(dbMessages) > 0 {
			// Walk root -> leaf (MessagesOnBranch returns chronological order).
			// Allocate a stable in-memory id per loaded row starting at nextMsgID,
			// and record parent / child relations using the in-memory ids so the
			// tree mirror is internally consistent.
			for _, dm := range dbMessages {
				imID := s.nextMsgID
				s.nextMsgID++

				// Find this row's in-memory parent id: it was the in-memory id
				// we allocated for dm.ParentID, which equals imID-1 only for
				// a linear branch. Look it up via a dbID->imID translation
				// table built on the fly (cheap for cold-start sizes).
				var imParent int64
				if dm.ParentID > 0 {
					imParent = s.dbIDToImID[dm.ParentID]
				}

				thinkDur := time.Duration(0)
				if dm.ThinkDurationMs > 0 {
					thinkDur = time.Duration(dm.ThinkDurationMs) * time.Millisecond
				}
				msg := Message{
					ID:            imID,
					ParentID:      imParent,
					DBID:          dm.ID,
					Role:          Role(dm.Role),
					Content:       dm.Content,
					ContentType:   ContentType(dm.ContentType),
					Reasoning:     dm.Reasoning,
					ThinkDuration: thinkDur,
					CreatedAt:     dm.CreatedAt,
					Final:         dm.Final,
				}
				s.parentOf[imID] = imParent
				if imParent != 0 {
					s.childrenOf[imParent] = append(s.childrenOf[imParent], imID)
				}
				s.dbIDToImID[dm.ID] = imID
				// Stash on the msgByID map so rebuildActiveBranch can find it.
				// We don't append to s.messages here — that is the job of
				// rebuildActiveBranch (single source of truth for ordering).
				//
				// The map holds values, not pointers. appendMessage used to store
				// &s.messages[len-1], an interior pointer that any reallocating append
				// orphaned and that rebuildActiveBranch invalidated wholesale by
				// assigning a fresh slice. It read correctly only because Message is
				// never mutated in place, while pinning every superseded backing array
				// against collection.
				s.msgByID[imID] = msg
			}

			// The last row in dbMessages is the leaf; record both its in-memory
			// id and its DB id so the next AddMessage picks up the right parent.
			leaf := dbMessages[len(dbMessages)-1]
			imLeaf := s.dbIDToImID[leaf.ID]
			s.leafID = imLeaf
			s.leafDBID = leaf.ID
			s.rebuildActiveBranch()
			// The translation map is only needed while loading; drop it so the
			// session doesn't carry a stale dual-id mapping for its lifetime.
			s.dbIDToImID = nil
		}
	}

	// Restore scratchpad entries from the per-key scratchpad_entries table.
	// This is independent of message state so a session with scratchpad data
	// but no messages still cold-starts correctly.
	if entries, err := s.db.LoadScratchpad(s.sessionID); err == nil {
		s.scratchpad = make(map[string]db.ScratchpadEntry, len(entries))
		for _, e := range entries {
			s.scratchpad[e.Key] = e
		}
	} else {
		s.logger.Warn("failed to load scratchpad", "error", err)
	}

	// Restore todos from session_state so a resumed session continues the
	// list it had — without this, the first todo.write after resume
	// full-replaces a list the model can no longer see.
	if todos, err := s.db.LoadTodos(s.sessionID); err == nil {
		s.todos = todos
	} else {
		s.logger.Warn("failed to load todos", "error", err)
	}
}

func (s *State) persistenceEnabled() bool {
	return s.db != nil && s.sessionID != "" && s.logger != nil
}

// appendMessage is the shared body for AddMessage / AddMessageFinal /
// AddMessageSalvaged. It persists first (best effort) and then inserts the
// message into the in-memory tree exactly once, with its final id, so no
// transient id is ever published to event subscribers or re-keyed later.
// s.mu is held across the DB write: appends are per-session serialized
// anyway, and holding it closes the race where a concurrent Rewind /
// SwitchBranch could orphan a stashed pointer mid-promotion.
func (s *State) appendMessage(role Role, content string, contentType ContentType, final bool, salvaged bool, salvageReason string, toolCallCount int, usage string) {
	s.mu.Lock()
	reasoning := s.inProgress.Reasoning
	var thinkDuration time.Duration
	if reasoning != "" {
		thinkDuration = time.Since(s.inProgress.StartedAt)
		if thinkDuration <= 0 {
			thinkDuration = time.Millisecond
		}
	}
	s.inProgress = InProgressMessage{}

	parent := s.leafID
	createdAt := time.Now()

	var id int64
	var dbID int64
	persisted := false
	if s.persistenceEnabled() {
		var err error
		dbID, err = s.db.SaveMessage(s.sessionID, string(role), content, string(contentType), createdAt, reasoning, thinkDuration, final, s.leafDBID)
		if err != nil {
			s.logger.Error("save message failed", "error", err, "session_id", s.sessionID, "role", role)
		} else {
			id = dbID
			s.leafDBID = dbID
			persisted = true
			// Keep the persisted branch tip current so restarts reconstruct
			// the transcript correctly (B-01).
			if err := s.db.SetBranchLeaf(s.sessionID, dbID); err != nil {
				s.logger.Error("set branch leaf failed", "error", err, "session_id", s.sessionID, "leaf", dbID)
			}
		}
	}
	if !persisted {
		// In-memory mode (or DB write failed): fall back to the transient
		// counter. The message still lands in the tree, it just isn't
		// persisted — same degrade semantics as before.
		id = s.nextMsgID
	}
	// Always advance the transient-ID counter so a failed SaveMessage can
	// never reuse the id of the previous leaf (B-21).
	s.nextMsgID++

	// Task 3/A1: flush the per-turn tool audit ledger into SQLite when
	// this message is an assistant final turn. Best-effort: log and
	// continue on failure so a write error never breaks the turn. The
	// in-memory buffer is always cleared at the message boundary so the
	// next turn starts fresh — persistence only happens when a DB is
	// attached.
	if role == RoleAssistant && final && len(s.toolAuditThisTurn) > 0 {
		if persisted && dbID != 0 {
			pendingEntries := append([]db.ToolAuditEntry(nil), s.toolAuditThisTurn...)
			if err := s.db.SaveTurnToolAudit(s.sessionID, dbID, pendingEntries); err != nil {
				s.logger.Warn("save turn tool audit failed", "error", err, "session_id", s.sessionID, "message_db_id", dbID)
			}
		}
		s.toolAuditThisTurn = nil
	}

	msg := Message{
		ID:            id,
		ParentID:      parent,
		DBID:          dbID,
		Role:          role,
		Content:       content,
		ContentType:   contentType,
		Reasoning:     reasoning,
		ThinkDuration: thinkDuration,
		CreatedAt:     createdAt,
		Final:         final,
		Salvaged:      salvaged,
		SalvageReason: salvageReason,
		ToolCallCount: toolCallCount,
		Usage:         usage,
	}
	s.messages = append(s.messages, msg)
	s.parentOf[id] = parent
	if parent != 0 {
		s.childrenOf[parent] = append(s.childrenOf[parent], id)
	}
	s.msgByID[id] = msg
	s.leafID = id
	published := msg
	s.mu.Unlock()

	s.publishEvent(EventMessageAdded, Event{Message: &published})
}

func (s *State) AddMessage(role Role, content string, contentType ContentType) {
	s.appendMessage(role, content, contentType, false, false, "", 0, "")
}

func (s *State) AddMessageFinal(role Role, content string, contentType ContentType) {
	s.appendMessage(role, content, contentType, true, false, "", 0, "")
}

// AddMessageFinalWithToolCount is AddMessageFinal plus a record of how many
// real tool calls the runner executed during the turn, so a later turn's
// history replay can distinguish a verified completion from a prose-only one.
func (s *State) AddMessageFinalWithToolCount(role Role, content string, contentType ContentType, toolCallCount int) {
	s.appendMessage(role, content, contentType, true, false, "", toolCallCount, "")
}

// AddMessageFinalWithUsage is AddMessageFinalWithToolCount plus a
// human-readable per-turn token-usage line for the session export.
func (s *State) AddMessageFinalWithUsage(role Role, content string, contentType ContentType, toolCallCount int, usage string) {
	s.appendMessage(role, content, contentType, true, false, "", toolCallCount, usage)
}

func (s *State) AddMessageSalvaged(role Role, content string, contentType ContentType, reason string) {
	s.appendMessage(role, content, contentType, true, true, reason, 0, "")
}

func (s *State) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages := make([]Message, len(s.messages))
	copy(messages, s.messages)
	return messages
}

// LatestNarrationLine returns the first line of the most recent narration
// message, or "" when the session has none. Used to seed the activity label
// for a new model call with what the agent last said it was doing, before
// any thinking text has streamed.
func (s *State) LatestNarrationLine() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.messages) - 1; i >= 0; i-- {
		m := s.messages[i]
		if m.ContentType != ContentTypeNarration {
			continue
		}
		line, _, _ := strings.Cut(m.Content, "\n")
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// rebuildActiveBranch reassembles s.messages as the path root -> leafID
// using the in-memory parent/child maps. Caller must hold s.mu.
func (s *State) rebuildActiveBranch() {
	if s.leafID == 0 {
		s.messages = nil
		return
	}
	var path []int64
	for cur := s.leafID; cur != 0; cur = s.parentOf[cur] {
		path = append(path, cur)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	msgs := make([]Message, 0, len(path))
	for _, id := range path {
		if m, ok := s.msgByID[id]; ok {
			msgs = append(msgs, m)
		}
	}
	s.messages = msgs
}

// Rewind moves the active leaf to the parent of the message identified by
// turnMsgID (i.e. just before that turn), so the next AddMessage starts a
// new branch. Returns the new leaf id. Does NOT restore files — the caller
// (/rewind) does that via Snapshotter.
func (s *State) Rewind(turnMsgID int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.parentOf[turnMsgID]
	if !ok {
		return s.leafID
	}
	s.leafID = parent
	s.leafDBID = 0
	if parent != 0 {
		if m, ok := s.msgByID[parent]; ok {
			s.leafDBID = m.DBID
		}
	}
	s.rebuildActiveBranch()
	if s.persistenceEnabled() {
		if err := s.db.SetBranchLeaf(s.sessionID, s.leafDBID); err != nil {
			s.logger.Error("set branch leaf failed", "error", err, "session_id", s.sessionID, "leaf", s.leafDBID)
		}
	}
	return parent
}

// Branches returns the in-memory branch tips (messages with no children),
// sorted ascending by id.
func (s *State) Branches() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var leaves []int64
	for id := range s.msgByID {
		if len(s.childrenOf[id]) == 0 {
			leaves = append(leaves, id)
		}
	}
	sort.Slice(leaves, func(i, j int) bool { return leaves[i] < leaves[j] })
	return leaves
}

// SwitchBranch sets the active leaf to leafID and rebuilds the
// active-branch view (s.messages) to match.
func (s *State) SwitchBranch(leafID int64) {
	s.mu.Lock()
	s.leafID = leafID
	var leafDBID int64
	if m, ok := s.msgByID[leafID]; ok {
		leafDBID = m.DBID
	}
	s.leafDBID = leafDBID
	s.rebuildActiveBranch()
	s.mu.Unlock()

	if s.persistenceEnabled() {
		if err := s.db.SetBranchLeaf(s.sessionID, leafDBID); err != nil {
			s.logger.Error("set branch leaf failed", "error", err, "session_id", s.sessionID, "leaf", leafDBID)
		}
	}
}

// LeafID returns the active-branch leaf id.
func (s *State) LeafID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leafID
}

// ClearMessages removes all messages from the transcript and returns the
// count of messages that were cleared. The branch tree is intentionally NOT
// reset: leafID, the parent/child maps, and nextMsgID all survive, so the
// next message forks a new branch off the old leaf and Branches() still
// reports the old tips — this is what lets /new keep rewind history.
//
// Memory trade-off: the maps grow monotonically across /new calls within
// a single session. For long-running sessions with many /new resets, these
// maps hold metadata for every message ever added. This is bounded by the
// session lifetime and is acceptable given that a session restart clears
// everything. If this becomes a concern, a separate ResetBranches method
// could be added for callers that don't need rewind history.
//
// It does not affect the audit log, pending approvals, backups, or context
// pack.
func (s *State) ClearMessages() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.messages)
	s.messages = nil
	return count
}
