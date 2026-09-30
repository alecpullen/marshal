package session

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Inspection caps.
//
// The numbers are deliberately modest. This snapshot exists to answer "what did
// Marshal actually send?" at a glance, and the way that question is answered
// badly is by holding a copy of a 400-message conversation in memory for every
// live State — including every subagent child — so the UI can show one screen
// of it.
const (
	// MaxInspectionFieldBytes caps one piece of content: a message body, a
	// tool-call argument blob, a tool description. It is generous enough for a
	// whole file to be visible as an argument, which is the ordinary case.
	MaxInspectionFieldBytes = 32 * 1024
	// MaxInspectionTotalBytes caps the content across the whole snapshot. Many
	// fields each within their own cap still add up without bound, and this is
	// the number that makes the snapshot's memory use something a caller can
	// reason about.
	MaxInspectionTotalBytes = 2 * 1024 * 1024
	// MaxInspectionMessages caps how many messages are kept. A long
	// conversation is bounded by the byte budget in practice, but a
	// pathological run of tiny messages needs its own limit or the snapshot
	// becomes thousands of rows.
	MaxInspectionMessages = 200
	// MaxInspectionTools caps the tool definitions kept. The definitions are
	// the agent's whole callable surface and a large MCP install can be
	// hundreds of them.
	MaxInspectionTools = 200
)

// InspectionStatus is the dispatch outcome of one conversation attempt.
//
// The values are deliberately about the ADAPTER, not the model. Marshal cannot
// observe whether a remote server received a request, and a snapshot that said
// "delivered" would be claiming knowledge it does not have.
type InspectionStatus string

const (
	// InspectionDispatched means the request was submitted to the provider
	// adapter and no outcome has been recorded yet. It is the state a snapshot
	// has at the moment it is written, which is immediately before the call.
	InspectionDispatched InspectionStatus = "dispatched"
	// InspectionStreaming means the adapter returned a stream and events have
	// begun. It is still not an acknowledgement from a server.
	InspectionStreaming InspectionStatus = "streaming"
	// InspectionCompleted means the attempt ran to completion.
	InspectionCompleted InspectionStatus = "completed"
	// InspectionFailed means the attempt ended in an error.
	InspectionFailed InspectionStatus = "failed"
	// InspectionCancelled means the attempt's context was cancelled — by the
	// user stopping the turn, or by the turn's own timeout.
	InspectionCancelled InspectionStatus = "cancelled"
)

// InspectionMessage is one message as it went into the request.
//
// Role and Content are copied; ToolCalls records the relationships the request
// expressed, because a tool result with no idea which call it answers is
// unreadable.
type InspectionMessage struct {
	Role       string
	Content    string
	ToolCalls  []InspectionToolCall
	ToolCallID string
	// Truncated and OmittedBytes describe Content when the field cap bit. The
	// pair is what lets a reader tell a short message from the visible part of
	// a long one.
	Truncated    bool
	OmittedBytes int
}

// InspectionToolCall is one tool call attached to a message.
type InspectionToolCall struct {
	ID   string
	Name string
	Args string
	// Truncated and OmittedBytes describe Args, as above.
	Truncated    bool
	OmittedBytes int
}

// InspectionTool is one tool definition offered on the request.
type InspectionTool struct {
	Name         string
	Description  string
	Parameters   string
	Truncated    bool
	OmittedBytes int
}

// InspectionOptions records the request options that actually went out.
//
// These are read from the request itself rather than from configuration,
// because configuration is not what the model saw: a temperature-locked backend
// has its temperature dropped, and a provider with no reasoning support has its
// thinking effort dropped. Showing the configured values would describe a
// request that was never sent.
type InspectionOptions struct {
	Thinking    string
	Streaming   bool
	MaxTokens   *int
	Temperature *float64
	// ResponseFormat names the response format type that was set ("" when
	// none). Only the type is kept: the schema itself can be large and is
	// derived, not chosen.
	ResponseFormat string
	// ToolChoice is the forced tool name when one was set.
	ToolChoice string
}

// InspectionOutcome is what became of the attempt.
type InspectionOutcome struct {
	Status InspectionStatus
	Err    string
	// At is when the outcome was recorded.
	At time.Time
}

// RequestInspection is a bounded, copied snapshot of one conversation attempt:
// the last request Marshal submitted to its provider adapter.
//
// It is NOT every model call. Marshal also makes title, summarizer, knowledge
// and rollover-digest calls through a different path, and folding those in
// would make "the last request" mean whichever of a dozen internal helpers ran
// most recently — the opposite of what a reader opens this view to see.
type RequestInspection struct {
	// AttemptID identifies this attempt so a late outcome cannot relabel a
	// newer one.
	AttemptID uint64
	At        time.Time
	Provider  string
	Model     string
	// Generation and LeafID place the request in the conversation's history.
	Generation string
	LeafID     int64

	Messages []InspectionMessage
	Tools    []InspectionTool
	Options  InspectionOptions

	// Outcome is the dispatch outcome. It starts as InspectionDispatched.
	Outcome InspectionOutcome

	// Truncated reports that ANY cap bit, so a caller can label the whole
	// snapshot without walking every field.
	Truncated bool
	// OmittedBytes is the total dropped from the snapshot's content.
	OmittedBytes int
	// MessagesOmitted and ToolsOmitted count entries dropped by the entry caps.
	MessagesOmitted int
	ToolsOmitted    int

	// PackTokens, PackWindow, PackTruncated and PackSections summarise the
	// context pack at DISPATCH time. They are here rather than read from the
	// live pack because the pack changes and this snapshot does not: a reader
	// comparing "what was sent" against "what the pack says now" needs the
	// value that was true THEN.
	//
	// PackTokens is the pack's OWN estimate, kept in a field named for that. It
	// is not the model's context window, and the two being adjacent numbers
	// about the same request is exactly how one gets read as the other.
	PackTokens    int
	PackWindow    int
	PackTruncated bool
	PackSections  int
	// PackKnown distinguishes "no pack was recorded" from "a pack with zero
	// tokens", which are different facts about the request.
	PackKnown bool
}

// TotalContentBytes reports the content the snapshot actually holds, so a
// caller can assert the budget without reaching into every field.
func (r RequestInspection) TotalContentBytes() int {
	total := 0
	for _, m := range r.Messages {
		total += len(m.Content)
		for _, tc := range m.ToolCalls {
			total += len(tc.Args)
		}
	}
	for _, t := range r.Tools {
		total += len(t.Name) + len(t.Description) + len(t.Parameters)
	}
	return total
}

// SetRequestInspection stores a copied, bounded snapshot of one attempt.
//
// The copy is the point. The runtime keeps the request it built (retries and
// diagnostics read it), and a snapshot that aliased those slices would change
// under the reader — or, worse, a reader mutating what they were handed would
// corrupt the runtime's own view.
//
// The budget is applied here rather than by the caller so it cannot be
// forgotten at one of the call sites, and so the numbers live in one place.
func (s *State) SetRequestInspection(req RequestInspection) {
	req = boundInspection(req)
	if req.Outcome.Status == "" {
		// A snapshot is written immediately BEFORE the call, so its truthful
		// state at that instant is "submitted, outcome not yet known".
		req.Outcome = InspectionOutcome{Status: InspectionDispatched, At: req.At}
	}
	s.mu.Lock()
	s.requestInspection = &req
	s.mu.Unlock()
}

// RequestInspection returns the last attempt's snapshot and whether one exists.
//
// The returned value is the caller's: every slice in it is a fresh copy, so a
// panel can sort, filter or annotate it without affecting the stored snapshot
// or racing the runner that wrote it.
func (s *State) RequestInspection() (RequestInspection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestInspection == nil {
		return RequestInspection{}, false
	}
	return cloneInspection(*s.requestInspection), true
}

// SetRequestInspectionOutcome records what became of an attempt.
//
// It reports whether the outcome was applied. The attempt ID is checked, and
// that check is the whole reason this is not just a field write: a provider can
// take minutes to fail, and by the time the failure arrives the user may be
// looking at a different request. Relabelling the newer one with the older
// one's failure is a false statement about the thing on screen.
//
// It is also refused when nothing has been recorded: an outcome describes a
// request, and manufacturing a snapshot to hold one would present a request
// that was never sent.
func (s *State) SetRequestInspectionOutcome(attemptID uint64, outcome InspectionOutcome) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestInspection == nil || s.requestInspection.AttemptID != attemptID {
		return false
	}
	if outcome.At.IsZero() {
		outcome.At = time.Now()
	}
	s.requestInspection.Outcome = outcome
	return true
}

// cloneInspection deep-copies a snapshot, including every nested slice.
//
// A shallow copy would leave the caller sharing the stored slices, which makes
// a panel's read of a large snapshot a data race against the runner writing the
// next one — and lets one mutate the other's view.
func cloneInspection(in RequestInspection) RequestInspection {
	out := in
	out.Messages = make([]InspectionMessage, len(in.Messages))
	for i, m := range in.Messages {
		out.Messages[i] = m
		if len(m.ToolCalls) > 0 {
			calls := make([]InspectionToolCall, len(m.ToolCalls))
			copy(calls, m.ToolCalls)
			out.Messages[i].ToolCalls = calls
		}
	}
	out.Tools = make([]InspectionTool, len(in.Tools))
	copy(out.Tools, in.Tools)
	if in.Options.MaxTokens != nil {
		v := *in.Options.MaxTokens
		out.Options.MaxTokens = &v
	}
	if in.Options.Temperature != nil {
		v := *in.Options.Temperature
		out.Options.Temperature = &v
	}
	return out
}

// boundInspection applies the field, entry and total caps to a snapshot,
// marking every drop it makes.
//
// Dropping is done from the TAIL, and the leading content is kept. A request's
// system prompt and first user turn are what the request is FOR; a snapshot
// that kept the last few messages and dropped the head would explain nothing
// about why the model answered as it did.
//
// Every drop is accounted: a reader must never see a bounded snapshot presented
// as a complete one.
func boundInspection(in RequestInspection) RequestInspection {
	out := cloneInspection(in)

	// Per-field and per-entry caps first, so the total budget is applied to
	// content that is already sane.
	for i := range out.Messages {
		m := &out.Messages[i]
		m.Content, m.Truncated, m.OmittedBytes = capBytes(m.Content, MaxInspectionFieldBytes)
		if m.Truncated || m.OmittedBytes > 0 {
			out.Truncated = true
			out.OmittedBytes += m.OmittedBytes
		}
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			tc.Args, tc.Truncated, tc.OmittedBytes = capBytes(tc.Args, MaxInspectionFieldBytes)
			if tc.Truncated || tc.OmittedBytes > 0 {
				out.Truncated = true
				out.OmittedBytes += tc.OmittedBytes
			}
		}
	}
	for i := range out.Tools {
		t := &out.Tools[i]
		t.Description, t.Truncated, t.OmittedBytes = capBytes(t.Description, MaxInspectionFieldBytes)
		if t.Truncated {
			out.Truncated = true
			out.OmittedBytes += t.OmittedBytes
		}
	}

	// Entry caps: a long conversation is bounded by bytes in practice, but a
	// run of tiny messages needs its own limit.
	if n := len(out.Messages); n > MaxInspectionMessages {
		out.MessagesOmitted = n - MaxInspectionMessages
		out.OmittedBytes += countMessageBytes(out.Messages[MaxInspectionMessages:])
		out.Messages = out.Messages[:MaxInspectionMessages]
		out.Truncated = true
	}
	if n := len(out.Tools); n > MaxInspectionTools {
		out.ToolsOmitted = n - MaxInspectionTools
		// Account the dropped definitions' bytes. The messages arm above has
		// always done this (countMessageBytes); the tools arm counting only the
		// entries meant the two halves of the same statement disagreed — the
		// snapshot said how MANY definitions it dropped but reported none of
		// their bytes, so a reader adding retained to omitted got less than the
		// original and no way to see that the arithmetic was short.
		out.OmittedBytes += countToolBytes(out.Tools[MaxInspectionTools:])
		out.Tools = out.Tools[:MaxInspectionTools]
		out.Truncated = true
	}

	// Total budget last: it is the only cap that has to consider the whole.
	used := 0
	for i := range out.Messages {
		cost := len(out.Messages[i].Content)
		for _, tc := range out.Messages[i].ToolCalls {
			cost += len(tc.Args)
		}
		if used+cost > MaxInspectionTotalBytes {
			// This message would break the budget. Drop it and everything
			// after it — keeping a later one would produce a snapshot with a
			// hole in the middle that no label can honestly describe.
			out.MessagesOmitted += len(out.Messages) - i
			out.OmittedBytes += countMessageBytes(out.Messages[i:])
			out.Messages = out.Messages[:i]
			out.Truncated = true
			break
		}
		used += cost
	}
	for i := range out.Tools {
		cost := len(out.Tools[i].Name) + len(out.Tools[i].Description) + len(out.Tools[i].Parameters)
		if used+cost > MaxInspectionTotalBytes {
			out.ToolsOmitted += len(out.Tools) - i
			out.OmittedBytes += countToolBytes(out.Tools[i:])
			out.Tools = out.Tools[:i]
			out.Truncated = true
			break
		}
		used += cost
	}
	return out
}

// countMessageBytes sums the content bytes of a run of messages.
func countMessageBytes(msgs []InspectionMessage) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
		for _, tc := range m.ToolCalls {
			total += len(tc.Args)
		}
	}
	return total
}

// countToolBytes sums the byte-bearing fields of a run of tool definitions.
//
// It measures the SAME fields the total budget charges for (name, description
// and parameters, in the loop above) rather than a fresh definition of "the
// bytes a tool costs". The two have to agree: the omitted count exists so that
// retained bytes plus omitted bytes reconstructs the original, and a counter
// that measured a different set of fields would leave the arithmetic short by
// exactly the fields it forgot.
func countToolBytes(tools []InspectionTool) int {
	total := 0
	for _, t := range tools {
		total += len(t.Name) + len(t.Description) + len(t.Parameters)
	}
	return total
}

// capBytes bounds s to at most limit bytes, keeping the LEADING bytes, and
// reports whether it cut and how many bytes it dropped.
//
// The cut is moved back to a UTF-8 boundary. Cutting at a byte offset would
// split a multi-byte rune and produce a snapshot that renders as a replacement
// character in the middle of the user's own prompt — a corruption the reader
// would attribute to the model.
//
// The omitted count is measured from the ORIGINAL length, so it stays the true
// byte count of what is missing even though the retained prefix is shorter than
// the limit by up to three bytes.
//
// The returned string is CLONED. A substring shares its backing array, so
// returning s[:cut] would keep the whole original alive: a 96 KiB message capped
// at 32 KiB would leave the snapshot pinning all 96 KiB, and a multi-megabyte
// tool-call argument would stay fully resident until the next request replaced
// it. That is precisely what MaxInspectionTotalBytes exists to make untrue (see
// the cap rationale above: "the number that makes the snapshot's memory use
// something a caller can reason about"), and the accounting cannot see it —
// TotalContentBytes measures len of the retained prefix, not the bytes the
// prefix keeps reachable. The clone is load-bearing for the cap's meaning, not a
// micro-optimisation, so do not "simplify" it back to a reslice.
//
// The UNCAPPED path returns s unchanged: it is the whole string, so its own
// bytes are all it retains and there is nothing to release.
func capBytes(s string, limit int) (string, bool, int) {
	if len(s) <= limit {
		return s, false, 0
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	// A limit smaller than one rune would otherwise return nothing at all; the
	// retained content is still a valid (possibly empty) prefix either way.
	return strings.Clone(s[:cut]), true, len(s) - cut
}
