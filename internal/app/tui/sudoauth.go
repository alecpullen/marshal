package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/strutil"
)

// sudoAuthModel renders the privileged-command password prompt inline in the
// input area. The command is shown verbatim so the user authorises exactly
// what will run; the password is masked (textinput.EchoPassword, the same
// primitive the provider connect flow uses at connect.go:465). Enter submits,
// Esc denies — a dismissed prompt never executes.
type sudoAuthModel struct {
	sa    acpwire.SudoRequest
	input textfield.Model
	width int
	// review is the bounded, pageable window over the command and the context
	// the user authorises. The password never enters it: it lives only in the
	// masked field and in Decision().
	review promptReview
	// bodyHeight is how many display rows the review may occupy. The parent
	// Model computes it from the frame budget; zero means the body does not
	// fit, and the prompt is then not actionable.
	bodyHeight int
	done       bool
	denied     bool
}

// sudoControlRows is how many pinned rows the sudo prompt renders below its
// review body: the overflow hint, the masked field and its key hint.
//
// The hint row is reserved even when nothing is omitted (it then renders
// blank), so paging cannot change the prompt's height — and therefore cannot
// make the row budget disagree with what is drawn.
func sudoControlRows() int { return 3 }

func newSudoAuthModel(sa acpwire.SudoRequest, width int) *sudoAuthModel {
	ti := textfield.New()
	ti.EchoMode = textinput.EchoPassword
	ti.SetVirtualCursor(true)
	ti.Focus()
	ti.Placeholder = "sudo password"
	return &sudoAuthModel{sa: sa, input: ti, width: max(width, 30), bodyHeight: defaultApprovalBodyHeight}
}

func (m *sudoAuthModel) SetSize(width int) { m.width = max(width, 30) }

// SetBodyHeight records how many review rows the frame can give this prompt.
// Zero means the body does not fit: the prompt renders its pinned controls
// only and accepts no decision (see Model.sudoActionable).
func (m *sudoAuthModel) SetBodyHeight(height int) {
	m.bodyHeight = max(height, 0)
}

// contentWidth is the width the review and its pinned rows wrap to: the
// console already spends gutterWidth cells on the prompt's own gutter.
func (m *sudoAuthModel) contentWidth() int {
	return max(m.width-gutterWidth, 1)
}

func (m *sudoAuthModel) Update(msg tea.Msg) (*sudoAuthModel, tea.Cmd) {
	if m.done {
		return m, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "enter":
			m.done = true
			return m, nil
		case "esc":
			m.done = true
			m.denied = true
			return m, nil
		}
	}
	// Everything else (including paste) goes to the masked field.
	ti, cmd := m.input.Update(msg)
	m.input = ti
	return m, cmd
}

// Decision returns the response to send. A denied prompt carries no password.
func (m *sudoAuthModel) Decision() acpwire.SudoAnswer {
	if m.denied {
		return acpwire.SudoAnswer{OK: false}
	}
	return acpwire.SudoAnswer{OK: true, Password: []byte(m.input.Value())}
}

func (m *sudoAuthModel) IsDone() bool { return m.done }

// reviewLines builds the reviewable context of the privileged command, as
// logical lines. The password is deliberately absent — it exists only in the
// masked field and in Decision() — and every field that is present is model-
// or guardrail-written, so its control characters are shown as visible
// escapes: a terminal acting on one could erase or redraw part of a command
// the user is about to run as root.
func (m *sudoAuthModel) reviewLines() []string {
	lines := make([]string, 0, 8)
	// The console header already says "⚠ Sudo password"; the title stays the
	// first body line so the prompt still reads as one block.
	lines = append(lines, "Run as root?")
	// The command is shown verbatim — the user authorises the literal text —
	// except that its controls are escaped. Its newlines are real line breaks
	// in the command, so they stay line breaks in the review.
	if command := strutil.ShowControls(m.sa.Command); command != "" {
		lines = append(lines, strings.Split(command, "\n")...)
	}
	// The model wrote the reason and the error too, so they get the same
	// treatment. Each segment is styled on its own: promptReviewRows splits on
	// newlines anyway, and one opened style would then cover only its first
	// segment.
	if reason := strutil.ShowControls(m.sa.Reason); reason != "" {
		for _, line := range strings.Split(reason, "\n") {
			lines = append(lines, mutedStyle().Render(line))
		}
	}
	if failure := strutil.ShowControls(m.sa.Error); failure != "" {
		for _, line := range strings.Split(failure, "\n") {
			lines = append(lines, warningStyle().Render(line))
		}
	}
	if m.sa.CommandApproval {
		lines = append(lines, mutedStyle().Render("This prompt also approves the exact command above."))
	}
	if m.sa.StepID != 0 {
		lines = append(lines, mutedStyle().Render(fmt.Sprintf("Requested by step %d.", m.sa.StepID)))
	}
	if m.sa.AllowUnlock {
		lines = append(lines, mutedStyle().Render("unlock is on: sudo's own timestamp grace applies (Defaults timestamp_timeout)"))
	}
	return lines
}

// reviewOmits reports whether the review window leaves rows unseen, i.e.
// whether the "rows x–y of n" indicator (or, for a one-row body, the pinned
// hint) is telling the user to page.
func (m *sudoAuthModel) reviewOmits(total int) bool {
	switch {
	case m.bodyHeight <= 0:
		return total > 0
	case m.bodyHeight == 1:
		return total > 1
	default:
		return total > m.bodyHeight-1
	}
}

// overflowHintRow is the pinned row that tells the user the body continues. It
// is reserved unconditionally and renders blank when everything is visible.
func (m *sudoAuthModel) overflowHintRow(total int) string {
	if !m.reviewOmits(total) {
		return ""
	}
	return mutedStyle().Render("PgUp/PgDn to review the rest")
}

func (m *sudoAuthModel) View() string {
	gutter := gutterPrefix(glyph.Question, warningColor)
	indent := strings.Repeat(" ", 3)

	rows := promptReviewRows(m.reviewLines(), m.contentWidth())
	lines := m.review.Window(rows, m.bodyHeight)
	// Pinned below the body, top to bottom: the overflow hint (blank when
	// nothing is omitted), the masked field, then its key hint. Reserving the
	// hint keeps the prompt's height constant as the user pages, and pinning
	// the field keeps password entry visible while they review the command.
	// The field's value is never part of the review body.
	lines = append(lines, m.overflowHintRow(len(rows)), m.input.View(), mutedStyle().Render("[↵] authenticate  [Esc] deny"))

	var b strings.Builder
	for i, line := range lines {
		if i == 0 {
			b.WriteString(gutter)
		} else {
			b.WriteString(indent)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	// No rail: the console draws the gutter around this section. Sudo keeps
	// its own key row, so its header carries no right label.
	return b.String()
}

// renderSudoAuthPanel is the fallback static panel shown before the
// interactive model is built (mirrors renderSkillGatePanel's role).
func renderSudoAuthPanel(sa acpwire.SudoRequest, width int) string {
	m := newSudoAuthModel(sa, width)
	return m.View()
}
