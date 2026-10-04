package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func newConfigTestManager(t *testing.T) *ConfigManager {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home+"/xdg")
	return NewConfigManager(home, nil, nil)
}

func cfgCall(t *testing.T, fn func(context.Context, json.RawMessage) (any, error), params string) any {
	t.Helper()
	v, err := fn(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("call %s: %v", params, err)
	}
	return v
}

func cfgErr(t *testing.T, fn func(context.Context, json.RawMessage) (any, error), params string) error {
	t.Helper()
	_, err := fn(context.Background(), json.RawMessage(params))
	if err == nil {
		t.Fatalf("call %s: want error", params)
	}
	return err
}

func cfgGet(t *testing.T, m *ConfigManager) ConfigGetResult {
	t.Helper()
	return cfgCall(t, m.Get, `{}`).(ConfigGetResult)
}

func seedProviderAndPreset(t *testing.T, m *ConfigManager) {
	t.Helper()
	cfgCall(t, m.SetProviders, `{"providers":{"local":{"type":"ollama","baseUrl":"http://localhost:11434","toolCalling":true}}}`)
	cfgCall(t, m.SetPresets, `{"presets":{"local/qwen":{"provider":"local","model":"qwen","contextWindow":32000}}}`)
}

func TestConfigGetSetRoundTrip(t *testing.T) {
	m := newConfigTestManager(t)
	seedProviderAndPreset(t, m)
	res := cfgGet(t, m)
	pv, ok := res.Providers["local"]
	if !ok || pv.BaseURL != "http://localhost:11434" || !pv.ToolCalling || pv.KeySource != "none" || pv.HasKey {
		t.Fatalf("provider = %+v", pv)
	}
	if pr := res.Presets["local/qwen"]; pr.Model != "qwen" || pr.ContextWindow != 32000 {
		t.Fatalf("preset = %+v", pr)
	}
	if len(res.Roles) == 0 || res.Roles[0] != "router" {
		t.Fatalf("roles = %v", res.Roles)
	}

	// A partial entry keeps the fields it leaves out.
	cfgCall(t, m.SetProviders, `{"providers":{"local":{"keepAlive":"10m"}}}`)
	pv = cfgGet(t, m).Providers["local"]
	if pv.KeepAlive != "10m" || pv.BaseURL != "http://localhost:11434" || pv.Type != "ollama" {
		t.Fatalf("after partial set = %+v", pv)
	}
	// null deletes.
	cfgCall(t, m.SetProviders, `{"providers":{"local":null}}`)
	if _, ok := cfgGet(t, m).Providers["local"]; ok {
		t.Fatal("provider not deleted")
	}
}

func TestConfigAPIKeyNeverReturnedOrAccepted(t *testing.T) {
	m := newConfigTestManager(t)
	seedProviderAndPreset(t, m)
	err := cfgErr(t, m.SetProviders, `{"providers":{"local":{"apiKey":"sk-secret"}}}`)
	var rpc *jsonRPCError
	if !errors.As(err, &rpc) || rpc.Code != invalidParams {
		t.Fatalf("err = %v", err)
	}
	cfgErr(t, m.SetProviders, `{"providers":{"local":{"api_key":"sk-secret"}}}`)

	cfgCall(t, m.SetProviderKey, `{"name":"local","key":"sk-secret"}`)
	res := cfgGet(t, m)
	data, _ := json.Marshal(res)
	if strings.Contains(string(data), "sk-secret") || strings.Contains(strings.ToLower(string(data)), `"apikey"`) {
		t.Fatalf("key leaked: %s", data)
	}
	if pv := res.Providers["local"]; !pv.HasKey || pv.KeySource != "config" {
		t.Fatalf("provider = %+v", pv)
	}
	cfgErr(t, m.SetProviderKey, `{"name":"missing","key":"k"}`)
	// Saving other fields keeps the stored key.
	cfgCall(t, m.SetProviders, `{"providers":{"local":{"keepAlive":"5m"}}}`)
	if pv := cfgGet(t, m).Providers["local"]; !pv.HasKey || pv.KeySource != "config" {
		t.Fatalf("key lost: %+v", pv)
	}
}

func TestConfigKeySourceEnv(t *testing.T) {
	m := newConfigTestManager(t)
	t.Setenv("MARSHAL_TEST_KEY", "abc")
	cfgCall(t, m.SetProviders, `{"providers":{"remote":{"type":"openai_compatible","baseUrl":"https://example.com/v1","apiKeyEnv":"MARSHAL_TEST_KEY"}}}`)
	if pv := cfgGet(t, m).Providers["remote"]; !pv.HasKey || pv.KeySource != "env" {
		t.Fatalf("provider = %+v", pv)
	}
	t.Setenv("MARSHAL_TEST_KEY", "")
	if pv := cfgGet(t, m).Providers["remote"]; pv.HasKey || pv.KeySource != "none" {
		t.Fatalf("provider = %+v", pv)
	}
}

func TestConfigPresetNeedsProvider(t *testing.T) {
	m := newConfigTestManager(t)
	cfgErr(t, m.SetPresets, `{"presets":{"x/y":{"provider":"x","model":"y"}}}`)
}

func TestConfigRouting(t *testing.T) {
	m := newConfigTestManager(t)
	seedProviderAndPreset(t, m)
	cfgErr(t, m.SetRouting, `{"profiles":{"p":{"implementer":{"preset":"nope"}}}}`)
	cfgErr(t, m.SetRouting, `{"profiles":{"p":{"bogus":{"preset":"local/qwen"}}}}`)
	cfgErr(t, m.SetRouting, `{"profiles":{"p":{"implementer":{"preset":"local/qwen"}}},"defaultProfile":"other"}`)

	cfgCall(t, m.SetRouting, `{"profiles":{"p":{"implementer":{"preset":"local/qwen"},"reviewer":{"preset":"local/qwen"}}},"defaultProfile":"p","activePreset":"local/qwen"}`)
	res := cfgGet(t, m)
	if res.DefaultProfile != "p" || res.ActivePreset != "local/qwen" || res.Profiles["p"]["reviewer"].Preset != "local/qwen" {
		t.Fatalf("routing = %+v", res)
	}
	// Providers and presets survive a routing save.
	if _, ok := res.Providers["local"]; !ok || res.Presets["local/qwen"].Model != "qwen" {
		t.Fatalf("lost data: %+v", res)
	}
}

func TestConfigBudgets(t *testing.T) {
	m := newConfigTestManager(t)
	cfgErr(t, m.SetBudgets, `{"budgets":{"dailyUsd":1,"perAgentUsd":0,"onDailyCap":"pause","onAgentCap":"warn"}}`)
	cfgErr(t, m.SetBudgets, `{"budgets":{"dailyUsd":-1,"perAgentUsd":0,"onDailyCap":"warn","onAgentCap":"warn"}}`)
	cfgCall(t, m.SetBudgets, `{"budgets":{"dailyUsd":25,"perAgentUsd":5,"onDailyCap":"block","onAgentCap":"pause"}}`)
	b := cfgGet(t, m).Budgets
	if b.DailyUSD != 25 || b.PerAgentUSD != 5 || b.OnDailyCap != "block" || b.OnAgentCap != "pause" {
		t.Fatalf("budgets = %+v", b)
	}
}

func TestConfigProbeProvider(t *testing.T) {
	m := newConfigTestManager(t)
	seedProviderAndPreset(t, m)
	var gotName string
	m.probe = func(ctx context.Context, name string, pc config.ProviderConfig) ([]ModelEntry, error) {
		gotName = name
		if pc.BaseURL == "http://bad" {
			return nil, errors.New("connection refused")
		}
		return []ModelEntry{{ID: "qwen", ContextWindow: 32000}}, nil
	}
	res := cfgCall(t, m.ProbeProvider, `{"name":"local"}`).(ConfigProbeResult)
	if gotName != "local" || len(res.Models) != 1 || res.Models[0].ID != "qwen" || res.Error != "" {
		t.Fatalf("probe = %+v (%s)", res, gotName)
	}
	res = cfgCall(t, m.ProbeProvider, `{"config":{"type":"ollama","baseUrl":"http://bad"}}`).(ConfigProbeResult)
	if res.Error != "connection refused" || res.Models == nil || len(res.Models) != 0 {
		t.Fatalf("failing probe = %+v", res)
	}
	cfgErr(t, m.ProbeProvider, `{"name":"missing"}`)
	cfgErr(t, m.ProbeProvider, `{}`)
}
