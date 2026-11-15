package bridge

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mcpFactoryToolFixture is the offline harness these tests share: one bridge
// with a real factory journal, one registered project and repository, and one
// MCP client holding a token and a grant. Nothing here reaches a forge, a git
// remote, a container runtime or a model provider.
type mcpFactoryToolFixture struct {
	*factoryMCPFixture
	clientID string
	token    string
}

// newMCPFactoryToolFixture builds the fixture with one granted client.
func newMCPFactoryToolFixture(t *testing.T, grant MCPFactoryProjectGrant) *mcpFactoryToolFixture {
	t.Helper()
	fx := newFactoryMCPFixture(t, grant)
	grant.ProjectID = fx.projectID
	id, token := testAddClient(t, fx.srv, MCPClient{
		ID: "c1", Name: "claude", FactoryGrants: []MCPFactoryProjectGrant{grant},
	})
	return &mcpFactoryToolFixture{factoryMCPFixture: fx, clientID: id, token: token}
}

// proposal is the create_draft argument object for this fixture's project.
func (f *mcpFactoryToolFixture) proposal(requestID string) map[string]any {
	return map[string]any{
		"projectId": f.projectID,
		"requestId": requestID,
		"title":     "Add the thing",
		"plan":      "implement the thing",
		"spec":      "the specification",
		"config": map[string]any{
			"repoId":                   "repo-1",
			"targetBranch":             "main",
			"native":                   true,
			"implementerRouting":       json.RawMessage(`{"profile":"chosen"}`),
			"reviewerRouting":          json.RawMessage(`{"profile":"chosen"}`),
			"noRequiredCiAcknowledged": true,
		},
	}
}

// update builds an update_draft argument object from the same proposal.
func (f *mcpFactoryToolFixture) update(itemID, requestID string, expectedVersion uint64, title string) map[string]any {
	args := f.proposal(requestID)
	args["itemId"] = itemID
	args["expectedVersion"] = expectedVersion
	args["title"] = title
	return args
}

// approve builds an approve_and_queue argument object.
func (f *mcpFactoryToolFixture) approve(itemID, requestID string, expectedVersion uint64, authority string) map[string]any {
	return map[string]any{
		"projectId": f.projectID, "itemId": itemID, "requestId": requestID,
		"expectedVersion": expectedVersion, "authority": authority,
	}
}

// revoke switches every capability off through the management path, which is
// the write an editor makes. It reports an error instead of failing so a
// goroutine can race it against a mutation.
func (f *mcpFactoryToolFixture) revoke() error {
	state, err := f.srv.factoryAuth.grantState(f.clientID, f.projectID)
	if err != nil {
		return err
	}
	_, _, err = f.srv.factoryAuth.putGrant(f.clientID, f.projectID,
		MCPFactoryProjectGrant{ProjectID: f.projectID, ApprovalAuthority: factoryAuthorityNone}, state.Version)
	return err
}

// restoreGrant puts the fixture's grant back, so one test can race a
// revocation more than once.
func (f *mcpFactoryToolFixture) restoreGrant(grant MCPFactoryProjectGrant) error {
	grant.ProjectID = f.projectID
	state, err := f.srv.factoryAuth.grantState(f.clientID, f.projectID)
	if err != nil {
		return err
	}
	_, _, err = f.srv.factoryAuth.putGrant(f.clientID, f.projectID, grant, state.Version)
	return err
}

// setDelegation writes the project's delegation toggle and designated client
// the way the Studio settings route does.
func (f *mcpFactoryToolFixture) setDelegation(t *testing.T, enabled bool, clientID string) {
	t.Helper()
	etag, err := f.fleet.ws.ProjectSettingsETag(f.root)
	if err != nil {
		t.Fatal(err)
	}
	ps := f.fleet.ws.ProjectSettingsFor(f.root)
	ps.PlannerDelegationEnabled = enabled
	ps.DesignatedMCPClientID = clientID
	if _, _, err := f.fleet.ws.PutProjectSettingsIfMatch(f.root, ps, etag); err != nil {
		t.Fatal(err)
	}
}

// seedQuestion moves an item into needs_input with an open decision, so the
// answer tool has one to answer.
func (f *mcpFactoryToolFixture) seedQuestion(t *testing.T, itemID string) uint64 {
	t.Helper()
	current, err := f.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := f.store.Update(itemID, "seed-needs-input", current.Version, json.RawMessage(`{"seed":"needs_input"}`), func(dst *FactoryItem) error {
		dst.State = "needs_input"
		dst.Effects = append(dst.Effects, FactoryEffect{
			ID: "effect-1", Kind: "implement", WorkerRole: "implementer", Status: "completed",
			Outcome: &FactoryOutcome{
				EffectID: "effect-1", Confirmed: true,
				Report: &FactoryReport{Outcome: "needs_input", Question: "which branch?"},
			},
		})
		dst.Decision = &FactoryDecision{ID: "decision-1", Kind: "question", Question: "which branch?", EffectID: "effect-1"}
		return nil
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return seeded.Version
}

// answer builds an answer_question argument object.
func (f *mcpFactoryToolFixture) answer(itemID, requestID string, expectedVersion uint64, decisionID, authority string) map[string]any {
	return map[string]any{
		"projectId": f.projectID, "itemId": itemID, "requestId": requestID,
		"expectedVersion": expectedVersion, "decisionId": decisionID,
		"answer": "use the release branch", "authority": authority,
	}
}

// factoryToolReply is the standard MCP tool result these tools produce.
type factoryToolReply struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// callFactoryTool sends one tools/call and decodes the standard result. It
// reports errors rather than failing the test, so a goroutine can use it.
func callFactoryTool(s *Server, token, tool string, args any) (factoryToolReply, map[string]any, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return factoryToolReply{}, nil, err
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":` +
		strconv.Quote(tool) + `,"arguments":` + string(raw) + `}}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var out rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		return factoryToolReply{}, nil, fmt.Errorf("decode rpc response: %w (%s)", err, rec.Body)
	}
	if out.Error != nil {
		return factoryToolReply{}, nil, fmt.Errorf("rpc error %d: %s", out.Error.Code, out.Error.Message)
	}
	result, err := json.Marshal(out.Result)
	if err != nil {
		return factoryToolReply{}, nil, err
	}
	var reply factoryToolReply
	if err := json.Unmarshal(result, &reply); err != nil {
		return factoryToolReply{}, nil, fmt.Errorf("decode tool result: %w (%s)", err, result)
	}
	if len(reply.Content) != 1 || reply.Content[0].Type != "text" {
		return reply, nil, fmt.Errorf("tool result has %d content blocks, want one text block: %s", len(reply.Content), result)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(reply.Content[0].Text), &value); err != nil {
		return reply, nil, fmt.Errorf("tool result text is not a JSON object: %w (%s)", err, reply.Content[0].Text)
	}
	return reply, value, nil
}

// runFactoryTool calls one tool and returns its result.
func runFactoryTool(t *testing.T, s *Server, token, tool string, args any) (factoryToolReply, map[string]any) {
	t.Helper()
	reply, value, err := callFactoryTool(s, token, tool, args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return reply, value
}

// factoryToolOK calls one tool and requires a successful domain result.
func factoryToolOK(t *testing.T, s *Server, token, tool string, args any) map[string]any {
	t.Helper()
	reply, value := runFactoryTool(t, s, token, tool, args)
	if reply.IsError {
		t.Fatalf("%s: domain failure %v", tool, value)
	}
	return value
}

// factoryToolErrorCode calls one tool and requires a domain failure, which it
// returns the code of.
func factoryToolErrorCode(t *testing.T, s *Server, token, tool string, args any) string {
	t.Helper()
	reply, value := runFactoryTool(t, s, token, tool, args)
	if !reply.IsError {
		t.Fatalf("%s: succeeded, want a domain failure: %v", tool, value)
	}
	code, _ := value["code"].(string)
	if code == "" {
		t.Fatalf("%s: domain failure carries no code: %v", tool, value)
	}
	return code
}

func TestMCPFactoryToolsListedWithSchemas(t *testing.T) {
	f := newMCPFactoryToolFixture(t, fullGrant())

	out := callMCPRaw(t, f.srv, f.token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if out.Error != nil {
		t.Fatalf("tools/list: %s", out.Error.Message)
	}
	raw, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		factoryToolProjects:        nil,
		factoryToolList:            {"projectId"},
		factoryToolGet:             {"projectId", "itemId"},
		factoryToolCreateDraft:     {"projectId", "requestId", "title", "plan", "spec", "config"},
		factoryToolUpdateDraft:     {"projectId", "itemId", "requestId", "expectedVersion", "title", "plan", "spec", "config"},
		factoryToolApproveAndQueue: {"projectId", "itemId", "requestId", "expectedVersion", "authority"},
		factoryToolAnswerQuestion:  {"projectId", "itemId", "requestId", "expectedVersion", "decisionId", "answer", "authority"},
	}
	seen := map[string]bool{}
	for _, tool := range listed.Tools {
		// No tool grants, revokes or edits authority: the whole point of the
		// grant editor being a Studio surface is that an MCP client cannot
		// widen its own authority.
		if strings.Contains(tool.Name, "grant") || strings.Contains(tool.Name, "revoke") {
			t.Fatalf("tools/list exposes an authority tool: %q", tool.Name)
		}
		if _, ok := want[tool.Name]; !ok {
			continue
		}
		seen[tool.Name] = true
		if tool.Description == "" {
			t.Fatalf("%s has no description", tool.Name)
		}
		if tool.InputSchema["type"] != "object" {
			t.Fatalf("%s inputSchema type = %v, want object", tool.Name, tool.InputSchema["type"])
		}
		props, _ := tool.InputSchema["properties"].(map[string]any)
		if props == nil {
			t.Fatalf("%s inputSchema has no properties", tool.Name)
		}
		var required []string
		if list, ok := tool.InputSchema["required"].([]any); ok {
			for _, r := range list {
				required = append(required, r.(string))
			}
		}
		if strings.Join(required, ",") != strings.Join(want[tool.Name], ",") {
			t.Fatalf("%s required = %v, want %v", tool.Name, required, want[tool.Name])
		}
		for _, name := range want[tool.Name] {
			if _, ok := props[name]; !ok {
				t.Fatalf("%s inputSchema has no %q property", tool.Name, name)
			}
		}
		if _, ok := props["projectRoot"]; ok {
			t.Fatalf("%s offers a projectRoot argument, which is resolved server-side", tool.Name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("tools/list is missing %s", name)
		}
	}
}

func TestMCPFactoryProjectAndRepoScope(t *testing.T) {
	f := newMCPFactoryToolFixture(t, fullGrant())

	otherRoot := t.TempDir()
	if err := f.fleet.ws.AddProject(otherRoot); err != nil {
		t.Fatal(err)
	}
	otherProject, ok := f.fleet.ws.ProjectID(otherRoot)
	if !ok {
		t.Fatal("a registered project has no factory id")
	}
	if err := f.fleet.ws.PutRepo(Repo{
		ID: "repo-2", URL: "https://example.invalid/other.git", Branch: "main",
		OwnerID: DefaultOwnerID, Forge: "github", CredRef: "test-pat",
	}); err != nil {
		t.Fatal(err)
	}
	// The client may spawn on any repo, but its factory authority names one.
	client, ok := f.fleet.ws.Client(f.clientID)
	if !ok {
		t.Fatal("the fixture client is missing")
	}
	client.AllowedRepos = []string{"repo-1"}
	if err := f.fleet.ws.PutClient(client); err != nil {
		t.Fatal(err)
	}

	// Its own project is what it sees, with the capabilities it holds.
	projects := factoryToolOK(t, f.srv, f.token, factoryToolProjects, map[string]any{})
	entries, _ := projects["projects"].([]any)
	if len(entries) != 1 {
		t.Fatalf("factory_projects listed %d projects, want the one this client was granted", len(entries))
	}
	entry := entries[0].(map[string]any)
	if entry["projectId"] != f.projectID {
		t.Fatalf("factory_projects listed %v, want %s", entry["projectId"], f.projectID)
	}
	capabilities, _ := entry["capabilities"].([]any)
	got := map[string]bool{}
	for _, capability := range capabilities {
		got[capability.(string)] = true
	}
	for _, capability := range []string{
		factoryCapabilityRead, factoryCapabilityCreateDraft, factoryCapabilityEditDraft,
		factoryCapabilityAnswerQuestion, factoryCapabilityApproveAndQueue,
	} {
		if !got[capability] {
			t.Fatalf("factory_projects did not report %q: %v", capability, capabilities)
		}
	}
	if got[factoryCapabilityDelegatedAnswer] {
		t.Fatal("factory_projects reported delegated answering while the project's toggle is off")
	}
	if entry["approvalAuthority"] != factoryAuthorityUserDirected {
		t.Fatalf("approvalAuthority = %v, want the granted authority", entry["approvalAuthority"])
	}

	// An item in the client's own project.
	created := factoryToolOK(t, f.srv, f.token, factoryToolCreateDraft, f.proposal("scope-1"))
	itemID := created["itemId"].(string)

	// An item in the other project, written straight to the journal: only the
	// grant decides who may see it.
	if _, err := f.store.Create(FactoryCreate{
		RequestID: "other-project-1", Title: "other project work", Plan: "plan", Spec: "spec",
		Config: FactoryConfig{RepoID: "repo-1", ProjectRoot: otherRoot, TargetBranch: "main", Native: true},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	// A project the client holds nothing on is not listed, not readable and
	// not writable — and it is the same answer as a project that never
	// existed.
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolList, map[string]any{"projectId": otherProject}); code != factoryCodeNotFound {
		t.Fatalf("factory_list on an ungranted project = %q, want %q", code, factoryCodeNotFound)
	}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolGet, map[string]any{"projectId": otherProject, "itemId": itemID}); code != factoryCodeNotFound {
		t.Fatalf("factory_get across projects = %q, want %q", code, factoryCodeNotFound)
	}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolList, map[string]any{"projectId": "not-a-project"}); code != factoryCodeNotFound {
		t.Fatalf("factory_list on an unknown project = %q, want %q", code, factoryCodeNotFound)
	}
	other := f.proposal("scope-other")
	other["projectId"] = otherProject
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolCreateDraft, other); code != factoryCodeNotFound {
		t.Fatalf("create in an ungranted project = %q, want %q", code, factoryCodeNotFound)
	}
	// A registered repository outside this client's allowlist is refused by
	// the same authorizer, and nothing is written.
	offList := f.proposal("scope-repo-2")
	offList["config"] = map[string]any{
		"repoId": "repo-2", "targetBranch": "main", "native": true,
		"implementerRouting": json.RawMessage(`{"profile":"chosen"}`),
		"reviewerRouting":    json.RawMessage(`{"profile":"chosen"}`),
	}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolCreateDraft, offList); code != factoryCodeNotFound {
		t.Fatalf("create against a repository outside the allowlist = %q, want %q", code, factoryCodeNotFound)
	}

	// This project's list is its own items only.
	list := factoryToolOK(t, f.srv, f.token, factoryToolList, map[string]any{"projectId": f.projectID})
	items, _ := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["itemId"] != itemID {
		t.Fatalf("factory_list returned %v, want only %s", items, itemID)
	}
	if n := len(f.api.List()); n != 2 {
		t.Fatalf("the journal holds %d items, want the two created here", n)
	}

	// A client with no grant at all sees no projects and reaches nothing.
	id, token := testAddClient(t, f.srv, MCPClient{ID: "c2", Name: "ungranted"})
	empty := factoryToolOK(t, f.srv, token, factoryToolProjects, map[string]any{})
	if entries, _ := empty["projects"].([]any); len(entries) != 0 {
		t.Fatalf("a client with no grant was listed %v", entries)
	}
	if code := factoryToolErrorCode(t, f.srv, token, factoryToolList, map[string]any{"projectId": f.projectID}); code != factoryCodeNotFound {
		t.Fatalf("an ungranted client's factory_list = %q, want %q", code, factoryCodeNotFound)
	}
	if code := factoryToolErrorCode(t, f.srv, token, factoryToolGet, map[string]any{"projectId": f.projectID, "itemId": itemID}); code != factoryCodeNotFound {
		t.Fatalf("an ungranted client's factory_get = %q, want %q", code, factoryCodeNotFound)
	}
	ungranted := f.proposal("scope-ungranted")
	if code := factoryToolErrorCode(t, f.srv, token, factoryToolCreateDraft, ungranted); code != factoryCodeNotFound {
		t.Fatalf("an ungranted client's create = %q, want %q", code, factoryCodeNotFound)
	}
	_ = id
	if n := len(f.api.List()); n != 2 {
		t.Fatalf("denied calls wrote %d items, want none", n-2)
	}
}

func TestMCPFactoryReadCreateUpdate(t *testing.T) {
	f := newMCPFactoryToolFixture(t, fullGrant())

	empty := factoryToolOK(t, f.srv, f.token, factoryToolList, map[string]any{"projectId": f.projectID})
	if items, _ := empty["items"].([]any); len(items) != 0 {
		t.Fatalf("a fresh project listed %v", items)
	}

	created := factoryToolOK(t, f.srv, f.token, factoryToolCreateDraft, f.proposal("create-1"))
	itemID, _ := created["itemId"].(string)
	if itemID == "" {
		t.Fatalf("create returned no item id: %v", created)
	}
	if created["state"] != "draft" || created["version"].(float64) != 1 {
		t.Fatalf("created %v, want a draft at version 1", created)
	}
	if link, _ := created["link"].(string); !strings.Contains(link, "#factory/") {
		t.Fatalf("create returned link %q, want a Studio item link", created["link"])
	}
	draft, _ := created["draft"].(map[string]any)
	if draft["plan"] != "implement the thing" || draft["spec"] != "the specification" {
		t.Fatalf("create returned draft %v, want the stored plan and spec", draft)
	}
	config, _ := draft["config"].(map[string]any)
	// The project root is a host path: it is resolved server-side and never
	// echoed, not even as the empty string's path.
	if root, ok := config["projectRoot"]; !ok || root != "" {
		t.Fatalf("create echoed projectRoot %v", root)
	}
	if raw, _ := json.Marshal(created); strings.Contains(string(raw), f.root) {
		t.Fatalf("create echoed this host's project root: %s", raw)
	}

	// The read view carries the plan, the spec, the effective configuration
	// and the journal's attribution.
	got := factoryToolOK(t, f.srv, f.token, factoryToolGet, map[string]any{"projectId": f.projectID, "itemId": itemID})
	if got["version"].(float64) != 1 || got["state"] != "draft" {
		t.Fatalf("get returned %v", got)
	}
	if _, ok := got["projectRoot"]; ok {
		t.Fatal("get echoed a project root")
	}
	gotDraft, _ := got["draft"].(map[string]any)
	if gotDraft["plan"] != "implement the thing" {
		t.Fatalf("get returned draft %v", gotDraft)
	}
	history, _ := got["history"].([]any)
	if len(history) != 1 {
		t.Fatalf("get returned %d history entries, want the create's own", len(history))
	}
	event := history[0].(map[string]any)
	if event["kind"] != "create" {
		t.Fatalf("first history entry = %v", event)
	}
	provenance, ok := event["provenance"].(map[string]any)
	if !ok || provenance["clientId"] != f.clientID || provenance["requestId"] != "create-1" {
		t.Fatalf("create provenance = %v, want this client and its request id", event["provenance"])
	}

	list := factoryToolOK(t, f.srv, f.token, factoryToolList, map[string]any{"projectId": f.projectID})
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list returned %v", items)
	}
	summary := items[0].(map[string]any)
	if summary["itemId"] != itemID || summary["version"].(float64) != 1 {
		t.Fatalf("summary = %v", summary)
	}
	if _, ok := summary["draft"]; ok {
		t.Fatal("a list summary carries the whole draft")
	}

	// An update replaces the content of the draft and bumps the version.
	updated := factoryToolOK(t, f.srv, f.token, factoryToolUpdateDraft, f.update(itemID, "update-1", 1, "Revised title"))
	if updated["version"].(float64) != 2 || updated["title"] != "Revised title" {
		t.Fatalf("update returned %v", updated)
	}
	got = factoryToolOK(t, f.srv, f.token, factoryToolGet, map[string]any{"projectId": f.projectID, "itemId": itemID})
	if got["title"] != "Revised title" || got["version"].(float64) != 2 {
		t.Fatalf("get after update returned %v", got)
	}

	// A stale expected version conflicts rather than overwriting.
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolUpdateDraft, f.update(itemID, "update-stale", 1, "Stale")); code != factoryCodeConflict {
		t.Fatalf("stale update = %q, want %q", code, factoryCodeConflict)
	}

	// A queued item cannot be edited.
	approved := factoryToolOK(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-1", 2, factoryAuthorityUserDirected))
	if approved["state"] != "queued" {
		t.Fatalf("approve returned %v", approved)
	}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolUpdateDraft, f.update(itemID, "update-queued", uint64(approved["version"].(float64)), "After approval")); code != factoryCodeConflict {
		t.Fatalf("update of a queued item = %q, want %q", code, factoryCodeConflict)
	}
}

func TestMCPFactoryUserDirectedAndStandingApproval(t *testing.T) {
	for _, configured := range []string{factoryAuthorityUserDirected, factoryAuthorityStanding} {
		t.Run(configured, func(t *testing.T) {
			grant := fullGrant()
			grant.ApprovalAuthority = configured
			f := newMCPFactoryToolFixture(t, grant)

			created := factoryToolOK(t, f.srv, f.token, factoryToolCreateDraft, f.proposal("create-"+configured))
			itemID := created["itemId"].(string)

			// A claim that does not match the configuration is a denial, and
			// the denial does not report which authority is configured.
			mismatched := factoryAuthorityStanding
			if configured == factoryAuthorityStanding {
				mismatched = factoryAuthorityUserDirected
			}
			code := factoryToolErrorCode(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-mismatch", 1, mismatched))
			if code != factoryCodeDenied {
				t.Fatalf("mismatched authority = %q, want %q", code, factoryCodeDenied)
			}
			// A value this tool does not accept is a rejected request, not a
			// silent downgrade.
			if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-bogus", 1, "human")); code != factoryCodeInvalidInput {
				t.Fatalf("unknown authority = %q, want %q", code, factoryCodeInvalidInput)
			}

			// Matching the configuration queues the exact stored draft.
			approved := factoryToolOK(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-ok", 1, configured))
			if approved["state"] != "queued" || approved["version"].(float64) != 2 {
				t.Fatalf("approve returned %v", approved)
			}
			item, err := f.store.Get(itemID)
			if err != nil {
				t.Fatal(err)
			}
			if len(item.Approvals) != 1 {
				t.Fatalf("approve recorded %d approvals", len(item.Approvals))
			}
			if item.Approvals[0].Plan != "implement the thing" || item.Approvals[0].PlanHash != FactoryPlanHash("implement the thing", "the specification") {
				t.Fatalf("approve queued %+v, want the exact stored draft", item.Approvals[0])
			}
			last := item.History[len(item.History)-1]
			if last.Kind != "approve" || last.Provenance == nil {
				t.Fatalf("approval entry %+v", last)
			}
			want := FactoryProvenance{
				ClientID: f.clientID, ProjectID: f.projectID,
				Action: factoryCapabilityApproveAndQueue, RequestID: "approve-ok",
				Authority: configured,
			}
			if *last.Provenance != want {
				t.Fatalf("approval provenance = %+v, want %+v", *last.Provenance, want)
			}
		})
	}
}

func TestMCPFactoryDelegatedAnswerRequiresToggleAndIdentity(t *testing.T) {
	f := newMCPFactoryToolFixture(t, fullGrant())
	created := factoryToolOK(t, f.srv, f.token, factoryToolCreateDraft, f.proposal("create-delegated"))
	itemID := created["itemId"].(string)
	factoryToolOK(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-delegated", 1, factoryAuthorityUserDirected))
	version := f.seedQuestion(t, itemID)

	grant := fullGrant()
	grant.ProjectID = f.projectID
	_, delegateToken := testAddClient(t, f.srv, MCPClient{
		ID: "c2", Name: "delegate", FactoryGrants: []MCPFactoryProjectGrant{grant},
	})

	// The toggle is off by default: a delegated answer is refused for the
	// client that would otherwise be designated.
	f.setDelegation(t, false, "c2")
	if code := factoryToolErrorCode(t, f.srv, delegateToken, factoryToolAnswerQuestion,
		f.answer(itemID, "delegated-off", version, "decision-1", factoryAuthorityDelegated)); code != factoryCodeNotFound {
		t.Fatalf("delegated answer with the toggle off = %q, want %q", code, factoryCodeNotFound)
	}

	// The toggle alone is not enough: only the designated client answers.
	f.setDelegation(t, true, "c1")
	if code := factoryToolErrorCode(t, f.srv, delegateToken, factoryToolAnswerQuestion,
		f.answer(itemID, "delegated-wrong-client", version, "decision-1", factoryAuthorityDelegated)); code != factoryCodeNotFound {
		t.Fatalf("delegated answer from a client that is not designated = %q, want %q", code, factoryCodeNotFound)
	}

	// The designated client, with the toggle on, answers; the other holder of
	// the same capability is refused.
	f.setDelegation(t, true, "c2")
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolAnswerQuestion,
		f.answer(itemID, "delegated-designated-other", version, "decision-1", factoryAuthorityDelegated)); code != factoryCodeNotFound {
		t.Fatalf("delegated answer from the non-designated holder = %q, want %q", code, factoryCodeNotFound)
	}
	answered := factoryToolOK(t, f.srv, delegateToken, factoryToolAnswerQuestion,
		f.answer(itemID, "delegated-ok", version, "decision-1", factoryAuthorityDelegated))
	if answered["state"] != "repairing" {
		t.Fatalf("delegated answer returned %v", answered)
	}
	item, err := f.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Decision != nil {
		t.Fatal("the answered decision is still open")
	}
	last := item.History[len(item.History)-1]
	if last.Kind != "answer" || last.Provenance == nil {
		t.Fatalf("answer entry %+v", last)
	}
	if last.Provenance.Authority != factoryAuthorityDelegated ||
		last.Provenance.Action != factoryCapabilityDelegatedAnswer ||
		last.Provenance.DecisionID != "decision-1" ||
		last.Provenance.ClientID != "c2" {
		t.Fatalf("answer provenance = %+v", *last.Provenance)
	}
}

func TestMCPFactoryUserDirectedAnswer(t *testing.T) {
	f := newMCPFactoryToolFixture(t, fullGrant())
	created := factoryToolOK(t, f.srv, f.token, factoryToolCreateDraft, f.proposal("create-answer"))
	itemID := created["itemId"].(string)
	factoryToolOK(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-answer", 1, factoryAuthorityUserDirected))
	version := f.seedQuestion(t, itemID)

	// The exact decision and the version the caller read are both checked.
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolAnswerQuestion,
		f.answer(itemID, "answer-wrong-decision", version, "not-the-decision", factoryAuthorityUserDirected)); code != factoryCodeConflict {
		t.Fatalf("answer to an unknown decision = %q, want %q", code, factoryCodeConflict)
	}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolAnswerQuestion,
		f.answer(itemID, "answer-stale", version-1, "decision-1", factoryAuthorityUserDirected)); code != factoryCodeConflict {
		t.Fatalf("stale answer = %q, want %q", code, factoryCodeConflict)
	}
	// This tool accepts the two answer authorities and nothing else.
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolAnswerQuestion,
		f.answer(itemID, "answer-standing", version, "decision-1", factoryAuthorityStanding)); code != factoryCodeInvalidInput {
		t.Fatalf("answer with an approval authority = %q, want %q", code, factoryCodeInvalidInput)
	}

	answered := factoryToolOK(t, f.srv, f.token, factoryToolAnswerQuestion,
		f.answer(itemID, "answer-ok", version, "decision-1", factoryAuthorityUserDirected))
	if answered["state"] != "repairing" {
		t.Fatalf("answer returned %v", answered)
	}
	item, err := f.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	last := item.History[len(item.History)-1]
	if last.Kind != "answer" || last.Message != "use the release branch" {
		t.Fatalf("answer entry %+v", last)
	}
	if last.Provenance == nil || last.Provenance.Authority != factoryAuthorityUserDirected ||
		last.Provenance.Action != factoryCapabilityAnswerQuestion || last.Provenance.DecisionID != "decision-1" {
		t.Fatalf("answer provenance = %+v", last.Provenance)
	}
}

func TestMCPFactoryStaleAndDuplicateRequests(t *testing.T) {
	f := newMCPFactoryToolFixture(t, fullGrant())

	first := factoryToolOK(t, f.srv, f.token, factoryToolCreateDraft, f.proposal("create-1"))
	itemID := first["itemId"].(string)

	// An unchanged replay returns the original accepted result, and one
	// append is what it costs.
	replay := factoryToolOK(t, f.srv, f.token, factoryToolCreateDraft, f.proposal("create-1"))
	if replay["itemId"] != itemID || replay["version"] != first["version"] {
		t.Fatalf("replay = %v, want the original %v", replay, first)
	}
	if n := len(f.api.List()); n != 1 {
		t.Fatalf("the replay created %d items, want one", n)
	}
	// The same request id with different contents is a conflict.
	different := f.proposal("create-1")
	different["title"] = "A different draft"
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolCreateDraft, different); code != factoryCodeConflict {
		t.Fatalf("reused request id with new contents = %q, want %q", code, factoryCodeConflict)
	}

	// A duplicated update replays its original result; a stale one conflicts.
	updated := factoryToolOK(t, f.srv, f.token, factoryToolUpdateDraft, f.update(itemID, "update-1", 1, "Revised"))
	again := factoryToolOK(t, f.srv, f.token, factoryToolUpdateDraft, f.update(itemID, "update-1", 1, "Revised"))
	if again["version"] != updated["version"] || updated["version"].(float64) != 2 {
		t.Fatalf("update replay = %v, want the original %v", again, updated)
	}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolUpdateDraft, f.update(itemID, "update-stale", 1, "Stale")); code != factoryCodeConflict {
		t.Fatalf("stale update = %q, want %q", code, factoryCodeConflict)
	}

	// The same for an approval: the replay is the original result, and the
	// same request id at a version that has moved on is a conflict.
	approved := factoryToolOK(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-1", 2, factoryAuthorityUserDirected))
	approvedAgain := factoryToolOK(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-1", 2, factoryAuthorityUserDirected))
	if approvedAgain["version"] != approved["version"] || approved["state"] != "queued" {
		t.Fatalf("approval replay = %v, want the original %v", approvedAgain, approved)
	}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-2", 2, factoryAuthorityUserDirected)); code != factoryCodeConflict {
		t.Fatalf("second approval at a settled version = %q, want %q", code, factoryCodeConflict)
	}

	// One client's request id is not another's: the same id from a second
	// client is a second operation.
	grant := fullGrant()
	grant.ProjectID = f.projectID
	_, tokenB := testAddClient(t, f.srv, MCPClient{ID: "c3", Name: "other", FactoryGrants: []MCPFactoryProjectGrant{grant}})
	other := factoryToolOK(t, f.srv, tokenB, factoryToolCreateDraft, f.proposal("create-1"))
	if other["itemId"] == itemID {
		t.Fatal("a second client's identical request id replayed the first client's item")
	}

	// Authorisation is checked before the replay: a revoked grant denies the
	// retry instead of handing back what it originally produced.
	if err := f.revoke(); err != nil {
		t.Fatal(err)
	}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolCreateDraft, f.proposal("create-1")); code != factoryCodeNotFound {
		t.Fatalf("replay after revocation = %q, want %q", code, factoryCodeNotFound)
	}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolApproveAndQueue, f.approve(itemID, "approve-1", 2, factoryAuthorityUserDirected)); code != factoryCodeNotFound {
		t.Fatalf("approval replay after revocation = %q, want %q", code, factoryCodeNotFound)
	}
}

// TestMCPFactoryToolGrantRevokeRacesMutation races a grant revocation against
// a mutation through the MCP tools, so the ordering the policy lock guarantees
// is exercised on the path a client actually drives.
func TestMCPFactoryToolGrantRevokeRacesMutation(t *testing.T) {
	f := newMCPFactoryToolFixture(t, fullGrant())
	created := 0
	const rounds = 16
	for round := 0; round < rounds; round++ {
		if err := f.restoreGrant(fullGrant()); err != nil {
			t.Fatal(err)
		}
		requestID := fmt.Sprintf("race-%d", round)
		start := make(chan struct{})
		mutationDone := make(chan struct{})
		revokeDone := make(chan struct{})
		var reply factoryToolReply
		var value map[string]any
		var mutationErr error
		var revokeErr error
		go func() {
			defer close(mutationDone)
			<-start
			reply, value, mutationErr = callFactoryTool(f.srv, f.token, factoryToolCreateDraft, f.proposal(requestID))
		}()
		go func() {
			defer close(revokeDone)
			<-start
			revokeErr = f.revoke()
		}()
		close(start)
		<-mutationDone
		<-revokeDone
		if mutationErr != nil {
			t.Fatalf("round %d: %v", round, mutationErr)
		}
		if revokeErr != nil {
			t.Fatalf("round %d: revocation: %v", round, revokeErr)
		}
		// The two orderings are the only outcomes: either the revocation was
		// admitted first and the mutation is denied without a write, or the
		// mutation committed under the lock and the revocation landed after.
		if !reply.IsError {
			created++
			itemID, _ := value["itemId"].(string)
			if _, err := f.store.Get(itemID); err != nil {
				t.Fatalf("round %d: the returned item is not in the journal: %v", round, err)
			}
		} else if value["code"] != factoryCodeNotFound {
			t.Fatalf("round %d: denial code = %v, want %q", round, value["code"], factoryCodeNotFound)
		}
		// The revocation that returned is in force for the next operation.
		if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolCreateDraft, f.proposal(requestID+"-after")); code != factoryCodeNotFound {
			t.Fatalf("round %d: mutation after revocation = %q, want %q", round, code, factoryCodeNotFound)
		}
	}
	if n := len(f.api.List()); n != created {
		t.Fatalf("the journal holds %d items, want exactly the %d admitted mutations", n, created)
	}
}

func TestMCPFactoryToolResultsProtocolShape(t *testing.T) {
	f := newMCPFactoryToolFixture(t, fullGrant())

	// A success is one text block holding a JSON object, with isError false
	// and no structured duplicate of the text.
	reply, value := runFactoryTool(t, f.srv, f.token, factoryToolProjects, map[string]any{})
	if reply.IsError {
		t.Fatalf("factory_projects failed: %v", value)
	}
	raw := callMCPRaw(t, f.srv, f.token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"factory_projects","arguments":{}}}`)
	envelope, _ := json.Marshal(raw.Result)
	var shape map[string]any
	if err := json.Unmarshal(envelope, &shape); err != nil {
		t.Fatal(err)
	}
	if _, ok := shape["structuredContent"]; ok {
		t.Fatal("the tool result carries structuredContent beside the text")
	}
	if _, ok := shape["isError"]; !ok {
		t.Fatal("the tool result has no isError field")
	}

	// A domain failure is isError true with a code and a message, and nothing
	// else.
	code := factoryToolErrorCode(t, f.srv, f.token, factoryToolList, map[string]any{"projectId": "no-such-project"})
	if code != factoryCodeNotFound {
		t.Fatalf("unknown project = %q", code)
	}
	_, failure := runFactoryTool(t, f.srv, f.token, factoryToolList, map[string]any{"projectId": "no-such-project"})
	if len(failure) != 2 {
		t.Fatalf("domain failure = %v, want exactly code and message", failure)
	}
	if message, _ := failure["message"].(string); message == "" || strings.Contains(message, "no-such-project") {
		t.Fatalf("domain failure message = %q, want a fixed message that names neither id", message)
	}

	// Unknown fields, trailing data and a client-supplied project root are
	// rejected requests, distinguishable from protocol failures.
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolList, map[string]any{"projectId": f.projectID, "extra": true}); code != factoryCodeInvalidInput {
		t.Fatalf("unknown field = %q", code)
	}
	// Trailing data after the arguments object is refused by the tool's own
	// decoder. It cannot be sent inside a JSON-RPC envelope — that would be
	// malformed JSON — so it is asserted where it is enforced.
	var projectArgs factoryToolProjectArgs
	if err := decodeFactoryToolArgs(json.RawMessage(`{"projectId":"p"}{}`), &projectArgs); err != errFactoryToolInput {
		t.Fatalf("trailing argument data = %v, want the tool's input error", err)
	}
	if err := decodeFactoryToolArgs(json.RawMessage(`{"projectId":"p","extra":true}`), &projectArgs); err != errFactoryToolInput {
		t.Fatalf("an unknown argument field = %v, want the tool's input error", err)
	}
	rooted := f.proposal("root-supplied")
	rooted["config"] = map[string]any{"repoId": "repo-1", "targetBranch": "main", "native": true, "projectRoot": t.TempDir()}
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolCreateDraft, rooted); code != factoryCodeInvalidInput {
		t.Fatalf("client-supplied projectRoot = %q, want %q", code, factoryCodeInvalidInput)
	}
	noRequestID := f.proposal("")
	delete(noRequestID, "requestId")
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolCreateDraft, noRequestID); code != factoryCodeInvalidInput {
		t.Fatalf("missing requestId = %q, want %q", code, factoryCodeInvalidInput)
	}

	// The plan and spec limit is the existing one and is enforced inside the
	// envelope.
	oversized := f.proposal("too-long")
	oversized["plan"] = strings.Repeat("x", factoryImportLimit+1)
	if code := factoryToolErrorCode(t, f.srv, f.token, factoryToolCreateDraft, oversized); code != factoryCodeInvalidInput {
		t.Fatalf("plan over the import limit = %q, want %q", code, factoryCodeInvalidInput)
	}

	// An unknown tool is a JSON-RPC invalid-params error, not a domain
	// failure: the two are distinguishable.
	unknown := callMCPRaw(t, f.srv, f.token,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"factory_nope","arguments":{}}}`)
	if unknown.Error == nil || unknown.Error.Code != rpcInvalidParams {
		t.Fatalf("unknown tool = %+v, want a JSON-RPC invalid-params error", unknown.Error)
	}

	// The envelope limit is the factory request limit, and it is answered
	// before the method runs.
	huge := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"factory_projects","arguments":{"pad":"` +
		strings.Repeat("x", factoryRequestLimit) + `"}}}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(huge))
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("oversized envelope = %d, want 413", rec.Code)
	}

	// Every factory operation authenticates.
	anon := httptest.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"factory_projects","arguments":{}}}`))
	rec = httptest.NewRecorder()
	f.srv.ServeHTTP(rec, anon)
	if rec.Code != 401 {
		t.Fatalf("unauthenticated factory call = %d, want 401", rec.Code)
	}
}

func TestMCPFactoryLegacyFleetToolsUnchanged(t *testing.T) {
	s, token := testServerWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID, Autonomous: true})
	registerGitRepo(t, s.fleet, "r1")

	// The tools are still there, with the arguments they always required.
	out := callMCPRaw(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if out.Error != nil {
		t.Fatalf("tools/list: %s", out.Error.Message)
	}
	raw, _ := json.Marshal(out.Result)
	var listed struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"spawn": {"repoId", "title"}, "status": {"agentId"}, "result": {"agentId"},
		"send": {"agentId", "text"}, "cancel": {"agentId"}, "list": nil,
	}
	seen := map[string]bool{}
	for _, tool := range listed.Tools {
		required, ok := want[tool.Name]
		if !ok {
			continue
		}
		seen[tool.Name] = true
		var got []string
		if list, ok := tool.InputSchema["required"].([]any); ok {
			for _, r := range list {
				got = append(got, r.(string))
			}
		}
		if strings.Join(got, ",") != strings.Join(required, ",") {
			t.Fatalf("%s required = %v, want %v", tool.Name, got, required)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("the legacy tool %s is gone", name)
		}
	}

	// spawn still returns its bare result: no MCP content envelope, so a
	// legacy consumer keeps working byte for byte.
	spawn := callMCPRaw(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"spawn","arguments":{"repoId":"r1","title":"add a flag","prompt":"do it"}}}`)
	if spawn.Error != nil {
		t.Fatalf("spawn: %s", spawn.Error.Message)
	}
	spawnResult := decodeBareResult(t, spawn)
	if _, ok := spawnResult["content"]; ok {
		t.Fatal("spawn acquired the factory tools' content envelope")
	}
	if _, ok := spawnResult["isError"]; ok {
		t.Fatal("spawn acquired an isError field")
	}
	if spawnResult["url"] == "" {
		t.Fatalf("spawn result = %v, want the operator URL it always returned", spawnResult)
	}
	if status, _ := spawnResult["status"].(string); status != "pending" && status != "running" {
		t.Fatalf("spawn status = %v, want pending or running", spawnResult["status"])
	}

	agentID := mustSubmit(t, s.fleet, SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "legacy work", Prompt: "y",
	})

	// status and result still answer with their own flat objects.
	status := decodeBareResult(t, callMCPRaw(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"status","arguments":{"agentId":"`+agentID+`"}}}`))
	if _, ok := status["content"]; ok {
		t.Fatal("status acquired the factory tools' content envelope")
	}
	if status["agentId"] != agentID || status["status"] == nil {
		t.Fatalf("status result = %v", status)
	}
	result := decodeBareResult(t, callMCPRaw(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"result","arguments":{"agentId":"`+agentID+`"}}}`))
	if _, ok := result["content"]; ok {
		t.Fatal("result acquired the factory tools' content envelope")
	}
	if _, ok := result["changedFiles"]; !ok {
		t.Fatalf("result = %v, want the changed files field it always had", result)
	}

	// list is still {agents: [...]}.
	list := decodeBareResult(t, callMCPRaw(t, s, token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
		"name":"list","arguments":{}}}`))
	if _, ok := list["content"]; ok {
		t.Fatal("list acquired the factory tools' content envelope")
	}
	agents, _ := list["agents"].([]any)
	found := false
	for _, entry := range agents {
		agent, _ := entry.(map[string]any)
		if agent["agentId"] == agentID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list returned %v, want this client's agents including %s", list["agents"], agentID)
	}
}

// decodeBareResult decodes a legacy tool's bare JSON result.
func decodeBareResult(t *testing.T, out rpcResponse) map[string]any {
	t.Helper()
	if out.Error != nil {
		t.Fatalf("rpc error %d: %s", out.Error.Code, out.Error.Message)
	}
	raw, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode result: %v (%s)", err, raw)
	}
	return value
}
