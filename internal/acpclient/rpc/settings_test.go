package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// callHost runs fn on its own goroutine, checks that the frame it sends is
// method's and names sessionID, answers it with result, and returns the params
// the frame carried with fn's error. A read that sends nothing must not use it.
func callHost(t *testing.T, a *scriptedAgent, method, sessionID string, result any, fn func() error) (json.RawMessage, error) {
	t.Helper()
	out := make(chan error, 1)
	go func() { out <- fn() }()

	req := a.next(t)
	if got := string(req.Method); got != method {
		t.Fatalf("the client sent %q, want %q", got, method)
	}
	var named struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(req.Params, &named); err != nil {
		t.Fatalf("decode %s params: %v", method, err)
	}
	if named.SessionID != sessionID {
		t.Fatalf("%s params name session %q, want %q", method, named.SessionID, sessionID)
	}
	a.answer(t, req, result)
	return req.Params, awaitCall(t, out)
}

// progressFrame builds the $/progress notification carrying one value for the
// request with the given raw ID.
func progressFrame(t *testing.T, id string, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal progress value: %v", err)
	}
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  acpwire.MethodProgress,
		"params":  acpwire.ProgressParams{ID: json.RawMessage(id), Value: raw},
	})
	if err != nil {
		t.Fatalf("marshal progress notification: %v", err)
	}
	return string(frame)
}

// TestAHostCallBeforeAnAttachFails pins decision 1: every host call names the
// client's current session, so before the first Attach each fails without
// putting a frame on the wire, and the two reads answer nothing.
func TestAHostCallBeforeAnAttachFails(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	ctx := context.Background()
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1"}

	if got := host.ProviderTemplates(); got != nil {
		t.Fatalf("ProviderTemplates before an attach = %+v, want nil", got)
	}
	if got := host.TrustPrompt(); got != (acpwire.TrustPromptResult{}) {
		t.Fatalf("TrustPrompt before an attach = %+v, want empty", got)
	}

	wantNoSession := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, errNoSession) {
			t.Fatalf("%s before an attach = %v, want %v", name, err, errNoSession)
		}
		if got := err.Error(); got != "rpc: no session attached" {
			t.Fatalf("%s before an attach = %q, want %q", name, got, "rpc: no session attached")
		}
	}

	wantNoSession("DecideTrust", host.DecideTrust(ctx, "/w", acpwire.TrustDecline))

	models, err := host.ProbeProvider(ctx, "probe", pc)
	if models != nil {
		t.Fatalf("ProbeProvider before an attach returned %+v", models)
	}
	wantNoSession("ProbeProvider", err)

	if host.CanProbeModel(ctx, "probe", pc) {
		t.Fatal("CanProbeModel before an attach = true, want false")
	}

	caps, err := host.ProbeModel(ctx, "probe", pc, "m1")
	if caps != (acpwire.ModelCapabilities{}) {
		t.Fatalf("ProbeModel before an attach returned %+v", caps)
	}
	wantNoSession("ProbeModel", err)

	list, found := host.ListModels(ctx, "probe")
	if list != nil || found {
		t.Fatalf("ListModels before an attach = %+v, %v, want nil, false", list, found)
	}

	wantNoSession("SaveModels", host.SaveModels(ctx, "probe", []schema.ModelInfo{{ID: "m1"}}))
	wantNoSession("ForgetModels", host.ForgetModels(ctx, "probe"))
	wantNoSession("RenameModels", host.RenameModels(ctx, "a", "b"))
	wantNoSession("PrepareProviderLogin", host.PrepareProviderLogin(ctx, "probe"))
	wantNoSession("LoginProvider", host.LoginProvider(ctx, "probe", func(string) {}))
	wantNoSession("SaveUserValue", host.SaveUserValue(ctx, "web.search_key", "k"))
	wantNoSession("SaveUserProviders", host.SaveUserProviders(ctx, nil, nil, nil))
	wantNoSession("SaveUserPresets", host.SaveUserPresets(ctx, nil, nil))
	wantNoSession("SaveProviderKey", host.SaveProviderKey(ctx, "probe", "sk-1"))
	presets, setup, err := host.PreparePresets(ctx, config.Config{}, "probe", "", nil)
	if presets != nil || !isZeroSetup(setup) {
		t.Fatalf("PreparePresets before an attach returned %+v, %+v", presets, setup)
	}
	wantNoSession("PreparePresets", err)
	wantNoSession("SaveProvenance", host.SaveProvenance(ctx, json.RawMessage(`{}`)))

	// Nothing went out: every call above refused before it sent a frame.
	select {
	case req := <-a.reqs:
		t.Fatalf("a host call before the attach sent %q", req.Method)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestHostCallsNameTheCurrentSession pins that each of the eighteen host calls
// sends its own method with the attached session's ID, carries its params, and
// decodes its result.
func TestHostCallsNameTheCurrentSession(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	ctx := context.Background()
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1"}

	// The two reads, each of which sends its method on this first read.
	var templates []acpwire.ProviderTemplate
	if _, err := callHost(t, a, acpwire.MethodProviderTemplates, "s1",
		[]acpwire.ProviderTemplate{{ID: "probe", Label: "Probe"}}, func() error {
			templates = host.ProviderTemplates()
			return nil
		}); err != nil {
		t.Fatalf("ProviderTemplates: %v", err)
	}
	if len(templates) != 1 || templates[0].ID != "probe" {
		t.Fatalf("ProviderTemplates = %+v, want the probe template", templates)
	}

	var prompt acpwire.TrustPromptResult
	if _, err := callHost(t, a, acpwire.MethodTrustPrompt, "s1",
		acpwire.TrustPromptResult{Dir: "/w"}, func() error {
			prompt = host.TrustPrompt()
			return nil
		}); err != nil {
		t.Fatalf("TrustPrompt: %v", err)
	}
	if prompt.Dir != "/w" {
		t.Fatalf("TrustPrompt = %+v, want /w", prompt)
	}

	// trust/decide.
	params, err := callHost(t, a, acpwire.MethodTrustDecide, "s1", struct{}{}, func() error {
		return host.DecideTrust(ctx, "/w", acpwire.TrustDecline)
	})
	if err != nil {
		t.Fatalf("DecideTrust: %v", err)
	}
	var decide acpwire.TrustDecideParams
	if err := json.Unmarshal(params, &decide); err != nil {
		t.Fatalf("decode trust/decide params: %v", err)
	}
	if decide.Dir != "/w" || decide.Decision != acpwire.TrustDecline {
		t.Fatalf("trust/decide params = %+v, want dir /w and a decline", decide)
	}

	// config/probe_provider.
	var models []schema.ModelInfo
	params, err = callHost(t, a, acpwire.MethodProbeProvider, "s1",
		acpwire.ModelsResult{Models: json.RawMessage(`[{"ID":"m1","ContextWindow":8192}]`)},
		func() error {
			var err error
			models, err = host.ProbeProvider(ctx, "probe", pc)
			return err
		})
	if err != nil {
		t.Fatalf("ProbeProvider: %v", err)
	}
	var probe acpwire.ProviderParams
	if err := json.Unmarshal(params, &probe); err != nil {
		t.Fatalf("decode config/probe_provider params: %v", err)
	}
	if probe.Name != "probe" || len(probe.Provider) == 0 {
		t.Fatalf("config/probe_provider params = %+v, want the probe provider", probe)
	}
	if len(models) != 1 || models[0].ID != "m1" || models[0].ContextWindow != 8192 {
		t.Fatalf("ProbeProvider = %+v, want the reported model", models)
	}

	// config/probe_model for a check.
	var canProbe bool
	params, err = callHost(t, a, acpwire.MethodProbeModel, "s1",
		acpwire.CanProbeResult{CanProbe: true}, func() error {
			canProbe = host.CanProbeModel(ctx, "probe", pc)
			return nil
		})
	if err != nil {
		t.Fatalf("CanProbeModel: %v", err)
	}
	var check acpwire.ProviderParams
	if err := json.Unmarshal(params, &check); err != nil {
		t.Fatalf("decode config/probe_model params: %v", err)
	}
	if !check.Check || check.Name != "probe" {
		t.Fatalf("config/probe_model params = %+v, want a check for probe", check)
	}
	if !canProbe {
		t.Fatal("CanProbeModel = false, want true")
	}

	// config/probe_model without a check.
	var caps acpwire.ModelCapabilities
	params, err = callHost(t, a, acpwire.MethodProbeModel, "s1",
		acpwire.ModelCapabilities{ContextWindow: 4096, Reasoning: boolPtr(true)}, func() error {
			var err error
			caps, err = host.ProbeModel(ctx, "probe", pc, "m1")
			return err
		})
	if err != nil {
		t.Fatalf("ProbeModel: %v", err)
	}
	var probeModel acpwire.ProviderParams
	if err := json.Unmarshal(params, &probeModel); err != nil {
		t.Fatalf("decode config/probe_model params: %v", err)
	}
	if probeModel.Check || probeModel.Model != "m1" {
		t.Fatalf("config/probe_model params = %+v, want the model m1 and no check", probeModel)
	}
	if caps.ContextWindow != 4096 || caps.Reasoning == nil || !*caps.Reasoning {
		t.Fatalf("ProbeModel = %+v, want the reported capabilities", caps)
	}

	// config/models_list.
	var listed []schema.ModelInfo
	var found bool
	params, err = callHost(t, a, acpwire.MethodModelsList, "s1",
		acpwire.ModelsResult{Models: json.RawMessage(`[{"ID":"m1"}]`), Found: true}, func() error {
			listed, found = host.ListModels(ctx, "probe")
			return nil
		})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	var listParams acpwire.ModelsParams
	if err := json.Unmarshal(params, &listParams); err != nil {
		t.Fatalf("decode config/models_list params: %v", err)
	}
	if listParams.Name != "probe" {
		t.Fatalf("config/models_list params = %+v, want probe", listParams)
	}
	if !found || len(listed) != 1 || listed[0].ID != "m1" {
		t.Fatalf("ListModels = %+v, %v, want the cached model and true", listed, found)
	}

	// config/models_save.
	params, err = callHost(t, a, acpwire.MethodModelsSave, "s1", struct{}{}, func() error {
		return host.SaveModels(ctx, "probe", []schema.ModelInfo{{ID: "m1"}})
	})
	if err != nil {
		t.Fatalf("SaveModels: %v", err)
	}
	var saveParams acpwire.ModelsParams
	if err := json.Unmarshal(params, &saveParams); err != nil {
		t.Fatalf("decode config/models_save params: %v", err)
	}
	var savedModels []schema.ModelInfo
	if err := json.Unmarshal(saveParams.Models, &savedModels); err != nil {
		t.Fatalf("decode config/models_save's models: %v", err)
	}
	if saveParams.Name != "probe" || len(savedModels) != 1 || savedModels[0].ID != "m1" {
		t.Fatalf("config/models_save params = %+v, want the model m1 for probe", saveParams)
	}

	// config/models_forget.
	params, err = callHost(t, a, acpwire.MethodModelsForget, "s1", struct{}{}, func() error {
		return host.ForgetModels(ctx, "probe")
	})
	if err != nil {
		t.Fatalf("ForgetModels: %v", err)
	}
	var forgetParams acpwire.ModelsParams
	if err := json.Unmarshal(params, &forgetParams); err != nil {
		t.Fatalf("decode config/models_forget params: %v", err)
	}
	if forgetParams.Name != "probe" {
		t.Fatalf("config/models_forget params = %+v, want probe", forgetParams)
	}

	// config/models_rename.
	params, err = callHost(t, a, acpwire.MethodModelsRename, "s1", struct{}{}, func() error {
		return host.RenameModels(ctx, "a", "b")
	})
	if err != nil {
		t.Fatalf("RenameModels: %v", err)
	}
	var rename acpwire.RenameModelsParams
	if err := json.Unmarshal(params, &rename); err != nil {
		t.Fatalf("decode config/models_rename params: %v", err)
	}
	if rename.From != "a" || rename.To != "b" {
		t.Fatalf("config/models_rename params = %+v, want a to b", rename)
	}

	// config/login_provider's prepare.
	params, err = callHost(t, a, acpwire.MethodLoginProvider, "s1", struct{}{}, func() error {
		return host.PrepareProviderLogin(ctx, "probe")
	})
	if err != nil {
		t.Fatalf("PrepareProviderLogin: %v", err)
	}
	var prepare acpwire.LoginParams
	if err := json.Unmarshal(params, &prepare); err != nil {
		t.Fatalf("decode config/login_provider params: %v", err)
	}
	if prepare.Name != "probe" || !prepare.Prepare {
		t.Fatalf("config/login_provider params = %+v, want a prepare for probe", prepare)
	}

	// config/login_provider's sign-in, whose URL is checked separately.
	params, err = callHost(t, a, acpwire.MethodLoginProvider, "s1", struct{}{}, func() error {
		return host.LoginProvider(ctx, "probe", func(url string) {
			t.Errorf("show was called with %q for a sign-in that reported no URL", url)
		})
	})
	if err != nil {
		t.Fatalf("LoginProvider: %v", err)
	}
	var login acpwire.LoginParams
	if err := json.Unmarshal(params, &login); err != nil {
		t.Fatalf("decode config/login_provider params: %v", err)
	}
	if login.Name != "probe" || login.Prepare {
		t.Fatalf("config/login_provider params = %+v, want a sign-in for probe", login)
	}

	// config/set_value.
	params, err = callHost(t, a, acpwire.MethodSetValue, "s1", struct{}{}, func() error {
		return host.SaveUserValue(ctx, "tools.shell.allow_network", true)
	})
	if err != nil {
		t.Fatalf("SaveUserValue: %v", err)
	}
	var set acpwire.SetValueParams
	if err := json.Unmarshal(params, &set); err != nil {
		t.Fatalf("decode config/set_value params: %v", err)
	}
	if set.Path != "tools.shell.allow_network" || string(set.Value) != "true" {
		t.Fatalf("config/set_value params = %+v, want the bool at its path", set)
	}

	// config/save_providers.
	providers := map[string]config.ProviderConfig{"probe": pc}
	baseline := map[string]config.ProviderConfig{"probe": {Type: "ollama"}}
	renamed := map[string]string{"probe": "old"}
	params, err = callHost(t, a, acpwire.MethodSaveProviders, "s1", struct{}{}, func() error {
		return host.SaveUserProviders(ctx, providers, baseline, renamed)
	})
	if err != nil {
		t.Fatalf("SaveUserProviders: %v", err)
	}
	var saveProviders acpwire.SaveProvidersParams
	if err := json.Unmarshal(params, &saveProviders); err != nil {
		t.Fatalf("decode config/save_providers params: %v", err)
	}
	var sentProviders map[string]config.ProviderConfig
	if err := json.Unmarshal(saveProviders.Providers, &sentProviders); err != nil {
		t.Fatalf("decode config/save_providers' providers: %v", err)
	}
	var sentBaseline map[string]config.ProviderConfig
	if err := json.Unmarshal(saveProviders.Baseline, &sentBaseline); err != nil {
		t.Fatalf("decode config/save_providers' baseline: %v", err)
	}
	if sentProviders["probe"].BaseURL != pc.BaseURL {
		t.Fatalf("config/save_providers' providers = %+v, want the probe entry", sentProviders)
	}
	if sentBaseline["probe"].Type != "ollama" {
		t.Fatalf("config/save_providers' baseline = %+v, want the ollama entry", sentBaseline)
	}
	if saveProviders.Renamed["probe"] != "old" {
		t.Fatalf("config/save_providers' renamed = %+v, want probe to old", saveProviders.Renamed)
	}

	// config/save_presets.
	presets := map[string]routing.ModelPreset{"fast": {Provider: "probe", Model: "m1"}}
	presetBaseline := map[string]routing.ModelPreset{"fast": {Provider: "probe", Model: "m0"}}
	params, err = callHost(t, a, acpwire.MethodSavePresets, "s1", struct{}{}, func() error {
		return host.SaveUserPresets(ctx, presets, presetBaseline)
	})
	if err != nil {
		t.Fatalf("SaveUserPresets: %v", err)
	}
	var savePresets acpwire.SavePresetsParams
	if err := json.Unmarshal(params, &savePresets); err != nil {
		t.Fatalf("decode config/save_presets params: %v", err)
	}
	var sentPresets, sentPresetBaseline map[string]routing.ModelPreset
	if err := config.UnmarshalPresetsJSON(savePresets.Presets, &sentPresets); err != nil {
		t.Fatalf("decode config/save_presets' presets: %v", err)
	}
	if err := config.UnmarshalPresetsJSON(savePresets.Baseline, &sentPresetBaseline); err != nil {
		t.Fatalf("decode config/save_presets' baseline: %v", err)
	}
	if sentPresets["fast"].Model != "m1" || sentPresetBaseline["fast"].Model != "m0" {
		t.Fatalf("config/save_presets = %+v against %+v, want m1 against m0", sentPresets, sentPresetBaseline)
	}

	// config/set_provider_key.
	params, err = callHost(t, a, acpwire.MethodSetProviderKey, "s1", struct{}{}, func() error {
		return host.SaveProviderKey(ctx, "probe", "sk-typed")
	})
	if err != nil {
		t.Fatalf("SaveProviderKey: %v", err)
	}
	var key acpwire.ProviderKeyParams
	if err := json.Unmarshal(params, &key); err != nil {
		t.Fatalf("decode config/set_provider_key params: %v", err)
	}
	if key.Name != "probe" || key.Key != "sk-typed" {
		t.Fatalf("config/set_provider_key params = %+v, want the typed key for probe", key)
	}

	// config/prepare_presets.
	var prepared map[string]routing.ModelPreset
	var setup acpwire.PresetSetup
	params, err = callHost(t, a, acpwire.MethodPreparePresets, "s1",
		acpwire.PreparedPresets{
			Presets: json.RawMessage(`{"fast":{"provider":"probe","model":"m1"}}`),
			Setup:   acpwire.PresetSetup{Managed: []string{"fast"}, Prepared: 1},
		}, func() error {
			var err error
			prepared, setup, err = host.PreparePresets(ctx, config.Config{}, "probe", "http://127.0.0.1:1", nil)
			return err
		})
	if err != nil {
		t.Fatalf("PreparePresets: %v", err)
	}
	var prepareParams acpwire.PreparePresetsParams
	if err := json.Unmarshal(params, &prepareParams); err != nil {
		t.Fatalf("decode config/prepare_presets params: %v", err)
	}
	if prepareParams.Provider != "probe" || prepareParams.BaseURL != "http://127.0.0.1:1" {
		t.Fatalf("config/prepare_presets params = %+v, want the probe provider", prepareParams)
	}
	if len(prepareParams.Config) == 0 {
		t.Fatal("config/prepare_presets params carry no config")
	}
	if prepared["fast"].Model != "m1" {
		t.Fatalf("PreparePresets = %+v, want the scripted preset", prepared)
	}
	if setup.Prepared != 1 || len(setup.Managed) != 1 || setup.Managed[0] != "fast" {
		t.Fatalf("PreparePresets setup = %+v, want one prepared preset", setup)
	}

	// config/save_provenance.
	provenance := json.RawMessage(`{"models":{}}`)
	params, err = callHost(t, a, acpwire.MethodSaveProvenance, "s1", struct{}{}, func() error {
		return host.SaveProvenance(ctx, provenance)
	})
	if err != nil {
		t.Fatalf("SaveProvenance: %v", err)
	}
	var saved acpwire.ProvenanceParams
	if err := json.Unmarshal(params, &saved); err != nil {
		t.Fatalf("decode config/save_provenance params: %v", err)
	}
	if string(saved.Provenance) != string(provenance) {
		t.Fatalf("config/save_provenance params = %+v, want the sidecar", saved)
	}
}

// TestSaveUserValueRefusesNilAndPointers pins decision 5's client half: the two
// values SaveUserConfigValue refuses in process are refused here, unsent.
func TestSaveUserValueRefusesNilAndPointers(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	ctx := context.Background()

	enabled := true
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{name: "nil", value: nil, want: "rpc: config/set_value cannot write a <nil> value"},
		{name: "pointer", value: &enabled, want: "rpc: config/set_value cannot write a *bool value"},
	}
	for _, tc := range cases {
		err := host.SaveUserValue(ctx, "tools.shell.allow_network", tc.value)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("SaveUserValue with a %s = %v, want %q", tc.name, err, tc.want)
		}
	}

	select {
	case req := <-a.reqs:
		t.Fatalf("a refused save sent %q", req.Method)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestTheHostReadsAreFetchedOnce pins decision 2: the two reads ask once and
// keep the answer, and a failed read asks again on the next one.
func TestTheHostReadsAreFetchedOnce(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	// A failed first read reports nothing and keeps nothing.
	out := make(chan []acpwire.ProviderTemplate, 1)
	go func() { out <- host.ProviderTemplates() }()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodProviderTemplates {
		t.Fatalf("the first read sent %q, want %q", got, acpwire.MethodProviderTemplates)
	}
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "the session is gone",
	}))
	select {
	case got := <-out:
		if got != nil {
			t.Fatalf("a failed ProviderTemplates = %+v, want nil", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ProviderTemplates did not return")
	}

	// The next read asks again, and its answer is kept.
	out = make(chan []acpwire.ProviderTemplate, 1)
	go func() { out <- host.ProviderTemplates() }()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodProviderTemplates {
		t.Fatalf("the retried read sent %q, want %q", got, acpwire.MethodProviderTemplates)
	}
	a.answer(t, req, []acpwire.ProviderTemplate{{ID: "probe"}})
	select {
	case got := <-out:
		if len(got) != 1 || got[0].ID != "probe" {
			t.Fatalf("ProviderTemplates = %+v, want the probe template", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ProviderTemplates did not return")
	}

	// The third read answers from the kept value, sending nothing.
	if got := host.ProviderTemplates(); len(got) != 1 || got[0].ID != "probe" {
		t.Fatalf("the kept ProviderTemplates = %+v, want the probe template", got)
	}
	expectNoFrame(t, a, "the second successful ProviderTemplates")

	// TrustPrompt's first read fails too, and answers nothing.
	promptOut := make(chan acpwire.TrustPromptResult, 1)
	go func() { promptOut <- host.TrustPrompt() }()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodTrustPrompt {
		t.Fatalf("the first prompt read sent %q, want %q", got, acpwire.MethodTrustPrompt)
	}
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "the session is gone",
	}))
	select {
	case got := <-promptOut:
		if got != (acpwire.TrustPromptResult{}) {
			t.Fatalf("a failed TrustPrompt = %+v, want empty", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TrustPrompt did not return")
	}

	// The next read asks again, and its answer is kept.
	promptOut = make(chan acpwire.TrustPromptResult, 1)
	go func() { promptOut <- host.TrustPrompt() }()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodTrustPrompt {
		t.Fatalf("the retried prompt read sent %q, want %q", got, acpwire.MethodTrustPrompt)
	}
	a.answer(t, req, acpwire.TrustPromptResult{Dir: "/w"})
	select {
	case got := <-promptOut:
		if got.Dir != "/w" {
			t.Fatalf("TrustPrompt = %+v, want /w", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TrustPrompt did not return")
	}

	if got := host.TrustPrompt(); got.Dir != "/w" {
		t.Fatalf("the kept TrustPrompt = %+v, want /w", got)
	}
	expectNoFrame(t, a, "the second successful TrustPrompt")
}

// isZeroSetup reports whether setup is the zero value, which a struct holding
// a json.RawMessage cannot compare for itself.
func isZeroSetup(setup acpwire.PresetSetup) bool {
	return len(setup.Provenance) == 0 && len(setup.Managed) == 0 &&
		setup.Prepared == 0 && setup.Matched == 0 && setup.Fallback == 0
}

// expectNoFrame fails the test when the client sends anything.
func expectNoFrame(t *testing.T, a *scriptedAgent, what string) {
	t.Helper()
	select {
	case req := <-a.reqs:
		t.Fatalf("%s sent %q", what, req.Method)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestLoginProviderHandsTheURLToShow pins the sign-in's progress: the URL
// arrives on the read goroutine before the response does, and show sees it
// once.
func TestLoginProviderHandsTheURLToShow(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	urls := make(chan string, 4)
	out := make(chan error, 1)
	go func() {
		out <- host.LoginProvider(context.Background(), "probe", func(url string) { urls <- url })
	}()

	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodLoginProvider {
		t.Fatalf("LoginProvider sent %q, want %q", got, acpwire.MethodLoginProvider)
	}
	var params acpwire.LoginParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode config/login_provider params: %v", err)
	}
	if params.SessionID != "s1" || params.Name != "probe" || params.Prepare {
		t.Fatalf("config/login_provider params = %+v, want a sign-in for probe", params)
	}

	a.send(t, progressFrame(t, string(req.ID), acpwire.LoginProgress{URL: "https://example.test/auth"}))
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("LoginProvider: %v", err)
	}

	select {
	case got := <-urls:
		if got != "https://example.test/auth" {
			t.Fatalf("show saw %q, want the authorization URL", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("show was never called")
	}
	select {
	case got := <-urls:
		t.Fatalf("show was called again with %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestPreparePresetsRestoresThePricing pins Task 1's helper on the way back: a
// priced preset decodes into a *pricing.ModelPricing, not a map.
func TestPreparePresetsRestoresThePricing(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	ctx := context.Background()

	var presets map[string]routing.ModelPreset
	params, err := callHost(t, a, acpwire.MethodPreparePresets, "s1", acpwire.PreparedPresets{
		Presets: json.RawMessage(`{"priced":{"provider":"probe","model":"m1","pricing":{"InputPerMTokCents":250}}}`),
	}, func() error {
		var err error
		presets, _, err = host.PreparePresets(ctx, config.Config{}, "probe", "", nil)
		return err
	})
	if err != nil {
		t.Fatalf("PreparePresets: %v", err)
	}
	var sent acpwire.PreparePresetsParams
	if err := json.Unmarshal(params, &sent); err != nil {
		t.Fatalf("decode config/prepare_presets params: %v", err)
	}

	priced, ok := presets["priced"]
	if !ok {
		t.Fatalf("PreparePresets = %+v, want the priced preset", presets)
	}
	pricingValue, ok := priced.Pricing.(*pricing.ModelPricing)
	if !ok {
		t.Fatalf("the priced preset's Pricing = %T, want *pricing.ModelPricing", priced.Pricing)
	}
	if pricingValue.InputPerMTokCents != 250 {
		t.Fatalf("the priced preset's input rate = %d, want 250", pricingValue.InputPerMTokCents)
	}
}

// TestTheHostCallsReturnCodedErrors pins that the agent's coded errors reach
// the caller with their code intact.
func TestTheHostCallsReturnCodedErrors(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	ctx := context.Background()

	out := make(chan error, 1)
	go func() { out <- host.SaveModels(ctx, "probe", []schema.ModelInfo{{ID: "m1"}}) }()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodModelsSave {
		t.Fatalf("SaveModels sent %q, want %q", got, acpwire.MethodModelsSave)
	}
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeNoDatabase,
		Message: "no database",
	}))
	err := awaitCall(t, out)
	if !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Fatalf("SaveModels error = %v, want code %q", err, acpwire.CodeNoDatabase)
	}

	// A call with a result reports the code as well as the zero result.
	var presets map[string]routing.ModelPreset
	var setup acpwire.PresetSetup
	go func() {
		var err error
		presets, setup, err = host.PreparePresets(ctx, config.Config{}, "probe", "", nil)
		out <- err
	}()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodPreparePresets {
		t.Fatalf("PreparePresets sent %q, want %q", got, acpwire.MethodPreparePresets)
	}
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "the session is gone",
	}))
	err = awaitCall(t, out)
	if !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("PreparePresets error = %v, want code %q", err, acpwire.CodeSessionClosed)
	}
	if presets != nil || !isZeroSetup(setup) {
		t.Fatalf("PreparePresets returned %+v, %+v alongside its error", presets, setup)
	}
}

// boolPtr is a pointer to v, for the optional flags of a capability result.
func boolPtr(v bool) *bool { return &v }

// configFrame builds the session/update notification carrying one
// session_config update.
func configFrame(t *testing.T, sessionID string, value acpwire.SessionConfig) string {
	t.Helper()
	return updateFrame(t, sessionID, acpwire.SessionConfigUpdate{
		Kind:   acpwire.UpdateSessionConfig,
		Config: value,
	})
}

// mirrorValue is a mirror value the tests recognize by the provider its config
// and layers name and by its tools.
func mirrorValue(t *testing.T, provider string, tools ...string) acpwire.SessionConfig {
	t.Helper()
	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		provider: {Type: "openai_compatible"},
	}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	layers, err := json.Marshal(config.Layers{User: cfg, Merged: cfg})
	if err != nil {
		t.Fatalf("marshal layers: %v", err)
	}
	return acpwire.SessionConfig{Config: raw, Layers: layers, Tools: tools, ToolsReady: true}
}

// providerName is the one provider cfg names, and "" when it names none or
// several.
func providerName(cfg config.Config) string {
	if len(cfg.Providers) != 1 {
		return ""
	}
	for name := range cfg.Providers {
		return name
	}
	return ""
}

// awaitConfig waits for one config read's value.
func awaitConfig(t *testing.T, out <-chan config.Config) config.Config {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("Config did not return")
		return config.Config{}
	}
}

// firstConfigRead performs the config mirror's first read, answering
// session/config with value, and returns what that read reported.
func firstConfigRead(t *testing.T, session *Session, a *scriptedAgent, value acpwire.SessionConfig) config.Config {
	t.Helper()
	out := make(chan config.Config, 1)
	go func() { out <- session.Config() }()

	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodSessionConfig {
		t.Fatalf("the client sent %q, want %q", got, acpwire.MethodSessionConfig)
	}
	var params acpwire.ConfigParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode session/config params: %v", err)
	}
	if params.SessionID != session.ID() {
		t.Fatalf("session/config params = %+v, want session %q", params, session.ID())
	}
	a.answer(t, req, value)
	return awaitConfig(t, out)
}

// TestConfigFetchesOnItsFirstRead pins the two halves of the mirror's read:
// the first one asks the agent, and every later one answers from the mirror.
func TestConfigFetchesOnItsFirstRead(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	if name := providerName(firstConfigRead(t, session, a, mirrorValue(t, "probe"))); name != "probe" {
		t.Fatalf("the first read's provider = %q, want probe", name)
	}
	if name := providerName(session.Config()); name != "probe" {
		t.Fatalf("the second read's provider = %q, want probe", name)
	}
	expectNoFrame(t, a, "the second Config read")
}

// TestConfigInstallsTheUpdateThatPrecedesTheResponse pins the barrier: the
// agent answers session/config with the config it had, so an update that
// reaches the client before the response is newer and must be kept.
func TestConfigInstallsTheUpdateThatPrecedesTheResponse(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := make(chan config.Config, 1)
	go func() { out <- session.Config() }()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodSessionConfig {
		t.Fatalf("the client sent %q, want %q", got, acpwire.MethodSessionConfig)
	}
	a.send(t, configFrame(t, "s1", mirrorValue(t, "A")))
	a.answer(t, req, mirrorValue(t, "B"))

	if name := providerName(awaitConfig(t, out)); name != "A" {
		t.Fatalf("the read's provider = %q, want A: the update that preceded the response is newer", name)
	}
}

// TestASessionConfigUpdateReplacesTheMirrorAndFiresChanged pins the mirror's
// other half: an update replaces the value and taps Changed.
func TestASessionConfigUpdateReplacesTheMirrorAndFiresChanged(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	firstConfigRead(t, session, a, mirrorValue(t, "A"))
	changed := session.Changed()
	drainChanged(t, changed)

	a.send(t, configFrame(t, "s1", mirrorValue(t, "B")))
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not fire for the update")
	}
	if name := providerName(session.Config()); name != "B" {
		t.Fatalf("the mirror's provider = %q, want B", name)
	}
}

// TestConfigReturnsACopy pins that a caller cannot reach the mirror through
// what it got: changing the returned config's provider map leaves the next
// read unchanged.
func TestConfigReturnsACopy(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	firstConfigRead(t, session, a, mirrorValue(t, "probe"))

	first := session.Config()
	first.Providers["added"] = config.ProviderConfig{Type: "ollama"}
	delete(first.Providers, "probe")

	if name := providerName(session.Config()); name != "probe" {
		t.Fatalf("the second read's provider = %q, want probe: the first read's map must not reach the mirror", name)
	}
	expectNoFrame(t, a, "the second Config read")
}

// TestLayersAndToolsReadTheMirror pins decision 3: one session/config read
// subscribes, and a session_config update then serves Layers and Tools with no
// call.
func TestLayersAndToolsReadTheMirror(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	firstConfigRead(t, session, a, mirrorValue(t, "A"))
	a.send(t, configFrame(t, "s1", mirrorValue(t, "B", "file_read", "shell_run")))
	// The ping proves the read goroutine dispatched the update already.
	pingScripted(t, host, a)

	if name := providerName(session.Layers().Merged); name != "B" {
		t.Fatalf("Layers' merged provider = %q, want B", name)
	}
	names, ready := session.Tools()
	if !ready {
		t.Fatal("Tools reports no registry, want the update's flag")
	}
	if len(names) != 2 || names[0] != "file_read" || names[1] != "shell_run" {
		t.Fatalf("Tools = %v, want the update's two tools", names)
	}
	expectNoFrame(t, a, "Layers and Tools after the update")
}

// reloadOutcome is one reload call's flag and error.
type reloadOutcome struct {
	reloaded bool
	err      error
}

// awaitReload waits for one reload call's outcome.
func awaitReload(t *testing.T, out <-chan reloadOutcome) reloadOutcome {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("the reload call did not return")
		return reloadOutcome{}
	}
}

// TestTheReloadCallsReturnTheFlagWithTheError pins decision 6: the result's
// flag comes back with the result's error, which a JSON-RPC error response
// could not carry.
func TestTheReloadCallsReturnTheFlagWithTheError(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	ctx := context.Background()

	cases := []struct {
		name   string
		method string
		call   func(context.Context) (bool, error)
	}{
		{
			name:   "ReloadConfig",
			method: acpwire.MethodReloadConfig,
			call:   func(ctx context.Context) (bool, error) { return session.ReloadConfig(ctx, config.Config{}) },
		},
		{
			name:   "ReconnectMCP",
			method: acpwire.MethodMCPReconnect,
			call:   session.ReconnectMCP,
		},
	}
	for _, tc := range cases {
		// The flag and the error travel together.
		out := make(chan reloadOutcome, 1)
		go func() {
			reloaded, err := tc.call(ctx)
			out <- reloadOutcome{reloaded: reloaded, err: err}
		}()
		req := a.next(t)
		if got := string(req.Method); got != tc.method {
			t.Fatalf("%s sent %q, want %q", tc.name, got, tc.method)
		}
		a.answer(t, req, acpwire.ReloadResult{
			Reloaded: true,
			Error:    &acpwire.Error{Message: "the runtime did not rebuild"},
		})
		got := awaitReload(t, out)
		if !got.reloaded {
			t.Fatalf("%s = false, want the result's flag", tc.name)
		}
		if got.err == nil || got.err.Error() != "the runtime did not rebuild" {
			t.Fatalf("%s error = %v, want the result's error", tc.name, got.err)
		}

		// A flag without an error is a plain answer.
		go func() {
			reloaded, err := tc.call(ctx)
			out <- reloadOutcome{reloaded: reloaded, err: err}
		}()
		req = a.next(t)
		if got := string(req.Method); got != tc.method {
			t.Fatalf("%s sent %q, want %q", tc.name, got, tc.method)
		}
		a.answer(t, req, acpwire.ReloadResult{})
		got = awaitReload(t, out)
		if got.reloaded || got.err != nil {
			t.Fatalf("%s = %v, %v; want false and no error", tc.name, got.reloaded, got.err)
		}
	}
}

// sendCall starts one session call on its own goroutine, checks the method it
// sent, answers it with result, and returns the params the frame carried.
func sendCall(t *testing.T, a *scriptedAgent, method string, result any, fn func() error) json.RawMessage {
	t.Helper()
	out := make(chan error, 1)
	go func() { out <- fn() }()

	req := a.next(t)
	if got := string(req.Method); got != method {
		t.Fatalf("the client sent %q, want %q", got, method)
	}
	a.answer(t, req, result)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return req.Params
}

// configParam decodes one call's params and reports the session and the config
// they carry.
func configParam(t *testing.T, params json.RawMessage, method string) (string, config.Config) {
	t.Helper()
	var sent acpwire.ConfigParams
	if err := json.Unmarshal(params, &sent); err != nil {
		t.Fatalf("decode %s params: %v", method, err)
	}
	var cfg config.Config
	if err := config.UnmarshalConfigJSON(sent.Config, &cfg); err != nil {
		t.Fatalf("decode %s' config: %v", method, err)
	}
	return sent.SessionID, cfg
}

// TestTheSessionConfigCallsSendTheirMethods pins each session call's method and
// params.
func TestTheSessionConfigCallsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	ctx := context.Background()
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"probe": {Type: "ollama"}}}

	// session/apply_config carries the config.
	params := sendCall(t, a, acpwire.MethodApplyConfig, struct{}{}, func() error {
		return session.ApplyConfig(ctx, cfg)
	})
	sessionID, applied := configParam(t, params, acpwire.MethodApplyConfig)
	if sessionID != "s1" || providerName(applied) != "probe" {
		t.Fatalf("session/apply_config params carry session %q and provider %q, want s1 and probe",
			sessionID, providerName(applied))
	}

	// session/save_project_config carries the config too.
	params = sendCall(t, a, acpwire.MethodSaveProjectConfig, struct{}{}, func() error {
		return session.SaveProjectConfig(ctx, cfg)
	})
	sessionID, saved := configParam(t, params, acpwire.MethodSaveProjectConfig)
	if sessionID != "s1" || providerName(saved) != "probe" {
		t.Fatalf("session/save_project_config params carry session %q and provider %q, want s1 and probe",
			sessionID, providerName(saved))
	}

	// session/reload_config carries the config and returns the result's flag.
	reloads := make(chan reloadOutcome, 1)
	go func() {
		reloaded, err := session.ReloadConfig(ctx, cfg)
		reloads <- reloadOutcome{reloaded: reloaded, err: err}
	}()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodReloadConfig {
		t.Fatalf("ReloadConfig sent %q, want %q", got, acpwire.MethodReloadConfig)
	}
	sessionID, reloadedCfg := configParam(t, req.Params, acpwire.MethodReloadConfig)
	if sessionID != "s1" || providerName(reloadedCfg) != "probe" {
		t.Fatalf("session/reload_config params carry session %q and provider %q, want s1 and probe",
			sessionID, providerName(reloadedCfg))
	}
	a.answer(t, req, acpwire.ReloadResult{Reloaded: true})
	if got := awaitReload(t, reloads); got.err != nil || !got.reloaded {
		t.Fatalf("ReloadConfig = %v, %v; want the result's flag", got.reloaded, got.err)
	}

	// session/mcp_reconnect names the session and carries no config.
	params = sendCall(t, a, acpwire.MethodMCPReconnect, acpwire.ReloadResult{Reloaded: true}, func() error {
		_, err := session.ReconnectMCP(ctx)
		return err
	})
	var reconnect acpwire.ConfigParams
	if err := json.Unmarshal(params, &reconnect); err != nil {
		t.Fatalf("decode session/mcp_reconnect params: %v", err)
	}
	if reconnect.SessionID != "s1" || len(reconnect.Config) != 0 {
		t.Fatalf("session/mcp_reconnect params = %+v, want session s1 and no config", reconnect)
	}

	// session/save_verify_commands carries both commands.
	params = sendCall(t, a, acpwire.MethodSaveVerifyCommands, struct{}{}, func() error {
		return session.SaveVerifyCommands(ctx, "go build ./...", "go test ./...")
	})
	var verify acpwire.VerifyCommandsParams
	if err := json.Unmarshal(params, &verify); err != nil {
		t.Fatalf("decode session/save_verify_commands params: %v", err)
	}
	if verify.SessionID != "s1" || verify.Build != "go build ./..." || verify.Test != "go test ./..." {
		t.Fatalf("session/save_verify_commands params = %+v, want the two commands", verify)
	}

	// session/mcp_login's prepare.
	params = sendCall(t, a, acpwire.MethodMCPLogin, struct{}{}, func() error {
		return session.PrepareMCPLogin(ctx, "server1")
	})
	var prepare acpwire.LoginParams
	if err := json.Unmarshal(params, &prepare); err != nil {
		t.Fatalf("decode session/mcp_login params: %v", err)
	}
	if prepare.SessionID != "s1" || prepare.Name != "server1" || !prepare.Prepare {
		t.Fatalf("session/mcp_login params = %+v, want a prepare for server1", prepare)
	}

	// session/mcp_login's sign-in, whose URL goes to show.
	urls := make(chan string, 4)
	out := make(chan error, 1)
	go func() {
		out <- session.LoginMCP(ctx, "server1", func(url string) { urls <- url })
	}()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodMCPLogin {
		t.Fatalf("LoginMCP sent %q, want %q", got, acpwire.MethodMCPLogin)
	}
	var login acpwire.LoginParams
	if err := json.Unmarshal(req.Params, &login); err != nil {
		t.Fatalf("decode session/mcp_login params: %v", err)
	}
	if login.SessionID != "s1" || login.Name != "server1" || login.Prepare {
		t.Fatalf("session/mcp_login params = %+v, want a sign-in for server1", login)
	}
	a.send(t, progressFrame(t, string(req.ID), acpwire.LoginProgress{URL: "https://example.test/auth"}))
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("LoginMCP: %v", err)
	}
	select {
	case got := <-urls:
		if got != "https://example.test/auth" {
			t.Fatalf("show saw %q, want the authorization URL", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("show was never called")
	}
}
