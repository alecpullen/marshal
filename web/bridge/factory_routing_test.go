package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
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
