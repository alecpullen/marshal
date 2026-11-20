package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// fixtureFiles are the shared fixtures under test/contract, one per contract
// source file. daemon-status.json is the tray's daemon document rather than a
// type-keyed fixture and is checked by its own tests.
var fixtureFiles = []string{
	"agents.json",
	"answers.json",
	"automations.json",
	"budgets.json",
	"clients.json",
	"connections.json",
	"drafts.json",
	"environments.json",
	"factory.json",
	"files.json",
	"finish.json",
	"inbox.json",
	"inspect.json",
	"intake.json",
	"issues.json",
	"library.json",
	"models.json",
	"network.json",
	"notifications.json",
	"planner.json",
	"projectsettings.json",
	"projects.json",
	"recipes.json",
	"review.json",
	"runs.json",
	"schedules.json",
	"secrets.json",
	"session.json",
	"spawn.json",
	"stack.json",
	"statuslinks.json",
	"stream.json",
	"terminal.json",
	"usage.json",
	"watches.json",
}

// fixtureTypes names the Go type each fixture value decodes into. The test
// below holds this map and the fixture files to each other in both
// directions: a fixture value whose type is not here fails (it is not a
// contract type), and an entry here with no fixture value fails. It does not
// read the contract package, so a type declared there that appears in
// neither this map nor a fixture goes unnoticed; see the implementation
// record in docs/studio-flutter/f1-client.md.
var fixtureTypes = map[string]reflect.Type{
	// files.go — the read-only file browser.
	"FileContent": reflect.TypeOf(FileContent{}),
	"FileEntry":   reflect.TypeOf(FileEntry{}),
	"FileList":    reflect.TypeOf(FileList{}),
	// inspect.go — one node's detail and the newest model request.
	"CallDetail":         reflect.TypeOf(CallDetail{}),
	"CompletionDetail":   reflect.TypeOf(CompletionDetail{}),
	"ExecutionDetail":    reflect.TypeOf(ExecutionDetail{}),
	"LastRequest":        reflect.TypeOf(LastRequest{}),
	"ModelRequest":       reflect.TypeOf(ModelRequest{}),
	"NodeDetail":         reflect.TypeOf(NodeDetail{}),
	"NodeDetailResponse": reflect.TypeOf(NodeDetailResponse{}),
	"NoticeDetail":       reflect.TypeOf(NoticeDetail{}),
	"RelationDetail":     reflect.TypeOf(RelationDetail{}),
	"RequestMessage":     reflect.TypeOf(RequestMessage{}),
	"RequestOptions":     reflect.TypeOf(RequestOptions{}),
	"RequestOutcome":     reflect.TypeOf(RequestOutcome{}),
	"RequestTool":        reflect.TypeOf(RequestTool{}),
	"RequestToolCall":    reflect.TypeOf(RequestToolCall{}),
	"StepDetail":         reflect.TypeOf(StepDetail{}),
	"StepDiff":           reflect.TypeOf(StepDiff{}),
	"StepDiffs":          reflect.TypeOf(StepDiffs{}),
	"SymbolDetail":       reflect.TypeOf(SymbolDetail{}),
	"ThoughtDetail":      reflect.TypeOf(ThoughtDetail{}),
	"TodoDetail":         reflect.TypeOf(TodoDetail{}),
	// review.go — a line comment on a diff and the thread it starts.
	"ReviewComment":        reflect.TypeOf(ReviewComment{}),
	"ReviewCommentRequest": reflect.TypeOf(ReviewCommentRequest{}),
	// terminal.go — a shell in the agent's workspace.
	"TerminalChunk":       reflect.TypeOf(TerminalChunk{}),
	"TerminalInput":       reflect.TypeOf(TerminalInput{}),
	"TerminalOpened":      reflect.TypeOf(TerminalOpened{}),
	"TerminalResize":      reflect.TypeOf(TerminalResize{}),
	"AccessState":         reflect.TypeOf(AccessState{}),
	"AgentState":          reflect.TypeOf(AgentState("")),
	"AgentStatus":         reflect.TypeOf(AgentStatus{}),
	"AgentWorkspace":      reflect.TypeOf(AgentWorkspace{}),
	"GateOverride":        reflect.TypeOf(GateOverride{}),
	"PendingKind":         reflect.TypeOf(PendingKind("")),
	"PendingRequest":      reflect.TypeOf(PendingRequest{}),
	"WorkspaceSource":     reflect.TypeOf(WorkspaceSource("")),
	"Answer":              reflect.TypeOf(Answer{}),
	"Answers":             reflect.TypeOf(Answers{}),
	"Decision":            reflect.TypeOf(Decision{}),
	"ModeAccessChoice":    reflect.TypeOf(ModeAccessChoice{}),
	"RecentPrompts":       reflect.TypeOf(RecentPrompts{}),
	"BudgetAgentSpend":    reflect.TypeOf(BudgetAgentSpend{}),
	"BudgetDaily":         reflect.TypeOf(BudgetDaily{}),
	"BudgetReport":        reflect.TypeOf(BudgetReport{}),
	"Budgets":             reflect.TypeOf(Budgets{}),
	"CommitDraft":         reflect.TypeOf(CommitDraft{}),
	"DiffFile":            reflect.TypeOf(DiffFile{}),
	"DiffResult":          reflect.TypeOf(DiffResult{}),
	"ExitRequest":         reflect.TypeOf(ExitRequest{}),
	"ExitResult":          reflect.TypeOf(ExitResult{}),
	"GateRecord":          reflect.TypeOf(GateRecord{}),
	"GateResult":          reflect.TypeOf(GateResult{}),
	"MergeRequest":        reflect.TypeOf(MergeRequest{}),
	"MergeResult":         reflect.TypeOf(MergeResult{}),
	"Inbox":               reflect.TypeOf(Inbox{}),
	"InboxDelta":          reflect.TypeOf(InboxDelta{}),
	"InboxGroups":         reflect.TypeOf(InboxGroups{}),
	"InboxItem":           reflect.TypeOf(InboxItem{}),
	"InboxItemKind":       reflect.TypeOf(InboxItemKind("")),
	"ActionStatus":        reflect.TypeOf(ActionStatus{}),
	"ApproveResult":       reflect.TypeOf(ApproveResult{}),
	"PendingSubmission":   reflect.TypeOf(PendingSubmission{}),
	"ProjectStatus":       reflect.TypeOf(ProjectStatus{}),
	"TrustState":          reflect.TypeOf(TrustState("")),
	"RepoIssue":           reflect.TypeOf(RepoIssue{}),
	"Recipe":              reflect.TypeOf(Recipe{}),
	"RecipeInput":         reflect.TypeOf(RecipeInput{}),
	"RecipeLimits":        reflect.TypeOf(RecipeLimits{}),
	"RecipeRun":           reflect.TypeOf(RecipeRun{}),
	"RecipeCopy":          reflect.TypeOf(RecipeCopy{}),
	"SpawnRequest":        reflect.TypeOf(SpawnRequest{}),
	"SpawnResult":         reflect.TypeOf(SpawnResult{}),
	"SessionSummary":      reflect.TypeOf(SessionSummary{}),
	"SessionList":         reflect.TypeOf(SessionList{}),
	"SessionIndex":        reflect.TypeOf(SessionIndex{}),
	"ProjectSessions":     reflect.TypeOf(ProjectSessions{}),
	"SessionRow":          reflect.TypeOf(SessionRow{}),
	"PromptRequest":       reflect.TypeOf(PromptRequest{}),
	"SteerRequest":        reflect.TypeOf(SteerRequest{}),
	"ModeRequest":         reflect.TypeOf(ModeRequest{}),
	"Roster":              reflect.TypeOf(Roster{}),
	"RosterRole":          reflect.TypeOf(RosterRole{}),
	"RosterBudget":        reflect.TypeOf(RosterBudget{}),
	"StackSnapshot":       reflect.TypeOf(StackSnapshot{}),
	"StackPatch":          reflect.TypeOf(StackPatch{}),
	"WireNode":            reflect.TypeOf(WireNode{}),
	"WireStep":            reflect.TypeOf(WireStep{}),
	"WireTodoLabel":       reflect.TypeOf(WireTodoLabel{}),
	"WireThought":         reflect.TypeOf(WireThought{}),
	"WireTool":            reflect.TypeOf(WireTool{}),
	"WireCall":            reflect.TypeOf(WireCall{}),
	"WireRunning":         reflect.TypeOf(WireRunning{}),
	"WireCompletion":      reflect.TypeOf(WireCompletion{}),
	"WirePlan":            reflect.TypeOf(WirePlan{}),
	"WireMessage":         reflect.TypeOf(WireMessage{}),
	"WireThinking":        reflect.TypeOf(WireThinking{}),
	"WireRunEvent":        reflect.TypeOf(WireRunEvent{}),
	"WireBand":            reflect.TypeOf(WireBand{}),
	"WireTask":            reflect.TypeOf(WireTask{}),
	"WireBandEntity":      reflect.TypeOf(WireBandEntity{}),
	"WireReceipt":         reflect.TypeOf(WireReceipt{}),
	"BudgetDelta":         reflect.TypeOf(BudgetDelta{}),
	"BudgetScope":         reflect.TypeOf(BudgetScope("")),
	"DeltaKind":           reflect.TypeOf(DeltaKind("")),
	"FleetDelta":          reflect.TypeOf(FleetDelta{}),
	"ProjectRemovedDelta": reflect.TypeOf(ProjectRemovedDelta{}),
	"DeltaSessions":       reflect.TypeOf(DeltaKind("")),
	"SessionsDelta":       reflect.TypeOf(SessionsDelta{}),
	// automations.go — the review bot and the CI fixer.
	"Automations":            reflect.TypeOf(Automations{}),
	"CIFixerSettings":        reflect.TypeOf(CIFixerSettings{}),
	"CIHistory":              reflect.TypeOf(CIHistory{}),
	"FactoryGrantState":      reflect.TypeOf(FactoryGrantState{}),
	"Finding":                reflect.TypeOf(Finding{}),
	"MCPFactoryProjectGrant": reflect.TypeOf(MCPFactoryProjectGrant{}),
	"ProjectAutomations":     reflect.TypeOf(ProjectAutomations{}),
	"PutGrantRequest":        reflect.TypeOf(PutGrantRequest{}),
	"ReviewBotSettings":      reflect.TypeOf(ReviewBotSettings{}),
	"ReviewDraft":            reflect.TypeOf(ReviewDraft{}),
	"WebhookSecret":          reflect.TypeOf(WebhookSecret{}),
	// schedules.go — cron entries the bridge holds.
	"Schedule": reflect.TypeOf(Schedule{}),
	// watches.go — what trips a watch and what it did.
	"LastReroute":  reflect.TypeOf(LastReroute{}),
	"OnTrip":       reflect.TypeOf(OnTrip{}),
	"RerouteRule":  reflect.TypeOf(RerouteRule{}),
	"WatchCreate":  reflect.TypeOf(WatchCreate{}),
	"WatchInfo":    reflect.TypeOf(WatchInfo{}),
	"WatchSample":  reflect.TypeOf(WatchSample{}),
	"WatchSpec":    reflect.TypeOf(WatchSpec{}),
	"WatchStarted": reflect.TypeOf(WatchStarted{}),
	// library.go — skills, plugins, MCP and memory.
	"MemoryConfidence":  reflect.TypeOf(MemoryConfidence{}),
	"MemoryEntry":       reflect.TypeOf(MemoryEntry{}),
	"MemoryList":        reflect.TypeOf(MemoryList{}),
	"MemoryPromote":     reflect.TypeOf(MemoryPromote{}),
	"MemorySuggestion":  reflect.TypeOf(MemorySuggestion{}),
	"MemorySuggestions": reflect.TypeOf(MemorySuggestions{}),
	"PluginConfirm":     reflect.TypeOf(PluginConfirm{}),
	"PluginContents":    reflect.TypeOf(PluginContents{}),
	"PluginDiscard":     reflect.TypeOf(PluginDiscard{}),
	"PluginEntry":       reflect.TypeOf(PluginEntry{}),
	"PluginList":        reflect.TypeOf(PluginList{}),
	"PluginScan":        reflect.TypeOf(PluginScan{}),
	"PluginStage":       reflect.TypeOf(PluginStage{}),
	"SkillConfirm":      reflect.TypeOf(SkillConfirm{}),
	"SkillDiscard":      reflect.TypeOf(SkillDiscard{}),
	"SkillEntry":        reflect.TypeOf(SkillEntry{}),
	"SkillList":         reflect.TypeOf(SkillList{}),
	"SkillPreview":      reflect.TypeOf(SkillPreview{}),
	"SkillStage":        reflect.TypeOf(SkillStage{}),
	// clients.go — the confirmation rule a client carries.
	"ApprovalAuthority":   reflect.TypeOf(ApprovalAuthority("")),
	"CreateClientRequest": reflect.TypeOf(CreateClientRequest{}),
	"CreateClientResult":  reflect.TypeOf(CreateClientResult{}),
	"MCPClient":           reflect.TypeOf(MCPClient{}),
	// connections.go — forges, their credentials and what they permit.
	"Credential":      reflect.TypeOf(Credential{}),
	"CredentialInput": reflect.TypeOf(CredentialInput{}),
	"Repo":            reflect.TypeOf(Repo{}),
	// models.go — providers, presets, routing and the caps.
	"Binding":         reflect.TypeOf(Binding{}),
	"BudgetsUpdate":   reflect.TypeOf(BudgetsUpdate{}),
	"ModelsConfig":    reflect.TypeOf(ModelsConfig{}),
	"Preset":          reflect.TypeOf(Preset{}),
	"PresetsUpdate":   reflect.TypeOf(PresetsUpdate{}),
	"ProbeModel":      reflect.TypeOf(ProbeModel{}),
	"ProbeRequest":    reflect.TypeOf(ProbeRequest{}),
	"ProbeResult":     reflect.TypeOf(ProbeResult{}),
	"ProviderKey":     reflect.TypeOf(ProviderKey{}),
	"ProviderView":    reflect.TypeOf(ProviderView{}),
	"ProviderWrite":   reflect.TypeOf(ProviderWrite{}),
	"ProvidersUpdate": reflect.TypeOf(ProvidersUpdate{}),
	"RoutingUpdate":   reflect.TypeOf(RoutingUpdate{}),
	// network.go — the egress hosts, their rules and the pending decision.
	"NetAgentRow":       reflect.TypeOf(NetAgentRow{}),
	"NetDecision":       reflect.TypeOf(NetDecision{}),
	"NetDecisionKind":   reflect.TypeOf(NetDecisionKind("")),
	"NetDecisionResult": reflect.TypeOf(NetDecisionResult{}),
	"NetHostRow":        reflect.TypeOf(NetHostRow{}),
	"NetHosts":          reflect.TypeOf(NetHosts{}),
	"NetPending":        reflect.TypeOf(NetPending{}),
	"NetPendingList":    reflect.TypeOf(NetPendingList{}),
	"NetRecord":         reflect.TypeOf(NetRecord{}),
	// secrets.go — references and a value that goes one way.
	"SecretList":    reflect.TypeOf(SecretList{}),
	"SecretWrite":   reflect.TypeOf(SecretWrite{}),
	"SecretsStatus": reflect.TypeOf(SecretsStatus{}),
	// environments.go — templates, their source and their builds.
	"EnvBuildStart": reflect.TypeOf(EnvBuildStart{}),
	"EnvBuilds":     reflect.TypeOf(EnvBuilds{}),
	"EnvCreate":     reflect.TypeOf(EnvCreate{}),
	"EnvDiagnostic": reflect.TypeOf(EnvDiagnostic{}),
	"EnvDoc":        reflect.TypeOf(EnvDoc{}),
	"EnvDraft":      reflect.TypeOf(EnvDraft{}),
	"EnvFile":       reflect.TypeOf(EnvFile{}),
	"EnvInject":     reflect.TypeOf(EnvInject{}),
	"EnvListItem":   reflect.TypeOf(EnvListItem{}),
	"EnvLoaded":     reflect.TypeOf(EnvLoaded{}),
	"EnvMeta":       reflect.TypeOf(EnvMeta{}),
	"EnvMount":      reflect.TypeOf(EnvMount{}),
	"EnvNetwork":    reflect.TypeOf(EnvNetwork{}),
	"EnvPackages":   reflect.TypeOf(EnvPackages{}),
	"EnvPolicy":     reflect.TypeOf(EnvPolicy{}),
	"EnvPool":       reflect.TypeOf(EnvPool{}),
	"EnvPreview":    reflect.TypeOf(EnvPreview{}),
	"EnvResources":  reflect.TypeOf(EnvResources{}),
	"EnvSection":    reflect.TypeOf(EnvSection{}),
	"EnvSetup":      reflect.TypeOf(EnvSetup{}),
	"EnvStarts":     reflect.TypeOf(EnvStarts{}),
	"EnvVersion":    reflect.TypeOf(EnvVersion{}),
	"EnvWorkspace":  reflect.TypeOf(EnvWorkspace{}),
	// usage.go — spend, the audit log and the disk.
	"AuditEvent":  reflect.TypeOf(AuditEvent{}),
	"DiskStatus":  reflect.TypeOf(DiskStatus{}),
	"PruneResult": reflect.TypeOf(PruneResult{}),
	"UsagePoint":  reflect.TypeOf(UsagePoint{}),
	"UsageReport": reflect.TypeOf(UsageReport{}),
	"UsageTotals": reflect.TypeOf(UsageTotals{}),
	// notifications.go — where Marshal reaches the user.
	"NotifyConfig":     reflect.TypeOf(NotifyConfig{}),
	"NotifyTestResult": reflect.TypeOf(NotifyTestResult{}),
	"NotifyWebhook":    reflect.TypeOf(NotifyWebhook{}),
	// statuslinks.go — one agent's revocable public link.
	"StatusLink":        reflect.TypeOf(StatusLink{}),
	"StatusLinkCreate":  reflect.TypeOf(StatusLinkCreate{}),
	"StatusLinkCreated": reflect.TypeOf(StatusLinkCreated{}),
	// factory.json — a work item in each of its four resting states, its
	// parts, and the command a client posts back.
	"FactoryItemDraft":          reflect.TypeOf(FactoryItem{}),
	"FactoryItemNeedsInput":     reflect.TypeOf(FactoryItem{}),
	"FactoryItemReady":          reflect.TypeOf(FactoryItem{}),
	"FactoryItemClosed":         reflect.TypeOf(FactoryItem{}),
	"FactoryConfig":             reflect.TypeOf(FactoryConfig{}),
	"FactoryCreate":             reflect.TypeOf(FactoryCreate{}),
	"FactoryApproval":           reflect.TypeOf(FactoryApproval{}),
	"FactoryWorker":             reflect.TypeOf(FactoryWorker{}),
	"FactoryPR":                 reflect.TypeOf(FactoryPR{}),
	"FactoryCheckKey":           reflect.TypeOf(FactoryCheckKey{}),
	"FactoryCheck":              reflect.TypeOf(FactoryCheck{}),
	"FactoryCI":                 reflect.TypeOf(FactoryCI{}),
	"FactoryRound":              reflect.TypeOf(FactoryRound{}),
	"FactoryReport":             reflect.TypeOf(FactoryReport{}),
	"FactoryDecision":           reflect.TypeOf(FactoryDecision{}),
	"FactoryEvent":              reflect.TypeOf(FactoryEvent{}),
	"FactoryGrant":              reflect.TypeOf(FactoryGrant{}),
	"FactoryCompletion":         reflect.TypeOf(FactoryCompletion{}),
	"FactoryCompletionRecord":   reflect.TypeOf(FactoryCompletionRecord{}),
	"FactoryAssessment":         reflect.TypeOf(FactoryAssessment{}),
	"FactoryPRLink":             reflect.TypeOf(FactoryPRLink{}),
	"FactoryMergeBatch":         reflect.TypeOf(FactoryMergeBatch{}),
	"FactoryMergeStep":          reflect.TypeOf(FactoryMergeStep{}),
	"FactorySubagentSettings":   reflect.TypeOf(FactorySubagentSettings{}),
	"FactorySubagentOverrides":  reflect.TypeOf(FactorySubagentOverrides{}),
	"EditSnapshot":              reflect.TypeOf(EditSnapshot{}),
	"SelectedFile":              reflect.TypeOf(SelectedFile{}),
	"ReviewFinding":             reflect.TypeOf(ReviewFinding{}),
	"FactoryRevisionAssessment": reflect.TypeOf(FactoryRevisionAssessment{}),
	"FactoryRevisionProposal":   reflect.TypeOf(FactoryRevisionProposal{}),
	"FactoryRevisionCommand":    reflect.TypeOf(FactoryRevisionCommand{}),
	"FactorySettingsVersion":    reflect.TypeOf(FactorySettingsVersion{}),
	"FactoryCompletionPR":       reflect.TypeOf(FactoryCompletionPR{}),
	"FactoryCompletionCommand":  reflect.TypeOf(FactoryCompletionCommand{}),
	"FactoryCommand":            reflect.TypeOf(FactoryCommand{}),
	// planner.json — the planning thread, its snapshots and its briefing.
	"PlannerOccupancy":        reflect.TypeOf(PlannerOccupancy("")),
	"WorkerCapabilities":      reflect.TypeOf(WorkerCapabilities{}),
	"PlannerArtifact":         reflect.TypeOf(PlannerArtifact{}),
	"PlannerTurn":             reflect.TypeOf(PlannerTurn{}),
	"PlannerDecision":         reflect.TypeOf(PlannerDecision{}),
	"PlannerView":             reflect.TypeOf(PlannerView{}),
	"PlannerCreateRequest":    reflect.TypeOf(PlannerCreateRequest{}),
	"PlannerPromptRequest":    reflect.TypeOf(PlannerPromptRequest{}),
	"PlannerAnswerRequest":    reflect.TypeOf(PlannerAnswerRequest{}),
	"PlannerTurnReply":        reflect.TypeOf(PlannerTurnReply{}),
	"PlannerEvent":            reflect.TypeOf(PlannerEvent{}),
	"PlannerSnapshotArtifact": reflect.TypeOf(PlannerSnapshotArtifact{}),
	"PlannerSnapshotRequest":  reflect.TypeOf(PlannerSnapshotRequest{}),
	"PlannerRejection":        reflect.TypeOf(PlannerRejection{}),
	"PlannerSnapshotFile":     reflect.TypeOf(PlannerSnapshotFile{}),
	"PlannerSnapshotView":     reflect.TypeOf(PlannerSnapshotView{}),
	"PlannerSnapshotReply":    reflect.TypeOf(PlannerSnapshotReply{}),
	"PlannerContextArtifact":  reflect.TypeOf(PlannerContextArtifact{}),
	"PlannerContextDecision":  reflect.TypeOf(PlannerContextDecision{}),
	"PlannerContextQuestion":  reflect.TypeOf(PlannerContextQuestion{}),
	"PlannerContextView":      reflect.TypeOf(PlannerContextView{}),
	// drafts.json — an unfinished handoff and its versioned writes.
	"WorkingDraft":              reflect.TypeOf(WorkingDraft{}),
	"WorkingDraftContent":       reflect.TypeOf(WorkingDraftContent{}),
	"WorkingDraftCreateRequest": reflect.TypeOf(WorkingDraftCreateRequest{}),
	"WorkingDraftSaveRequest":   reflect.TypeOf(WorkingDraftSaveRequest{}),
	"WorkingDraftDeleteRequest": reflect.TypeOf(WorkingDraftDeleteRequest{}),
	// runs.json — a plan run, its tasks and the list entry.
	"SDDRun":       reflect.TypeOf(SDDRun{}),
	"RunTask":      reflect.TypeOf(RunTask{}),
	"RunStage":     reflect.TypeOf(RunStage{}),
	"RunCommit":    reflect.TypeOf(RunCommit{}),
	"RunGate":      reflect.TypeOf(RunGate{}),
	"RunListEntry": reflect.TypeOf(RunListEntry{}),
	// projectsettings.json — a project's settings, its health and discovery.
	"ProjectIntake":            reflect.TypeOf(ProjectIntake{}),
	"FactoryDefaults":          reflect.TypeOf(FactoryDefaults{}),
	"ProjectSettings":          reflect.TypeOf(ProjectSettings{}),
	"MirrorHealth":             reflect.TypeOf(MirrorHealth{}),
	"WorkspaceHealth":          reflect.TypeOf(WorkspaceHealth{}),
	"VerifyCommands":           reflect.TypeOf(VerifyCommands{}),
	"ProjectHealth":            reflect.TypeOf(ProjectHealth{}),
	"DiscoveryCapabilityState": reflect.TypeOf(DiscoveryCapabilityState("")),
	"DiscoveryCapability":      reflect.TypeOf(DiscoveryCapability{}),
	"DiscoveryFact":            reflect.TypeOf(DiscoveryFact{}),
	"DiscoveryRecommendation":  reflect.TypeOf(DiscoveryRecommendation{}),
	"DiscoveryGap":             reflect.TypeOf(DiscoveryGap{}),
	"DiscoveryWorktree":        reflect.TypeOf(DiscoveryWorktree{}),
	"DiscoveryReport":          reflect.TypeOf(DiscoveryReport{}),
}

// TestContractFixturesRoundTrip holds the contract types to the shared
// fixtures: every value decodes into its Go type and re-encodes to the same
// JSON, so a renamed tag or a changed field shape fails here before any
// client is written against it. The app compares the same fixtures with the
// generated Dart types (app/test/client/contract_fixture_test.dart).
func TestContractFixturesRoundTrip(t *testing.T) {
	seen := make(map[string]bool, len(fixtureTypes))
	for _, name := range fixtureFiles {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("../../../test/contract", name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var values map[string]json.RawMessage
			if err := json.Unmarshal(raw, &values); err != nil {
				t.Fatalf("%s is not an object of type names to values: %v", name, err)
			}
			for _, key := range sortedTypeNames(values) {
				seen[key] = true
				typ, ok := fixtureTypes[key]
				if !ok {
					t.Errorf("%s: %q is not a contract type; add it to fixtureTypes", name, key)
					continue
				}
				value := reflect.New(typ)
				if err := json.Unmarshal(values[key], value.Interface()); err != nil {
					t.Errorf("%s: decode %s: %v", name, key, err)
					continue
				}
				encoded, err := json.Marshal(value.Elem().Interface())
				if err != nil {
					t.Errorf("%s: encode %s: %v", name, key, err)
					continue
				}
				same, err := sameJSON(values[key], encoded)
				if err != nil {
					t.Errorf("%s: %s: %v", name, key, err)
					continue
				}
				if !same {
					t.Errorf("%s: %s does not round trip\n got: %s\nwant: %s", name, key, encoded, values[key])
				}
			}
		})
	}

	// Every listed type appears in a fixture, so an entry cannot be added
	// without the value a client is written against. A type declared in the
	// contract package but listed in neither this map nor a fixture passes
	// unnoticed (see the note on fixtureTypes).
	names := make([]string, 0, len(fixtureTypes))
	for name := range fixtureTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !seen[name] {
			t.Errorf("%s has no fixture value", name)
		}
	}
}

// sortedTypeNames returns a fixture object's keys in order, so a failure
// reads the same on every run.
func sortedTypeNames(values map[string]json.RawMessage) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// sameJSON reports whether two JSON documents mean the same thing: key order,
// whitespace and the spelling of a number do not count.
func sameJSON(a, b []byte) (bool, error) {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false, err
	}
	return reflect.DeepEqual(x, y), nil
}
