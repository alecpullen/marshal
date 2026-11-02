package inproc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/permissions"
	"marshal/internal/tools/registry"
)

// The answer calls settle the requests Pending lists. Each one finds its
// request by the key Pending gave, settles it through the state that raised it
// (the session's own, or a running subagent's child), and reports
// errRequestSettled for a key that names nothing open any more.
//
// The policy the TUI applied moves here unchanged: the always and session
// rules, the forced deny, the skill gate's remembered choice, and the
// rollback's audit row.

// errRequestSettled is what an answer to a request that is no longer open
// returns (C7).
var errRequestSettled = &acpwire.Error{
	Code:    acpwire.CodeRequestSettled,
	Message: "inproc: the request is no longer open",
}

// AnswerApproval answers the approval with that key.
func (s *Session) AnswerApproval(ctx context.Context, key string, answer acpwire.ApprovalAnswer) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	owner, call, ok := s.findApproval(key)
	if !ok {
		return errRequestSettled
	}
	if call.Access != nil {
		// A typed access request borrows the approval slot but settles through
		// its own path, which is the one place allowed to acknowledge a grant.
		return fmt.Errorf("inproc: an access request is answered with AnswerAccess")
	}

	decision := session.UserApprovalDecision{}
	var apply func() error
	switch answer.Decision {
	case acpwire.ApprovalOnce:
		decision = session.UserApprovalDecision{Approved: true, Edited: answer.Edited}
	case acpwire.ApprovalDeny:
		decision = session.UserApprovalDecision{Approved: false}
	case acpwire.ApprovalSession, acpwire.ApprovalAlways:
		decision = session.UserApprovalDecision{Approved: true}
		if forcedApproval(call) {
			// A standing choice must never persist a bypass for a prompt that
			// only accepts an exact decision.
			decision = session.UserApprovalDecision{Approved: false}
			break
		}
		if answer.Decision == acpwire.ApprovalAlways {
			apply = s.alwaysRule(call)
		} else {
			apply = s.sessionRule(call)
		}
	default:
		return fmt.Errorf("inproc: unknown approval decision %q", answer.Decision)
	}
	return s.settleApproval(owner, call, decision, apply)
}

// alwaysRule is the always answer's policy side effect: an allow rule saved to
// the user config and applied to the session's config, or — when the save
// fails — a session rule instead. It is a side effect because a decision that
// lost to an accepted steering cancellation must persist nothing.
func (s *Session) alwaysRule(call *session.PendingToolCall) func() error {
	rule := permissions.Rule{
		Permission: permissions.PermissionForTool(call.Name),
		Pattern:    permissions.PatternForApproval(call),
		Action:     permissions.ActionAllow,
	}
	return func() error {
		// An always answer needs a trustworthy user config directory. Without
		// one the save path would be a relative config.toml in the process's
		// working directory, so the rule stays on the session instead.
		if dir := userConfigDir(); dir != "" {
			err := config.SaveUserConfigRule(filepath.Join(dir, "config.toml"), config.PermissionRule{
				Permission: rule.Permission,
				Pattern:    rule.Pattern,
				Action:     string(rule.Action),
			})
			if err == nil {
				newCfg := s.state.ConfigSnapshot()
				newCfg.Permissions.Rules = append(newCfg.Permissions.Rules, config.PermissionRule{
					Permission: rule.Permission,
					Pattern:    rule.Pattern,
					Action:     string(rule.Action),
				})
				// The installed policy engine reads the owner's live config
				// through State.PolicySnapshot; a runner-local rule mutation
				// would create a second source of truth.
				_, _ = s.state.RebaseInteractionConfig(newCfg)
				return nil
			}
		}
		s.state.AddSessionRule(call.Command)
		return nil
	}
}

// sessionRule is the session answer's policy side effect. The rule lands on
// the session, whichever subagent asked.
func (s *Session) sessionRule(call *session.PendingToolCall) func() error {
	return func() error {
		s.state.AddSessionRule(call.Command)
		return nil
	}
}

// settleApproval settles one approval decision on the state that raised it,
// through the canonical registry when the call registered a key and raw
// Respond otherwise. apply carries policy side effects and runs only for the
// claim that won.
func (s *Session) settleApproval(owner *session.State, call *session.PendingToolCall, decision session.UserApprovalDecision, apply func() error) error {
	if call.Key.ID != "" && owner != nil {
		won, err := owner.ResolveApproval(call.Key, decision, apply)
		if err != nil {
			owner.ClearPrompt(call.Key)
			return err
		}
		if !won {
			return errRequestSettled
		}
		return nil
	}
	// An unregistered legacy fixture owns its own channel, and nothing else can
	// claim that settlement, so its side effect runs unconditionally.
	if apply != nil {
		_ = apply()
	}
	// Settle only the call still in the slot: a replacement opened after the
	// snapshot keeps its own waiter.
	if owner.PendingApproval() == call {
		call.Respond(decision)
		owner.SetPendingApproval(nil)
	}
	return nil
}

// AnswerQuestion answers the question with that key, one answer per question
// in order.
func (s *Session) AnswerQuestion(ctx context.Context, key string, answers []acpwire.Answer) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	reqs := s.open()
	q := reqs.question
	if q == nil || reqs.wire.Question == nil || reqs.wire.Question.Key != key {
		return errRequestSettled
	}
	converted := make([]session.Answer, len(answers))
	for i, a := range answers {
		converted[i] = session.Answer{Question: a.Question, Answer: a.Answer}
	}
	if q.Key.ID != "" {
		if !s.state.ResolveQuestion(q.Key, converted) {
			return errRequestSettled
		}
		return nil
	}
	// Settle only the question still in the slot, as the skill gate does.
	if s.state.PendingQuestion() == q {
		q.Respond(converted)
		s.state.SetPendingQuestion(nil)
	}
	return nil
}

// AnswerSkillGate answers the skill gate with that key, and records an
// allow_skill or allow_all choice for the rest of the session.
func (s *Session) AnswerSkillGate(ctx context.Context, key string, choice acpwire.SkillGateChoice) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	reqs := s.open()
	sg := reqs.skill
	if sg == nil || reqs.wire.SkillGate == nil || reqs.wire.SkillGate.Key != key {
		return errRequestSettled
	}
	var mapped session.SkillGateChoice
	switch choice {
	case acpwire.SkillGateAllowOnce:
		mapped = session.SkillGateAllowOnce
	case acpwire.SkillGateAllowSkill:
		mapped = session.SkillGateAllowSkill
	case acpwire.SkillGateAllowAll:
		mapped = session.SkillGateAllowAll
	case acpwire.SkillGateDeny:
		mapped = session.SkillGateDeny
	default:
		return fmt.Errorf("inproc: unknown skill gate choice %q", choice)
	}
	// The sticky-skill record is a session side effect: it belongs inside the
	// apply callback, so a decision that lost to an accepted steering
	// cancellation never records a bypass for a gate it cancelled.
	apply := func() error {
		switch mapped {
		case session.SkillGateAllowSkill:
			s.state.SkillGateRecordAllow(sg.Skill, false)
		case session.SkillGateAllowAll:
			s.state.SkillGateRecordAllow(sg.Skill, true)
		}
		return nil
	}
	if sg.Key.ID != "" {
		won, err := s.state.ResolveSkillGate(sg.Key, mapped, apply)
		if err != nil {
			s.state.ClearPrompt(sg.Key)
			return err
		}
		if !won {
			return errRequestSettled
		}
		return nil
	}
	_ = apply()
	if s.state.PendingSkillGate() == sg {
		sg.Respond(mapped)
		s.state.SetPendingSkillGate(nil)
	}
	return nil
}

// AnswerSudo answers the sudo prompt with that key. The password is handed to
// the handler as the same slice; nothing here logs, formats or copies it.
func (s *Session) AnswerSudo(ctx context.Context, key string, answer acpwire.SudoAnswer) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	reqs := s.open()
	sa := reqs.sudo
	if sa == nil || reqs.wire.Sudo == nil || reqs.wire.Sudo.Key != key {
		return errRequestSettled
	}
	decision := session.SudoAuthDecision{OK: answer.OK, Password: answer.Password}
	if sa.Key.ID != "" {
		if !s.state.ResolveSudoAuth(sa.Key, decision) {
			return errRequestSettled
		}
		return nil
	}
	// Settle only the prompt still in the slot, as the skill gate does.
	if s.state.PendingSudoAuth() == sa {
		sa.Respond(decision)
		s.state.SetPendingSudoAuth(nil)
	}
	return nil
}

// ReconcileAccess settles the typed access request that no longer asks for
// anything, then returns the one the access panel should present, if any.
func (s *Session) ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool) {
	if s.closed.Load() {
		return acpwire.AccessRequest{}, false
	}
	if s.accessRequest == nil || s.resolveAccess == nil {
		// Without the seam a request could never be answered: settle it as
		// failed rather than present one the person cannot resolve.
		s.failAccess()
		return acpwire.AccessRequest{}, false
	}
	req, ok := s.accessRequest()
	if !ok || req.SessionID != s.state.SessionID() {
		// A request another session owns is not this session's to present.
		return acpwire.AccessRequest{}, false
	}
	wire := accessRequest(req)
	if wire == nil {
		return acpwire.AccessRequest{}, false
	}
	return *wire, true
}

// AnswerAccess answers the access request the choice names.
func (s *Session) AnswerAccess(ctx context.Context, choice acpwire.AccessChoice) (acpwire.AccessResult, error) {
	if s.closed.Load() {
		return acpwire.AccessResult{}, errSessionClosed
	}
	if s.accessRequest == nil || s.resolveAccess == nil {
		s.failAccess()
		return acpwire.AccessResult{}, fmt.Errorf("inproc: no access resolver")
	}
	result, err := s.resolveAccess(session.AccessChoice{
		RequestID: choice.RequestID,
		SessionID: choice.SessionID,
		Current: session.AccessState{
			Mode:     choice.Current.Mode,
			Auto:     choice.Current.Auto,
			System:   choice.Current.System,
			Revision: choice.Current.Revision,
		},
		Approved: choice.Approved,
		Mode:     choice.Mode,
	})
	return accessResultWire(result), err
}

// Rollback restores the last backup and logs the rollback as an audit row in
// the transcript, failed or not.
func (s *Session) Rollback(ctx context.Context) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	// The error is load-bearing: a partial rollback leaves a mixed working
	// tree, and reporting success would hide that from both the user and the
	// audit trail.
	ev := registry.AuditEvent{
		Timestamp:     time.Now(),
		ToolName:      "rollback",
		ResultSummary: "Rollback applied successfully",
	}
	err := s.state.RollbackBackup()
	if err != nil {
		ev.Error = err.Error()
		ev.ResultSummary = "Rollback failed"
	}
	s.state.LogToolCall(ev)
	return err
}

// findApproval returns the state that raised the open approval with that key,
// the call itself, and whether one was found at all.
func (s *Session) findApproval(key string) (*session.State, *session.PendingToolCall, bool) {
	reqs := s.open()
	for i, a := range reqs.approvals {
		if reqs.wire.Approvals[i].Key == key {
			return a.owner, a.call, true
		}
	}
	return nil, nil, false
}

// failAccess settles the state's typed access request as failed, for a session
// that cannot answer one: an unanswered request would block its tool forever.
func (s *Session) failAccess() {
	req, ok := s.state.AccessRequest()
	if !ok {
		return
	}
	s.state.CancelAccess(req.RequestID, session.AccessFailed, "Access resolver unavailable")
}

// accessResultWire copies an access result onto the wire.
func accessResultWire(r session.AccessResult) acpwire.AccessResult {
	return acpwire.AccessResult{
		RequestID: r.RequestID,
		SessionID: r.SessionID,
		Status:    string(r.Status),
		Effective: acpwire.AccessState{
			Mode:     r.Effective.Mode,
			Auto:     r.Effective.Auto,
			System:   r.Effective.System,
			Revision: r.Effective.Revision,
		},
		Message: r.Message,
	}
}

// userConfigDir returns the absolute path to the user-level Marshal
// config directory (typically ~/.config/marshal). The path is
// symlink-resolved and verified to be under the user's home directory
// to defend against an attacker-controlled $HOME pointing at a
// sensitive location (e.g. /root, /etc). See F-SEC-27.
func userConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return ""
	}
	resHome, err := filepath.EvalSymlinks(absHome)
	if err != nil {
		resHome = absHome
	}
	cfgDir := filepath.Join(resHome, ".config", "marshal")
	// If the config dir doesn't exist yet, the parent path is what
	// matters for the trust check.
	resCfg, err := filepath.EvalSymlinks(cfgDir)
	if err != nil {
		resCfg = filepath.Dir(cfgDir)
	}
	rel, err := filepath.Rel(resHome, resCfg)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		slog.Default().Warn("userConfigDir outside home; refusing",
			"home", resHome, "configDir", resCfg)
		return ""
	}
	return cfgDir
}
