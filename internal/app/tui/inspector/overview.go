package inspector

import (
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/sidepanel"
)

// overviewSections is the rail's section stack, in render order. It is the
// same list the root builds for the side rail, so Overview and the rail can
// never disagree about what telemetry exists.
//
// The inspector owns its own copy rather than reading the root's *Rail: the
// rail is built from the config's hidden list at construction time, while the
// inspector receives the hidden set per snapshot and must honour a change
// without being rebuilt.
func overviewSections() []sidepanel.Section {
	return []sidepanel.Section{
		sidepanel.SwarmSection{},
		sidepanel.SDDSection{},
		sidepanel.ContextSection{},
		sidepanel.ChangedSection{},
		sidepanel.WorktreesSection{},
		sidepanel.WorkingSetSection{},
		sidepanel.ToolsSection{},
		sidepanel.RulesSection{},
		sidepanel.RepoSection{},
		sidepanel.SkillsSection{},
		sidepanel.SessionSection{},
	}
}

// footerID is the section the rail pins to its bottom edge. Overview renders
// it in place, but keeps the rail's bare-rule introduction so the two surfaces
// read the same.
const footerID = "session"

// liveSections returns the sections that are both relevant to d and not
// hidden. It never writes to d.Hidden: a render that mutated its input would
// surface as a spurious config write.
func liveSections(d Data) []sidepanel.Section {
	all := overviewSections()
	out := make([]sidepanel.Section, 0, len(all))
	for _, s := range all {
		if d.Hidden[s.ID()] {
			continue
		}
		if !s.Relevant(d.Side) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// overviewRows builds the full, unwindowed document: every live section's
// header followed by its expanded body, separated by a blank row.
//
// Sections are rendered at their natural height rather than through the rail's
// fit() collapse. That is the point of the Overview: the rail drops sections
// when the terminal is short, and the inspector exists so the dropped content
// is still reachable — by scrolling, not by being lost.
func overviewRows(d Data, width int) []string {
	if width <= 0 {
		return nil
	}
	sections := liveSections(d)
	rows := make([]string, 0, len(sections)*4)
	for i, s := range sections {
		if i > 0 {
			rows = append(rows, "")
		}
		if s.ID() == footerID {
			// The rail sets its footer off with a blank row and a bare rule
			// because the footer has no title of its own.
			rows = append(rows, "", chrome.Header("", "", width))
		} else {
			rows = append(rows, chrome.Header(s.Title(), "", width))
		}
		rows = append(rows, s.Render(d.Side, width, 0)...)
	}
	// Sections truncate their own rows, but a section that composes a row
	// from styled fragments can still land a cell over. Truncating here is
	// the single guarantee that no row is wider than the frame it is joined
	// into.
	for i, r := range rows {
		rows[i] = ansi.Truncate(r, width, "…")
	}
	return rows
}

// viewOverview renders the Overview tab: the full section document windowed to
// the recorded height at the recorded scroll offset.
func (m *Model) viewOverview(d Data) string {
	rows := overviewRows(d, m.width)
	if len(rows) == 0 {
		return ""
	}
	start := min(max(m.State(TabOverview).Scroll, 0), max(0, len(rows)-m.height))
	end := min(start+m.height, len(rows))

	out := make([]string, 0, m.height)
	out = append(out, rows[start:end]...)
	// Pad to the full height so the dock reserves the rows it was given and
	// the side column joins cleanly against the transcript.
	for len(out) < m.height {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// maxScroll is the largest scroll offset that still shows content: the number
// of rows that do not fit in the recorded height.
func (m *Model) maxScroll(d Data) int {
	if m.width <= 0 || m.height <= 0 {
		return 0
	}
	return max(0, len(overviewRows(d, m.width))-m.height)
}

// Summary is the Overview's compact one-line digest, for a narrow inspector
// that cannot show the full document.
//
// It reuses each section's own OneLine — a genuine summary, not a truncation
// of the body — ordered by section priority so the most important facts
// survive the width cut. Ties keep the rail's render order, so the digest is
// stable across frames.
func (m *Model) Summary(d Data, width int) string {
	if width <= 0 {
		return ""
	}
	sections := liveSections(d)
	sort.SliceStable(sections, func(i, j int) bool {
		return sections[i].Priority() < sections[j].Priority()
	})

	parts := make([]string, 0, len(sections))
	for _, s := range sections {
		if line := strings.TrimSpace(sidepanel.StripANSI(s.OneLine(d.Side, width))); line != "" {
			parts = append(parts, line)
		}
	}
	return ansi.Truncate(strings.Join(parts, " · "), width, "…")
}
