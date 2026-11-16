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
	"finish.json",
	"inbox.json",
	"intake.json",
	"library.json",
	"projects.json",
	"recipes.json",
	"schedules.json",
	"spawn.json",
	"watches.json",
	"stream.json",
}

// fixtureTypes names the Go type each fixture value decodes into. The test
// below holds this map and the fixture files to each other in both
// directions: a fixture value whose type is not here fails (it is not a
// contract type), and an entry here with no fixture value fails. It does not
// read the contract package, so a type declared there that appears in
// neither this map nor a fixture goes unnoticed; see the implementation
// record in docs/studio-flutter/f1-client.md.
var fixtureTypes = map[string]reflect.Type{
	"AccessState":            reflect.TypeOf(AccessState{}),
	"AgentState":             reflect.TypeOf(AgentState("")),
	"AgentStatus":            reflect.TypeOf(AgentStatus{}),
	"AgentWorkspace":         reflect.TypeOf(AgentWorkspace{}),
	"GateOverride":           reflect.TypeOf(GateOverride{}),
	"PendingKind":            reflect.TypeOf(PendingKind("")),
	"PendingRequest":         reflect.TypeOf(PendingRequest{}),
	"WorkspaceSource":        reflect.TypeOf(WorkspaceSource("")),
	"Answer":                 reflect.TypeOf(Answer{}),
	"Answers":                reflect.TypeOf(Answers{}),
	"Decision":               reflect.TypeOf(Decision{}),
	"ModeAccessChoice":       reflect.TypeOf(ModeAccessChoice{}),
	"RecentPrompts":          reflect.TypeOf(RecentPrompts{}),
	"BudgetAgentSpend":       reflect.TypeOf(BudgetAgentSpend{}),
	"BudgetDaily":            reflect.TypeOf(BudgetDaily{}),
	"BudgetReport":           reflect.TypeOf(BudgetReport{}),
	"Budgets":                reflect.TypeOf(Budgets{}),
	"CommitDraft":            reflect.TypeOf(CommitDraft{}),
	"DiffFile":               reflect.TypeOf(DiffFile{}),
	"DiffResult":             reflect.TypeOf(DiffResult{}),
	"ExitRequest":            reflect.TypeOf(ExitRequest{}),
	"ExitResult":             reflect.TypeOf(ExitResult{}),
	"GateRecord":             reflect.TypeOf(GateRecord{}),
	"GateResult":             reflect.TypeOf(GateResult{}),
	"MergeRequest":           reflect.TypeOf(MergeRequest{}),
	"MergeResult":            reflect.TypeOf(MergeResult{}),
	"Inbox":                  reflect.TypeOf(Inbox{}),
	"InboxDelta":             reflect.TypeOf(InboxDelta{}),
	"InboxGroups":            reflect.TypeOf(InboxGroups{}),
	"InboxItem":              reflect.TypeOf(InboxItem{}),
	"InboxItemKind":          reflect.TypeOf(InboxItemKind("")),
	"ActionStatus":           reflect.TypeOf(ActionStatus{}),
	"ApproveResult":          reflect.TypeOf(ApproveResult{}),
	"PendingSubmission":      reflect.TypeOf(PendingSubmission{}),
	"ProjectStatus":          reflect.TypeOf(ProjectStatus{}),
	"TrustState":             reflect.TypeOf(TrustState("")),
	"SpawnRequest":           reflect.TypeOf(SpawnRequest{}),
	"SpawnResult":            reflect.TypeOf(SpawnResult{}),
	"BudgetDelta":            reflect.TypeOf(BudgetDelta{}),
	"BudgetScope":            reflect.TypeOf(BudgetScope("")),
	"DeltaKind":              reflect.TypeOf(DeltaKind("")),
	"FleetDelta":             reflect.TypeOf(FleetDelta{}),
	"ProjectRemovedDelta":    reflect.TypeOf(ProjectRemovedDelta{}),
	"Schedule":               reflect.TypeOf(Schedule{}),
	"Recipe":                 reflect.TypeOf(Recipe{}),
	"RecipeCopy":             reflect.TypeOf(RecipeCopy{}),
	"RecipeInput":            reflect.TypeOf(RecipeInput{}),
	"RecipeLimits":           reflect.TypeOf(RecipeLimits{}),
	"RecipeRun":              reflect.TypeOf(RecipeRun{}),
	"OnTrip":                 reflect.TypeOf(OnTrip{}),
	"RerouteRule":            reflect.TypeOf(RerouteRule{}),
	"WatchInfo":              reflect.TypeOf(WatchInfo{}),
	"WatchSample":            reflect.TypeOf(WatchSample{}),
	"Automations":            reflect.TypeOf(Automations{}),
	"CIFixerSettings":        reflect.TypeOf(CIFixerSettings{}),
	"CIHistory":              reflect.TypeOf(CIHistory{}),
	"Finding":                reflect.TypeOf(Finding{}),
	"ProjectAutomations":     reflect.TypeOf(ProjectAutomations{}),
	"ReviewBotSettings":      reflect.TypeOf(ReviewBotSettings{}),
	"ReviewDraft":            reflect.TypeOf(ReviewDraft{}),
	"ApprovalAuthority":      reflect.TypeOf(ApprovalAuthority("")),
	"MCPClient":              reflect.TypeOf(MCPClient{}),
	"CreateClientRequest":    reflect.TypeOf(CreateClientRequest{}),
	"CreateClientResult":     reflect.TypeOf(CreateClientResult{}),
	"MCPFactoryProjectGrant": reflect.TypeOf(MCPFactoryProjectGrant{}),
	"FactoryGrantState":      reflect.TypeOf(FactoryGrantState{}),
	"PutGrantRequest":        reflect.TypeOf(PutGrantRequest{}),
	"SkillEntry":             reflect.TypeOf(SkillEntry{}),
	"SkillPreview":           reflect.TypeOf(SkillPreview{}),
	"SkillList":              reflect.TypeOf(SkillList{}),
	"SkillStage":             reflect.TypeOf(SkillStage{}),
	"SkillConfirm":           reflect.TypeOf(SkillConfirm{}),
	"SkillDiscard":           reflect.TypeOf(SkillDiscard{}),
	"PluginEntry":            reflect.TypeOf(PluginEntry{}),
	"PluginContents":         reflect.TypeOf(PluginContents{}),
	"PluginScan":             reflect.TypeOf(PluginScan{}),
	"PluginList":             reflect.TypeOf(PluginList{}),
	"PluginStage":            reflect.TypeOf(PluginStage{}),
	"PluginConfirm":          reflect.TypeOf(PluginConfirm{}),
	"PluginDiscard":          reflect.TypeOf(PluginDiscard{}),
	"MemoryEntry":            reflect.TypeOf(MemoryEntry{}),
	"MemoryList":             reflect.TypeOf(MemoryList{}),
	"MemorySuggestion":       reflect.TypeOf(MemorySuggestion{}),
	"MemorySuggestions":      reflect.TypeOf(MemorySuggestions{}),
	"MemoryPromote":          reflect.TypeOf(MemoryPromote{}),
	"MemoryConfidence":       reflect.TypeOf(MemoryConfidence{}),
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
