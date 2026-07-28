// Package agents renders the /agents roster panel: a resolved cast table
// for the routing roles + custom agents + swarm/SDD budgets, edited in
// place via the settings field/picker machinery.
package agents

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

// DispatchFn starts an agent run for `agentName` with `goal`. nil disables Run-now.
type DispatchFn func(agentName, goal string) tea.Cmd

// Panel is the docked /agents roster. It owns a settings.State (so it
// reuses the field/picker/persistence machinery) and a flat FieldList
// built from the resolved cast.
type Panel struct {
	state       *settings.State
	cfgPath     string
	filter      textfield.Model
	list        *settings.FieldList
	dispatch    DispatchFn
	reg         *registry.Registry // live tool registry for denylist validation
	showLegend  bool               // ? toggles the glyph legend overlay
	pendingPick func(string) error // stored from a picker push, consumed by PickedMsg

	// pickerActive tracks whether a picker overlay is open.
	pickerActive bool
	pickerModel  *picker.Model

	// stack holds drilled frames (custom agent editor, swarm/SDD budgets)
	// above the roster root list.
	stack []*settings.Frame
}

var _ dock.Panel = (*Panel)(nil)

// NewRosterPanel builds the roster panel pre-filtered by `arg` (a role
// or agent name). dispatch is used for Run-now (may be nil).
func NewRosterPanel(cfg config.Config, cfgPath, arg string, dispatch DispatchFn) *Panel {
	return NewRosterPanelWithRegistry(cfg, cfgPath, arg, dispatch, nil)
}

// NewRosterPanelWithRegistry builds the roster panel with a tool registry
// for denylist validation. When reg is nil, validation is skipped.
func NewRosterPanelWithRegistry(cfg config.Config, cfgPath, arg string, dispatch DispatchFn, reg *registry.Registry) *Panel {
	st := settings.NewState(cfg)
	f := textfield.New()
	f.SetVirtualCursor(true)
	f.Focus()
	f.SetValue(arg)
	f.CursorEnd()
	p := &Panel{state: st, cfgPath: cfgPath, filter: f, dispatch: dispatch, reg: reg}
	p.list = settings.NewFieldList(p.matchedFields)
	return p
}

// FilterValue returns the current filter text.
func (p *Panel) FilterValue() string { return p.filter.Value() }

// activeList returns the list keys should go to: the top drilled frame's
// list, or the roster root.
func (p *Panel) activeList() *settings.FieldList {
	if len(p.stack) > 0 {
		return settings.FrameList(p.stack[len(p.stack)-1])
	}
	return p.list
}

// pushFrame drills into f.
func (p *Panel) pushFrame(f *settings.Frame) {
	p.stack = append(p.stack, f)
}

// matchedFields builds the flat field list for the roster.
func (p *Panel) matchedFields() []*settings.Field {
	query := strings.TrimSpace(p.filter.Value())
	cfg := settings.StateCfg(p.state)
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	cast := router.Cast(routing.AllRoles)

	// Build a lookup of resolved routes by role.
	routeByRole := make(map[routing.AgentRole]routing.CastEntry, len(cast))
	for _, ce := range cast {
		routeByRole[ce.Role] = ce
	}

	var fields []*settings.Field

	// Profile header row.
	profileField := settings.NewField("roster.profile", "Profile", settings.KindPicker)
	settings.SetFieldDesc(profileField, "active agent profile that sets model routing roles")
	settings.SetFieldGetStr(profileField, func() string {
		return cfg.Profile.Default
	})
	settings.SetFieldPickOptions(profileField, func() []picker.Item {
		names := sortedKeys(cfg.AgentProfiles)
		items := make([]picker.Item, 0, len(names))
		current := cfg.Profile.Default
		for _, n := range names {
			badge := ""
			if n == current {
				badge = "● now"
			}
			items = append(items, picker.Item{Label: n, Value: n, Badge: badge})
		}
		return items
	})
	settings.SetFieldPickOnPick(profileField, func(v string) error {
		if _, ok := cfg.AgentProfiles[v]; ok {
			cfg.Profile.Default = v
		}
		return nil
	})
	fields = append(fields, profileField)

	// Swarm roles section header.
	swarmHeader := settings.NewField("header.swarm_roles", "Swarm Roles", settings.KindHeader)
	fields = append(fields, swarmHeader)

	swarmRoles := []routing.AgentRole{
		routing.RolePlanner, routing.RoleRepoScout, routing.RoleImplementer,
		routing.RoleTester, routing.RoleReviewer,
	}
	for _, role := range swarmRoles {
		fields = append(fields, p.roleField(cfg, role, routeByRole[role]))
	}

	// SDD roles section header.
	sddHeader := settings.NewField("header.sdd_roles", "SDD Roles", settings.KindHeader)
	fields = append(fields, sddHeader)

	sddRoles := []routing.AgentRole{
		routing.RoleSDDImplementer, routing.RoleSDDReviewer, routing.RoleSDDBranchReviewer,
	}
	for _, role := range sddRoles {
		fields = append(fields, p.roleField(cfg, role, routeByRole[role]))
	}

	// Housekeeping roles section header.
	hkHeader := settings.NewField("header.housekeeping_roles", "Housekeeping Roles", settings.KindHeader)
	fields = append(fields, hkHeader)

	housekeepingRoles := []routing.AgentRole{
		routing.RoleRouter, routing.RoleKnowledge, routing.RoleSummarizer,
		routing.RoleSecurityReviewer, routing.RoleSubtask, routing.RoleTitle,
	}
	for _, role := range housekeepingRoles {
		fields = append(fields, p.roleField(cfg, role, routeByRole[role]))
	}

	// Custom Agents section header.
	caHeader := settings.NewField("header.custom_agents", "Custom Agents", settings.KindHeader)
	fields = append(fields, caHeader)

	caNames := sortedKeys(cfg.CustomAgents)
	for _, name := range caNames {
		ca := cfg.CustomAgents[name]
		caField := settings.NewField("roster.custom_agent."+name, name, settings.KindDrill)
		settings.SetFieldDesc(caField, "custom agent: "+ca.Preset)
		settings.SetFieldSummary(caField, func() string {
			if ca.SystemPrompt != "" {
				return "custom"
			}
			return "preset"
		})
		settings.SetFieldBuild(caField, func() *settings.Frame {
			return p.customAgentFrame(name)
		})
		fields = append(fields, caField)
	}

	// Run budgets section header.
	budgetHeader := settings.NewField("header.run_budgets", "Run Budgets", settings.KindHeader)
	fields = append(fields, budgetHeader)

	swarmField := settings.NewField("roster.swarm_budget", "Swarm", settings.KindDrill)
	settings.SetFieldDesc(swarmField, "swarm orchestrator budget limits")
	settings.SetFieldSummary(swarmField, func() string { return "budget" })
	settings.SetFieldBuild(swarmField, func() *settings.Frame { return settings.SwarmFrame(p.state) })
	fields = append(fields, swarmField)

	sddField := settings.NewField("roster.sdd_budget", "SDD", settings.KindDrill)
	settings.SetFieldDesc(sddField, "SDD orchestrator budget limits")
	settings.SetFieldSummary(sddField, func() string { return "budget" })
	settings.SetFieldBuild(sddField, func() *settings.Frame { return settings.SDDFrame(p.state) })
	fields = append(fields, sddField)

	// Filter by query if non-empty.
	if query != "" {
		var filtered []*settings.Field
		for _, f := range fields {
			if f.Kind == settings.KindHeader {
				filtered = append(filtered, f)
				continue
			}
			haystack := settings.FieldID(f) + " " + settings.FieldTitle(f) + " " + settings.FieldDesc(f)
			if len(fuzzy.Rank(query, []string{haystack})) > 0 {
				filtered = append(filtered, f)
			}
		}
		fields = filtered
	}

	return fields
}

// roleField builds a picker field for a single role row.
func (p *Panel) roleField(cfg config.Config, role routing.AgentRole, ce routing.CastEntry) *settings.Field {
	rt := roleTitle(role)
	glyph, source := resolveGlyph(ce)

	desc := fmt.Sprintf("%s · %s %s", rt, glyph, source)
	if ce.Err == nil {
		desc = fmt.Sprintf("→ %s/%s · %s %s", ce.Route.Preset.Provider, ce.Route.Preset.Model, glyph, source)
	}

	f := settings.NewField("roster.role."+string(role), rt, settings.KindPicker)
	settings.SetFieldDesc(f, desc)
	settings.SetFieldGetStr(f, func() string { return glyph })
	settings.SetFieldPickOptions(f, func() []picker.Item {
		names := sortedKeys(cfg.Models.Presets)
		items := make([]picker.Item, 0, len(names)+1)
		for _, n := range names {
			p := cfg.Models.Presets[n]
			items = append(items, picker.Item{Label: n, Detail: p.Provider + "/" + p.Model, Value: n})
		}
		// Add custom agents as pickable items.
		for _, caName := range sortedKeys(cfg.CustomAgents) {
			items = append(items, picker.Item{Label: "◆ " + caName, Detail: "custom agent", Value: "__ca:" + caName})
		}
		items = append(items, picker.Item{Label: "(unset — fallback)", Value: "__unset__", Badge: "clear"})
		return items
	})
	settings.SetFieldPickOnPick(f, func(v string) error {
		// Mutate the working config (p.state.cfg) so persistNow captures the change.
		stateCfg := settings.StateCfg(p.state)
		profile, ok := stateCfg.AgentProfiles[stateCfg.Profile.Default]
		if !ok {
			return fmt.Errorf("no active profile")
		}
		if profile.Roles == nil {
			profile.Roles = map[routing.AgentRole]routing.RoleBinding{}
		}
		switch {
		case v == "__unset__":
			delete(profile.Roles, role)
		case strings.HasPrefix(v, "__ca:"):
			caName := strings.TrimPrefix(v, "__ca:")
			profile.Roles[role] = routing.RoleBinding{CustomAgent: caName}
		default:
			profile.Roles[role] = routing.RoleBinding{Preset: v}
		}
		stateCfg.AgentProfiles[stateCfg.Profile.Default] = profile
		return nil
	})
	return f
}

// validateToolDenylist checks that every name in the list is registered
// in the panel's tool registry. When reg is nil, no tools are known so
// every name fails validation.
func (p *Panel) ValidateToolDenylist(names []string) error {
	for _, name := range names {
		if p.reg == nil {
			return fmt.Errorf("unknown tool: %q (no registry available)", name)
		}
		if _, ok := p.reg.Lookup(name); !ok {
			return fmt.Errorf("unknown tool: %q", name)
		}
	}
	return nil
}

// customAgentFrame builds a drill frame for editing a custom agent.
func (p *Panel) customAgentFrame(name string) *settings.Frame {
	return settings.NewFrame(name, func() []*settings.Field {
		cfg := settings.StateCfg(p.state)
		ca := cfg.CustomAgents[name]

		presetField := settings.NewField("roster.ca."+name+".preset", "Preset", settings.KindPicker)
		settings.SetFieldDesc(presetField, "model preset for this custom agent")
		settings.SetFieldGetStr(presetField, func() string { return ca.Preset })
		settings.SetFieldPickOptions(presetField, func() []picker.Item {
			names := sortedKeys(cfg.Models.Presets)
			items := make([]picker.Item, 0, len(names))
			for _, n := range names {
				pr := cfg.Models.Presets[n]
				items = append(items, picker.Item{Label: n, Detail: pr.Provider + "/" + pr.Model, Value: n})
			}
			return items
		})
		settings.SetFieldPickOnPick(presetField, func(v string) error {
			ca2 := cfg.CustomAgents[name]
			ca2.Preset = v
			cfg.CustomAgents[name] = ca2
			return nil
		})

		promptField := settings.NewField("roster.ca."+name+".system_prompt", "System prompt", settings.KindScalar)
		settings.SetFieldDesc(promptField, "custom system prompt addendum")
		settings.SetFieldGetStr(promptField, func() string { return ca.SystemPrompt })
		settings.SetFieldSetStr(promptField, func(v string) error {
			ca2 := cfg.CustomAgents[name]
			ca2.SystemPrompt = v
			cfg.CustomAgents[name] = ca2
			return nil
		})

		// Tool denylist: comma-separated list validated against the registry.
		denylistField := settings.NewField("roster.ca."+name+".tool_denylist", "Tool denylist", settings.KindScalar)
		settings.SetFieldDesc(denylistField, "comma-separated tool names to deny (validated against live registry)")
		settings.SetFieldGetStr(denylistField, func() string {
			return strings.Join(ca.ToolDenylist, ", ")
		})
		settings.SetFieldSetStr(denylistField, func(v string) error {
			var names []string
			if v != "" {
				for _, part := range strings.Split(v, ",") {
					trimmed := strings.TrimSpace(part)
					if trimmed != "" {
						names = append(names, trimmed)
					}
				}
			}
			if err := p.ValidateToolDenylist(names); err != nil {
				return err
			}
			ca2 := cfg.CustomAgents[name]
			ca2.ToolDenylist = names
			cfg.CustomAgents[name] = ca2
			return nil
		})

		modeField := settings.NewField("roster.ca."+name+".approval_mode", "Approval mode", settings.KindEnum)
		settings.SetFieldDesc(modeField, "approval mode for this custom agent")
		settings.SetFieldGetStr(modeField, func() string {
			if ca.ApprovalMode == "" {
				return "default"
			}
			return ca.ApprovalMode
		})
		settings.SetFieldOptions(modeField, func() []string { return []string{"plan", "default", "edit", "copilot", "auto"} })
		settings.SetFieldSetStr(modeField, func(v string) error {
			ca2 := cfg.CustomAgents[name]
			ca2.ApprovalMode = v
			cfg.CustomAgents[name] = ca2
			return nil
		})

		iterField := settings.NewField("roster.ca."+name+".max_iterations", "Max iterations", settings.KindScalar)
		settings.SetFieldDesc(iterField, "max tool iterations (0 = inherit)")
		settings.SetFieldGetStr(iterField, func() string {
			if ca.MaxIterations == 0 {
				return "0 (inherit)"
			}
			return fmt.Sprintf("%d", ca.MaxIterations)
		})
		settings.SetFieldSetStr(iterField, func(v string) error {
			n := 0
			if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
				return fmt.Errorf("must be a number")
			}
			ca2 := cfg.CustomAgents[name]
			ca2.MaxIterations = n
			cfg.CustomAgents[name] = ca2
			return nil
		})

		// Context budget: max_repo_context_tokens.
		ctxField := settings.NewField("roster.ca."+name+".context", "Context (max repo tokens)", settings.KindScalar)
		settings.SetFieldDesc(ctxField, "max repo context tokens (0 = inherit from role)")
		settings.SetFieldGetStr(ctxField, func() string {
			if ca.Context.MaxRepoContextTokens == 0 {
				return "0 (inherit)"
			}
			return strconv.Itoa(ca.Context.MaxRepoContextTokens)
		})
		settings.SetFieldSetStr(ctxField, func(v string) error {
			n := 0
			if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
				return fmt.Errorf("must be a number")
			}
			ca2 := cfg.CustomAgents[name]
			ca2.Context.MaxRepoContextTokens = n
			cfg.CustomAgents[name] = ca2
			return nil
		})

		// Run-now action: dispatches the custom agent with an inline goal prompt.
		runField := settings.NewField("roster.ca."+name+".run", "Run now", settings.KindAction)
		settings.SetFieldDesc(runField, "run this custom agent with a goal")
		settings.SetFieldAct(runField, func() tea.Cmd {
			if p.dispatch == nil {
				return nil
			}
			// Open inline goal prompt via the dispatch closure.
			// The dispatch closure (set by openAgentsRoster) handles
			// building the runner and starting the agent run.
			return p.dispatch(name, "")
		})

		return []*settings.Field{
			presetField,
			promptField,
			denylistField,
			modeField,
			iterField,
			ctxField,
			runField,
		}
	})
}

// Update handles messages for the roster panel.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case picker.PickedMsg:
		if p.pendingPick != nil {
			if err := p.pendingPick(msg.Value); err != nil {
				// Error rendering is handled by the field list; just clear.
			}
			p.pendingPick = nil
		}
		p.pickerActive = false
		p.pickerModel = nil
		// A picker commit is always a commit gesture — persist immediately.
		return p.persistNow()
	case picker.CancelledMsg:
		p.pendingPick = nil
		p.pickerActive = false
		p.pickerModel = nil
		return nil
	case tea.KeyPressMsg:
		// When a picker overlay is active, forward all key events to it.
		if p.pickerActive && p.pickerModel != nil {
			return p.pickerModel.Update(msg)
		}
		switch msg.String() {
		case "esc":
			if len(p.stack) > 0 {
				p.stack = p.stack[:len(p.stack)-1]
				settings.FieldListRefresh(p.activeList())
				return nil
			}
			return func() tea.Msg { return settings.BrowserClosedMsg{} }
		case "?":
			p.showLegend = !p.showLegend
			return nil
		case "up", "down":
			cmd := settings.FieldListUpdate(p.activeList(), msg)
			return p.maybePersist(cmd)
		case "enter":
			l := p.activeList()
			cmd := settings.FieldListUpdate(l, msg)
			if f := settings.FieldListTakePushRequest(l); f != nil {
				p.pushFrame(f)
			}
			if pr := settings.FieldListTakePushPicker(l); pr != nil {
				p.pendingPick = pr.OnPick
				p.pickerModel = picker.New(pr.Title, pr.Footer, pr.Items)
				if pr.AllowCustom {
					p.pickerModel.SetAllowCustom(true)
				}
				p.pickerActive = true
			}
			return p.maybePersist(cmd)
		default:
			l := p.activeList()
			// While drilled (or mid-edit), non-nav keys belong to the frame
			// list — scalar edits, enum cycles, action keys. At the roster
			// root they type into the filter as before.
			if len(p.stack) > 0 || l.Editing() {
				cmd := settings.FieldListUpdate(l, msg)
				return p.maybePersist(cmd)
			}
			var cmd tea.Cmd
			p.filter, cmd = p.filter.Update(msg)
			settings.FieldListRefresh(p.list)
			return cmd
		}
	case tea.PasteMsg:
		var cmd tea.Cmd
		p.filter, cmd = p.filter.Update(msg)
		settings.FieldListRefresh(p.list)
		return cmd
	default:
		cmd := settings.FieldListUpdate(p.list, msg)
		return p.maybePersist(cmd)
	}
}

// persistNow saves the working config to disk and emits a ChangedMsg
// unconditionally. Used for picker commits and other explicit gestures.
func (p *Panel) persistNow() tea.Cmd {
	cfg := settings.StateCfg(p.state)
	return func() tea.Msg {
		if p.cfgPath == "" {
			return settings.ChangedMsg{Cfg: cfg, Receipts: nil}
		}
		saveErr := config.SaveProjectConfig(p.cfgPath, cfg)
		return settings.ChangedMsg{Cfg: cfg, Receipts: nil, SaveErr: saveErr}
	}
}

// maybePersist wraps cmd with a persistence check: if the field list committed
// a change, save the config to disk and emit a ChangedMsg so the model can
// apply and reload it.
func (p *Panel) maybePersist(inner tea.Cmd) tea.Cmd {
	if !settings.FieldListCommitted(p.list) {
		return inner
	}
	cfg := settings.StateCfg(p.state)
	changed := func() tea.Msg {
		if p.cfgPath == "" {
			return settings.ChangedMsg{Cfg: cfg, Receipts: nil}
		}
		saveErr := config.SaveProjectConfig(p.cfgPath, cfg)
		return settings.ChangedMsg{Cfg: cfg, Receipts: nil, SaveErr: saveErr}
	}
	if inner == nil {
		return changed
	}
	return tea.Batch(inner, changed)
}

// Sizing declares the full-frame budget: the transcript hides while this
// panel is open.
func (p *Panel) Sizing() dock.Sizing { return dock.FullFrame }

// View renders the roster panel.
func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 2 {
		return ""
	}

	// When a picker overlay is active, render it instead of the roster.
	if p.pickerActive && p.pickerModel != nil {
		return p.pickerModel.View(width, maxHeight)
	}

	panelWidth := layout.PanelWidth(width)
	innerWidth := panelWidth - 3

	twoCol := layout.TwoColumn(innerWidth)
	listWidth := innerWidth
	if twoCol {
		listWidth, _ = layout.SplitPanes(innerWidth)
	}

	title := "Agents"
	l := p.list
	if len(p.stack) > 0 {
		l = p.activeList()
		parts := []string{"Agents"}
		for _, f := range p.stack {
			parts = append(parts, settings.FrameTitle(f))
		}
		title = strings.Join(parts, " › ")
	}
	settings.FieldListSetSize(l, listWidth, max(maxHeight-4, 1))
	settings.FieldListSetDescSuppressed(l, twoCol)
	body := ""
	if len(p.stack) == 0 {
		body = "/ " + p.filter.View() + "\n"
		if p.showLegend {
			legend := "● preset bound  ◆ custom agent bound  ↩ impl fallback  legacy  ⚠ unresolved  ←/→ drill"
			body += lipgloss.NewStyle().Foreground(theme.Current().FGMuted).Render(legend) + "\n"
		}
	}
	listView := settings.FieldListView(l)
	if twoCol {
		_, detailWidth := layout.SplitPanes(innerWidth)
		detail := lipgloss.NewStyle().Width(detailWidth).Foreground(theme.Current().FGMuted).
			Render(settings.FieldListCursorDesc(l))
		listView = lipgloss.JoinHorizontal(lipgloss.Top, listView, "  ", detail)
	}
	body += listView
	footer := fmt.Sprintf("%d entries", len(settings.FieldListRows(l)))

	content := body + "\n" + lipgloss.NewStyle().Foreground(theme.Current().FGMuted).Render(footer)
	panelHeight := min(lipgloss.Height(content)+1, maxHeight)
	return chrome.PanelWithHints(title, "", content, panelWidth, panelHeight, true, theme.Current())
}

// resolveGlyph returns the glyph and source label for a cast entry.
func resolveGlyph(ce routing.CastEntry) (glyph, source string) {
	if ce.Err != nil {
		return "⚠", "unresolved"
	}
	if ce.Route.CustomAgent != nil {
		return "◆", "custom agent"
	}
	if ce.Route.Legacy {
		return "↩", "legacy"
	}
	return "●", "preset"
}

// roleTitle returns a human-readable title for a role.
func roleTitle(role routing.AgentRole) string {
	titles := map[routing.AgentRole]string{
		routing.RoleRouter:            "Router",
		routing.RoleKnowledge:         "Knowledge",
		routing.RoleSummarizer:        "Summarizer",
		routing.RoleRepoScout:         "Repo scout",
		routing.RoleTester:            "Tester",
		routing.RolePlanner:           "Planner",
		routing.RoleImplementer:       "Implementer",
		routing.RoleReviewer:          "Reviewer",
		routing.RoleSecurityReviewer:  "Security reviewer",
		routing.RoleSubtask:           "Subtask",
		routing.RoleTitle:             "Title generator",
		routing.RoleSDDImplementer:    "SDD implementer",
		routing.RoleSDDReviewer:       "SDD reviewer",
		routing.RoleSDDBranchReviewer: "SDD branch reviewer",
	}
	if t, ok := titles[role]; ok {
		return t
	}
	return string(role)
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
