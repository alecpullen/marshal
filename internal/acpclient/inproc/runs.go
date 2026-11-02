package inproc

import (
	"strings"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/repo/gitinfo"
	"marshal/internal/strutil"
)

// subagentsOf projects the session's subagent views onto the wire, leaving out
// the child state. It returns nil for none.
func subagentsOf(views []session.SubagentView) []acpwire.Subagent {
	if len(views) == 0 {
		return nil
	}
	out := make([]acpwire.Subagent, len(views))
	for i, v := range views {
		out[i] = acpwire.Subagent{
			ID:       v.ID,
			Label:    v.Label,
			Status:   subagentStatusOf(v.Status),
			Role:     string(v.Role),
			Model:    v.Model,
			Provider: v.Provider,
			Fallback: v.Fallback,
		}
		if v.Status == session.SubagentRunning && v.Child != nil {
			out[i].Headline = subagentHeadline(v.Child)
		}
		if v.Child != nil {
			out[i].Drill = &acpwire.SubagentDrill{
				ActiveRoot:  v.Child.Workspace().ActiveRoot,
				ActiveRoute: routeOf(v.Child.ActiveRoute()),
				Activity:    activityWire(v.Child.Activity()),
			}
		}
	}
	return out
}

// activityWire is a session activity on the wire.
func activityWire(a session.Activity) acpwire.Activity {
	return acpwire.Activity{
		Kind:      string(a.Kind),
		Label:     a.Label,
		StartedAt: acpwire.Millis(a.StartedAt),
	}
}

// subagentStatusOf maps a session subagent status onto its wire constant.
func subagentStatusOf(s session.SubagentStatus) string {
	switch s {
	case session.SubagentDone:
		return acpwire.SubagentDone
	case session.SubagentFailed:
		return acpwire.SubagentFailed
	default:
		return acpwire.SubagentRunning
	}
}

// subagentHeadline is the first sentence of a child's latest narration: what
// it says it is doing, for the now bar's agent rows.
func subagentHeadline(child *session.State) string {
	if child == nil {
		return ""
	}
	line := strings.TrimSpace(child.LatestNarrationLine())
	if line == "" {
		return ""
	}
	head, _ := strutil.FirstSentence(line)
	return strutil.StripEmphasis(head)
}

// swarmOf copies the session's swarm progress onto the wire.
func swarmOf(p session.SwarmProgress) acpwire.SwarmProgress {
	out := acpwire.SwarmProgress{
		Goal:       p.Goal,
		Active:     p.Active,
		TokensUsed: p.TokensUsed,
		TokensMax:  p.TokensMax,
	}
	if len(p.Roles) > 0 {
		out.Roles = make([]acpwire.SwarmRole, len(p.Roles))
		for i, r := range p.Roles {
			out.Roles[i] = acpwire.SwarmRole{
				Name:      r.Name,
				Status:    string(r.Status),
				Detail:    r.Detail,
				Tokens:    r.Tokens,
				StartedAt: acpwire.Millis(r.StartedAt),
			}
		}
	}
	return out
}

// sddOf copies the session's plan-run progress onto the wire, converting every
// time to Unix milliseconds. A nil slice stays nil.
func sddOf(p session.SDDProgress) acpwire.SDDProgress {
	out := acpwire.SDDProgress{
		Active:         p.Active,
		PlanName:       p.PlanName,
		PlanPath:       p.PlanPath,
		Branch:         p.Branch,
		BaseRef:        p.BaseRef,
		LedgerPath:     p.LedgerPath,
		ArtifactsDir:   p.ArtifactsDir,
		Tasks:          append([]string(nil), p.Tasks...),
		TaskExecTypes:  append([]string(nil), p.TaskExecTypes...),
		TotalTasks:     p.TotalTasks,
		DoneTasks:      p.DoneTasks,
		CurrentTask:    p.CurrentTask,
		Phase:          p.Phase,
		PhaseStartedAt: acpwire.Millis(p.PhaseStartedAt),
		Detail:         p.Detail,
		FixRound:       p.FixRound,
		MaxFixRounds:   p.MaxFixRounds,
		TokensUsed:     p.TokensUsed,
		TokensMax:      p.TokensMax,
		StartedAt:      acpwire.Millis(p.StartedAt),
		Finished:       p.Finished,
		Succeeded:      p.Succeeded,
		EndedAt:        acpwire.Millis(p.EndedAt),
		Error:          p.Error,
	}
	if len(p.TaskTimings) > 0 {
		out.TaskTimings = make([]acpwire.TaskTiming, len(p.TaskTimings))
		for i, t := range p.TaskTimings {
			out.TaskTimings[i] = acpwire.TaskTiming{
				StartedAt: acpwire.Millis(t.StartedAt),
				EndedAt:   acpwire.Millis(t.EndedAt),
			}
		}
	}
	return out
}

// gitOf copies gitinfo.Info onto the wire.
func gitOf(i gitinfo.Info) acpwire.Git {
	return acpwire.Git{
		Branch:   i.Branch,
		Worktree: i.Worktree,
		InRepo:   i.InRepo,
	}
}
