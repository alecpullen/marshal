package plugins

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/textfield"
	plugs "marshal/internal/plugins"
)

// Panel is the TUI management surface for installed plugins.
type Panel struct {
	homeDir        string
	workDir        string
	projectTrusted bool

	filter textfield.Model
	list   *settings.FieldList
	stack  []*settings.Frame

	installSource   string
	installScope    string
	scannedContents *plugs.Contents
	scannedName     string
	scannedSource   string
	scanDir         string
	removeArmed     map[string]bool
}

var _ dock.Panel = (*Panel)(nil)

// NewPanel builds a plugins panel.
func NewPanel(homeDir, workDir string, projectTrusted bool) *Panel {
	p := &Panel{
		homeDir:        homeDir,
		workDir:        workDir,
		projectTrusted: projectTrusted,
	}
	p.filter = textfield.New()
	p.filter.SetVirtualCursor(true)
	p.filter.Focus()
	p.list = settings.NewFieldList(p.buildFields)
	return p
}

func (p *Panel) buildFields() []*settings.Field {
	return []*settings.Field{
		settings.NewField("stub", "Plugins panel placeholder", settings.KindHeader),
	}
}

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.Code == tea.KeyEscape {
			return func() tea.Msg { return settings.BrowserClosedMsg{} }
		}
	}
	return nil
}

func (p *Panel) View(width, maxHeight int) string {
	return "Plugins panel\n" + p.list.View()
}

func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
