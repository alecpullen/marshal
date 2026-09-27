package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/history"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

const (
	// transcriptDefaultLimit is the default number of turns returned when
	// neither turn_seq nor limit narrows the request.
	transcriptDefaultLimit = 20
	// transcriptLimitCeiling is the hard maximum for `limit`. A transcript
	// can be arbitrarily long, so the ceiling keeps one call from trying
	// to return an entire long session at once — the same "clamp the
	// model's request" discipline recall_history uses (limit > 20 -> 20).
	transcriptLimitCeiling = 100
	// transcriptMaxChars is the default character budget for a result.
	transcriptMaxChars = 16000
	// transcriptMaxCharsCeiling is the hard maximum for `max_chars`.
	// transcript_read exists to recover context that aged out of the
	// replayed window, so an unbounded result would defeat its own purpose.
	transcriptMaxCharsCeiling = 64000
)

// transcriptTool reads the live session transcript, or an archived
// generation when generation_seq is supplied.
type transcriptTool struct {
	state *session.State
	db    *db.DB
}

// NewTranscriptTool creates a new transcript_read tool wired to the live
// session state and (optionally) the project db used for archived
// generations.
func NewTranscriptTool(state *session.State, database *db.DB) registry.Tool {
	t := &transcriptTool{state: state, db: database}
	return registry.Tool{
		Name: "transcript_read",
		Description: "Read this session's own conversation transcript, including " +
			"turns that have aged out of the replayed history window (collapsed to " +
			"a one-line stub). Reads the live current session by default; pass " +
			"generation_seq to read an archived generation instead. Returns turns " +
			"in the same shape the model recognises as its prior turns (user turns " +
			"and final, non-salvaged assistant answers), with tool-call counts noted.",
		Schema: json.RawMessage(`{"type":"object","properties":{"generation_seq":{"type":"integer","description":"Read an archived generation by seq. Omit to read the live current session transcript."},"session_id":{"type":"string","description":"Session to read; defaults to the current session."},"turn_seq":{"type":"integer","description":"Return only this one turn (the recall_history follow-up case)."},"offset":{"type":"integer","description":"Skip this many turns (paging)."},"limit":{"type":"integer","description":"Max turns to return (default 20)."},"max_chars":{"type":"integer","description":"Hard cap on returned characters (default 16000)."},"include_all":{"type":"boolean","description":"Also show content types excluded from history replay (narration, skill bodies, system markers) in the live transcript."}},"additionalProperties":false}`),
		Risk:   registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				GenerationSeq int    `json:"generation_seq"`
				SessionID     string `json:"session_id"`
				TurnSeq       int    `json:"turn_seq"`
				Offset        int    `json:"offset"`
				Limit         int    `json:"limit"`
				MaxChars      int    `json:"max_chars"`
				IncludeAll    bool   `json:"include_all"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("transcript_read: invalid args: %w", err)
			}

			sessionID := args.SessionID
			if sessionID == "" && t.state != nil {
				sessionID = t.state.SessionID()
			}

			if args.GenerationSeq > 0 {
				if t.db == nil {
					return registry.ToolResult{}, fmt.Errorf("transcript_read: generation_seq %d requested but no database is available for archived generations", args.GenerationSeq)
				}
				dump, err := history.DumpGeneration(ctx, t.db, history.DumpOptions{SessionID: sessionID, GenerationSeq: args.GenerationSeq})
				if err != nil {
					return registry.ToolResult{}, fmt.Errorf("transcript_read: %w", err)
				}
				preamble, turns := parseDumpTurns(dump)
				return registry.ToolResult{Content: renderTranscript(preamble, turns, args.TurnSeq, args.Offset, args.Limit, args.MaxChars)}, nil
			}

			if t.state == nil {
				return registry.ToolResult{}, fmt.Errorf("transcript_read: no live session is available")
			}
			if args.SessionID != "" && args.SessionID != t.state.SessionID() {
				return registry.ToolResult{}, fmt.Errorf("transcript_read: session_id %q requires generation_seq; only the current session's live transcript is readable", args.SessionID)
			}
			turns := liveTurns(t.state.Messages(), args.IncludeAll)
			return registry.ToolResult{Content: renderTranscript("", turns, args.TurnSeq, args.Offset, args.Limit, args.MaxChars)}, nil
		},
	}
}

// transcriptReadTool returns the transcript_read tool wired to the
// toolSet's session state and db.
func (t *toolSet) transcriptReadTool() registry.Tool {
	return NewTranscriptTool(t.sessionState, t.db)
}

// transcriptTurn is one renderable turn of a transcript.
type transcriptTurn struct {
	role string
	// body is everything under the turn header, excluding the header
	// itself. For live turns it is the message content plus an optional
	// tool-call note; for archived turns it is the block rendered by
	// history.DumpGeneration (content plus any [tool_calls] section).
	body string
}

// renderTurn renders one turn in the same shape history.DumpGeneration
// uses, so live and archived reads look alike.
func renderTurn(t transcriptTurn, ordinal int) string {
	return fmt.Sprintf("--- turn %d (%s) ---\n%s", ordinal, t.role, t.body)
}

// liveTurns filters the live session's messages to the turns the model
// would recognise as its own prior turns and renders them. By default
// that matches internal/agent/history.go's buildHistoryMessages replay
// semantics: user turns, plus assistant messages that are Final and not
// Salvaged. includeAll widens this to non-replayed content types
// (notably non-final assistant narration).
func liveTurns(msgs []session.Message, includeAll bool) []transcriptTurn {
	out := make([]transcriptTurn, 0, len(msgs))
	for _, m := range msgs {
		if !transcriptVisible(m, includeAll) {
			continue
		}
		body := m.Content
		if m.ToolCallCount > 0 {
			body += fmt.Sprintf("\n[tool_calls: %d]", m.ToolCallCount)
		}
		out = append(out, transcriptTurn{role: string(m.Role), body: body})
	}
	return out
}

// transcriptVisible reports whether a message is a turn worth showing.
func transcriptVisible(m session.Message, includeAll bool) bool {
	if strings.TrimSpace(m.Content) == "" {
		return false
	}
	if includeAll {
		// Even with include_all, skip content types that are markers
		// rather than turns: a compacted-context marker, a loaded-skill
		// tag, or a skill body the transcript deliberately does not
		// render. None of these read as a turn of conversation.
		switch m.ContentType {
		case session.ContentTypeCompaction,
			session.ContentTypeSkill,
			session.ContentTypeSkillBody,
			session.ContentTypeSkillAuto:
			return false
		}
		return true
	}
	switch m.Role {
	case session.RoleUser:
		return true
	case session.RoleAssistant:
		return m.Final && !m.Salvaged
	}
	return false
}

// parseDumpTurns splits a history.DumpGeneration rendering into its
// preamble (the generation header, everything before the first turn) and
// one transcriptTurn per archived turn. Reusing DumpGeneration's output
// keeps the archived path's rendering identical to the CLI's transcript
// dump instead of reimplementing it.
func parseDumpTurns(dump string) (string, []transcriptTurn) {
	const marker = "--- turn "
	parts := strings.Split(dump, marker)
	if len(parts) <= 1 {
		return strings.TrimRight(dump, "\n"), nil
	}
	preamble := strings.TrimRight(parts[0], "\n")
	turns := make([]transcriptTurn, 0, len(parts)-1)
	for _, p := range parts[1:] {
		// Each block looks like `1 (user) ---\n<content>`; the trailing
		// newline that preceded the next marker stays on the body.
		head, body, ok := strings.Cut(p, " ---\n")
		if !ok {
			head, body, ok = strings.Cut(p, " ---")
			if !ok {
				continue
			}
		}
		role := ""
		if i := strings.Index(head, " ("); i >= 0 && strings.HasSuffix(head, ")") {
			role = head[i+2 : len(head)-1]
		}
		turns = append(turns, transcriptTurn{role: role, body: strings.TrimRight(body, "\n")})
	}
	return preamble, turns
}

// renderTranscript pages and truncates a set of turns. It is shared by the
// live and archived paths so both disclose incompleteness the same way.
//
// offset is a 0-based count of turns to skip; limit is the maximum number
// of turns to emit; turn_seq (1-based) selects a single turn and is bounded
// by construction, so it never emits a continuation footer. maxChars is a
// hard cap on the rendered result. When turns remain unshown — either
// because limit was reached or maxChars was hit — the result ends with a
// continuation footer naming the offset of the next not-yet-shown turn, so
// the model can page without re-reading what it already has.
func renderTranscript(preamble string, turns []transcriptTurn, turnSeq, offset, limit, maxChars int) string {
	limit = clampTranscriptLimit(limit)
	maxChars = clampTranscriptMaxChars(maxChars)
	if offset < 0 {
		offset = 0
	}

	if turnSeq > 0 {
		if turnSeq > len(turns) {
			return fmt.Sprintf("No turn %d in transcript (%d turn(s) available).", turnSeq, len(turns))
		}
		block := renderTurn(turns[turnSeq-1], turnSeq)
		if len([]rune(block)) > maxChars {
			return strutil.Truncate(block, maxChars, false) + "\n[truncated]"
		}
		return block
	}

	if len(turns) == 0 {
		if strings.TrimSpace(preamble) != "" {
			return preamble
		}
		return "No turns to show."
	}

	var b strings.Builder
	runes := 0
	if preamble != "" {
		b.WriteString(preamble)
		b.WriteString("\n\n")
		runes = len([]rune(preamble)) + 2
	}

	shown := 0
	for i := offset; i < len(turns) && shown < limit; i++ {
		block := renderTurn(turns[i], i+1)
		sep := ""
		if shown > 0 {
			sep = "\n\n"
		}
		need := len([]rune(sep)) + len([]rune(block))
		if runes+need > maxChars {
			if shown == 0 {
				// A single turn larger than the whole budget. Emit it
				// truncated (to honour the hard cap) and count it as
				// shown so paging still makes progress rather than
				// looping on the same offset forever.
				room := maxChars - runes
				if room < 0 {
					room = 0
				}
				b.WriteString(strutil.Truncate(block, room, false))
				b.WriteString("\n[truncated]")
				shown++
			}
			break
		}
		b.WriteString(sep)
		b.WriteString(block)
		runes += need
		shown++
	}

	// The footer names the offset of the next turn that was NOT emitted,
	// derived from what was actually written: offset (turns skipped) plus
	// shown (turns emitted).
	if offset+shown < len(turns) {
		fmt.Fprintf(&b, "\n\n[truncated — page with offset=%d]", offset+shown)
	}
	return b.String()
}

func clampTranscriptLimit(limit int) int {
	if limit <= 0 {
		return transcriptDefaultLimit
	}
	if limit > transcriptLimitCeiling {
		return transcriptLimitCeiling
	}
	return limit
}

func clampTranscriptMaxChars(n int) int {
	if n <= 0 {
		return transcriptMaxChars
	}
	if n > transcriptMaxCharsCeiling {
		return transcriptMaxCharsCeiling
	}
	return n
}
