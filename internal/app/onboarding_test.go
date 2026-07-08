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
