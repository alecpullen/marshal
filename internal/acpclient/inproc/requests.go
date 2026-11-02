package inproc

import (
	"fmt"
	"strings"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/strutil"
	"marshal/internal/viewmodel"
)

// Pending lists every open request a person can answer: the session's own,
// and the approvals of its running subagents. It reads the state and settles
// nothing, so a closed session keeps answering (C4) and a render is safe.
func (s *Session) Pending() acpwire.PendingRequests { return s.open().wire }

// openRequests is what Pending reads, with the state and prompt each request
// answers to. The answer calls find a request by its key here (Task 2).
type openRequests struct {
	approvals []openApproval
	question  *session.PendingQuestion
	skill     *session.PendingSkillGate
	sudo      *session.PendingSudoAuth
	wire      acpwire.PendingRequests
}

type openApproval struct {
	owner *session.State // the state that raised it: the session's, or a subagent's
	call  *session.PendingToolCall
}

// open reads every open request. It is Pending's body, and it refreshes the
// minted keys.
func (s *Session) open() openRequests {
	st := s.state
	var reqs openRequests

	// Every slot is read from the state first, holding no adapter lock: the
	// state takes its own. The approvals come in the order pendingApprovalTarget
	// searches them, every match kept: the session's own, then each running
	// subagent's, in registration order.
	if tc := st.PendingApproval(); tc != nil {
		reqs.approvals = append(reqs.approvals, openApproval{owner: st, call: tc})
		reqs.wire.Approvals = append(reqs.wire.Approvals, approvalRequest(st, tc, 0, ""))
	}
	for _, v := range st.Subagents() {
		if v.Status != session.SubagentRunning || v.Child == nil {
			continue
		}
		tc := v.Child.PendingApproval()
		if tc == nil {
			continue
		}
		reqs.approvals = append(reqs.approvals, openApproval{owner: v.Child, call: tc})
		reqs.wire.Approvals = append(reqs.wire.Approvals, approvalRequest(v.Child, tc, v.ID, v.Label))
	}
	if q := st.PendingQuestion(); q != nil {
		reqs.question = q
		reqs.wire.Question = questionRequest(q)
	}
	if sg := st.PendingSkillGate(); sg != nil {
		reqs.skill = sg
		reqs.wire.SkillGate = skillGateRequest(sg)
	}
	if sa := st.PendingSudoAuth(); sa != nil {
		reqs.sudo = sa
		reqs.wire.Sudo = sudoRequest(sa)
	}
	if req, ok := st.AccessRequest(); ok {
		reqs.wire.Access = accessRequest(req)
	}

	// Then mu once, for the keys and the minted-key book. Only the keys are
	// the adapter's; everything else above is the state's.
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[any]string, len(reqs.approvals)+3)
	for i, a := range reqs.approvals {
		key := a.call.Key
		if a.call.Access != nil {
			// A typed access request sits in the approval slot with no
			// registry key of its own; it carries the request's ID.
			key = session.PromptKey{ID: a.call.Access.RequestID}
		}
		reqs.wire.Approvals[i].Key = s.promptKey(a.call, key, seen)
	}
	if reqs.question != nil {
		reqs.wire.Question.Key = s.promptKey(reqs.question, reqs.question.Key, seen)
	}
	if reqs.skill != nil {
		reqs.wire.SkillGate.Key = s.promptKey(reqs.skill, reqs.skill.Key, seen)
	}
	if reqs.sudo != nil {
		reqs.wire.Sudo.Key = s.promptKey(reqs.sudo, reqs.sudo.Key, seen)
	}
	s.localKeys = seen
	return reqs
}

// promptKey is a prompt's wire key: its registry ID when it has one, else the
// key minted for that pointer. seen collects the pointers open read, so the
// keys of prompts that left can be dropped. Caller holds mu.
func (s *Session) promptKey(prompt any, key session.PromptKey, seen map[any]string) string {
	if key.ID != "" {
		return key.ID
	}
	if minted, ok := s.localKeys[prompt]; ok {
		seen[prompt] = minted
		return minted
	}
	s.localSeq++
	minted := fmt.Sprintf("local-%d", s.localSeq)
	seen[prompt] = minted
	return minted
}

// approvalRequest copies one pending call onto the wire, with the owner of the
// step that asked for it. subagentID and label name the subagent whose child
// raised it; they are zero for the session's own.
func approvalRequest(owner *session.State, tc *session.PendingToolCall, subagentID int64, label string) acpwire.ApprovalRequest {
	actor, why := approvalWhy(owner, tc)
	return acpwire.ApprovalRequest{
		SubagentID: subagentID,
		Subagent:   label,
		ToolCallID: tc.ID,
		Tool:       tc.Name,
		ToolLabel:  viewmodel.DisplayToolName(tc.Name),
		Command:    tc.Command,
		Args:       tc.Args,
		Risk:       tc.Risk,
		Reason:     tc.Reason,
		Diff:       tc.Diff,
		Schema:     tc.Schema,
		Forced:     forcedApproval(tc),
		Access:     tc.Access != nil,
		Actor:      actor,
		Why:        why,
	}
}

// questionRequest copies the agent's clarifying questions onto the wire.
func questionRequest(q *session.PendingQuestion) *acpwire.QuestionRequest {
	out := &acpwire.QuestionRequest{Questions: make([]acpwire.Question, len(q.Questions))}
	for i, question := range q.Questions {
		wire := acpwire.Question{Question: question.Question, Multi: question.Multi}
		if len(question.Options) > 0 {
			wire.Options = make([]acpwire.QuestionOption, len(question.Options))
			for j, option := range question.Options {
				wire.Options[j] = acpwire.QuestionOption{Label: option.Label, Description: option.Description}
			}
		}
		out.Questions[i] = wire
	}
	return out
}

// skillGateRequest copies a skill gate prompt onto the wire.
func skillGateRequest(sg *session.PendingSkillGate) *acpwire.SkillGateRequest {
	return &acpwire.SkillGateRequest{
		Skill:       sg.Skill,
		Description: sg.Description,
		Reason:      sg.Reason,
	}
}

// sudoRequest copies a privileged-auth prompt onto the wire. The attempt count
// is not carried: nothing reads it.
func sudoRequest(sa *session.PendingSudoAuth) *acpwire.SudoRequest {
	return &acpwire.SudoRequest{
		Command:         sa.Command,
		Reason:          sa.Reason,
		StepID:          int64(sa.StepID),
		AllowUnlock:     sa.AllowUnlock,
		Error:           sa.Error,
		CommandApproval: sa.CommandApproval,
	}
}

// accessRequest copies the waiting typed access request onto the wire.
func accessRequest(req session.AccessRequest) *acpwire.AccessRequest {
	return &acpwire.AccessRequest{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Actor:     req.Actor,
		StepID:    int64(req.StepID),
		Reason:    req.Reason,
		Current: acpwire.AccessState{
			Mode:     req.Current.Mode,
			Auto:     req.Current.Auto,
			System:   req.Current.System,
			Revision: req.Current.Revision,
		},
		MissingEditing: req.MissingEditing,
		MissingSystem:  req.MissingSystem,
	}
}

// forcedApproval reports whether only an exact decision may answer tc
// (decision 4): a privileged command, a mode elevation, a cost consent, or a
// prompt the session itself marked exact-only. A session or always answer to
// one of these is a deny.
func forcedApproval(tc *session.PendingToolCall) bool {
	if tc == nil {
		return false
	}
	return tc.Name == "shell.sudo" ||
		tc.Name == "mode.request" ||
		tc.Risk == "model-cost-consent" ||
		tc.ExactCommandApproval ||
		strings.HasPrefix(tc.Reason, "mode-elevation:")
}

// approvalWhy is the owner and the narration headline of the step that asked
// for tc, read from the state that raised it. A step ID is only meaningful in
// the state that issued it, so owner is the state holding the pending call: the
// session's, or a running child's. Both are empty when the step is unknown or
// the call carries no step.
func approvalWhy(owner *session.State, tc *session.PendingToolCall) (actor, why string) {
	if tc == nil || tc.StepID == 0 || owner == nil {
		return "", ""
	}
	step, ok := owner.Step(tc.StepID)
	if !ok {
		return "", ""
	}
	actor = viewmodel.StepOwner(step)
	if head, _ := strutil.FirstSentence(owner.StepNarration(tc.StepID)); head != "" {
		why = strutil.StripEmphasis(head)
	}
	return actor, why
}
