package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidate_MissingExecutorModel(t *testing.T) {
	cfg := &Config{
		Executor: AgentConfig{
			BaseURL: "https://api.example.com",
			APIKey:  "key",
		},
		Critic: AgentConfig{
			Model:   "model",
			BaseURL: "https://api.example.com",
			APIKey:  "key",
		},
	}
	err := cfg.Validate()
	if err == nil || err.Error() != "executor.model is required" {
		t.Errorf("expected 'executor.model is required', got: %v", err)
	}
}

func TestValidate_MissingExecutorBaseURL(t *testing.T) {
	cfg := &Config{
		Executor: AgentConfig{
			Model:  "model",
			APIKey: "key",
		},
		Critic: AgentConfig{
			Model:   "model",
			BaseURL: "https://api.example.com",
			APIKey:  "key",
		},
	}
	err := cfg.Validate()
	if err == nil || err.Error() != "executor.base_url is required" {
		t.Errorf("expected 'executor.base_url is required', got: %v", err)
	}
}

func TestValidate_MissingExecutorAPIKey(t *testing.T) {
	cfg := &Config{
		Executor: AgentConfig{
			Model:   "model",
			BaseURL: "https://api.example.com",
		},
		Critic: AgentConfig{
			Model:   "model",
			BaseURL: "https://api.example.com",
			APIKey:  "key",
		},
	}
	err := cfg.Validate()
	expectedMsg := "executor.api_key / $FIREWORKS_API_KEY is required (not needed for ollama/bedrock providers)"
	if err == nil || err.Error() != expectedMsg {
		t.Errorf("expected '%s', got: %v", expectedMsg, err)
	}
}

func TestValidate_MissingCriticModel(t *testing.T) {
	cfg := &Config{
		Executor: AgentConfig{
			Model:   "model",
			BaseURL: "https://api.example.com",
			APIKey:  "key",
		},
		Critic: AgentConfig{
			BaseURL: "https://api.example.com",
			APIKey:  "key",
		},
	}
	err := cfg.Validate()
	if err == nil || err.Error() != "critic.model is required" {
		t.Errorf("expected 'critic.model is required', got: %v", err)
	}
}

func TestValidate_MissingCriticBaseURL(t *testing.T) {
	cfg := &Config{
		Executor: AgentConfig{
			Model:   "model",
			BaseURL: "https://api.example.com",
			APIKey:  "key",
		},
		Critic: AgentConfig{
			Model:  "model",
			APIKey: "key",
		},
	}
	err := cfg.Validate()
	if err == nil || err.Error() != "critic.base_url is required" {
		t.Errorf("expected 'critic.base_url is required', got: %v", err)
	}
}

func TestValidate_MissingCriticAPIKey(t *testing.T) {
	cfg := &Config{
		Executor: AgentConfig{
			Model:   "model",
			BaseURL: "https://api.example.com",
			APIKey:  "key",
		},
		Critic: AgentConfig{
			Model:   "model",
			BaseURL: "https://api.example.com",
		},
	}
	err := cfg.Validate()
	expectedMsg := "critic.api_key / $FIREWORKS_API_KEY is required (not needed for ollama/bedrock providers)"
	if err == nil || err.Error() != expectedMsg {
		t.Errorf("expected '%s', got: %v", expectedMsg, err)
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	cfg := &Config{
		Executor: AgentConfig{
			Model:   "executor-model",
			BaseURL: "https://api.example.com",
			APIKey:  "executor-key",
		},
		Critic: AgentConfig{
			Model:   "critic-model",
			BaseURL: "https://api.example.com",
			APIKey:  "critic-key",
		},
		Loop: LoopConfig{
			MaxRounds:    3,
			CompactAfter: 2,
		},
	}
	err := cfg.Validate()
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestLoad_EnvVarExpansion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.toml")

	content := `
[executor]
model = "test-model"
base_url = "https://api.example.com"
api_key = "${TEST_API_KEY}"

[critic]
model = "critic-model"
base_url = "https://api.example.com"
api_key = "critic-key"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	os.Setenv("TEST_API_KEY", "expanded-key")
	defer os.Unsetenv("TEST_API_KEY")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Executor.APIKey != "expanded-key" {
		t.Errorf("expected APIKey to be 'expanded-key', got: %s", cfg.Executor.APIKey)
	}
}

func TestLoad_MissingEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.toml")

	content := `
[executor]
model = "test-model"
base_url = "https://api.example.com"
api_key = "${MISSING_VAR}"

[critic]
model = "critic-model"
base_url = "https://api.example.com"
api_key = "critic-key"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Executor.APIKey != "" {
		t.Errorf("expected empty APIKey for missing env var, got: %s", cfg.Executor.APIKey)
	}
}

func TestLoad_MalformedTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.toml")

	content := `
[executor
model = "test-model"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Error("expected error for malformed TOML, got nil")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}

func TestLoad_DotEnvExpansion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.toml")
	dotEnvPath := filepath.Join(tmpDir, ".env")

	configContent := `
[executor]
model = "test-model"
base_url = "https://api.example.com"
api_key = "${DOTENV_KEY}"

[critic]
model = "critic-model"
base_url = "https://api.example.com"
api_key = "critic-key"
`
	dotEnvContent := "DOTENV_KEY=from-dotenv"

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	if err := os.WriteFile(dotEnvPath, []byte(dotEnvContent), 0644); err != nil {
		t.Fatalf("failed to write .env file: %v", err)
	}

	// Change to temp dir so loadDotEnv finds .env
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Executor.APIKey != "from-dotenv" {
		t.Errorf("expected APIKey to be 'from-dotenv', got: %s", cfg.Executor.APIKey)
	}
}

func TestLoad_ShellEnvTakesPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.toml")
	dotEnvPath := filepath.Join(tmpDir, ".env")

	configContent := `
[executor]
model = "test-model"
base_url = "https://api.example.com"
api_key = "${PRECEDENCE_KEY}"

[critic]
model = "critic-model"
base_url = "https://api.example.com"
api_key = "critic-key"
`
	dotEnvContent := "PRECEDENCE_KEY=from-dotenv"

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	if err := os.WriteFile(dotEnvPath, []byte(dotEnvContent), 0644); err != nil {
		t.Fatalf("failed to write .env file: %v", err)
	}

	// Shell env takes precedence
	os.Setenv("PRECEDENCE_KEY", "from-shell")
	defer os.Unsetenv("PRECEDENCE_KEY")

	// Change to temp dir so loadDotEnv finds .env
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Executor.APIKey != "from-shell" {
		t.Errorf("expected APIKey to be 'from-shell' (shell takes precedence), got: %s", cfg.Executor.APIKey)
	}
}
