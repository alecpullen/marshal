package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// factoryRoutingModelConfig is the canned config/get result the route-rule
// tests share. It carries a configured credential-bearing provider, an
// uncredentialed anthropic provider (the one type the credential rule does
// not exempt alongside openai_codex), a resolvable preset for each, and a
// profile for every way a route can fail: one that names a resolvable preset,
// one whose role preset does not resolve, one whose preset names no
// configured provider, and one whose provider has no credential.
func factoryRoutingModelConfig() map[string]any {
	return map[string]any{
		"providers": map[string]any{
			"test":  map[string]any{"type": "openai_compatible", "hasKey": true},
			"nokey": map[string]any{"type": "anthropic", "hasKey": false},
		},
		"presets": map[string]any{
			"test/model":  map[string]any{"provider": "test", "model": "model"},
			"nokey/model": map[string]any{"provider": "nokey", "model": "model"},
			"ghost/model": map[string]any{"provider": "ghost", "model": "model"},
		},
		"profiles": map[string]any{
			"chosen": map[string]any{
				"implementer": map[string]any{"preset": "test/model"},
				"reviewer":    map[string]any{"preset": "test/model"},
			},
			"alternate": map[string]any{
				"implementer": map[string]any{"preset": "test/model"},
				"reviewer":    map[string]any{"preset": "test/model"},
			},
			"nopreset": map[string]any{
				"implementer": map[string]any{"preset": "absent"},
				"reviewer":    map[string]any{"preset": "absent"},
			},
			"noprovider": map[string]any{
				"implementer": map[string]any{"preset": "ghost/model"},
				"reviewer":    map[string]any{"preset": "ghost/model"},
			},
			"nokey": map[string]any{
				"implementer": map[string]any{"preset": "nokey/model"},
				"reviewer":    map[string]any{"preset": "nokey/model"},
			},
		},
		"defaultProfile": "chosen",
	}
}

// factoryRoutingAPI builds the transport-free API over a control agent whose
// config/get serves cfg, so the route rules can be exercised without the rest
// of the preflight's repository, project and forge fixtures.
func factoryRoutingAPI(t *testing.T, cfg map[string]any) *factoryAPI {
	t.Helper()
	agent := newFakeAgent()
	agent.results["config/get"] = cfg
	return &factoryAPI{fleet: ctlFleet(t, agent)}
}

func TestFactoryValidateRoutesAcceptsAConfiguredProfile(t *testing.T) {
	api := factoryRoutingAPI(t, factoryRoutingModelConfig())
	route := json.RawMessage(`{"profile":"chosen"}`)
	if err := api.factoryValidateRoutes(context.Background(), FactoryConfig{ImplementerRouting: route, ReviewerRouting: route}); err != nil {
		t.Fatalf("configured routes refused: %v", err)
	}
}

func TestFactoryValidateRoutesNamesTheFailingRole(t *testing.T) {
	api := factoryRoutingAPI(t, factoryRoutingModelConfig())
	valid := json.RawMessage(`{"profile":"chosen"}`)
	for _, tc := range []struct {
		name  string
		route json.RawMessage
		want  string
	}{
		{"no route", nil, "explicit %s routing required"},
		{"not a routing object", json.RawMessage(`5`), "explicit %s routing required"},
		{"no profile", json.RawMessage(`{}`), "explicit %s routing profile required"},
		{"unknown profile", json.RawMessage(`{"profile":"gone"}`), "unknown %s routing profile"},
		{"unresolvable preset", json.RawMessage(`{"profile":"nopreset"}`), "%s model preset unavailable"},
		{"unconfigured provider", json.RawMessage(`{"profile":"noprovider"}`), "%s provider unavailable"},
		{"missing credential", json.RawMessage(`{"profile":"nokey"}`), "%s provider credential unavailable"},
	} {
		for _, role := range []string{"implementer", "reviewer"} {
			t.Run(role+" "+tc.name, func(t *testing.T) {
				conf := FactoryConfig{ImplementerRouting: valid, ReviewerRouting: valid}
				if role == "implementer" {
					conf.ImplementerRouting = tc.route
				} else {
					conf.ReviewerRouting = tc.route
				}
				err := api.factoryValidateRoutes(context.Background(), conf)
				want := errFactoryConfig.Error() + ": " + fmt.Sprintf(tc.want, role)
				if !errors.Is(err, errFactoryConfig) || err == nil || err.Error() != want {
					t.Fatalf("error = %v, want %q", err, want)
				}
			})
		}
	}
}

func TestFactoryRouteSelectionRejectsANullRoute(t *testing.T) {
	for _, field := range []string{"implementerRouting", "reviewerRouting"} {
		var cmd FactoryCommand
		if err := json.Unmarshal([]byte(`{"`+field+`":null}`), &cmd); err != nil {
			t.Fatal(err)
		}
		_, err := factoryRouteSelectionOf(cmd)
		want := errFactoryInput.Error() + ": " + field + " must be a routing object, not null"
		if !errors.Is(err, errFactoryInput) || err == nil || err.Error() != want {
			t.Fatalf("%s: error = %v, want %q", field, err, want)
		}
	}
}

func TestFactoryRouteSelectionReadsAbsentAndPresentRoutes(t *testing.T) {
	var absent FactoryCommand
	if err := json.Unmarshal([]byte(`{"kind":"retry"}`), &absent); err != nil {
		t.Fatal(err)
	}
	sel, err := factoryRouteSelectionOf(absent)
	if err != nil || sel.hasImplementer || sel.hasReviewer {
		t.Fatalf("absent routes: %+v, %v", sel, err)
	}
	var present FactoryCommand
	if err := json.Unmarshal([]byte(`{"reviewerRouting":{"profile":"cheap"}}`), &present); err != nil {
		t.Fatal(err)
	}
	sel, err = factoryRouteSelectionOf(present)
	if err != nil || sel.hasImplementer || !sel.hasReviewer {
		t.Fatalf("one route: %+v, %v", sel, err)
	}
	if string(sel.reviewer) != `{"profile":"cheap"}` {
		t.Fatalf("reviewer route = %s, want the bytes the command carried", sel.reviewer)
	}
	// The reported route is the exact bytes the command carried, whitespace
	// included: a selection that re-encoded the route would silently rewrite
	// the approval's configuration.
	exact := json.RawMessage("{ \"profile\" : \"frontier\" }")
	sel, err = factoryRouteSelectionOf(FactoryCommand{ImplementerRouting: exact})
	if err != nil || !sel.hasImplementer || !bytes.Equal(sel.implementer, exact) {
		t.Fatalf("exact bytes: %+v, %v", sel, err)
	}
}

func TestFactoryValidateRoutesRejectsANilFleet(t *testing.T) {
	api := &factoryAPI{}
	route := json.RawMessage(`{"profile":"chosen"}`)
	err := api.factoryValidateRoutes(context.Background(), FactoryConfig{ImplementerRouting: route, ReviewerRouting: route})
	if !errors.Is(err, ErrFactoryUnavailable) || err == nil || err.Error() != ErrFactoryUnavailable.Error()+": fleet unavailable" {
		t.Fatalf("error = %v, want the unavailable error", err)
	}
}

func TestFactoryPreflightUsesTheSharedRouteRules(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	api, ok := f.api.(*factoryAPI)
	if !ok {
		t.Fatalf("fixture API is %T, want *factoryAPI", f.api)
	}
	conf := FactoryConfig{
		RepoID:             "repo-1",
		ProjectRoot:        f.root,
		TargetBranch:       "main",
		Native:             true,
		ImplementerRouting: json.RawMessage(`{"profile":"gone"}`),
		ReviewerRouting:    factoryDefaultsRouting(),
	}
	err := api.preflight(context.Background(), conf, true)
	want := errFactoryConfig.Error() + ": unknown implementer routing profile"
	if !errors.Is(err, errFactoryConfig) || err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// setFactoryModelConfig replaces the canned config/get result. The fake agent
// reads its results under its own lock, so the write takes the same one.
func setFactoryModelConfig(f *factoryDefaultsFixture, cfg map[string]any) {
	f.agent.mu.Lock()
	f.agent.results["config/get"] = cfg
	f.agent.mu.Unlock()
}

// routingFixture is the offline HTTP fixture whose model configuration serves
// factoryRoutingModelConfig, so a retry can name profiles the rules accept and
// profiles they refuse.
func routingFixture(t *testing.T) *factoryDefaultsFixture {
	t.Helper()
	f := newFactoryDefaultsFixture(t)
	setFactoryModelConfig(f, factoryRoutingModelConfig())
	return f
}

// blockedRoutedItem creates and approves a work item through the real surface,
// then parks it on a decision — the position a retry is applied from. Its
// approval names the "chosen" profile for both roles.
func blockedRoutedItem(t *testing.T, f *factoryDefaultsFixture) FactoryItem {
	t.Helper()
	f.putSettings(t, f.root, ProjectSettings{FactoryDefaults: &FactoryDefaults{
		TargetBranch:       "main",
		ImplementerRouting: factoryDefaultsRouting(),
		ReviewerRouting:    factoryDefaultsRouting(),
	}})
	item := f.created(t, f.handoff("create-1", f.root))
	approved, err := f.api.Command(context.Background(), item.ID, FactoryCommand{RequestID: "approve-1", Kind: "approve", ExpectedVersion: item.Version})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := f.store.Update(approved.ID, "park", approved.Version, json.RawMessage(`{"decision":"conflict"}`), func(dst *FactoryItem) error {
		dst.State = "needs_input"
		dst.Decision = &FactoryDecision{ID: "block", Kind: "conflict", Reason: "the run stalled"}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return blocked
}

// retryCommand sends a retry for item through the HTTP surface the studio uses.
func retryCommand(t *testing.T, f *factoryDefaultsFixture, item FactoryItem, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, f.srv, http.MethodPost, "/api/factory/items/"+item.ID+"/commands", body, f.headers())
}

// commandError decodes a command refusal's status, code and message.
func commandError(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	decodeBody(t, rec, &body)
	return body.Code, body.Error
}

func TestFactoryRouteOnlyRetryAcceptsAValidRoute(t *testing.T) {
	f := routingFixture(t)
	item := blockedRoutedItem(t, f)
	route := json.RawMessage(`{"profile":"alternate"}`)
	rec := retryCommand(t, f, item, map[string]any{
		"requestId": "retry-1", "kind": "retry", "decisionId": "block", "reason": "the implementer model stalls",
		"expectedVersion": item.Version, "additionalRounds": 2, "implementerRouting": json.RawMessage(`{"profile":"alternate"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: status %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, rec, &body)
	got := body.Item
	if len(got.Approvals) != 2 {
		t.Fatalf("approvals = %d, want 2", len(got.Approvals))
	}
	previous := item.Approvals[len(item.Approvals)-1]
	a := got.Approvals[len(got.Approvals)-1]
	if a.Plan != previous.Plan || a.Spec != previous.Spec || a.PlanHash != previous.PlanHash {
		t.Fatalf("approval did not carry the plan and specification over: %+v", a)
	}
	if !bytes.Equal(a.Config.ImplementerRouting, route) {
		t.Fatalf("implementer routing = %s, want %s", a.Config.ImplementerRouting, route)
	}
	grant := got.Grants[len(got.Grants)-1]
	if grant.AdditionalRounds != 2 || grant.Reason != "the implementer model stalls" {
		t.Fatalf("grant = %+v", grant)
	}
}

func TestFactoryRouteOnlyRetryRefusesAnInvalidRoute(t *testing.T) {
	for _, tc := range []struct {
		name  string
		role  string
		route json.RawMessage
		why   string
	}{
		{"not a routing object", "implementer", json.RawMessage(`5`), "routing required"},
		{"no profile", "implementer", json.RawMessage(`{}`), "routing profile required"},
		{"unknown profile", "implementer", json.RawMessage(`{"profile":"gone"}`), "unknown implementer routing profile"},
		{"unresolvable preset", "implementer", json.RawMessage(`{"profile":"nopreset"}`), "implementer model preset unavailable"},
		{"unconfigured provider", "implementer", json.RawMessage(`{"profile":"noprovider"}`), "implementer provider unavailable"},
		{"missing credential", "implementer", json.RawMessage(`{"profile":"nokey"}`), "implementer provider credential unavailable"},
		{"unknown reviewer profile", "reviewer", json.RawMessage(`{"profile":"gone"}`), "unknown reviewer routing profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := routingFixture(t)
			item := blockedRoutedItem(t, f)
			cmd := map[string]any{
				"requestId": "retry-1", "kind": "retry", "decisionId": "block", "reason": "the model stalls",
				"expectedVersion": item.Version, "additionalRounds": 1,
			}
			if tc.role == "implementer" {
				cmd["implementerRouting"] = tc.route
			} else {
				cmd["reviewerRouting"] = tc.route
			}
			rec := retryCommand(t, f, item, cmd)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			code, message := commandError(t, rec)
			if code != "unsupported" || !strings.Contains(message, tc.why) || !strings.Contains(message, tc.role) {
				t.Fatalf("error code=%q message=%q, want unsupported naming the %s", code, message, tc.role)
			}
			saved, err := f.store.Get(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(item)
			after, _ := json.Marshal(saved)
			if !bytes.Equal(before, after) {
				t.Fatalf("refused retry changed the item: %s", after)
			}
		})
	}
}

func TestFactoryRouteOnlyRetryRefusesAnInvalidInheritedRoute(t *testing.T) {
	f := routingFixture(t)
	item := blockedRoutedItem(t, f)
	// The profile the approval names is withdrawn from the model
	// configuration after the approval was granted.
	withdrawn := factoryRoutingModelConfig()
	delete(withdrawn["profiles"].(map[string]any), "chosen")
	setFactoryModelConfig(f, withdrawn)
	rec := retryCommand(t, f, item, map[string]any{
		"requestId": "retry-1", "kind": "retry", "decisionId": "block", "reason": "the reviewer model stalls",
		"expectedVersion": item.Version, "additionalRounds": 1, "reviewerRouting": json.RawMessage(`{"profile":"alternate"}`),
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	code, message := commandError(t, rec)
	if code != "unsupported" || !strings.Contains(message, "unknown implementer routing profile") {
		t.Fatalf("error code=%q message=%q, want the untouched implementer route named", code, message)
	}
}

func TestFactoryRouteOnlyRetryRefusesANullRoute(t *testing.T) {
	f := routingFixture(t)
	item := blockedRoutedItem(t, f)
	rec := retryCommand(t, f, item, map[string]any{
		"requestId": "retry-1", "kind": "retry", "decisionId": "block", "reason": "the implementer model stalls",
		"expectedVersion": item.Version, "additionalRounds": 1, "implementerRouting": nil,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	code, message := commandError(t, rec)
	if code != "invalid_input" || !strings.Contains(message, "implementerRouting must be a routing object, not null") {
		t.Fatalf("error code=%q message=%q", code, message)
	}
	saved, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != item.Version || len(saved.Approvals) != 1 || len(saved.Grants) != 1 {
		t.Fatalf("refused retry changed the item: %+v", saved)
	}
}

func TestFactoryRouteOnlyRetryIsNotBlockedByAnUnrelatedPreflightCondition(t *testing.T) {
	f := routingFixture(t)
	// The approval names a workspace the fleet cannot resolve. An approval is
	// append-only, so the item is built directly rather than through the
	// surface, whose preflight would refuse that workspace at approval time.
	created, err := f.store.Create(f.handoff("create-1", f.root), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.store.Update(created.ID, "approve", created.Version, json.RawMessage(`{"approve":true}`), func(dst *FactoryItem) error {
		conf := dst.Draft.Config
		conf.Native = false
		conf.Workspace = "repo:missing-template"
		conf.TargetBranch = "main"
		conf.ImplementerRouting = factoryDefaultsRouting()
		conf.ReviewerRouting = factoryDefaultsRouting()
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: dst.Draft.Plan, Spec: dst.Draft.Spec, PlanHash: FactoryPlanHash(dst.Draft.Plan, dst.Draft.Spec), Config: conf, ApprovedAt: time.Now()}}
		dst.Grants = []FactoryGrant{{ID: "initial", Reason: "initial approval", AdditionalRounds: 5, At: time.Now()}}
		dst.State = "needs_input"
		dst.Decision = &FactoryDecision{ID: "block", Kind: "conflict", Reason: "the run stalled"}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	replacement := FactoryCreate{
		RequestID: "replacement",
		Title:     "Add the thing",
		Plan:      "implement the thing a different way",
		Config: FactoryConfig{
			RepoID:                   "repo-1",
			ProjectRoot:              f.root,
			TargetBranch:             "main",
			Workspace:                "repo:missing-template",
			ImplementerRouting:       factoryDefaultsRouting(),
			ReviewerRouting:          factoryDefaultsRouting(),
			NoRequiredCIAcknowledged: true,
		},
	}
	refused := retryCommand(t, f, item, map[string]any{
		"requestId": "retry-replacement", "kind": "retry", "decisionId": "block", "reason": "replace the plan",
		"expectedVersion": item.Version, "additionalRounds": 1, "replacement": replacement,
	})
	if refused.Code != http.StatusUnprocessableEntity || !strings.Contains(refused.Body.String(), "workspace unavailable") {
		t.Fatalf("plan replacement: status %d, body %s", refused.Code, refused.Body.String())
	}
	accepted := retryCommand(t, f, item, map[string]any{
		"requestId": "retry-route", "kind": "retry", "decisionId": "block", "reason": "the implementer model stalls",
		"expectedVersion": item.Version, "additionalRounds": 1, "implementerRouting": json.RawMessage(`{"profile":"alternate"}`),
	})
	if accepted.Code != http.StatusOK {
		t.Fatalf("route-only retry: status %d, body %s", accepted.Code, accepted.Body.String())
	}
}

func TestFactoryRouteOnlyRetryWithAReplacementValidatesTheCombinedConfig(t *testing.T) {
	f := routingFixture(t)
	item := blockedRoutedItem(t, f)
	replacement := FactoryCreate{
		RequestID: "replacement",
		Title:     "Add the thing",
		Plan:      "implement the thing a different way",
		Spec:      "replace the specification too",
		Config: FactoryConfig{
			RepoID:                   "repo-1",
			ProjectRoot:              f.root,
			TargetBranch:             "main",
			Native:                   true,
			ImplementerRouting:       json.RawMessage(`{"profile":"alternate"}`),
			ReviewerRouting:          json.RawMessage(`{"profile":"alternate"}`),
			NoRequiredCIAcknowledged: true,
		},
	}
	rec := retryCommand(t, f, item, map[string]any{
		"requestId": "retry-1", "kind": "retry", "decisionId": "block", "reason": "new plan and new implementer",
		"expectedVersion": item.Version, "additionalRounds": 1,
		"implementerRouting": json.RawMessage(`{"profile":"chosen"}`), "replacement": replacement,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, rec, &body)
	a := body.Item.Approvals[len(body.Item.Approvals)-1]
	if a.Plan != replacement.Plan || a.Spec != replacement.Spec {
		t.Fatalf("approval plan/spec = %q/%q, want the replacement's", a.Plan, a.Spec)
	}
	if !bytes.Equal(a.Config.ImplementerRouting, json.RawMessage(`{"profile":"chosen"}`)) {
		t.Fatalf("implementer routing = %s, want the route the command supplied", a.Config.ImplementerRouting)
	}
	if !bytes.Equal(a.Config.ReviewerRouting, json.RawMessage(`{"profile":"alternate"}`)) {
		t.Fatalf("reviewer routing = %s, want the replacement's, not the previous approval's", a.Config.ReviewerRouting)
	}
}

func TestFactoryPlanReplacementRetryWithoutRoutesIsUnchanged(t *testing.T) {
	f := routingFixture(t)
	item := blockedRoutedItem(t, f)
	replacement := FactoryCreate{
		RequestID: "replacement",
		Title:     "Add the thing",
		Plan:      "implement the thing a different way",
		Config: FactoryConfig{
			RepoID:                   "repo-1",
			ProjectRoot:              f.root,
			TargetBranch:             "release",
			Native:                   true,
			ImplementerRouting:       json.RawMessage(`{"profile":"alternate"}`),
			ReviewerRouting:          factoryDefaultsRouting(),
			NoRequiredCIAcknowledged: true,
		},
	}
	rec := retryCommand(t, f, item, map[string]any{
		"requestId": "retry-1", "kind": "retry", "decisionId": "block", "reason": "replace the plan",
		"expectedVersion": item.Version, "additionalRounds": 1, "replacement": replacement,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, rec, &body)
	if len(body.Item.Approvals) != 2 {
		t.Fatalf("approvals = %d, want 2", len(body.Item.Approvals))
	}
	a := body.Item.Approvals[len(body.Item.Approvals)-1]
	want := replacement
	if err := factoryNormalize(&want); err != nil {
		t.Fatal(err)
	}
	if a.Plan != replacement.Plan || a.Spec != replacement.Spec || a.PlanHash != FactoryPlanHash(replacement.Plan, replacement.Spec) {
		t.Fatalf("approval did not take the replacement's plan: %+v", a)
	}
	sameFactoryConfig(t, a.Config, want.Config)
}

func TestFactoryRouteOnlyRetryReplaysToTheOriginalItem(t *testing.T) {
	f := routingFixture(t)
	item := blockedRoutedItem(t, f)
	cmd := map[string]any{
		"requestId": "retry-1", "kind": "retry", "decisionId": "block", "reason": "the implementer model stalls",
		"expectedVersion": item.Version, "additionalRounds": 1, "implementerRouting": json.RawMessage(`{"profile":"alternate"}`),
	}
	first := retryCommand(t, f, item, cmd)
	if first.Code != http.StatusOK {
		t.Fatalf("retry: status %d, body %s", first.Code, first.Body.String())
	}
	var once struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, first, &once)
	replay := retryCommand(t, f, item, cmd)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: status %d, body %s", replay.Code, replay.Body.String())
	}
	var twice struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, replay, &twice)
	if twice.Item.Version != once.Item.Version || len(twice.Item.Approvals) != 2 {
		t.Fatalf("replay = %s@%d with %d approvals, want the original %s@%d", twice.Item.ID, twice.Item.Version, len(twice.Item.Approvals), once.Item.ID, once.Item.Version)
	}
	changed := map[string]any{
		"requestId": "retry-1", "kind": "retry", "decisionId": "block", "reason": "a different reason for the same request",
		"expectedVersion": item.Version, "additionalRounds": 1, "implementerRouting": json.RawMessage(`{"profile":"alternate"}`),
	}
	if rec := retryCommand(t, f, item, changed); rec.Code != http.StatusConflict {
		t.Fatalf("changed content: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestFactoryRouteOnlyRetryReplaysAfterTheProfileIsWithdrawn pins the replay
// promise: a replayed route-only retry returns the original item even when
// the route it named has since been withdrawn from the model configuration.
// The replay is answered from the recorded request, not re-validated.
func TestFactoryRouteOnlyRetryReplaysAfterTheProfileIsWithdrawn(t *testing.T) {
	f := routingFixture(t)
	item := blockedRoutedItem(t, f)
	cmd := map[string]any{
		"requestId": "retry-1", "kind": "retry", "decisionId": "block", "reason": "the implementer model stalls",
		"expectedVersion": item.Version, "additionalRounds": 1, "implementerRouting": json.RawMessage(`{"profile":"alternate"}`),
	}
	first := retryCommand(t, f, item, cmd)
	if first.Code != http.StatusOK {
		t.Fatalf("retry: status %d, body %s", first.Code, first.Body.String())
	}
	var once struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, first, &once)
	// The profile the retry named is withdrawn from the model configuration.
	withdrawn := factoryRoutingModelConfig()
	delete(withdrawn["profiles"].(map[string]any), "alternate")
	setFactoryModelConfig(f, withdrawn)
	replay := retryCommand(t, f, item, cmd)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay after the profile was withdrawn: status %d, body %s", replay.Code, replay.Body.String())
	}
	var twice struct {
		Item FactoryItem `json:"item"`
	}
	decodeBody(t, replay, &twice)
	if twice.Item.Version != once.Item.Version || len(twice.Item.Approvals) != 2 {
		t.Fatalf("replay = %s@%d with %d approvals, want the original %s@%d", twice.Item.ID, twice.Item.Version, len(twice.Item.Approvals), once.Item.ID, once.Item.Version)
	}
}
