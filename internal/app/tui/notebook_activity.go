package tui

import (
	"fmt"
	"time"

	"charm.land/lipgloss/v2"
	"marshal/internal/activity"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/theme"
	"marshal/internal/strutil"
)

// notebookActivityInput is a value snapshot of only the viewed scope's public
// runtime state. In particular it has no reasoning/thinking text field.
type notebookActivityInput struct {
	activity  session.Activity
	tool      session.ActiveToolCall
	hasTool   bool
	approval  bool
	question  bool
	busy      bool
	children  int
	jobs      int
	narration string
	now       time.Time
}

type notebookActivity struct {
	actor, status, label, detail string
}

func projectNotebookActivity(in notebookActivityInput) notebookActivity {
	switch {
	case in.approval || in.activity.Kind == session.ActivityApproval:
		return notebookActivity{actor: "You", status: "Needs approval", label: boundedActivityLabel(in.activity.Label)}
	case in.question || in.activity.Kind == session.ActivityQuestion:
		return notebookActivity{actor: "You", status: "Needs answer", label: boundedActivityLabel(in.activity.Label)}
	case in.activity.Kind == session.ActivityReconnecting:
		return notebookActivity{actor: "Agent", status: "Reconnecting"}
	case in.hasTool:
		label := boundedActivityLabel(in.tool.Name)
		if in.tool.StartedAt.IsZero() {
			return notebookActivity{actor: "Agent", status: "Running tool", label: label}
		}
		return notebookActivity{actor: "Agent", status: "Running tool", label: label, detail: formatElapsed(max(in.now.Sub(in.tool.StartedAt), 0))}
	case in.busy:
		return notebookActivity{actor: "Agent", status: "Generating", label: boundedActivityLabel(in.narration)}
	case in.children > 0:
		return notebookActivity{actor: "Agent", status: "Working", label: fmt.Sprintf("%d child agent(s) active", in.children)}
	case in.jobs > 0:
		return notebookActivity{actor: "Agent", status: "Idle", label: fmt.Sprintf("%d background job(s) active", in.jobs)}
	default:
		return notebookActivity{}
	}
}

func boundedActivityLabel(s string) string {
	if s == "" {
		return ""
	}
	return strutil.Truncate(s, 48, true)
}

func (m Model) notebookActivitySnapshot() notebookActivityInput {
	viewed, _ := m.conversationSource()
	if viewed == nil {
		viewed = m.state
	}
	var in notebookActivityInput
	in.now = m.now()
	in.busy = false
	if viewed == nil {
		return in
	}
	if viewed == m.state {
		in.jobs = max(m.jobCount, viewed.RunningJobsCount())
		if len(m.jobs) > 0 {
			in.jobs = max(in.jobs, len(m.jobs))
		}
	} else {
		// The job lane cache belongs to the root TUI. A drilled transcript
		// must report only the state owned by that child session.
		in.jobs = viewed.RunningJobsCount()
	}
	in.activity = viewed.Activity()
	in.tool, in.hasTool = viewed.ActiveToolCall()
	in.approval = viewed.PendingApproval() != nil
	in.question = viewed.PendingQuestion() != nil || viewed.PendingChildQuestion() != nil
	if viewed != m.state {
		if child, drilling := m.drilledInto(); drilling && child.Child == viewed {
			if pending := m.state.PendingChildQuestion(); pending != nil && pending.ChildID == child.ID {
				in.question = true
			}
		}
	}
	for _, child := range viewed.Subagents() {
		if child.Status == session.SubagentRunning {
			in.children++
		}
	}
	if in.activity.Kind == session.ActivityThinking {
		snapshot := viewed.ActivitySnapshot()
		for i := len(snapshot.Narrations) - 1; i >= 0; i-- {
			n := snapshot.Narrations[i]
			if n.ResponseID == snapshot.Response.ResponseID && n.Source == activity.SourceModelProse {
				in.narration = n.Text
				break
			}
		}
	}
	if viewed == m.state {
		in.busy = m.busy
	} else {
		progress := viewed.InProgress()
		in.busy = in.hasTool || progress.Active || in.activity.Kind == session.ActivityThinking
	}
	return in
}

func (m Model) renderNotebookActivity() string {
	if !m.notebookView {
		return ""
	}
	a := projectNotebookActivity(m.notebookActivitySnapshot())
	if a.status == "" {
		return ""
	}
	text := a.actor + " · " + a.status
	if a.label != "" {
		text += " · " + a.label
	}
	if a.detail != "" {
		text += " · " + a.detail
	}
	text = strutil.Truncate(text, max(m.leftWidth-2, 1), true)
	return chrome.PaintBand(lipgloss.NewStyle().Foreground(theme.Current().FGMuted).Render(" "+text), m.leftWidth, theme.Current().ChromeBG())
}

func (m Model) notebookActivityRows() int {
	if m.renderNotebookActivity() == "" {
		return 0
	}
	return 1
}
