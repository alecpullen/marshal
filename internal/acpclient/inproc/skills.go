package inproc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/skills"
)

// The two scopes a skill store has, and the two an install and a removal take.
const (
	skillScopeGlobal  = "global"
	skillScopeProject = "project"
)

// skillListingKey is the skillListing key of one listed entry. The scope and
// the discovered source are joined by a NUL, which neither can hold, so no two
// entries collide.
func skillListingKey(scope, source string) string { return scope + "\x00" + source }

// skillGateOf copies the session's skill load gate onto the wire, its sticky
// decisions in the state's order (sorted by skill). Decisions stays nil when
// there are none, so an empty gate encodes as absent under omitempty.
func skillGateOf(st *session.State) acpwire.SkillGateState {
	out := acpwire.SkillGateState{Enabled: st.SkillGateEnabled()}
	decisions := st.SkillGateDecisions()
	if len(decisions) == 0 {
		return out
	}
	out.Decisions = make([]acpwire.SkillGateDecision, 0, len(decisions))
	for _, d := range decisions {
		out.Decisions = append(out.Decisions, acpwire.SkillGateDecision{
			Skill:   d.Skill,
			Allowed: d.Allowed,
			Count:   d.Count,
		})
	}
	return out
}

// globalSkillsDir is the global skill store: the skills directory under the
// user config dir, exactly as the skills panel resolves it. Never
// skills.ScopeDir: its global path is hardcoded to ~/.config, so a store under
// MARSHAL_CONFIG_DIR or XDG_CONFIG_HOME would land somewhere the panel and the
// loader never look.
func (s *Session) globalSkillsDir() string {
	return filepath.Join(config.UserDir(s.pluginHome()), "skills")
}

// projectSkillsDir is the project skill store under the session's working
// directory.
func (s *Session) projectSkillsDir() string {
	return filepath.Join(s.state.WorkingDir, ".marshal", "skills")
}

// index is the runtime's skill index, or nil when it has none. It is the one
// LoadSkill checks against: loading from a freshly built index would silently
// disagree with the session.
func (s *Session) index() *skills.Index {
	if s.skillIndex == nil {
		return nil
	}
	return s.skillIndex()
}

// listed is the entry the last Skills listing recorded, under mu.
func (s *Session) listed(scope, source string) (skills.ScopedSkill, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	listed, ok := s.skillListing[skillListingKey(scope, source)]
	return listed, ok
}

// loadBlocked is the first of loadableSelection's index and session reasons
// that applies to scoped: the session has no index, its index lacks the name,
// its index holds a different value for the name, or the skill is already
// active. It is "" when the entry can load now.
func (s *Session) loadBlocked(index *skills.Index, scoped skills.ScopedSkill) string {
	if index == nil {
		return "this session has no skill index — restart Marshal to build one"
	}
	indexed, ok := index.Load(scoped.Skill.Name)
	if !ok {
		return fmt.Sprintf("the current session has no %s — restart Marshal to pick it up", scoped.Skill.Name)
	}
	if indexed != scoped.Skill {
		return fmt.Sprintf("the current session holds a different %s — another source may take precedence; restart Marshal to apply disk changes", scoped.Skill.Name)
	}
	if s.state.HasActiveSkill(scoped.Skill.Name) {
		return fmt.Sprintf("%s is already active — its current body is kept until unloaded; unload it before loading another version", scoped.Skill.Name)
	}
	return ""
}

// Skills lists both skill stores, and remembers the listing for LoadSkill and
// RemoveSkill. A directory read error is returned unchanged: it is not an
// empty valid store, so it fails closed.
func (s *Session) Skills(ctx context.Context) ([]acpwire.SkillEntry, error) {
	if s.closed.Load() {
		return nil, errSessionClosed
	}
	list, err := skills.ListScopes(s.globalSkillsDir(), s.projectSkillsDir())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.skillListing = make(map[string]skills.ScopedSkill, len(list))
	for _, scoped := range list {
		s.skillListing[skillListingKey(scoped.Scope, scoped.SourceName)] = scoped
	}
	s.mu.Unlock()

	index := s.index()
	out := make([]acpwire.SkillEntry, 0, len(list))
	for _, scoped := range list {
		out = append(out, acpwire.SkillEntry{
			Scope:       scoped.Scope,
			Source:      scoped.SourceName,
			Name:        scoped.Skill.Name,
			Description: scoped.Skill.Description,
			LoadBlocked: s.loadBlocked(index, scoped),
		})
	}
	return out, nil
}

// loadableSelection re-reads the stores and returns the exact current-session
// value to load, or the reason the listed entry cannot be loaded now. It is
// the panel's check, moved agent side: the file and the shared index can both
// change while a listing is in flight, so the stores are re-read here rather
// than trusting the snapshot the listing took.
func (s *Session) loadableSelection(listed skills.ScopedSkill) (skills.Skill, string) {
	index := s.index()
	if index == nil {
		return skills.Skill{}, "this session has no skill index — restart Marshal to build one"
	}
	list, err := skills.ListScopes(s.globalSkillsDir(), s.projectSkillsDir())
	if err != nil {
		return skills.Skill{}, fmt.Sprintf("cannot read the skills directories: %v", err)
	}
	var found *skills.ScopedSkill
	for i := range list {
		if list[i].Scope == listed.Scope && list[i].SourceName == listed.SourceName {
			found = &list[i]
			break
		}
	}
	if found == nil {
		return skills.Skill{}, fmt.Sprintf("%s is no longer in the %s store — reopen the panel", listed.SourceName, listed.Scope)
	}
	if listed.SourceInfo != nil && found.SourceInfo != nil && !os.SameFile(listed.SourceInfo, found.SourceInfo) {
		return skills.Skill{}, fmt.Sprintf("%s was replaced on disk — reopen the panel", listed.SourceName)
	}
	if found.Skill != listed.Skill {
		return skills.Skill{}, fmt.Sprintf("%s changed on disk — reopen the panel to pick up the new content", listed.SourceName)
	}
	indexed, ok := index.Load(listed.Skill.Name)
	if !ok {
		return skills.Skill{}, fmt.Sprintf("the current session has no %s — restart Marshal to pick it up", listed.Skill.Name)
	}
	if indexed != found.Skill {
		return skills.Skill{}, fmt.Sprintf("the current session holds a different %s — another source may take precedence; restart Marshal to apply disk changes", listed.Skill.Name)
	}
	if s.state.HasActiveSkill(listed.Skill.Name) {
		return skills.Skill{}, fmt.Sprintf("%s is already active — its current body is kept until unloaded; unload it before loading another version", listed.Skill.Name)
	}
	return found.Skill, ""
}

// LoadSkill injects the listed skill into the session, if the store and the
// session's index still hold the content the listing saw, and returns its
// name. The body comes from a one-entry index built from the value just
// checked, so a concurrent update to the shared index cannot swap it between
// validation and lookup; the session's index is never mutated.
func (s *Session) LoadSkill(ctx context.Context, scope, source string) (string, error) {
	if s.closed.Load() {
		return "", errSessionClosed
	}
	listed, ok := s.listed(scope, source)
	if !ok {
		return "", fmt.Errorf("%s is no longer in the %s store — reopen the panel", source, scope)
	}
	exact, reason := s.loadableSelection(listed)
	if reason != "" {
		return "", fmt.Errorf("%s", reason)
	}
	index := skills.NewIndex()
	index.Set(exact.Name, exact)
	if err := skills.LoadSkillIntoSession(index, s.state, exact.Name); err != nil {
		return "", err
	}
	return exact.Name, nil
}

// InstallSkill installs a skill from source into scope's store and returns its
// name. The three refusals are the panel's, in the panel's words: a blank
// source, a project scope on an untrusted session, and an unknown scope. A
// project-scope write on an untrusted session is refused rather than
// redirected to global.
func (s *Session) InstallSkill(ctx context.Context, source, scope string) (string, error) {
	if s.closed.Load() {
		return "", errSessionClosed
	}
	if strings.TrimSpace(source) == "" {
		return "", fmt.Errorf("enter a source first")
	}
	var targetDir string
	switch scope {
	case skillScopeGlobal:
		targetDir = s.globalSkillsDir()
	case skillScopeProject:
		if !s.state.Trusted() {
			return "", fmt.Errorf("project scope needs a trusted folder")
		}
		targetDir = s.projectSkillsDir()
	default:
		return "", fmt.Errorf("unknown skill scope %q", scope)
	}
	return skills.Install(ctx, source, targetDir, "")
}

// RemoveSkill deletes the listed entry's source from its own store. The root
// is chosen from the scope alone and the removal is confined to that store by
// skills.RemoveScopedSkill: the target is never rebuilt from the frontmatter
// name, which is untrusted content.
func (s *Session) RemoveSkill(ctx context.Context, scope, source string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	listed, ok := s.listed(scope, source)
	if !ok {
		return fmt.Errorf("%s is no longer in the %s store — reopen the panel", source, scope)
	}
	var root string
	switch scope {
	case skillScopeGlobal:
		root = s.globalSkillsDir()
	case skillScopeProject:
		root = s.projectSkillsDir()
	default:
		return fmt.Errorf("cannot remove skill: unknown scope %q", scope)
	}
	return skills.RemoveScopedSkill(root, listed)
}

// SetSkillAutoload adds name to skills.autoload in the user config, or removes
// it, then installs a copy of the live config holding the new list.
//
// Autoload always writes the user config, never the project one, even for a
// project-scoped skill: it is a preference about how the agent behaves, not a
// property of the repository, and writing it project-side would commit one
// developer's always-on skills into a shared tree. SaveUserConfigValue writes
// just this key, so the rest of the user config is left alone. A write error
// is returned unchanged and nothing is installed.
func (s *Session) SetSkillAutoload(ctx context.Context, name string, on bool) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	current := s.state.ConfigSnapshot().Skills.Autoload
	next := make([]string, 0, len(current)+1)
	for _, n := range current {
		if n != name {
			next = append(next, n)
		}
	}
	if on {
		next = append(next, name)
	}
	path, err := userConfigPath(Deps{HomeDir: s.homeDir, UserConfigPath: s.userConfigFile})
	if err != nil {
		return err
	}
	if err := config.SaveUserConfigValue(path, "skills.autoload", next); err != nil {
		return err
	}
	refreshLayers(s.state, s.reloadLayers)
	cfg := s.state.ConfigSnapshot()
	cfg.Skills.Autoload = next
	_, err = s.state.RebaseInteractionConfig(cfg)
	return err
}

// SetSkillGate turns the session's skill load gate on or off. Turning it on
// clears its decisions, so a re-tighten starts fresh.
func (s *Session) SetSkillGate(ctx context.Context, on bool) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.state.SkillGateSetEnabled(on)
	s.signal()
	return nil
}

// ClearSkillGateDecision clears the gate's sticky decision for skill, so its
// next load asks again.
func (s *Session) ClearSkillGateDecision(ctx context.Context, skill string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.state.SkillGateClearDecision(skill)
	s.signal()
	return nil
}
