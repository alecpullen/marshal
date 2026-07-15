package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"marshal/internal/app/config"
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
	if m.state != stateConfigureURL {
		t.Fatalf("after entering project name, state = %d, want stateConfigureURL", m.state)
	}

	// 3. Configure URL
	m.textInput.SetValue("http://localhost:11434/v1")
	m2, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)
	if m.state != stateModelSelection {
		t.Fatalf("after entering URL, state = %d, want stateModelSelection", m.state)
	}

	// Simulate Ollama tags fetch failing
	m2, _ = m.Update(ollamaModelsFailedMsg{})
	m = m2.(*OnboardingModel)
	if !m.ollamaErr {
		t.Fatal("expected ollamaErr to be true")
	}

	// 3. Model selection (enter custom model name)
	m.textInput.SetValue("my-test-model")
	m2, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(*OnboardingModel)
	if m.state != stateDone {
		t.Fatalf("after selecting model, state = %d, want stateDone", m.state)
	}

	// 4. Verify config file was written successfully
	configPath := filepath.Join(tempDir, ".marshal", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, `model = "my-test-model"`) {
		t.Errorf("config does not contain model name, content:\n%s", content)
	}
	if !strings.Contains(content, `base_url = "http://localhost:11434/v1"`) {
		t.Errorf("config does not contain base url, content:\n%s", content)
	}

	// 5. Verify the config is loadable by our loader
	cfg, err := config.Load(config.LoadOptions{WorkingDir: tempDir})
	if err != nil {
		t.Fatalf("failed to load generated config: %v", err)
	}
	expectedName := filepath.Base(tempDir)
	if cfg.Project.Name != expectedName {
		t.Errorf("loaded config project name = %q, want %q", cfg.Project.Name, expectedName)
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
