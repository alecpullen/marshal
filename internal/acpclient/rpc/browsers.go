package rpc

import (
	"context"
	"time"

	"marshal/internal/acpwire"
)

// commandsCallTimeout bounds the first read's session/command_list.
const commandsCallTimeout = 5 * time.Second

// The session's notes, command, browser and project calls (plan 7b). Each
// sends its method with this session's ID and returns the result, as the turn
// calls do. Commands is the one read: it keeps its answer.

// AddNote appends note to the session's transcript. It returns when the call
// does, so the agent's stack patch is applied before it returns (decision 3).
func (s *Session) AddNote(ctx context.Context, note acpwire.Note) error {
	params := acpwire.NoteParams{SessionID: s.id, Note: note}
	return s.conn.Call(ctx, acpwire.MethodNote, params, nil)
}

// Commands is the session's command list: no I/O once the first read has a
// value, and never a read the agent has to answer. It sends
// session/command_list under commandsCallTimeout on its first read and keeps
// the answer, which does not change while the session runs (decision 2). A
// failed read is not kept, so the next one asks again. Its slice is shared
// with the kept list, so a caller must not change it.
func (s *Session) Commands() []acpwire.CommandInfo {
	s.mu.Lock()
	if s.commandsSet {
		commands := s.commands
		s.mu.Unlock()
		return commands
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), commandsCallTimeout)
	defer cancel()
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.CommandListResult
	if err := s.conn.Call(ctx, acpwire.MethodCommandList, params, &result); err != nil {
		// The read is not kept, so the next one asks again.
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commandsSet {
		// A concurrent first read answered already, and its answer stands.
		return s.commands
	}
	s.commands = result.Commands
	s.commandsSet = true
	return s.commands
}

// Command runs the slash command called name with args.
func (s *Session) Command(ctx context.Context, name string, args []string) (acpwire.CommandResult, error) {
	params := acpwire.CommandCallParams{SessionID: s.id, Name: name, Args: args}
	var result acpwire.CommandResult
	if err := s.conn.Call(ctx, acpwire.MethodCommand, params, &result); err != nil {
		return acpwire.CommandResult{}, err
	}
	return result, nil
}

// CommandAction runs the doc row action with that ID.
func (s *Session) CommandAction(ctx context.Context, id string) (acpwire.CommandResult, error) {
	params := acpwire.CommandActionParams{SessionID: s.id, ID: id}
	var result acpwire.CommandResult
	if err := s.conn.Call(ctx, acpwire.MethodCommandAction, params, &result); err != nil {
		return acpwire.CommandResult{}, err
	}
	return result, nil
}

// RecentPrompts is the newest prompts the session sent, newest first.
func (s *Session) RecentPrompts(ctx context.Context, limit int) ([]string, error) {
	params := acpwire.PromptHistoryParams{SessionID: s.id, Limit: limit}
	var result acpwire.PromptHistoryResult
	if err := s.conn.Call(ctx, acpwire.MethodPromptHistory, params, &result); err != nil {
		return nil, err
	}
	return result.Prompts, nil
}

// SavePrompt adds text to the session's prompt history.
func (s *Session) SavePrompt(ctx context.Context, text string) error {
	params := acpwire.PromptSaveParams{SessionID: s.id, Text: text}
	return s.conn.Call(ctx, acpwire.MethodPromptHistorySave, params, nil)
}

// Messages is the transcript messages query selects.
func (s *Session) Messages(ctx context.Context, query acpwire.MessageQuery) (acpwire.MessagePage, error) {
	params := acpwire.MessagesParams{SessionID: s.id, Query: query}
	var result acpwire.MessagePage
	if err := s.conn.Call(ctx, acpwire.MethodMessages, params, &result); err != nil {
		return acpwire.MessagePage{}, err
	}
	return result, nil
}

// FileIndex is the paths the session's file index holds.
func (s *Session) FileIndex(ctx context.Context) ([]string, error) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.FileIndexResult
	if err := s.conn.Call(ctx, acpwire.MethodFileIndex, params, &result); err != nil {
		return nil, err
	}
	return result.Paths, nil
}

// Intelligence is the doctor's Intelligence section.
func (s *Session) Intelligence(ctx context.Context) ([]acpwire.IntelligenceCheck, error) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.IntelligenceResult
	if err := s.conn.Call(ctx, acpwire.MethodIntelligence, params, &result); err != nil {
		return nil, err
	}
	return result.Checks, nil
}

// Plans is the plans an SDD run can execute.
func (s *Session) Plans(ctx context.Context) ([]acpwire.PlanCandidate, error) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.PlansResult
	if err := s.conn.Call(ctx, acpwire.MethodPlans, params, &result); err != nil {
		return nil, err
	}
	return result.Plans, nil
}

// DiscardCandidate deletes the plan candidate at path.
func (s *Session) DiscardCandidate(ctx context.Context, path string) error {
	params := acpwire.PathParams{SessionID: s.id, Path: path}
	return s.conn.Call(ctx, acpwire.MethodSDDDiscard, params, nil)
}

// ScaffoldPlan writes a starter plan, and returns where it is.
func (s *Session) ScaffoldPlan(ctx context.Context) (acpwire.ScaffoldResult, error) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.ScaffoldResult
	if err := s.conn.Call(ctx, acpwire.MethodSDDScaffold, params, &result); err != nil {
		return acpwire.ScaffoldResult{}, err
	}
	return result, nil
}

// Diff is the unified diff of query's range, Base..Head.
func (s *Session) Diff(ctx context.Context, query acpwire.DiffQuery) (string, error) {
	params := acpwire.DiffCallParams{SessionID: s.id, Base: query.Base, Head: query.Head}
	var result acpwire.DiffResult
	if err := s.conn.Call(ctx, acpwire.MethodDiff, params, &result); err != nil {
		return "", err
	}
	return result.Diff, nil
}

// ReadFile is the content of the file at path.
func (s *Session) ReadFile(ctx context.Context, path string) (acpwire.FileContent, error) {
	params := acpwire.PathParams{SessionID: s.id, Path: path}
	var result acpwire.FileContent
	if err := s.conn.Call(ctx, acpwire.MethodFile, params, &result); err != nil {
		return acpwire.FileContent{}, err
	}
	return result, nil
}

// Memories is the project's memories, as the browser lists them.
func (s *Session) Memories(ctx context.Context) ([]acpwire.MemoryEntry, error) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.MemoriesResult
	if err := s.conn.Call(ctx, acpwire.MethodMemoryList, params, &result); err != nil {
		return nil, err
	}
	return result.Memories, nil
}

// DeleteMemory drops the memory with that ID.
func (s *Session) DeleteMemory(ctx context.Context, id int64) error {
	params := acpwire.MemoryDeleteParams{SessionID: s.id, ID: id}
	return s.conn.Call(ctx, acpwire.MethodMemoryDelete, params, nil)
}

// Plugins is every installed plugin, global and project.
func (s *Session) Plugins(ctx context.Context) ([]acpwire.PluginEntry, error) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.PluginsResult
	if err := s.conn.Call(ctx, acpwire.MethodPluginsList, params, &result); err != nil {
		return nil, err
	}
	return result.Plugins, nil
}

// ScanPlugin clones and scans the plugin at source for review, without
// installing it.
func (s *Session) ScanPlugin(ctx context.Context, source string) (acpwire.PluginScan, error) {
	params := acpwire.PluginScanParams{SessionID: s.id, Source: source}
	var result acpwire.PluginScan
	if err := s.conn.Call(ctx, acpwire.MethodPluginsInstallScan, params, &result); err != nil {
		return acpwire.PluginScan{}, err
	}
	return result, nil
}

// InstallPlugin installs the scanned plugin token names, with the contents the
// scan listed, in scope.
func (s *Session) InstallPlugin(ctx context.Context, token, scope string) error {
	params := acpwire.PluginInstallParams{SessionID: s.id, ScanToken: token, Scope: scope}
	return s.conn.Call(ctx, acpwire.MethodPluginsInstallConfirm, params, nil)
}

// DiscardPluginScan drops the staged clone token names.
func (s *Session) DiscardPluginScan(ctx context.Context, token string) error {
	params := acpwire.PluginDiscardParams{SessionID: s.id, ScanToken: token}
	return s.conn.Call(ctx, acpwire.MethodPluginsInstallDiscard, params, nil)
}

// RemovePlugin uninstalls the plugin called name from scope.
func (s *Session) RemovePlugin(ctx context.Context, name, scope string) error {
	params := acpwire.PluginRemoveParams{SessionID: s.id, Name: name, Scope: scope}
	return s.conn.Call(ctx, acpwire.MethodPluginsRemove, params, nil)
}

// Skills is every listed skill, global and project.
func (s *Session) Skills(ctx context.Context) ([]acpwire.SkillEntry, error) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.SkillsResult
	if err := s.conn.Call(ctx, acpwire.MethodSkillsList, params, &result); err != nil {
		return nil, err
	}
	return result.Skills, nil
}

// LoadSkill loads the listed skill with that scope and source, and returns its
// name.
func (s *Session) LoadSkill(ctx context.Context, scope, source string) (string, error) {
	params := acpwire.SkillParams{SessionID: s.id, Scope: scope, Source: source}
	var result acpwire.SkillNameResult
	if err := s.conn.Call(ctx, acpwire.MethodSkillsLoad, params, &result); err != nil {
		return "", err
	}
	return result.Name, nil
}

// InstallSkill installs the skill at source into scope, and returns its name.
func (s *Session) InstallSkill(ctx context.Context, source, scope string) (string, error) {
	params := acpwire.SkillInstallParams{SessionID: s.id, Source: source, Scope: scope}
	var result acpwire.SkillNameResult
	if err := s.conn.Call(ctx, acpwire.MethodSkillsInstall, params, &result); err != nil {
		return "", err
	}
	return result.Name, nil
}

// RemoveSkill removes the listed skill with that scope and source.
func (s *Session) RemoveSkill(ctx context.Context, scope, source string) error {
	params := acpwire.SkillParams{SessionID: s.id, Scope: scope, Source: source}
	return s.conn.Call(ctx, acpwire.MethodSkillsRemove, params, nil)
}

// SetSkillAutoload turns the skill called name's autoload on or off.
func (s *Session) SetSkillAutoload(ctx context.Context, name string, on bool) error {
	params := acpwire.SkillAutoloadParams{SessionID: s.id, Name: name, On: on}
	return s.conn.Call(ctx, acpwire.MethodSkillsAutoload, params, nil)
}

// SetSkillGate turns the session's skill load gate on or off.
func (s *Session) SetSkillGate(ctx context.Context, on bool) error {
	params := acpwire.SkillGateParams{SessionID: s.id, On: on}
	return s.conn.Call(ctx, acpwire.MethodSkillGateSet, params, nil)
}

// ClearSkillGateDecision drops the gate's sticky decision for skill.
func (s *Session) ClearSkillGateDecision(ctx context.Context, skill string) error {
	params := acpwire.SkillGateClearParams{SessionID: s.id, Skill: skill}
	return s.conn.Call(ctx, acpwire.MethodSkillGateClear, params, nil)
}
