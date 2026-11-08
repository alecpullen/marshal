package rpc

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// decodeParams decodes req's params into v.
func decodeParams(t *testing.T, req fakeRequest, v any) {
	t.Helper()
	if err := json.Unmarshal(req.Params, v); err != nil {
		t.Fatalf("decode %s params: %v", req.Method, err)
	}
}

// TestAddNoteSendsTheNote pins the note's frame: its text, role and content
// type travel with the session's ID, and a nil answer is all the call needs.
func TestAddNoteSendsTheNote(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	note := acpwire.Note{
		Text:        "typed while no turn could run",
		Role:        acpwire.RoleUser,
		ContentType: acpwire.ContentTypePlain,
	}
	out := make(chan error, 1)
	go func() { out <- session.AddNote(context.Background(), note) }()

	req := nextMethod(t, a, acpwire.MethodNote)
	var params acpwire.NoteParams
	decodeParams(t, req, &params)
	if params.SessionID != "s1" {
		t.Fatalf("note params = %+v, want session s1", params)
	}
	if params.Note != note {
		t.Fatalf("note params = %+v, want note %+v", params.Note, note)
	}

	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
}

// TestCommandsAreFetchedOnce pins the kept list: the first read sends one
// session/command_list and keeps it, the second sends nothing, and a failed
// first read is not kept, so the next read asks again.
func TestCommandsAreFetchedOnce(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	first := []acpwire.CommandInfo{
		{Name: "plan", Description: "write a plan", Kind: acpwire.CommandKindHeadless},
		{Name: "help", Description: "list commands", Kind: acpwire.CommandKindTUIOnly},
	}
	out := startCall(func() ([]acpwire.CommandInfo, error) { return session.Commands(), nil })
	req := nextMethod(t, a, acpwire.MethodCommandList)
	var params acpwire.ControlParams
	decodeParams(t, req, &params)
	if params.SessionID != "s1" {
		t.Fatalf("command_list params = %+v, want session s1", params)
	}
	a.answer(t, req, acpwire.CommandListResult{Commands: first})
	res := awaitValue(t, out)
	if res.err != nil {
		t.Fatalf("the first Commands read: %v", res.err)
	}
	if !reflect.DeepEqual(res.value, first) {
		t.Fatalf("the first Commands read = %+v, want %+v", res.value, first)
	}

	// The second read answers the same list without asking the agent.
	second := session.Commands()
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("the second Commands read = %+v, want %+v", second, first)
	}
	select {
	case req := <-a.reqs:
		t.Fatalf("the second Commands read sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}

	// A failed first read is not kept: the next read asks again.
	other := attachScripted(t, host, a, "s2", acpwire.SessionStatus{WorkingDir: "B"})
	failed := startCall(func() ([]acpwire.CommandInfo, error) { return other.Commands(), nil })
	req = nextMethod(t, a, acpwire.MethodCommandList)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "no command list",
	}))
	res = awaitValue(t, failed)
	if res.value != nil {
		t.Fatalf("a failed Commands read = %+v, want nil", res.value)
	}

	retried := startCall(func() ([]acpwire.CommandInfo, error) { return other.Commands(), nil })
	req = nextMethod(t, a, acpwire.MethodCommandList)
	a.answer(t, req, acpwire.CommandListResult{Commands: first})
	res = awaitValue(t, retried)
	if res.err != nil {
		t.Fatalf("the retried Commands read: %v", res.err)
	}
	if !reflect.DeepEqual(res.value, first) {
		t.Fatalf("the retried Commands read = %+v, want %+v", res.value, first)
	}
}

// TestTheCommandCallsSendTheirMethods pins the command calls' frames and their
// results: a command's name and args, an action's ID, and a doc whose row
// carries the action that ran it.
func TestTheCommandCallsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	ctx := context.Background()

	doc := &acpwire.WireDoc{
		Title: "Plans",
		Rows: []acpwire.WireRow{
			{Text: "one plan", ActionLabel: "run", ActionID: "action-1"},
		},
	}
	out := startCall(func() (acpwire.CommandResult, error) {
		return session.Command(ctx, "plan", []string{"--review", "task 1"})
	})
	req := nextMethod(t, a, acpwire.MethodCommand)
	var params acpwire.CommandCallParams
	decodeParams(t, req, &params)
	if params.SessionID != "s1" {
		t.Fatalf("command params = %+v, want session s1", params)
	}
	if params.Name != "plan" || !reflect.DeepEqual(params.Args, []string{"--review", "task 1"}) {
		t.Fatalf("command params = %+v, want name plan and its args", params)
	}
	a.answer(t, req, acpwire.CommandResult{Text: "here", Doc: doc})
	res := awaitValue(t, out)
	if res.err != nil {
		t.Fatalf("Command: %v", res.err)
	}
	if res.value.Text != "here" {
		t.Fatalf("Command result text = %q, want here", res.value.Text)
	}
	if !reflect.DeepEqual(res.value.Doc, doc) {
		t.Fatalf("Command result doc = %+v, want %+v", res.value.Doc, doc)
	}

	action := startCall(func() (acpwire.CommandResult, error) {
		return session.CommandAction(ctx, "action-1")
	})
	req = nextMethod(t, a, acpwire.MethodCommandAction)
	var actionParams acpwire.CommandActionParams
	decodeParams(t, req, &actionParams)
	if actionParams.SessionID != "s1" || actionParams.ID != "action-1" {
		t.Fatalf("command_action params = %+v, want session s1 and action-1", actionParams)
	}
	a.answer(t, req, acpwire.CommandResult{Text: "ran it", AgentGoal: "run task 1"})
	res = awaitValue(t, action)
	if res.err != nil {
		t.Fatalf("CommandAction: %v", res.err)
	}
	if res.value.Text != "ran it" || res.value.AgentGoal != "run task 1" {
		t.Fatalf("CommandAction result = %+v, want its text and agent goal", res.value)
	}
}

// TestTheProjectCallsSendTheirMethods pins the project reads' frames: each
// sends its method with this session's ID, and each result decodes.
func TestTheProjectCallsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	ctx := context.Background()

	prompts := startCall(func() ([]string, error) { return session.RecentPrompts(ctx, 5) })
	req := nextMethod(t, a, acpwire.MethodPromptHistory)
	var history acpwire.PromptHistoryParams
	decodeParams(t, req, &history)
	if history.SessionID != "s1" || history.Limit != 5 {
		t.Fatalf("prompt_history params = %+v, want session s1 and limit 5", history)
	}
	a.answer(t, req, acpwire.PromptHistoryResult{Prompts: []string{"newest", "older"}})
	res := awaitValue(t, prompts)
	if res.err != nil {
		t.Fatalf("RecentPrompts: %v", res.err)
	}
	if !reflect.DeepEqual(res.value, []string{"newest", "older"}) {
		t.Fatalf("RecentPrompts = %+v, want the agent's prompts", res.value)
	}

	saved := make(chan error, 1)
	go func() { saved <- session.SavePrompt(ctx, "remember this") }()
	req = nextMethod(t, a, acpwire.MethodPromptHistorySave)
	var save acpwire.PromptSaveParams
	decodeParams(t, req, &save)
	if save.SessionID != "s1" || save.Text != "remember this" {
		t.Fatalf("prompt_history_save params = %+v, want session s1 and the text", save)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, saved); err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}

	query := acpwire.MessageQuery{Role: acpwire.RoleUser, Last: 3}
	messages := startCall(func() (acpwire.MessagePage, error) { return session.Messages(ctx, query) })
	req = nextMethod(t, a, acpwire.MethodMessages)
	var messageParams acpwire.MessagesParams
	decodeParams(t, req, &messageParams)
	if messageParams.SessionID != "s1" || messageParams.Query != query {
		t.Fatalf("messages params = %+v, want session s1 and query %+v", messageParams, query)
	}
	page := acpwire.MessagePage{Total: 2, Messages: []acpwire.HistoryMessage{{Role: acpwire.RoleUser, Content: "hi"}}}
	a.answer(t, req, page)
	pageRes := awaitValue(t, messages)
	if pageRes.err != nil {
		t.Fatalf("Messages: %v", pageRes.err)
	}
	if !reflect.DeepEqual(pageRes.value, page) {
		t.Fatalf("Messages = %+v, want %+v", pageRes.value, page)
	}

	index := startCall(func() ([]string, error) { return session.FileIndex(ctx) })
	req = nextMethod(t, a, acpwire.MethodFileIndex)
	var control acpwire.ControlParams
	decodeParams(t, req, &control)
	if control.SessionID != "s1" {
		t.Fatalf("file_index params = %+v, want session s1", control)
	}
	a.answer(t, req, acpwire.FileIndexResult{Paths: []string{"a.go", "b.go"}})
	res = awaitValue(t, index)
	if res.err != nil {
		t.Fatalf("FileIndex: %v", res.err)
	}
	if !reflect.DeepEqual(res.value, []string{"a.go", "b.go"}) {
		t.Fatalf("FileIndex = %+v, want the agent's paths", res.value)
	}

	checks := startCall(func() ([]acpwire.IntelligenceCheck, error) {
		return session.Intelligence(ctx)
	})
	req = nextMethod(t, a, acpwire.MethodIntelligence)
	decodeParams(t, req, &control)
	if control.SessionID != "s1" {
		t.Fatalf("intelligence params = %+v, want session s1", control)
	}
	wantChecks := []acpwire.IntelligenceCheck{{Name: "index", Status: "ok", Detail: "fresh"}}
	a.answer(t, req, acpwire.IntelligenceResult{Checks: wantChecks})
	checkRes := awaitValue(t, checks)
	if checkRes.err != nil {
		t.Fatalf("Intelligence: %v", checkRes.err)
	}
	if !reflect.DeepEqual(checkRes.value, wantChecks) {
		t.Fatalf("Intelligence = %+v, want %+v", checkRes.value, wantChecks)
	}

	plans := startCall(func() ([]acpwire.PlanCandidate, error) { return session.Plans(ctx) })
	req = nextMethod(t, a, acpwire.MethodPlans)
	decodeParams(t, req, &control)
	if control.SessionID != "s1" {
		t.Fatalf("plans params = %+v, want session s1", control)
	}
	wantPlans := []acpwire.PlanCandidate{{Path: "docs/p/plan.md", Name: "plan", Tasks: 3}}
	a.answer(t, req, acpwire.PlansResult{Plans: wantPlans})
	planRes := awaitValue(t, plans)
	if planRes.err != nil {
		t.Fatalf("Plans: %v", planRes.err)
	}
	if !reflect.DeepEqual(planRes.value, wantPlans) {
		t.Fatalf("Plans = %+v, want %+v", planRes.value, wantPlans)
	}

	discarded := make(chan error, 1)
	go func() { discarded <- session.DiscardCandidate(ctx, "docs/p/plan.md") }()
	req = nextMethod(t, a, acpwire.MethodSDDDiscard)
	var pathParams acpwire.PathParams
	decodeParams(t, req, &pathParams)
	if pathParams.SessionID != "s1" || pathParams.Path != "docs/p/plan.md" {
		t.Fatalf("sdd_discard params = %+v, want session s1 and the path", pathParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, discarded); err != nil {
		t.Fatalf("DiscardCandidate: %v", err)
	}

	scaffold := startCall(func() (acpwire.ScaffoldResult, error) { return session.ScaffoldPlan(ctx) })
	req = nextMethod(t, a, acpwire.MethodSDDScaffold)
	decodeParams(t, req, &control)
	if control.SessionID != "s1" {
		t.Fatalf("sdd_scaffold params = %+v, want session s1", control)
	}
	wantScaffold := acpwire.ScaffoldResult{Path: "docs/p/plan.md"}
	a.answer(t, req, wantScaffold)
	scaffoldRes := awaitValue(t, scaffold)
	if scaffoldRes.err != nil {
		t.Fatalf("ScaffoldPlan: %v", scaffoldRes.err)
	}
	if scaffoldRes.value != wantScaffold {
		t.Fatalf("ScaffoldPlan = %+v, want %+v", scaffoldRes.value, wantScaffold)
	}

	diff := startCall(func() (string, error) {
		return session.Diff(ctx, acpwire.DiffQuery{Base: "main", Head: "HEAD"})
	})
	req = nextMethod(t, a, acpwire.MethodDiff)
	var diffParams acpwire.DiffCallParams
	decodeParams(t, req, &diffParams)
	if diffParams.SessionID != "s1" || diffParams.Base != "main" || diffParams.Head != "HEAD" {
		t.Fatalf("diff params = %+v, want session s1 and base main..HEAD", diffParams)
	}
	a.answer(t, req, acpwire.DiffResult{Diff: "--- a\n+++ b\n"})
	diffRes := awaitValue(t, diff)
	if diffRes.err != nil {
		t.Fatalf("Diff: %v", diffRes.err)
	}
	if diffRes.value != "--- a\n+++ b\n" {
		t.Fatalf("Diff = %q, want the agent's diff", diffRes.value)
	}

	content := startCall(func() (acpwire.FileContent, error) { return session.ReadFile(ctx, "a.go") })
	req = nextMethod(t, a, acpwire.MethodFile)
	var fileParams acpwire.PathParams
	decodeParams(t, req, &fileParams)
	if fileParams.SessionID != "s1" || fileParams.Path != "a.go" {
		t.Fatalf("file params = %+v, want session s1 and a.go", fileParams)
	}
	wantFile := acpwire.FileContent{Path: "a.go", Size: 12, Content: "package main"}
	a.answer(t, req, wantFile)
	fileRes := awaitValue(t, content)
	if fileRes.err != nil {
		t.Fatalf("ReadFile: %v", fileRes.err)
	}
	if fileRes.value != wantFile {
		t.Fatalf("ReadFile = %+v, want %+v", fileRes.value, wantFile)
	}
}

// TestTheBrowserCallsSendTheirMethods pins the browser calls' frames: the
// memory, plugin and skill calls, including the scan token that names a staged
// clone.
func TestTheBrowserCallsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	ctx := context.Background()

	var control acpwire.ControlParams

	memories := startCall(func() ([]acpwire.MemoryEntry, error) { return session.Memories(ctx) })
	req := nextMethod(t, a, acpwire.MethodMemoryList)
	decodeParams(t, req, &control)
	if control.SessionID != "s1" {
		t.Fatalf("memory_list params = %+v, want session s1", control)
	}
	wantMemories := []acpwire.MemoryEntry{{ID: 1, Kind: "decision", Content: "keep it small"}}
	a.answer(t, req, acpwire.MemoriesResult{Memories: wantMemories})
	res := awaitValue(t, memories)
	if res.err != nil {
		t.Fatalf("Memories: %v", res.err)
	}
	if !reflect.DeepEqual(res.value, wantMemories) {
		t.Fatalf("Memories = %+v, want %+v", res.value, wantMemories)
	}

	deleted := make(chan error, 1)
	go func() { deleted <- session.DeleteMemory(ctx, 7) }()
	req = nextMethod(t, a, acpwire.MethodMemoryDelete)
	var deleteParams acpwire.MemoryDeleteParams
	decodeParams(t, req, &deleteParams)
	if deleteParams.SessionID != "s1" || deleteParams.ID != 7 {
		t.Fatalf("memory_delete params = %+v, want session s1 and id 7", deleteParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, deleted); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	plugins := startCall(func() ([]acpwire.PluginEntry, error) { return session.Plugins(ctx) })
	req = nextMethod(t, a, acpwire.MethodPluginsList)
	decodeParams(t, req, &control)
	if control.SessionID != "s1" {
		t.Fatalf("plugins_list params = %+v, want session s1", control)
	}
	wantPlugins := []acpwire.PluginEntry{{Name: "hooks", Source: "https://example.test/hooks", Scope: "global"}}
	a.answer(t, req, acpwire.PluginsResult{Plugins: wantPlugins})
	pluginRes := awaitValue(t, plugins)
	if pluginRes.err != nil {
		t.Fatalf("Plugins: %v", pluginRes.err)
	}
	if !reflect.DeepEqual(pluginRes.value, wantPlugins) {
		t.Fatalf("Plugins = %+v, want %+v", pluginRes.value, wantPlugins)
	}

	scan := startCall(func() (acpwire.PluginScan, error) {
		return session.ScanPlugin(ctx, "https://example.test/hooks")
	})
	req = nextMethod(t, a, acpwire.MethodPluginsInstallScan)
	var scanParams acpwire.PluginScanParams
	decodeParams(t, req, &scanParams)
	if scanParams.SessionID != "s1" || scanParams.Source != "https://example.test/hooks" {
		t.Fatalf("plugins_install_scan params = %+v, want session s1 and the source", scanParams)
	}
	wantScan := acpwire.PluginScan{Token: "token-1", Name: "hooks", Source: "https://example.test/hooks"}
	a.answer(t, req, wantScan)
	res2 := awaitValue(t, scan)
	if res2.err != nil {
		t.Fatalf("ScanPlugin: %v", res2.err)
	}
	if !reflect.DeepEqual(res2.value, wantScan) {
		t.Fatalf("ScanPlugin = %+v, want %+v", res2.value, wantScan)
	}

	installed := make(chan error, 1)
	go func() { installed <- session.InstallPlugin(ctx, "token-1", "global") }()
	req = nextMethod(t, a, acpwire.MethodPluginsInstallConfirm)
	var confirmParams acpwire.PluginInstallParams
	decodeParams(t, req, &confirmParams)
	if confirmParams.SessionID != "s1" || confirmParams.ScanToken != "token-1" || confirmParams.Scope != "global" {
		t.Fatalf("plugins_install_confirm params = %+v, want session s1, the token and scope global", confirmParams)
	}
	keys := map[string]json.RawMessage{}
	decodeParams(t, req, &keys)
	if _, ok := keys["scanToken"]; !ok {
		t.Fatalf("plugins_install_confirm params = %s, want a scanToken key", req.Params)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, installed); err != nil {
		t.Fatalf("InstallPlugin: %v", err)
	}

	discarded := make(chan error, 1)
	go func() { discarded <- session.DiscardPluginScan(ctx, "token-1") }()
	req = nextMethod(t, a, acpwire.MethodPluginsInstallDiscard)
	var discardParams acpwire.PluginDiscardParams
	decodeParams(t, req, &discardParams)
	if discardParams.SessionID != "s1" || discardParams.ScanToken != "token-1" {
		t.Fatalf("plugins_install_discard params = %+v, want session s1 and the token", discardParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, discarded); err != nil {
		t.Fatalf("DiscardPluginScan: %v", err)
	}

	removed := make(chan error, 1)
	go func() { removed <- session.RemovePlugin(ctx, "hooks", "project") }()
	req = nextMethod(t, a, acpwire.MethodPluginsRemove)
	var removeParams acpwire.PluginRemoveParams
	decodeParams(t, req, &removeParams)
	if removeParams.SessionID != "s1" || removeParams.Name != "hooks" || removeParams.Scope != "project" {
		t.Fatalf("plugins_remove params = %+v, want session s1, hooks and scope project", removeParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, removed); err != nil {
		t.Fatalf("RemovePlugin: %v", err)
	}

	skills := startCall(func() ([]acpwire.SkillEntry, error) { return session.Skills(ctx) })
	req = nextMethod(t, a, acpwire.MethodSkillsList)
	decodeParams(t, req, &control)
	if control.SessionID != "s1" {
		t.Fatalf("skills_list params = %+v, want session s1", control)
	}
	wantSkills := []acpwire.SkillEntry{{Scope: "global", Source: "review", Name: "review"}}
	a.answer(t, req, acpwire.SkillsResult{Skills: wantSkills})
	skillRes := awaitValue(t, skills)
	if skillRes.err != nil {
		t.Fatalf("Skills: %v", skillRes.err)
	}
	if !reflect.DeepEqual(skillRes.value, wantSkills) {
		t.Fatalf("Skills = %+v, want %+v", skillRes.value, wantSkills)
	}

	loaded := startCall(func() (string, error) { return session.LoadSkill(ctx, "global", "review") })
	req = nextMethod(t, a, acpwire.MethodSkillsLoad)
	var skillParams acpwire.SkillParams
	decodeParams(t, req, &skillParams)
	if skillParams.SessionID != "s1" || skillParams.Scope != "global" || skillParams.Source != "review" {
		t.Fatalf("skills_load params = %+v, want session s1, scope global and source review", skillParams)
	}
	a.answer(t, req, acpwire.SkillNameResult{Name: "review"})
	loadRes := awaitValue(t, loaded)
	if loadRes.err != nil {
		t.Fatalf("LoadSkill: %v", loadRes.err)
	}
	if loadRes.value != "review" {
		t.Fatalf("LoadSkill = %q, want review", loadRes.value)
	}

	skillInstall := startCall(func() (string, error) { return session.InstallSkill(ctx, "https://example.test/s", "project") })
	req = nextMethod(t, a, acpwire.MethodSkillsInstall)
	var installParams acpwire.SkillInstallParams
	decodeParams(t, req, &installParams)
	if installParams.SessionID != "s1" || installParams.Source != "https://example.test/s" || installParams.Scope != "project" {
		t.Fatalf("skills_install params = %+v, want session s1, the source and scope project", installParams)
	}
	a.answer(t, req, acpwire.SkillNameResult{Name: "deploy"})
	installRes := awaitValue(t, skillInstall)
	if installRes.err != nil {
		t.Fatalf("InstallSkill: %v", installRes.err)
	}
	if installRes.value != "deploy" {
		t.Fatalf("InstallSkill = %q, want deploy", installRes.value)
	}

	skillRemove := make(chan error, 1)
	go func() { skillRemove <- session.RemoveSkill(ctx, "global", "review") }()
	req = nextMethod(t, a, acpwire.MethodSkillsRemove)
	decodeParams(t, req, &skillParams)
	if skillParams.SessionID != "s1" || skillParams.Scope != "global" || skillParams.Source != "review" {
		t.Fatalf("skills_remove params = %+v, want session s1, scope global and source review", skillParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, skillRemove); err != nil {
		t.Fatalf("RemoveSkill: %v", err)
	}

	autoload := make(chan error, 1)
	go func() { autoload <- session.SetSkillAutoload(ctx, "review", true) }()
	req = nextMethod(t, a, acpwire.MethodSkillsAutoload)
	var autoloadParams acpwire.SkillAutoloadParams
	decodeParams(t, req, &autoloadParams)
	if autoloadParams.SessionID != "s1" || autoloadParams.Name != "review" || !autoloadParams.On {
		t.Fatalf("skills_autoload params = %+v, want session s1, review and on", autoloadParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, autoload); err != nil {
		t.Fatalf("SetSkillAutoload: %v", err)
	}

	gate := make(chan error, 1)
	go func() { gate <- session.SetSkillGate(ctx, true) }()
	req = nextMethod(t, a, acpwire.MethodSkillGateSet)
	var gateParams acpwire.SkillGateParams
	decodeParams(t, req, &gateParams)
	if gateParams.SessionID != "s1" || !gateParams.On {
		t.Fatalf("skill_gate_set params = %+v, want session s1 and on", gateParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, gate); err != nil {
		t.Fatalf("SetSkillGate: %v", err)
	}

	cleared := make(chan error, 1)
	go func() { cleared <- session.ClearSkillGateDecision(ctx, "review") }()
	req = nextMethod(t, a, acpwire.MethodSkillGateClear)
	var clearParams acpwire.SkillGateClearParams
	decodeParams(t, req, &clearParams)
	if clearParams.SessionID != "s1" || clearParams.Skill != "review" {
		t.Fatalf("skill_gate_clear params = %+v, want session s1 and skill review", clearParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, cleared); err != nil {
		t.Fatalf("ClearSkillGateDecision: %v", err)
	}
}

// TestABrowserCallsErrorComesBack pins that a coded refusal reaches the
// caller as the connection's coded error.
func TestABrowserCallsErrorComesBack(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := startCall(func() ([]acpwire.MemoryEntry, error) {
		return session.Memories(context.Background())
	})
	req := nextMethod(t, a, acpwire.MethodMemoryList)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "the session is gone",
	}))
	res := awaitValue(t, out)
	if !acpclient.IsCode(res.err, acpwire.CodeSessionClosed) {
		t.Fatalf("Memories error = %v, want an error with code %q", res.err, acpwire.CodeSessionClosed)
	}
}
