package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
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
	// transcriptSeqListMax caps how many turn numbers the not-found
	// message lists, so a long generation cannot turn an error into a
	// transcript dump of its own.
	transcriptSeqListMax = 12
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
			"generation_seq (sequence 0 is the first generation and is a valid " +
			"value) to read an archived generation instead. Every turn is " +
			"labelled with the turn number printed in its header — pass that " +
			"number back as turn_seq to re-read one turn. For archived turns " +
			"the header number is the same turn number recall_history prints.",
		Schema: json.RawMessage(`{"type":"object","properties":{"generation_seq":{"type":"integer","description":"Read an archived generation by sequence number. Sequence 0 is the first generation of the session and is a valid value; omit this field entirely to read the live current session transcript."},"session_id":{"type":"string","description":"Session to read; defaults to the current session."},"turn_seq":{"type":"integer","description":"Return only the one turn whose header prints this number (the recall_history follow-up case). 0 is valid for archived generations, whose turn numbers start at 0."},"offset":{"type":"integer","description":"Skip this many turns before emitting (paging). Use the offset named in the continuation footer."},"limit":{"type":"integer","description":"Max turns to return (default 20, max 100)."},"max_chars":{"type":"integer","description":"Hard cap on returned characters (default 16000, max 64000)."},"include_all":{"type":"boolean","description":"Live transcript only: also show turns excluded from history replay, such as non-final assistant narration and system notes. Internal markers (compaction, loaded-skill tags and skill bodies) stay hidden."}},"additionalProperties":false}`),
		Risk:   registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			// generation_seq and turn_seq are pointers so an explicit 0 is
			// distinguishable from an omitted field: 0 is the first
			// generation (rollover.Controller.Start opens generation seq
			// 0), and archived turn numbers are 0-based, so a plain int
			// would make both unreachable.
			var args struct {
				GenerationSeq *int   `json:"generation_seq"`
				SessionID     string `json:"session_id"`
				TurnSeq       *int   `json:"turn_seq"`
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

			if args.GenerationSeq != nil {
				seq := *args.GenerationSeq
				if seq < 0 {
					return registry.ToolResult{}, fmt.Errorf("transcript_read: generation_seq %d is invalid (sequence numbers start at 0)", seq)
				}
				if t.db == nil {
					return registry.ToolResult{}, fmt.Errorf("transcript_read: generation_seq %d requested but no database is available for archived generations", seq)
				}
				preamble, turns, err := t.archivedTurns(sessionID, seq)
				if err != nil {
					return registry.ToolResult{}, fmt.Errorf("transcript_read: %w", err)
				}
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

// archivedTurns loads one archived generation straight from the db rows
// and renders the generation header itself. Reading rows (rather than
// splitting history.DumpGeneration's rendering on its turn markers) keeps
// turn content that happens to contain a turn header literal from being
// split into fabricated turns, keeps turns whose body is malformed
// unrepresentable, and preserves each turn's stored TurnSeq so the
// printed header number matches what recall_history prints.
func (t *transcriptTool) archivedTurns(sessionID string, seq int) (string, []transcriptTurn, error) {
	gens, err := t.db.GenerationsForSession(sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("load generations for session %s: %w", sessionID, err)
	}
	var gen *db.Generation
	for i := range gens {
		if gens[i].Seq == seq {
			gen = &gens[i]
			break
		}
	}
	if gen == nil {
		return "", nil, fmt.Errorf("generation seq %d not found in session %s", seq, sessionID)
	}

	rows, err := t.db.TurnsForGeneration(gen.ID)
	if err != nil {
		return "", nil, fmt.Errorf("load generation %d turns: %w", seq, err)
	}
	turns := make([]transcriptTurn, 0, len(rows))
	for _, r := range rows {
		body := r.Content
		if strings.TrimSpace(r.ToolCalls) != "" {
			body += "\n[tool_calls]\n" + strings.TrimRight(r.ToolCalls, "\n")
		}
		turns = append(turns, transcriptTurn{seq: r.TurnSeq, role: r.Role, body: body})
	}
	return generationPreamble(gen, len(rows)), turns, nil
}

// generationPreamble reproduces the header history.DumpGeneration writes
// for a generation, without its trailing blank line (renderTranscript adds
// the separator). Keeping it identical means a transcript_read of an
// archive looks the same as the CLI's transcript dump.
func generationPreamble(gen *db.Generation, turnCount int) string {
	var b strings.Builder
	status := "ended"
	if gen.EndedAt == nil {
		status = "live"
	}
	fmt.Fprintf(&b, "Generation %d (%s)\n", gen.Seq, status)
	fmt.Fprintf(&b, "  Started: %s\n", gen.StartedAt.UTC().Format(time.RFC3339))
	if gen.EndedAt != nil {
		fmt.Fprintf(&b, "  Ended:   %s\n", gen.EndedAt.UTC().Format(time.RFC3339))
	}
	if gen.SeedDigest != "" {
		fmt.Fprintf(&b, "  Digest:  %s\n", gen.SeedDigest)
	}
	fmt.Fprintf(&b, "  Turns:   %d", turnCount)
	return b.String()
}

// transcriptTurn is one renderable turn of a transcript.
type transcriptTurn struct {
	// seq is the turn number printed in the turn's header and the number
	// turn_seq selects by. Live turns are numbered 1..n by position
	// (matching the replayed history window); archived turns carry the
	// TurnSeq their row was stored with, which is 0-based and is the same
	// number recall_history reports for that turn.
	seq int
	// role is the turn's role, as printed beside the turn number.
	role string
	// body is everything under the turn header, excluding the header
	// itself. For live turns it is the message content plus an optional
	// tool-call note; for archived turns it is the archived content plus
	// any [tool_calls] section.
	body string
}

// renderTurn renders one turn in the same shape history.DumpGeneration
// uses, so live and archived reads look alike.
func renderTurn(t transcriptTurn) string {
	return fmt.Sprintf("--- turn %d (%s) ---\n%s", t.seq, t.role, t.body)
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
		out = append(out, transcriptTurn{seq: len(out) + 1, role: string(m.Role), body: body})
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

// renderTranscript pages and truncates a set of turns. It is shared by the
// live and archived paths so both disclose incompleteness the same way.
//
// turnSeq selects a single turn by the number printed in its header (see
// transcriptTurn.seq) and ignores offset and limit. Otherwise offset is a
// 0-based count of turns to skip and limit is the maximum number of turns
// to emit; maxChars is a hard cap on the rendered result. When turns
// remain unshown — because limit was reached, maxChars was hit, or offset
// ran past the end — the model is told, and a continuation footer names
// the offset of the next not-yet-shown turn so it can page without
// re-reading what it already has. The generation preamble, when there is
// one, is returned on every path so the model always knows which
// generation it is reading.
func renderTranscript(preamble string, turns []transcriptTurn, turnSeq *int, offset, limit, maxChars int) string {
	limit = clampTranscriptLimit(limit)
	maxChars = clampTranscriptMaxChars(maxChars)
	if offset < 0 {
		offset = 0
	}

	if turnSeq != nil {
		for i := range turns {
			if turns[i].seq != *turnSeq {
				continue
			}
			block := withPreamble(preamble, renderTurn(turns[i]))
			if len([]rune(block)) > maxChars {
				return strutil.Truncate(block, maxChars, false) + "\n[truncated]"
			}
			return block
		}
		return withPreamble(preamble, fmt.Sprintf("No turn %d in transcript (%d turn(s) available%s).",
			*turnSeq, len(turns), availableSeqs(turns)))
	}

	if len(turns) == 0 {
		if strings.TrimSpace(preamble) != "" {
			return preamble
		}
		return "No turns to show."
	}
	if offset >= len(turns) {
		return withPreamble(preamble, fmt.Sprintf("No turns at offset %d (%d turn(s) total).", offset, len(turns)))
	}

	var b strings.Builder
	runes := 0
	if strings.TrimSpace(preamble) != "" {
		b.WriteString(preamble)
		b.WriteString("\n\n")
		runes = len([]rune(preamble)) + 2
	}

	shown := 0
	for i := offset; i < len(turns) && shown < limit; i++ {
		block := renderTurn(turns[i])
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

// withPreamble prefixes a one-block answer with the generation header, when
// there is one.
func withPreamble(preamble, body string) string {
	if strings.TrimSpace(preamble) == "" {
		return body
	}
	return preamble + "\n\n" + body
}

// availableSeqs renders the turn numbers a transcript holds, bounded by
// transcriptSeqListMax, for the not-found message. It is a pointer into the
// turns' seqs, not their positions, so it names the same numbers the
// headers print.
func availableSeqs(turns []transcriptTurn) string {
	if len(turns) == 0 {
		return ""
	}
	parts := make([]string, 0, transcriptSeqListMax+1)
	for i := range turns {
		if i >= transcriptSeqListMax {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprintf("%d", turns[i].seq))
	}
	return ": " + strings.Join(parts, ", ")
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
