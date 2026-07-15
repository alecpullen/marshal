package app

import (
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
	if m.state != stateConfigureURL {
		t.Fatalf("after selecting provider, state = %d, want stateConfigureURL", m.state)
	}

	// 2. Configure URL
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
	if cfg.Project.Name != "my-project" {
		t.Errorf("loaded config project name = %q, want 'my-project'", cfg.Project.Name)
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

func TestOnboardingNeverWritesRawKeyToProject(t *testing.T) {
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
	data, err := os.ReadFile(filepath.Join(dir, ".marshal", "config.toml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "sk-deadbeef") {
		t.Fatalf("raw key leaked into project config: %s", data)
	}
	if !strings.Contains(string(data), `api_key_env = "MARSHAL_GLOBAL_API_KEY"`) {
		t.Fatalf("expected api_key_env reference, got: %s", data)
	}
	// Verify the global config was written with the raw key and a header.
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
