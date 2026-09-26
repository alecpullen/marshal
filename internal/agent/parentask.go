package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// SubagentRef identifies the asking child to the parent.
type SubagentRef struct {
	ID   int64
	Desc string
}

// Built-in defaults for the parent/child question bridge, matching the
// [agent] config comments and config.Default(): a child waits at most 5
// minutes for its parent to answer and may ask at most 3 questions per
// task. A zero (or negative) configured value falls back to these at the
// app wiring seam (internal/app/app.go) — 0 means "default", never
// "disabled".
const (
	DefaultParentQuestionTimeout    = 5 * time.Minute
	DefaultParentQuestionMaxPerTask = 3
)

// ParentQuestioner is the child-facing seam: the child can only ask, never
// read the parent State or its transcript directly. The concrete impl wraps
// the parent's session.State (ad-hoc agent.run) or the pipeline owner (SDD).
type ParentQuestioner interface {
	AskParent(ctx context.Context, from SubagentRef, q []session.Question) ([]session.Answer, error)
}

// stateParentQuestioner adapts a parent's session.State to ParentQuestioner.
// It publishes a PendingChildQuestion, then blocks on the buffered
// ResponseChan until answered, timed out, or the parent session ends.
type stateParentQuestioner struct {
	parent  *session.State
	timeout time.Duration
}

// NewStateParentQuestioner builds the ad-hoc agent.run implementation of the
// ParentQuestioner seam. A non-positive timeout disables the parent-side
// deadline (the child-side parent.ask tool still applies its own).
func NewStateParentQuestioner(parent *session.State, timeout time.Duration) ParentQuestioner {
	return &stateParentQuestioner{parent: parent, timeout: timeout}
}

func (s *stateParentQuestioner) AskParent(ctx context.Context, from SubagentRef, q []session.Question) ([]session.Answer, error) {
	if s.parent == nil {
		return nil, errors.New("parent no longer available")
	}
	// Buffered cap 1: PendingChildQuestion.Respond uses a non-blocking send,
	// so an unbuffered channel would silently drop a late answer after the
	// child timed out while close still fired.
	ch := make(chan []session.Answer, 1)
	pending := &session.PendingChildQuestion{
		ChildID:      from.ID,
		ChildDesc:    from.Desc,
		Questions:    q,
		ResponseChan: ch,
	}
	// Queue, not slot: concurrent children append behind one another and a
	// child's teardown resolves only its own question, so a timed-out child
	// can never wipe a still-waiting sibling's question (spec §7).
	s.parent.PushChildQuestion(pending)
	defer s.parent.ResolveChildQuestion(pending)

	// Also push a synthetic subagent report so a LIVE parent loop sees the
	// question at its next loop-top. It rides the report queue rather than
	// steering precisely so a turn-cancel (ClearSteering) cannot drop it.
	// The questions themselves are included verbatim — the parent must be
	// able to answer from context without a second round-trip.
	s.parent.PushSubagentReport(formatChildQuestionReport(from, q))

	var timeout <-chan time.Time
	if s.timeout > 0 {
		timer := time.NewTimer(s.timeout)
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case answers, ok := <-ch:
		if !ok {
			return nil, errors.New("parent no longer available")
		}
		return answers, nil
	case <-timeout:
		return nil, fmt.Errorf("parent did not answer within %s; proceed with best judgement and note the assumption", s.timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.parent.Context().Done():
		return nil, errors.New("parent session ended")
	}
}

// formatChildQuestions renders the question list a child asked, numbered,
// one per line, with options folded in. Shared by the synthetic subagent
// report and the loop-top hint so both carry the question text verbatim —
// the parent must be able to answer from context, not answer blind.
func formatChildQuestions(q []session.Question) string {
	var b strings.Builder
	for i, question := range q {
		fmt.Fprintf(&b, "%d. %s", i+1, question.Question)
		if len(question.Options) > 0 {
			labels := make([]string, 0, len(question.Options))
			for _, o := range question.Options {
				labels = append(labels, o.Label)
			}
			fmt.Fprintf(&b, " (options: %s)", strings.Join(labels, ", "))
		}
		if question.Multi {
			b.WriteString(" [multiple may be chosen]")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// formatChildQuestionReport builds the synthetic subagent report that
// announces a child's parent.ask to a live parent loop at its next
// loop-top. The questions are included verbatim so the parent can answer
// from context without a second round-trip to the child.
func formatChildQuestionReport(from SubagentRef, q []session.Question) string {
	return fmt.Sprintf(
		"[subagent %d asked] %s — blocked on your answer:\n%sAnswer with parent.respond (one answer per question, same order), or escalate to the user with question.ask and relay their answer.",
		from.ID, from.Desc, formatChildQuestions(q))
}

// parentAskConfig holds the parent.ask tool's dependencies. It is built from
// functional options so the tool can be constructed in tests without a
// session.State.
type parentAskConfig struct {
	pq         ParentQuestioner
	timeout    time.Duration
	maxPerTask int
	ref        SubagentRef
}

// ParentAskOption configures the parent.ask tool.
type ParentAskOption func(*parentAskConfig)

// WithParentAskRef sets the asking child's own identity, so the parent can
// attribute the question to a specific subagent. Defaults to the zero ref
// (ID 0, empty description), which is what unit tests want.
func WithParentAskRef(id int64, desc string) ParentAskOption {
	return func(c *parentAskConfig) {
		c.ref = SubagentRef{ID: id, Desc: desc}
	}
}

// parentAskArgs mirrors the question.ask payload so a child can ask with the
// same shape it already knows.
type parentAskArgs struct {
	Questions []session.Question `json:"questions"`
}

// NewParentAskTool builds the child-side parent.ask tool. The child can only
// ask through the ParentQuestioner seam; it never sees the parent's State.
//
// timeout is a belt-and-braces guard on top of whatever timeout the
// ParentQuestioner implementation applies: if the implementation itself
// hangs (a pipeline owner that does not honour deadlines), the tool still
// returns an actionable error instead of wedging the child's turn.
// maxPerTask caps how many questions one child task may ask; 0 disables the
// cap. Only one question may be outstanding at a time.
func NewParentAskTool(pq ParentQuestioner, timeout time.Duration, maxPerTask int, opts ...ParentAskOption) registry.Tool {
	cfg := parentAskConfig{pq: pq, timeout: timeout, maxPerTask: maxPerTask}
	for _, o := range opts {
		o(&cfg)
	}

	// asked/inFlight are per-tool-closure state: one tool instance belongs to
	// exactly one child task, so the cap is per-task by construction.
	var (
		mu       sync.Mutex
		asked    int
		inFlight bool
	)

	tool := registry.Tool{
		Name: "parent.ask",
		Description: "Ask the parent agent a clarifying question when a decision would materially change the outcome and you cannot resolve it from the task description or the repository. " +
			"Use this only when you are genuinely blocked: the parent may answer from context, or escalate to the user, and either way it costs a round-trip. " +
			"If the question has a known set of choices, pass them in the options field. " +
			"If the parent does not answer in time you will be told to proceed with your best judgement and note the assumption you made — do that rather than asking again. " +
			"Example: {\"questions\":[{\"question\":\"Pick one\",\"options\":[\"simple\",{\"label\":\"rich\",\"description\":\"with explanation\"}]}]}",
		Schema: json.RawMessage(`{"type":"object","properties":{"questions":{"type":"array","items":{"type":"object","properties":{"question":{"type":"string"},"options":{"type":"array","items":{"anyOf":[{"type":"string"},{"type":"object","properties":{"label":{"type":"string"},"description":{"type":"string"}},"required":["label"],"additionalProperties":false}]}},"multi":{"type":"boolean"}},"required":["question"],"additionalProperties":false}}},"required":["questions"],"additionalProperties":false}`),
		Risk:   registry.RiskReadOnly,
	}

	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args parentAskArgs
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode parent.ask arguments: %w", err)
		}
		if len(args.Questions) == 0 {
			return registry.ToolResult{}, fmt.Errorf("at least one question is required")
		}

		mu.Lock()
		if inFlight {
			mu.Unlock()
			return registry.ToolResult{}, fmt.Errorf("a parent.ask is already outstanding; wait for its answer before asking again")
		}
		if cfg.maxPerTask > 0 && asked >= cfg.maxPerTask {
			mu.Unlock()
			return registry.ToolResult{}, fmt.Errorf("parent.ask budget exhausted (%d per task); proceed with best judgement and note the assumption", cfg.maxPerTask)
		}
		inFlight = true
		asked++
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight = false
			mu.Unlock()
		}()

		if cfg.pq == nil {
			return registry.ToolResult{}, fmt.Errorf("parent no longer available")
		}

		// The call runs on its own goroutine so the tool-level timeout can
		// fire even if the implementation ignores deadlines. done is buffered
		// so an abandoned goroutine still exits cleanly instead of leaking.
		type askResult struct {
			answers []session.Answer
			err     error
		}
		done := make(chan askResult, 1)
		go func() {
			a, err := cfg.pq.AskParent(ctx, cfg.ref, args.Questions)
			done <- askResult{answers: a, err: err}
		}()

		var res askResult
		if cfg.timeout > 0 {
			timer := time.NewTimer(cfg.timeout)
			defer timer.Stop()
			select {
			case res = <-done:
			case <-timer.C:
				return registry.ToolResult{}, fmt.Errorf("parent did not answer within %s; proceed with best judgement and note the assumption", cfg.timeout)
			case <-ctx.Done():
				return registry.ToolResult{}, ctx.Err()
			}
		} else {
			select {
			case res = <-done:
			case <-ctx.Done():
				return registry.ToolResult{}, ctx.Err()
			}
		}

		if res.err != nil {
			return registry.ToolResult{}, res.err
		}

		parts := []string{"The parent answered:"}
		for _, a := range res.answers {
			parts = append(parts, fmt.Sprintf("%q=%q", a.Question, a.Answer))
		}
		return registry.ToolResult{
			Summary: "parent answered",
			Content: strings.Join(parts, "\n"),
		}, nil
	}
	return tool
}

// formatAnsweredNotice renders the user-visible record of a question the
// parent answered on the user's behalf. A child question is normally answered
// by the parent from context, so the user must be able to see what was decided
// for them and correct it in their next message — the parent then pushes that
// correction into the live child via parent.correct.
func formatAnsweredNotice(pending *session.PendingChildQuestion, answers []session.Answer) string {
	pairs := make([]string, 0, len(answers))
	for _, a := range answers {
		pairs = append(pairs, fmt.Sprintf("%q → %q", a.Question, a.Answer))
	}
	return fmt.Sprintf(
		"subagent %d (%s) asked %s; I answered on your behalf. Reply with a correction if that is wrong and I will push it to the subagent.",
		pending.ChildID, pending.ChildDesc, strings.Join(pairs, ", "),
	)
}

// parentCorrectArgs carries a user override for an answer the parent already
// gave a child.
type parentCorrectArgs struct {
	ChildID    int64  `json:"child_id"`
	Correction string `json:"correction"`
}

// NewParentCorrectTool builds the parent-side override affordance: it turns a
// user's correction into a steering message on the LIVE child's own state, so
// the child sees it at its next loop-top exactly like a user steering message.
// Reusing PushSteering means the correction rides the existing, already-tested
// steering path rather than a new delivery mechanism.
func NewParentCorrectTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name: "parent.correct",
		Description: "Push a correction into a subagent that is still running, after you already answered its parent.ask question. " +
			"Use this when the user replies that your answer was wrong. The correction is delivered to the child as steering at its next step, so phrase it as an instruction (e.g. \"use Postgres, not SQLite\"). " +
			"Only a running subagent can be corrected; a finished one has stopped and cannot receive it.",
		Schema: json.RawMessage(`{"type":"object","properties":{"child_id":{"type":"integer","description":"The subagent id shown on its card, e.g. 3 for \"subagent 3\"."},"correction":{"type":"string","description":"The correction to deliver, phrased as an instruction to the subagent."}},"required":["child_id","correction"],"additionalProperties":false}`),
		Risk:   registry.RiskReadOnly,
	}
	tool.Handler = func(_ context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args parentCorrectArgs
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode parent.correct arguments: %w", err)
		}
		correction := strings.TrimSpace(args.Correction)
		if correction == "" {
			return registry.ToolResult{}, errors.New("correction must not be empty")
		}
		view, ok := state.Subagent(args.ChildID)
		if !ok {
			return registry.ToolResult{}, fmt.Errorf("unknown subagent %d", args.ChildID)
		}
		if view.Child == nil {
			return registry.ToolResult{}, fmt.Errorf("subagent %d has no live session to correct", args.ChildID)
		}
		// A finished child no longer drains its steering queue, so accepting
		// the correction would silently drop it and leave the user believing
		// it landed.
		if view.Status != session.SubagentRunning {
			return registry.ToolResult{}, fmt.Errorf("subagent %d is no longer running; a correction cannot reach a finished subagent", args.ChildID)
		}
		view.Child.PushSteering("[parent correction] " + correction)
		return registry.ToolResult{
			Summary: fmt.Sprintf("corrected subagent %d", args.ChildID),
			Content: fmt.Sprintf("Pushed the correction to subagent %d (%s); it will see it at its next step.", args.ChildID, view.Label),
		}, nil
	}
	return tool
}

// parentRespondArgs mirrors the question.ask answer shape (and session.Answer)
// so the parent can supply answers with the same field names the client uses.
// child_id is an optional guard: when non-zero it must name the subagent whose
// question is at the HEAD of the queue, so answers can never be delivered to
// the wrong sibling after a queue rotation.
type parentRespondArgs struct {
	ChildID int64            `json:"child_id"`
	Answers []session.Answer `json:"answers"`
}

// NewParentRespondTool builds the parent-side answer affordance, the mirror
// of parent.ask. It is registered on the MAIN agent only: a child has no
// pending child question to answer. The handler delivers the answers to the
// subagent whose question is at the HEAD of the FIFO queue (spec §7): one
// answer per question it asked, in the same order.
func NewParentRespondTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name: "parent.respond",
		Description: "Answer a question asked by a background subagent via parent.ask. " +
			"A subagent is blocked until you answer, time out, or escalate — so answer as soon as you can, from the task description and the repository where the answer is already determined. " +
			"If you genuinely cannot decide without the user, ask them with question.ask first, then relay their answer here. " +
			"Subagent questions queue FIFO; you are always answering the head question, and this tool tells you when nothing is waiting.",
		Schema: json.RawMessage(`{"type":"object","properties":{"child_id":{"type":"integer","description":"Optional guard: the subagent id whose question you intend to answer (the queue head). The call is rejected if it does not match, so answers can never be delivered to the wrong subagent after the queue rotates."},"answers":{"type":"array","items":{"type":"object","properties":{"question":{"type":"string"},"answer":{"type":"string"}},"required":["question","answer"],"additionalProperties":false},"description":"One answer per question the head subagent asked, in the same order."}},"required":["answers"],"additionalProperties":false}`),
		Risk:   registry.RiskReadOnly,
	}
	tool.Handler = func(_ context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args parentRespondArgs
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode parent.respond arguments: %w", err)
		}
		if len(args.Answers) == 0 {
			return registry.ToolResult{}, errors.New("at least one answer is required")
		}
		pending := state.PendingChildQuestion()
		if pending == nil {
			return registry.ToolResult{
				Summary: "no subagent waiting",
				Content: "No subagent is waiting on a question right now; nothing was sent.",
			}, nil
		}
		if args.ChildID != 0 && args.ChildID != pending.ChildID {
			return registry.ToolResult{}, fmt.Errorf("subagent %d is not the queue head (subagent %d is); refusing to deliver answers to the wrong subagent", args.ChildID, pending.ChildID)
		}
		// One answer per asked question, same order. Validated BEFORE Respond
		// fires: Respond is sync.Once-guarded, so a short delivery could never
		// be corrected afterwards.
		if len(args.Answers) != len(pending.Questions) {
			return registry.ToolResult{}, fmt.Errorf("subagent %d asked %d question(s) but %d answer(s) were given; supply exactly one answer per question, in the same order", pending.ChildID, len(pending.Questions), len(args.Answers))
		}
		// Respond is sync.Once-guarded and non-blocking, so a second call (or a
		// call racing the child's timeout teardown) is a harmless no-op.
		pending.Respond(args.Answers)
		// The answered question leaves the queue so the next queued child
		// becomes the head. (Resolve is identity-guarded, so a racing teardown
		// that already removed it changes nothing.)
		state.ResolveChildQuestion(pending)
		// Surface the decision in the transcript. The RoleSystem voice is
		// deliberate: this is informational for the user, not a machine report
		// that should replay into model context as a user message.
		state.AddMessage(session.RoleSystem, formatAnsweredNotice(pending, args.Answers), session.ContentTypePlain)
		parts := []string{fmt.Sprintf("Answered subagent %d:", pending.ChildID)}
		for _, a := range args.Answers {
			parts = append(parts, fmt.Sprintf("%q=%q", a.Question, a.Answer))
		}
		if remaining := len(state.ChildQuestions()) - 1; remaining > 0 {
			parts = append(parts, fmt.Sprintf("%d more subagent question(s) are queued; the next head is answerable now.", remaining))
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("answered subagent %d", pending.ChildID),
			Content: strings.Join(parts, "\n"),
		}, nil
	}
	return tool
}
