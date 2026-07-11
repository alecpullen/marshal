package settings

import (
	"fmt"
	"strconv"

	"charm.land/huh/v2"
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

type mcpServerEntry struct {
	key    string
	server config.MCPServerConfig
}

func (m mcpServerEntry) Title() string {
	return m.key + "  (" + m.server.Command + ")"
}
func (m mcpServerEntry) Key() string { return m.key }

// mcpServerEditPane is a composite sub-pane for editing a single MCP server.
// Args is a listStrings (one row per arg, so paths with spaces survive) and
// Env is a mapEditor (KEY=VAL pairs, one per row). The Command field is a
// scalar huh form. Edits mutate a local copy; the commit callback writes
// the local copy back into the working config.
type mcpServerEditPane struct {
	inner *mixedPane
	local *config.MCPServerConfig
}

func newMCPServerEditPane(local *config.MCPServerConfig) *mcpServerEditPane {
	form := newScalarPane(func() *huh.Form {
		return newSectionForm(
			huh.NewInput().Key("Command").Title("Command").Value(&local.Command),
		)
	})
	args := newListStrings("Args", &local.Args)
	env := newMapStringEditor("Env", &local.Env)
	return &mcpServerEditPane{
		inner: newMixedPane(form, args, env),
		local: local,
	}
}

func (p *mcpServerEditPane) Init() tea.Cmd { return p.inner.Init() }

func (p *mcpServerEditPane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	updated, cmd := p.inner.Update(msg)
	if mp, ok := updated.(*mixedPane); ok {
		p.inner = mp
	}
	return p, cmd
}

func (p *mcpServerEditPane) View(width int) string         { return p.inner.View(width) }
func (p *mcpServerEditPane) SetWidth(w int)                { p.inner.SetWidth(w) }
func (p *mcpServerEditPane) HasInnerFocus() bool           { return p.inner.HasInnerFocus() }
func (p *mcpServerEditPane) CloseInner()                   { p.inner.CloseInner() }
func (p *mcpServerEditPane) FocusedFieldTitle() string     { return p.inner.FocusedFieldTitle() }

func newMCPPane(s *state) sectionPane {
	form := newScalarPane(func() *huh.Form {
		b := &struct{ threshold string }{threshold: strconv.Itoa(s.cfg.MCP.DisclosureThresholdTools)}
		return newSectionForm(
			numField("Disclosure threshold tools", &b.threshold, 0, func(v int) {
				s.cfg.MCP.DisclosureThresholdTools = v
			}),
		)
	})
	serversSpec := collectionSpec{
		heading:   "Servers",
		keyPrompt: "New server name",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.MCP.Servers))
			for k, srv := range s.cfg.MCP.Servers {
				out = append(out, mcpServerEntry{key: k, server: srv})
			}
			return out
		},
		add: func(s *state, key string) error {
			if key == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.MCP.Servers[key]; ok {
				return fmt.Errorf("entry already exists")
			}
			s.cfg.MCP.Servers[key] = config.MCPServerConfig{Env: map[string]string{}}
			return nil
		},
		editSubPane: func(s *state, key string) (sectionPane, func()) {
			local := s.cfg.MCP.Servers[key]
			if local.Env == nil {
				local.Env = map[string]string{}
			}
			pane := newMCPServerEditPane(&local)
			return pane, func() { s.cfg.MCP.Servers[key] = local }
		},
		delete: func(s *state, key string) { delete(s.cfg.MCP.Servers, key) },
	}
	collection := newCollectionPane(s, serversSpec)
	policies := newMapStringEditor("Policies", &s.cfg.MCP.Policies)
	return newCompositePane(form, collection, policies)
}
