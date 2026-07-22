package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"marshal/internal/app/config"
	"marshal/internal/app/tui/connect"
	"marshal/internal/llm/routing"
)

func TestOnboardingWizardTransitionsAndSaves(t *testing.T) {
	tempDir := t.TempDir()

	m := NewOnboardingModel(tempDir)
	if m.state != stateSelectProvider {
		t.Fatalf("initial state = %d, want stateSelectProvider", m.state)
	}

	// 1. Select provider (default is Ollama)
	m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)
	if m.state != stateProjectName {
		t.Fatalf("after selecting provider, state = %d, want stateProjectName", m.state)
	}

	// 2. Enter project name (accept default derived from working dir)
	m2, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)
	if m.state != stateConnect {
		t.Fatalf("after entering project name, state = %d, want stateConnect", m.state)
	}
	if m.connectModel == nil {
		t.Fatal("expected connectModel to be non-nil after transitioning to stateConnect")
	}
}

// newTestOnboardingModel builds an OnboardingModel with every step
// pre-filled for a non-Ollama provider, ready to call saveConfig.
func newTestOnboardingModel() *OnboardingModel {
	m := NewOnboardingModel("") // workingDir set by caller
	m.state = stateModelSelection
	m.selectedProvider = "OpenAI"
	m.baseURL = ""
	m.keyMode = keyModeEnvName
	m.apiKey = "OPENAI_API_KEY"
	m.modelName = "gpt-4o"
	return m
}

func TestInlineAPIKeyLivesInGlobalConfigOnly(t *testing.T) {
	m := newTestOnboardingModel()
	m.keyMode = keyModeInline
	m.keySecret = "sk-deadbeef-no-underscore"
	dir := t.TempDir()
	m.workingDir = dir
	// Point the global config to a temp file so the test never touches
	// the real ~/.config/marshal/config.toml.
	globalDir := t.TempDir()
	m.globalConfigPath = filepath.Join(globalDir, "config.toml")

	if err := m.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	// Project config must NOT contain the raw key.
	projectData, err := os.ReadFile(filepath.Join(dir, ".marshal", "config.toml"))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(projectData), "sk-deadbeef") {
		t.Fatalf("raw key leaked into project config: %s", projectData)
	}
	// Project config must NOT contain a [providers.<name>] block in inline
	// mode — the global entry (with the real api_key) is preserved by the
	// loader's whole-entry merge when the project file has no entry.
	if strings.Contains(string(projectData), "[providers.openai]") {
		t.Fatalf("project config must not contain [providers.openai] block in inline mode, got: %s", projectData)
	}
	// Project config should have the explanatory comment instead.
	if !strings.Contains(string(projectData), "Provider config (api_key) lives in the global config") {
		t.Fatalf("expected explanatory comment in project config, got: %s", projectData)
	}

	// Global config must contain the raw key and a header.
	globalData, err := os.ReadFile(m.globalConfigPath)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	if !strings.Contains(string(globalData), "sk-deadbeef") {
		t.Fatalf("raw key not found in global config: %s", globalData)
	}
	if !strings.Contains(string(globalData), "# Marshal global configuration") {
		t.Fatalf("expected header in global config, got: %s", globalData)
	}
}

func TestOnboardingEnvVarModeWritesAPIKeyEnv(t *testing.T) {
	m := newTestOnboardingModel()
	m.keyMode = keyModeEnvName
	m.apiKey = "MY_CUSTOM_ENV_VAR"
	dir := t.TempDir()
	m.workingDir = dir

	if err := m.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".marshal", "config.toml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `api_key_env = "MY_CUSTOM_ENV_VAR"`) {
		t.Fatalf("expected api_key_env = \"MY_CUSTOM_ENV_VAR\", got: %s", data)
	}
	if strings.Contains(string(data), `api_key = "MY_CUSTOM_ENV_VAR"`) {
		t.Fatalf("raw key written instead of env var reference: %s", data)
	}
}

func TestOnboardingProjectNameFromWorkingDir(t *testing.T) {
	m := newTestOnboardingModel()
	m.workingDir = t.TempDir()
	m.projectName = "" // simulate user pressing Enter on default

	if err := m.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(m.workingDir, ".marshal", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	expectedName := filepath.Base(m.workingDir)
	if !strings.Contains(string(data), `name = "`+expectedName+`"`) {
		t.Fatalf("expected name derived from working dir (%q), got: %s", expectedName, data)
	}
}

func TestOnboardingProjectNameCustomValue(t *testing.T) {
	m := newTestOnboardingModel()
	m.workingDir = t.TempDir()
	m.projectName = "my-custom-project"

	if err := m.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(m.workingDir, ".marshal", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), `name = "my-custom-project"`) {
		t.Fatalf("expected name = \"my-custom-project\", got: %s", data)
	}
}

func TestFetchOllamaModelsFallsBackOn404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:14b"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	msg := fetchOllamaModels(ts.URL + "/v1")()
	loaded, ok := msg.(ollamaModelsLoadedMsg)
	if !ok {
		t.Fatalf("expected ollamaModelsLoadedMsg, got %T", msg)
	}
	if len(loaded) != 1 || loaded[0] != "qwen2.5-coder:14b" {
		t.Fatalf("models = %v", loaded)
	}
}

func TestFetchOllamaModelsNon200Status(t *testing.T) {
	// Start a test server that returns 500 with a JSON body.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer ts.Close()

	cmd := fetchOllamaModels(ts.URL)
	msg := cmd()
	if _, ok := msg.(ollamaModelsFailedMsg); !ok {
		t.Fatalf("expected ollamaModelsFailedMsg, got %T", msg)
	}
}

// TestOnboardingPointerReceiverSemantics verifies that mutations to a plain
// (int) field on OnboardingModel persist across Update calls. This test
// would fail if any method used a value receiver instead of a pointer
// receiver, because the int field would be copied and the increment lost.
func TestOnboardingPointerReceiverSemantics(t *testing.T) {
	m := newTestOnboardingModel()
	m.attempts = 0

	// Dispatch two "enter" key presses. Each one hits the
	// stateSelectProvider branch (we reset state between dispatches) and
	// increments attempts. If receivers were value receivers, the
	// increment from the first dispatch would be lost and attempts would
	// still be 1 after the second dispatch.
	for i := 1; i <= 2; i++ {
		m.state = stateSelectProvider
		m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = m2.(*OnboardingModel)
		if m.attempts != i {
			t.Fatalf("after dispatch %d: attempts = %d, want %d (value receiver would give %d)",
				i, m.attempts, i, i-1)
		}
	}
}

func TestOnboardingKeyEntryMasksInput(t *testing.T) {
	t.Run("inline mode", func(t *testing.T) {
		m := NewOnboardingModel(t.TempDir())
		m.selectedProvider = "OpenAI"
		m.keyMode = keyModeInline
		m.state = stateKeyMode
		m.textInput.SetValue("")

		m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = m2.(*OnboardingModel)

		if m.state != stateConfigureKey {
			t.Fatalf("expected stateConfigureKey, got %v", m.state)
		}
		if m.textInput.EchoMode != textinput.EchoPassword {
			t.Fatalf("expected EchoPassword for inline API key input, got %v", m.textInput.EchoMode)
		}
	})

	t.Run("env var mode", func(t *testing.T) {
		m := NewOnboardingModel(t.TempDir())
		m.selectedProvider = "OpenAI"
		m.keyMode = keyModeEnvName
		m.state = stateKeyMode
		m.textInput.SetValue("")

		m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = m2.(*OnboardingModel)

		if m.state != stateConfigureKey {
			t.Fatalf("expected stateConfigureKey, got %v", m.state)
		}
		if m.textInput.EchoMode != textinput.EchoPassword {
			t.Fatalf("expected EchoPassword for env var API key input, got %v", m.textInput.EchoMode)
		}
	})
}

func TestOnboardingUnsetModeWritesCommentedPlaceholder(t *testing.T) {
	m := newTestOnboardingModel()
	m.keyMode = keyModeUnset
	m.apiKey = ""
	dir := t.TempDir()
	m.workingDir = dir

	if err := m.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".marshal", "config.toml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "# api_key_env") {
		t.Fatalf("expected commented-out api_key_env placeholder, got: %s", data)
	}
}

func TestOnboardingEmbedsConnectAfterProjectName(t *testing.T) {
	// After entering the project name, the onboarding should transition to
	// the connect wizard (stateConnect).
	m := NewOnboardingModel(t.TempDir())

	// Press Enter to go to project name
	m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)
	if m.state != stateProjectName {
		t.Fatalf("after selecting provider, state = %d, want stateProjectName", m.state)
	}

	// Press Enter to accept default project name
	m2, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)
	if m.state != stateConnect {
		t.Fatalf("after entering project name, state = %d, want stateConnect", m.state)
	}
	if m.connectModel == nil {
		t.Fatal("expected connectModel to be non-nil")
	}
}

func TestOnboardingSaveConfigFromDoneMsg(t *testing.T) {
	// Simulate the full flow: select provider -> project name -> connect wizard
	// -> DoneMsg -> config saved.
	dir := t.TempDir()
	m := NewOnboardingModel(dir)

	// Select provider (default Ollama)
	m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)

	// Enter project name
	m2, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)
	if m.state != stateConnect {
		t.Fatalf("expected stateConnect, got %d", m.state)
	}

	// Send DoneMsg as if the connect wizard completed
	doneMsg := connect.DoneMsg{
		Provider: "ollama",
		Model:    "qwen2.5-coder:7b",
		ProviderCfg: config.ProviderConfig{
			Type:    "openai_compatible",
			BaseURL: "http://localhost:11434/v1",
		},
	}
	m2, _ = m.Update(doneMsg)
	m = m2.(*OnboardingModel)
	if m.state != stateDone {
		t.Fatalf("after DoneMsg, state = %d, want stateDone", m.state)
	}

	// Verify config was written
	configPath := filepath.Join(dir, ".marshal", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `model = "qwen2.5-coder:7b"`) {
		t.Errorf("config does not contain model name, content:\n%s", content)
	}
	if !strings.Contains(content, `base_url = "http://localhost:11434/v1"`) {
		t.Errorf("config does not contain base url, content:\n%s", content)
	}
}

func TestOnboardingInlineKeyGoesToGlobalConfigOnly(t *testing.T) {
	// When the connect wizard returns a DoneMsg with an inline API key,
	// the key should be written to the global config, not the project config.
	dir := t.TempDir()
	globalDir := t.TempDir()
	m := NewOnboardingModel(dir)
	m.globalConfigPath = filepath.Join(globalDir, "config.toml")

	// Select provider
	m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)

	// Enter project name -> stateConnect
	m2, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)
	if m.state != stateConnect {
		t.Fatalf("expected stateConnect, got %d", m.state)
	}

	// Send DoneMsg with inline API key
	doneMsg := connect.DoneMsg{
		Provider: "openai",
		Model:    "gpt-4o",
		ProviderCfg: config.ProviderConfig{
			Type:    "openai_compatible",
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "sk-test-inline-key",
		},
	}
	m2, _ = m.Update(doneMsg)
	m = m2.(*OnboardingModel)
	if m.state != stateDone {
		t.Fatalf("after DoneMsg, state = %d, want stateDone", m.state)
	}

	// Project config must NOT contain the raw key.
	projectData, err := os.ReadFile(filepath.Join(dir, ".marshal", "config.toml"))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(projectData), "sk-test-inline-key") {
		t.Fatalf("raw key leaked into project config: %s", projectData)
	}

	// Global config must contain the raw key.
	globalData, err := os.ReadFile(m.globalConfigPath)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	if !strings.Contains(string(globalData), "sk-test-inline-key") {
		t.Fatalf("raw key not found in global config: %s", globalData)
	}
}

func TestSaveConfigAssignsPresetToEveryRoutingRole(t *testing.T) {
	m := newTestOnboardingModel()
	dir := t.TempDir()
	m.workingDir = dir

	if err := m.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".marshal", "config.toml"))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	content := string(data)
	for _, role := range routing.AllRoles {
		want := string(role) + ` = "onboarded_preset"`
		if !strings.Contains(content, want) {
			t.Errorf("project config missing role assignment %q", want)
		}
	}
	if got := strings.Count(content, ` = "onboarded_preset"`); got != len(routing.AllRoles) {
		t.Errorf("onboarded_preset assignments = %d, want %d (one per routing role)", got, len(routing.AllRoles))
	}
}
