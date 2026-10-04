package bridge

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestModelsRoutesProxyToConfigMethods(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, rpc string
		body                    any
		section                 string
	}{
		{"providers", http.MethodPut, "/api/models/providers", "config/set_providers",
			map[string]any{"providers": map[string]any{"ollama": map[string]any{"base_url": "http://x"}}}, "providers"},
		{"presets", http.MethodPut, "/api/models/presets", "config/set_presets",
			map[string]any{"presets": map[string]any{"fast": map[string]any{"model": "m"}}}, "presets"},
		{"routing", http.MethodPut, "/api/models/routing", "config/set_routing",
			map[string]any{"profiles": map[string]any{}, "defaultProfile": "d", "activePreset": "fast"}, "routing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, agent := libraryServer(t)
			rec := doReq(t, s, tc.method, tc.path, tc.body, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			calls := agent.calls(tc.rpc)
			if len(calls) != 1 {
				t.Fatalf("%s sent %d times", tc.rpc, len(calls))
			}
			want, _ := json.Marshal(tc.body)
			var sent, expect map[string]json.RawMessage
			_ = json.Unmarshal([]byte(calls[0]), &sent)
			_ = json.Unmarshal(want, &expect)
			for k, v := range expect {
				if string(sent[k]) != string(v) {
					t.Errorf("param %s = %s, want %s", k, sent[k], v)
				}
			}
			if _, has := sent["sessionId"]; has {
				t.Error("config methods take no session")
			}
			if e := findEvent(auditTail(t, f), AuditModelsChanged); e == nil || e.Detail != tc.section {
				t.Errorf("audit = %+v, want section %q", e, tc.section)
			}
		})
	}
}

func TestModelsGetAndProbeProxy(t *testing.T) {
	s, _, agent := libraryServer(t)
	agent.results["config/get"] = map[string]any{"providers": map[string]any{}, "roles": []string{"implementer"}}
	agent.results["config/probe_provider"] = map[string]any{"models": []map[string]any{{"id": "m1"}}}

	rec := doReq(t, s, http.MethodGet, "/api/models", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "implementer") {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	rec = doReq(t, s, http.MethodPost, "/api/models/probe", map[string]any{"name": "ollama"}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"m1"`) {
		t.Fatalf("probe: %d %s", rec.Code, rec.Body.String())
	}
	if got := agent.calls("config/probe_provider"); len(got) != 1 || !strings.Contains(got[0], `"name":"ollama"`) {
		t.Fatalf("probe params = %v", got)
	}
}

func TestModelsKeyIsProxiedButNeverAudited(t *testing.T) {
	s, f, agent := libraryServer(t)
	const secret = "sk-very-secret-value"
	rec := doReq(t, s, http.MethodPut, "/api/models/providers/openai/key", map[string]any{"key": secret}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	got := agent.calls("config/set_provider_key")
	if len(got) != 1 || !strings.Contains(got[0], `"name":"openai"`) || !strings.Contains(got[0], secret) {
		t.Fatalf("key params = %v", got)
	}
	events := auditTail(t, f)
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("the key reached the audit log: %s", raw)
	}
	if e := findEvent(events, AuditModelsChanged); e == nil || e.Detail != "provider_key:openai" {
		t.Fatalf("audit = %+v", e)
	}
	if rec := doReq(t, s, http.MethodPut, "/api/models/providers/openai/key", map[string]any{"key": ""}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty key: %d", rec.Code)
	}
}

func TestModelsAgentRefusalIs400(t *testing.T) {
	s, f, agent := libraryServer(t)
	agent.handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
		if m == "config/set_presets" {
			return nil, &rpcError{Code: -32602, Message: "preset fast: unknown model"}, true
		}
		return nil, nil, false
	}
	rec := doReq(t, s, http.MethodPut, "/api/models/presets", map[string]any{"presets": map[string]any{}}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown model") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	if findEvent(auditTail(t, f), AuditModelsChanged) != nil {
		t.Fatal("a refused change was audited as applied")
	}
}

func TestModelsUnsupportedAgentIs501(t *testing.T) {
	s, _, agent := libraryServer(t)
	agent.handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
		if m == "config/get" {
			return nil, &rpcError{Code: -32601, Message: "method not found"}, true
		}
		return nil, nil, false
	}
	rec := doReq(t, s, http.MethodGet, "/api/models", nil, nil)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "get_unsupported") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSpawnForwardsRoutingToSessionNew(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	s := NewServer(f, "")
	rec := doReq(t, s, http.MethodPost, "/api/agents", map[string]any{
		"project": t.TempDir(), "name": "routed",
		"routing": map[string]any{"implementer": "fast"},
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		AgentID string `json:"agentId"`
	}
	decodeBody(t, rec, &out)
	calls := agentOf(out.AgentID).calls("session/new")
	if len(calls) != 1 || !strings.Contains(calls[0], `"routing":{"implementer":"fast"}`) {
		t.Fatalf("session/new = %v", calls)
	}
}

func TestSpawnWithoutRoutingSendsNone(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, err := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls := agentOf(id).calls("session/new"); len(calls) != 1 || strings.Contains(calls[0], "routing") {
		t.Fatalf("session/new = %v", calls)
	}
}
