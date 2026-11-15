package fleet

// spawn.go is decision 6's spawn modal (fleet-cli §3.8): the four fields a new
// agent is started with, and the command that starts it.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/interaction"
	"marshal/web/bridge/contract"
)

// noProjectText is what the modal says when the fleet has no project to spawn
// in (decision 6). Nothing is spawned.
const noProjectText = "add a project from the web UI or `marshal daemon pin`"

// recentPromptLimit is how many of a project's recent prompts the modal keeps
// for ctrl+r (decision 6).
const recentPromptLimit = 20

// recentPromptTimeout bounds the modal's one read of the project's prompts. It
// is shorter than actionTimeout because the read happens on the key's own
// turn, so a slow bridge would hold the screen.
const recentPromptTimeout = 5 * time.Second

// spawnFieldWidth is the name and the prompt fields' width in cells, as
// questionFieldWidth is the question modal's.
const spawnFieldWidth = 40

// spawnField is one of the modal's four fields, in tab order.
type spawnField int

const (
	spawnFieldProject spawnField = iota
	spawnFieldName
	spawnFieldMode
	spawnFieldPrompt
	spawnFieldCount
)

// spawnModes are the mode field's choices, in the order space cycles them: the
// project's default, ask, act, and act with auto. auto exists only with act
// (internal/interaction/state.go:59). The first choice carries neither, so the
// request leaves both out and the bridge applies the project's default
// (web/bridge/http.go:511).
var spawnModes = []struct {
	text string
	mode string
	auto bool
}{
	{text: "default"},
	{text: "ask", mode: string(interaction.ModeAsk)},
	{text: "act", mode: string(interaction.ModeAct)},
	{text: "act+auto", mode: string(interaction.ModeAct), auto: true},
}

// spawnModal is decision 6's spawn form: the project, the name, the mode and
// the first prompt, moved through with tab.
type spawnModal struct {
	// projects are the fleet's available projects, in its order, and at is the
	// one the project field shows.
	projects []contract.ProjectStatus
	at       int
	// field is the field the keys go to, and mode the mode field's choice, an
	// index into spawnModes.
	field  spawnField
	mode   int
	name   textfield.Model
	prompt textfield.Model
	// recent is the project's recent prompts, newest first, read once when the
	// modal opened; used is how many ctrl+r presses have taken one.
	recent []string
	used   int
	// refusal is why the form cannot spawn, drawn under its fields.
	refusal string
}

// newSpawnModal builds the modal (decision 6): the fleet's available projects,
// starting on the selected agent's project when it has one.
//
// The recent prompts are read here, once, rather than by a command: an open
// modal is handed keys and nothing else, so a command's answer would have no
// way back to it. The read is bounded by recentPromptTimeout and its failure is
// quiet — ctrl+r then has nothing to cycle, and the prompt can still be typed.
func newSpawnModal(m Model) *spawnModal {
	s := &spawnModal{name: textfield.New(), prompt: textfield.New()}
	s.name.SetWidth(spawnFieldWidth)
	s.name.Placeholder = "unnamed"
	s.prompt.SetWidth(spawnFieldWidth)
	s.prompt.Placeholder = "the first prompt"
	for _, p := range m.state.Projects {
		if p.Available {
			s.projects = append(s.projects, p)
		}
	}
	if row, ok := m.selectedRow(); ok {
		for i, p := range s.projects {
			if p.Root == row.project {
				s.at = i
				break
			}
		}
	}
	if len(s.projects) == 0 {
		s.refusal = noProjectText
		return s
	}
	s.recent = m.recentPrompts(s.project())
	s.focus()
	return s
}

// openSpawn opens the spawn modal (n). While the connection is down it refuses,
// as every other action does (decision 9).
func (m Model) openSpawn() (tea.Model, tea.Cmd) {
	if m.refusedWhileDown() {
		return m, nil
	}
	m.modal = newSpawnModal(m)
	return m, nil
}

// recentPrompts is a project's last prompts, newest first, or none when the
// read fails.
func (m Model) recentPrompts(project string) []string {
	if project == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), recentPromptTimeout)
	defer cancel()
	prompts, err := m.fleet.RecentPrompts(ctx, project, recentPromptLimit)
	if err != nil {
		return nil
	}
	return prompts
}

// update applies one key to the modal. esc closes it, tab moves to the next
// field (or the previous one with shift), j and k choose the project, space
// cycles the mode, ctrl+r in the prompt field cycles the recent prompts, and
// enter on the prompt field spawns.
func (s *spawnModal) update(m Model, msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if msg.Code == tea.KeyEsc {
		m.modal = nil
		return m, nil
	}
	if msg.Code == tea.KeyEnter && len(s.projects) == 0 {
		// There is nothing to spawn in: the modal says so and does nothing.
		s.refusal = noProjectText
		return m, nil
	}
	if msg.Code == tea.KeyTab {
		s.move(msg.Mod.Contains(tea.ModShift))
		return m, nil
	}
	switch s.field {
	case spawnFieldProject:
		switch msg.Code {
		case tea.KeyDown, 'j':
			s.moveProject(1)
		case tea.KeyUp, 'k':
			s.moveProject(-1)
		}
	case spawnFieldMode:
		if msg.Code == tea.KeySpace || msg.Text == " " {
			s.mode = (s.mode + 1) % len(spawnModes)
		}
	case spawnFieldName:
		field, cmd := s.name.Update(msg)
		s.name = field
		return m, cmd
	case spawnFieldPrompt:
		if msg.Code == tea.KeyEnter {
			return s.spawn(m)
		}
		if msg.Mod.Contains(tea.ModCtrl) && msg.String() == "ctrl+r" {
			s.cyclePrompt()
			return m, nil
		}
		field, cmd := s.prompt.Update(msg)
		s.prompt = field
		return m, cmd
	}
	return m, nil
}

// move takes the focus to the next field, or the previous one when back is
// set. The four fields are a ring, so tab from the last comes back to the
// first.
func (s *spawnModal) move(back bool) {
	n := int(spawnFieldCount)
	if back {
		s.field = spawnField((int(s.field) + n - 1) % n)
	} else {
		s.field = spawnField((int(s.field) + 1) % n)
	}
	s.focus()
}

// focus points the text fields at the field the keys go to: the one under the
// cursor is focused, the other blurred.
func (s *spawnModal) focus() {
	if s.field == spawnFieldName {
		s.name.Focus()
	} else {
		s.name.Blur()
	}
	if s.field == spawnFieldPrompt {
		s.prompt.Focus()
	} else {
		s.prompt.Blur()
	}
}

// moveProject moves the project field's cursor over the available projects.
func (s *spawnModal) moveProject(delta int) {
	if len(s.projects) == 0 {
		return
	}
	s.at = clamp(s.at+delta, 0, len(s.projects)-1)
}

// cyclePrompt puts the next of the project's recent prompts in the prompt
// field, newest first, and wraps back to the newest after the last.
func (s *spawnModal) cyclePrompt() {
	if len(s.recent) == 0 {
		return
	}
	s.prompt.SetValue(s.recent[s.used%len(s.recent)])
	s.prompt.CursorEnd()
	s.used++
}

// project is the root of the project field's choice, or "" when it has none.
func (s *spawnModal) project() string {
	if s.at < 0 || s.at >= len(s.projects) {
		return ""
	}
	return s.projects[s.at].Root
}

// spawn builds the request from the fields and starts the agent (decision 6).
func (s *spawnModal) spawn(m Model) (Model, tea.Cmd) {
	if len(s.projects) == 0 {
		s.refusal = noProjectText
		return m, nil
	}
	if m.refusedWhileDown() {
		return m, nil
	}
	m.modal = nil
	return m, m.spawnCmd(s.request())
}

// request is the form's spawn request. The project's default leaves both mode
// and auto out, so the bridge applies the project's own setting.
func (s *spawnModal) request() contract.SpawnRequest {
	req := contract.SpawnRequest{
		Project: s.project(),
		Name:    strings.TrimSpace(s.name.Value()),
		Prompt:  strings.TrimSpace(s.prompt.Value()),
	}
	if choice := spawnModes[clamp(s.mode, 0, len(spawnModes)-1)]; choice.mode != "" {
		req.Mode = choice.mode
		if choice.auto {
			auto := true
			req.Auto = &auto
		}
	}
	return req
}

// spawnCmd spawns the request in a command, bounded by actionTimeout. The
// answer selects the new agent when the next state names it, and reports the
// spawn's warning in Events (decision 6).
func (m Model) spawnCmd(req contract.SpawnRequest) tea.Cmd {
	f := m.fleet
	name := req.Name
	if name == "" {
		name = projectName(req.Project)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		res, err := f.Spawn(ctx, req)
		return actionDoneMsg{what: "spawn", name: name, err: err, selectID: res.AgentID, warning: res.Warning}
	}
}

// projectText is the project field's value: the chosen root, and which one of
// how many when there is more than one.
func (s *spawnModal) projectText() string {
	if len(s.projects) == 0 {
		return "–"
	}
	text := s.project()
	if len(s.projects) > 1 {
		text += fmt.Sprintf("  %d/%d", s.at+1, len(s.projects))
	}
	return text
}

// view draws the form over the right panel: one line per field, with the field
// the keys go to carrying the selection band, and the refusal under them when
// there is nothing to spawn in.
func (s *spawnModal) view(m Model, width, height int) string {
	inner := max(width-4, 1)
	body := []string{
		s.line(m, spawnFieldProject, "project", s.projectText(), "j/k", inner),
		s.line(m, spawnFieldName, "name", s.name.View(), "", inner),
		s.line(m, spawnFieldMode, "mode", spawnModes[clamp(s.mode, 0, len(spawnModes)-1)].text, "space", inner),
		s.line(m, spawnFieldPrompt, "prompt", s.prompt.View(), "ctrl+r recent", inner),
	}
	if s.refusal != "" {
		body = append(body, "")
		for _, line := range wrapText(s.refusal, inner) {
			body = append(body, m.plain(line))
		}
	}
	return m.frame("New agent", "tab next · ⏎ spawn · esc close", body, width, height, true)
}

// line draws one field: its label, its value and the keys it answers, with the
// field the keys go to carrying the selection band.
func (s *spawnModal) line(m Model, f spawnField, label, value, keys string, inner int) string {
	text := label + strings.Repeat(" ", max(labelWidth-len(label), 1)) + value
	if keys != "" {
		text += "  " + keys
	}
	text = cut(text, inner)
	if s.field == f {
		return chrome.SelectionStyle().Render(text)
	}
	return m.plain(text)
}
