package viewmodel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"marshal/internal/app/session"
	"marshal/internal/progress"
	"marshal/internal/pubsub"
)

// BandInfo is the pinned band's content: todo progress with its stale
// signal, and the running cards. The TUI and web both draw it from here, so
// the two can never show different numbers.
type BandInfo struct {
	Todos    bool // the turn is working from a list of MinTaskTodos or more
	Done     int
	Total    int
	Active   string // the in-progress item's content, else the first pending one
	Stale    bool
	StaleAge time.Duration
	// StaleSince is when the stale clock started (set while Stale), so a
	// client can tick the age on its own clock.
	StaleSince time.Time
	// StaleAt is when the band would turn stale with no further event: the
	// tag can appear on elapsed time alone, so a projector that refreshes
	// only on events must wake for it. Zero when no such moment is ahead.
	StaleAt time.Time
	Next    []string // waiting items after the active one, in list order
	// Tasks is the whole list with each item's share of the transcript. It
	// is not gated like Todos: the Tasks views show a short list, and one
	// left unfinished when the turn ended, which the band itself drops.
	Tasks    []BandTask
	Entities []BandEntity
}

// BandTask is one todo in the full list the Tasks views show.
type BandTask struct {
	ID, Content, Status    string
	StartedAt, CompletedAt time.Time
	TaskStat
}

// TaskStat is a todo's share of the transcript: the beats bound to it, their
// responses and finished working time. It is counted from the same beats the
// transcript draws under the todo's divider, so the views always agree.
type TaskStat struct {
	Steps int
	Work  time.Duration
	// First is the first beat bound to the todo, where a jump to it lands;
	// the zero NodeID when no beat has worked on it yet.
	First NodeID
}

// TaskStats sums the responses and working time bound to each todo.
func TaskStats(turns []*Node) map[string]TaskStat {
	stats := map[string]TaskStat{}
	for _, turn := range turns {
		for _, n := range turn.Children {
			if n.Kind != KindStep || n.Step == nil || n.Step.TodoID == "" {
				continue
			}
			st := stats[n.Step.TodoID]
			if st.First == (NodeID{}) {
				st.First = n.ID
			}
			st.Steps += max(len(n.Step.StepIDs), 1)
			if s, e := n.Step.Step.StartedAt, n.Step.Step.EndedAt; !s.IsZero() && e.After(s) {
				st.Work += e.Sub(s)
			}
			stats[n.Step.TodoID] = st
		}
	}
	return stats
}

// BandEntity is one running card the band lists.
type BandEntity struct {
	Ref     NodeID
	Label   string // "#3 Pass 1 Task 1" or "job-4"
	Detail  string // latest activity, or the command's first line
	Started time.Time
}

// TodoClockOf reads the todo clock from the transcript: the later of the last
// todo.write and the last user turn's start, plus the list owner's responses
// since. A list carried in from an earlier session therefore starts its clock
// with the turn, not with when it was written.
//
// The list owner is the agent that opened the current turn — the top-level
// runner, whatever its role. That runner counts exactly its own responses
// for the nudge (agent.noteResponse), so the band and the nudge count the
// same steps; a subagent or pipeline role sharing the session is not the
// owner, and its responses are not the list's progress.
func TodoClockOf(s Snapshot) session.TodoClock {
	var c session.TodoClock
	var turn int64
	for _, it := range s.Items {
		if it.Kind == session.KindMessage && session.IsUserTurnMessage(it.Message) {
			turn = it.Message.ID
			if it.Timestamp.After(c.LastWrite) {
				c.LastWrite = it.Timestamp
			}
		}
		if it.Audit != nil && it.Audit.ToolName == todoWriteTool && it.Audit.Error == "" && it.Timestamp.After(c.LastWrite) {
			c.LastWrite = it.Timestamp
		}
	}
	var owner session.Actor
	for _, st := range s.Steps {
		if st.TurnMsgID == turn {
			owner = st.Actor
			break
		}
	}
	for _, st := range s.Steps {
		if st.Actor.Role == owner.Role && st.Actor.Label == owner.Label && st.StartedAt.After(c.LastWrite) {
			c.Responses++
		}
	}
	return c
}

// BandOf builds the band for the latest turn.
func BandOf(s Snapshot, turns []*Node) BandInfo {
	var b BandInfo
	if len(s.Todos) > 0 {
		stats := TaskStats(turns)
		b.Tasks = make([]BandTask, len(s.Todos))
		for i, t := range s.Todos {
			b.Tasks[i] = BandTask{ID: t.ID, Content: ScrubText(t.Content), Status: t.Status,
				StartedAt: t.StartedAt, CompletedAt: t.CompletedAt}
			if t.ID != "" {
				b.Tasks[i].TaskStat = stats[t.ID]
			}
		}
	}
	if len(turns) > 0 && taskMode(s.Todos) && DrivenByTodos(turns[len(turns)-1]) {
		b.Total = len(s.Todos)
		active := -1
		for i, t := range s.Todos {
			switch {
			case t.Status == "completed":
				b.Done++
			case t.Status == "in_progress" && active < 0:
				active = i
			}
		}
		if active < 0 {
			for i, t := range s.Todos {
				if t.Status != "completed" {
					active = i
					break
				}
			}
		}
		if active >= 0 {
			b.Active = ScrubText(s.Todos[active].Content)
			for _, t := range s.Todos[active+1:] {
				if t.Status != "completed" && t.Status != "in_progress" {
					b.Next = append(b.Next, ScrubText(t.Content))
				}
			}
		}
		// A fully completed list is not progress, and an unfinished one is
		// only pinned while a turn runs: either would otherwise hold band
		// rows for the rest of the session. The list is still there for
		// Ctrl+T. Gated here, not per client, so the TUI and web agree.
		b.Todos = s.Busy && b.Done < len(s.Todos)
		clock := TodoClockOf(s)
		b.Stale, b.StaleAge = session.TodoStale(s.Todos, clock, s.Busy, s.Now)
		switch {
		case b.Stale:
			b.StaleSince = clock.LastWrite
		case b.Todos && !clock.LastWrite.IsZero():
			if at := clock.LastWrite.Add(session.TodoStaleAfter); at.After(s.Now) {
				b.StaleAt = at
			}
		}
	}
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			switch {
			case n.Kind == KindSubagent && n.Item != nil && n.Item.Subagent != nil && n.Item.Subagent.Status == session.SubagentRunning:
				v := n.Item.Subagent
				b.Entities = append(b.Entities, BandEntity{Ref: n.ID, Label: fmt.Sprintf("#%d %s", v.ID, ScrubText(v.Label)), Detail: ScrubText(v.CurrentTool), Started: v.StartedAt})
			case n.Job != nil && n.Job.Running:
				b.Entities = append(b.Entities, BandEntity{Ref: n.ID, Label: n.Job.ID, Detail: ScrubText(n.Job.Command), Started: n.JobStart()})
			}
			walk(n.Children)
		}
	}
	walk(turns)
	return b
}

// WorkOrigin identifies the conversation work a pinned card came from.
type WorkOrigin struct {
	TurnID     string `json:"turnId,omitempty"`
	StepID     string `json:"stepId,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

// WorkReference points at a retained result: the transcript message, the tree
// node or the child session that holds the full detail.
type WorkReference struct {
	MessageID      int64  `json:"messageId,omitempty"`
	NodeKey        string `json:"nodeKey,omitempty"`
	ChildSessionID string `json:"childSessionId,omitempty"`
}

// WorkCapabilities lists the actions a client may request for one item.
type WorkCapabilities struct {
	Inspect       bool `json:"inspect"`
	Stop          bool `json:"stop"`
	Acknowledge   bool `json:"acknowledge"`
	ResolvePrompt bool `json:"resolvePrompt"`
}

// WorkPrompt identifies a blocked request awaiting a client's answer.
type WorkPrompt struct {
	Kind      string `json:"kind"`
	RequestID string `json:"requestId"`
	AgentID   int64  `json:"agentId,omitempty"`
}

// WorkItem is one immutable projected unit of work.
type WorkItem struct {
	TaskKey        string             `json:"taskKey"`
	DisplayID      string             `json:"displayId"`
	ConversationID string             `json:"conversationId"`
	RuntimeEpoch   string             `json:"runtimeEpoch"`
	SourceID       string             `json:"sourceId"`
	SourceRevision uint64             `json:"sourceRevision"`
	Kind           string             `json:"kind"`
	Label          string             `json:"label"`
	Owner          string             `json:"owner,omitempty"`
	Origin         WorkOrigin         `json:"origin"`
	State          string             `json:"state"`
	Outcome        string             `json:"outcome,omitempty"`
	ExitCode       *int               `json:"exitCode,omitempty"`
	Attachment     string             `json:"attachment,omitempty"`
	StartedAt      time.Time          `json:"startedAt"`
	EndedAt        time.Time          `json:"endedAt,omitempty"`
	ResultRevision string             `json:"terminalResultRevision,omitempty"`
	Activity       string             `json:"activity,omitempty"`
	OutputTail     string             `json:"outputTail,omitempty"`
	Warning        string             `json:"warning,omitempty"`
	Attention      string             `json:"attention,omitempty"`
	Reference      WorkReference      `json:"reference"`
	Prompt         *WorkPrompt        `json:"prompt,omitempty"`
	Capabilities   WorkCapabilities   `json:"capabilities"`
	ProgressConfig *progress.Config   `json:"progressConfig,omitempty"`
	Progress       *progress.Snapshot `json:"progress,omitempty"`
}

// WorkSnapshot is the complete, immutable live projection for one conversation.
type WorkSnapshot struct {
	Version             int        `json:"version"`
	ConversationID      string     `json:"conversationId"`
	RuntimeEpoch        string     `json:"runtimeEpoch"`
	Revision            uint64     `json:"revision"`
	HistoryRevision     uint64     `json:"historyRevision"`
	ObservedAt          time.Time  `json:"observedAt"`
	Items               []WorkItem `json:"items"`
	ActiveCount         int        `json:"activeCount"`
	AttentionCount      int        `json:"attentionCount"`
	SuccessReceiptCount int        `json:"successReceiptCount"`
	StoppedReceiptCount int        `json:"stoppedReceiptCount"`
}

// WorkQuery asks for one page of the Work list. View selects the source set
// ("attention" or "history"); Cursor resumes a previous page.
type WorkQuery struct {
	ConversationID string
	View           string
	Query          string
	Cursor         string
	Limit          int
}

// WorkPage is one decoded page of the Work list.
type WorkPage struct {
	RuntimeEpoch string     `json:"runtimeEpoch"`
	Revision     uint64     `json:"revision"`
	Items        []WorkItem `json:"items"`
	NextCursor   string     `json:"nextCursor,omitempty"`
}

// WorkTarget names one item in a conversation.
type WorkTarget struct{ ConversationID, TaskKey string }

// WorkAck acknowledges one terminal result by its exact revision.
type WorkAck struct {
	WorkTarget
	ResultRevision string
}

// WorkDetail is the read-only detail for one item.
type WorkDetail struct {
	Item        WorkItem `json:"item"`
	Output      string   `json:"output"`
	Raw         bool     `json:"raw"`
	Report      string   `json:"report"`
	Unavailable string   `json:"unavailable"`
}

// WorkStopResult describes the outcome of an accepted stop request.
type WorkStopResult struct {
	TaskKey  string `json:"taskKey"`
	State    string `json:"state"`
	Summary  string `json:"summary"`
	Accepted bool   `json:"accepted"`
}

// WorkService is the runtime facade the TUI and ACP clients call for the Work
// view. It is satisfied by *app.Runtime; it is declared here so the renderer
// never imports the agent or app packages.
type WorkService interface {
	WorkSnapshot() WorkSnapshot
	SubscribeWork(context.Context) <-chan pubsub.Event[WorkSnapshot]
	ListWork(context.Context, WorkQuery) (WorkPage, error)
	InspectWork(context.Context, WorkTarget, bool) (WorkDetail, error)
	StopWork(context.Context, WorkTarget, string) (WorkStopResult, error)
	AcknowledgeWork(context.Context, WorkAck) error
}

// The neutral lifecycle vocabulary. Finer native outcomes are preserved
// separately in WorkItem.Outcome.
const (
	workStateRunning   = "running"
	workStateStopping  = "stopping"
	workStateWatching  = "watching"
	workStateBlocked   = "blocked"
	workStateCompleted = "completed"
	workStateFailed    = "failed"
	workStateStopped   = "stopped"
	workStateUnknown   = "unknown"

	workOutcomeStartupFailure = "startup_failure"
	workOutcomeLost           = "lost"
	workOutcomeStopped        = "stopped"

	workKindCommand = "command"

	workAttachmentForeground = "foreground"

	workViewAttention = "attention"
	workViewHistory   = "history"

	workPageDefaultLimit = 50
	workPageMaxLimit     = 100
	workQueryMaxRunes    = 200
	workCursorMaxBytes   = 2 << 10
)

// Work receipt window: a terminal result's receipt is shown for five seconds
// from its end, and never replayed once that window has passed.
const workReceiptWindow = 5 * time.Second

var ErrWorkStaleCursor = errors.New("work history changed; refresh the list")
var ErrWorkStaleTarget = errors.New("work target is unavailable in this conversation")
var ErrWorkUnsupported = errors.New("work action is unavailable")

// WorkKey delegates to the dependency-neutral helper so source packages and
// the projection agree on a canonical identity without an import cycle.
func WorkKey(conversation, epoch, kind, sourceID string) string {
	return progress.WorkKey(conversation, epoch, kind, sourceID)
}

// WorkResultRevision returns the stable terminal-result revision. A source's
// persisted revision always wins; otherwise it is computed once from the
// stored outcome, exit code and end time — never from observation time or
// progress. Semantic progress errors affect Attention, not this revision.
func WorkResultRevision(item WorkItem) string {
	if item.ResultRevision != "" {
		return item.ResultRevision
	}
	return progress.ResultRevision(item.TaskKey, item.State, item.Outcome, item.ExitCode, item.EndedAt)
}

// CloneWorkItem deep-copies every nested value so a caller can retain or
// mutate its copy without touching the projection.
func CloneWorkItem(item WorkItem) WorkItem {
	out := item
	if item.ExitCode != nil {
		v := *item.ExitCode
		out.ExitCode = &v
	}
	if item.Prompt != nil {
		p := *item.Prompt
		out.Prompt = &p
	}
	if item.ProgressConfig != nil {
		c := *item.ProgressConfig
		out.ProgressConfig = &c
	}
	out.Progress = progress.Clone(item.Progress)
	return out
}

// CloneWorkSnapshot deep-copies a snapshot and every item it holds.
func CloneWorkSnapshot(snapshot WorkSnapshot) WorkSnapshot {
	out := snapshot
	if snapshot.Items != nil {
		out.Items = make([]WorkItem, len(snapshot.Items))
		for i, it := range snapshot.Items {
			out.Items[i] = CloneWorkItem(it)
		}
	}
	return out
}

// workTerminalState reports whether a lifecycle state is settled.
func workTerminalState(state string) bool {
	switch state {
	case workStateCompleted, workStateFailed, workStateStopped, workStateUnknown:
		return true
	}
	return false
}

// WorkActive reports whether an item belongs in the pinned live membership.
// A command still attached to its foreground caller stays an ordinary tool
// row, so it is inspectable in Work history but not pinned. A stopped state is
// not active: its process is still being terminated.
func WorkActive(item WorkItem) bool {
	if item.Kind == workKindCommand && item.Attachment == workAttachmentForeground {
		return false
	}
	switch item.State {
	case workStateRunning, workStateStopping, workStateWatching, workStateBlocked:
		return true
	}
	return false
}

// WorkNeedsAttention reports whether a settled item should collapse to
// compact attention. It is pure: acknowledgment is applied by the
// coordinator, not suppressed here, and a running error row stays on its live
// card without becoming an acknowledgeable result. An explicit stop with no
// other error is not a failure.
func WorkNeedsAttention(item WorkItem) bool {
	if !workTerminalState(item.State) {
		return false
	}
	switch item.State {
	case workStateFailed, workStateUnknown:
		return true
	}
	switch item.Outcome {
	case workOutcomeStartupFailure, workOutcomeLost:
		return true
	}
	if item.Attention != "" {
		return true
	}
	if item.Progress != nil && item.Progress.ReportedError {
		return true
	}
	return false
}

// WorkReceipts classifies a settled, nonattention result as a success or a
// stopped receipt. A receipt is only live for five seconds from a nonzero end
// time; a missing or older timestamp can never restart one.
func WorkReceipts(item WorkItem, now time.Time) (success, stopped bool) {
	if !workTerminalState(item.State) || WorkNeedsAttention(item) {
		return false, false
	}
	if item.EndedAt.IsZero() || !now.Before(item.EndedAt.Add(workReceiptWindow)) {
		return false, false
	}
	if item.State == workStateStopped || item.Outcome == workOutcomeStopped {
		return false, true
	}
	return true, false
}

// workCursor is the opaque page position. It carries only the facts that
// define the page order — never progress, observation time or elapsed display.
type workCursor struct {
	Epoch    string `json:"e"`
	Revision uint64 `json:"r"`
	View     string `json:"v"`
	Query    string `json:"q"`
	Offset   int    `json:"o"`
}

// workMatch reports whether an item's safe display fields contain the
// case-folded query.
func workMatch(item WorkItem, query string) bool {
	return strings.Contains(strings.ToLower(item.Label), query) ||
		strings.Contains(strings.ToLower(item.Kind), query) ||
		strings.Contains(strings.ToLower(item.Owner), query)
}

// PageWork returns one deterministic page of the Work list. It validates the
// view, query and limit, decodes a bounded revision-bound cursor and copies the
// returned items. Progress, observation time and elapsed display never enter
// the cursor, so a progress-only update cannot invalidate a reader's page.
func PageWork(items []WorkItem, epoch string, revision uint64, query WorkQuery) (WorkPage, error) {
	page := WorkPage{RuntimeEpoch: epoch, Revision: revision}
	switch query.View {
	case workViewAttention, workViewHistory:
	default:
		return WorkPage{}, fmt.Errorf("work: view must be %q or %q", workViewAttention, workViewHistory)
	}
	if !utf8.ValidString(query.Query) || utf8.RuneCountInString(query.Query) > workQueryMaxRunes {
		return WorkPage{}, fmt.Errorf("work: query must be valid utf-8 of at most %d runes", workQueryMaxRunes)
	}
	limit := query.Limit
	if limit == 0 {
		limit = workPageDefaultLimit
	}
	if limit < 1 || limit > workPageMaxLimit {
		return WorkPage{}, fmt.Errorf("work: limit must be between 1 and %d", workPageMaxLimit)
	}
	normalized := strings.ToLower(query.Query)
	offset := 0
	if query.Cursor != "" {
		if len(query.Cursor) > workCursorMaxBytes {
			return WorkPage{}, ErrWorkStaleCursor
		}
		raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		if err != nil {
			return WorkPage{}, ErrWorkStaleCursor
		}
		var cur workCursor
		if err := json.Unmarshal(raw, &cur); err != nil {
			return WorkPage{}, ErrWorkStaleCursor
		}
		if cur.Epoch != epoch || cur.Revision != revision || cur.View != query.View ||
			cur.Query != normalized || cur.Offset < 0 {
			return WorkPage{}, ErrWorkStaleCursor
		}
		offset = cur.Offset
	}
	ordered := make([]WorkItem, 0, len(items))
	for _, item := range items {
		if query.View == workViewAttention && !WorkNeedsAttention(item) {
			continue
		}
		if normalized != "" && !workMatch(item, normalized) {
			continue
		}
		ordered = append(ordered, item)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].StartedAt.Equal(ordered[j].StartedAt) {
			return ordered[i].StartedAt.After(ordered[j].StartedAt)
		}
		return ordered[i].TaskKey < ordered[j].TaskKey
	})
	if offset > len(ordered) {
		return WorkPage{}, ErrWorkStaleCursor
	}
	end := offset + limit
	if end > len(ordered) {
		end = len(ordered)
	}
	page.Items = make([]WorkItem, 0, end-offset)
	for _, item := range ordered[offset:end] {
		page.Items = append(page.Items, CloneWorkItem(item))
	}
	if end < len(ordered) {
		raw, _ := json.Marshal(workCursor{Epoch: epoch, Revision: revision, View: query.View, Query: normalized, Offset: end})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
