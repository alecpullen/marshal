package app

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/connect"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/llm/routing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type onboardingState int

const (
	stateSelectProvider onboardingState = iota
	stateProjectName
	stateConnect
	stateConfigureURL
	stateKeyMode
	stateConfigureKey
	stateModelSelection
	stateDone
)

type keyModeKind int

const (
	keyModeUnset keyModeKind = iota
	keyModeEnvName
	keyModeInline
)

const (
	marshalGlobalAPIKey = "MARSHAL_GLOBAL_API_KEY"
)

type OnboardingModel struct {
	state      onboardingState
	workingDir string
	cancelled  bool

	// Step 1: Provider selection
	providers     []string
	providerIndex int

	// Step 2/3: Input fields
	textInput textfield.Model
	loading   bool
	err       string

	// Collected inputs
	selectedProvider string
	projectName      string
	baseURL          string
	apiKey           string
	modelName        string

	// API key mode (F-UIUX-137)
	keyMode   keyModeKind // keyModeEnvName | keyModeInline | keyModeUnset
	keySecret string      // when keyMode == inline, the value the user typed

	// globalConfigPath overrides the default global config path for testing.
	// If empty, defaults to ~/.config/marshal/config.toml.
	globalConfigPath string

	// Ollama dynamic models
	ollamaModels []string
	ollamaIndex  int
	ollamaErr    bool

	// projectNameTouched tracks whether the user typed in the project name
	// field, so we can distinguish "pressed Enter on the default" from
	// "typed then cleared the input" (F-POL-168).
	projectNameTouched bool

	// connectModel is the embedded connect wizard model, active during
	// stateConnect. It replaces the old inline key/model steps.
	connectModel *connect.Model

	// attempts is a plain int field used only by the pointer-receiver
	// regression test (F-BUG-158). It is incremented on each "enter" key
	// press while in stateSelectProvider. If receivers were value receivers,
	// the increment would not persist across Update calls.
	attempts int
}

type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func NewOnboardingModel(workingDir string) *OnboardingModel {
	ti := textfield.New()
	ti.Focus()
	ti.CharLimit = 156
	ti.SetWidth(40)

	return &OnboardingModel{
		state:            stateSelectProvider,
		workingDir:       workingDir,
		providers:        []string{"Ollama (Local)", "OpenRouter", "OpenAI"},
		textInput:        ti,
		selectedProvider: "Ollama (Local)",
	}
}

// Cancelled returns true when the user pressed Esc or Ctrl+C to exit the
// onboarding wizard before completing all steps.
func (m *OnboardingModel) Cancelled() bool { return m.cancelled }

// InputValue returns the onboarding text input's current text.
func (m *OnboardingModel) InputValue() string { return m.textInput.Value() }

func (m *OnboardingModel) Init() tea.Cmd {
	return textinput.Blink
}

type ollamaModelsLoadedMsg []string
type ollamaModelsFailedMsg struct{}

func fetchOllamaModels(url string) tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{Timeout: 2 * time.Second}
		reqURL := strings.TrimRight(url, "/") + "/tags"
		resp, err := client.Get(reqURL)
		if err != nil || resp.StatusCode != http.StatusOK {
			// The default "/v1" base URL answers 404 for /v1/tags, so retry
			// the native /api/tags path on non-200 too (previously only
			// transport errors retried, making the default Ollama path
			// unreachable).
			if resp != nil {
				resp.Body.Close()
			}
			reqURL = strings.Replace(reqURL, "/v1/tags", "/api/tags", 1)
			resp, err = client.Get(reqURL)
			if err != nil {
				return ollamaModelsFailedMsg{}
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			slog.Default().Warn("ollama /api/tags returned non-200",
				"status", resp.StatusCode,
				"body", strings.TrimSpace(string(body)),
			)
			return ollamaModelsFailedMsg{}
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return ollamaModelsFailedMsg{}
		}

		var parsed ollamaTagsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return ollamaModelsFailedMsg{}
		}

		var models []string
		for _, model := range parsed.Models {
			models = append(models, model.Name)
		}
		if len(models) == 0 {
			return ollamaModelsFailedMsg{}
		}
		return ollamaModelsLoadedMsg(models)
	}
}

func (m *OnboardingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	// Forward messages to the connect model when in stateConnect.
	if m.state == stateConnect && m.connectModel != nil {
		switch typed := msg.(type) {
		case tea.KeyPressMsg:
			ks := typed.String()
			if ks == "esc" {
				// Esc during connect: return to project name step.
				m.state = stateProjectName
				m.connectModel = nil
				m.textInput.SetValue("")
				m.textInput.Placeholder = filepath.Base(m.workingDir)
				m.textInput.Focus()
				return m, nil
			}
			if ks == "ctrl+c" {
				m.cancelled = true
				return m, tea.Quit
			}
		case connect.DoneMsg:
			// Persist the provider/model from the connect wizard.
			m.selectedProvider = typed.Provider
			m.modelName = typed.Model
			m.baseURL = typed.ProviderCfg.BaseURL
			if typed.ProviderCfg.APIKey != "" {
				m.keyMode = keyModeInline
				m.keySecret = typed.ProviderCfg.APIKey
				m.apiKey = marshalGlobalAPIKey
			} else if typed.ProviderCfg.APIKeyEnv != "" {
				m.keyMode = keyModeEnvName
				m.apiKey = typed.ProviderCfg.APIKeyEnv
			}
			m.connectModel = nil
			if err := m.saveConfig(); err != nil {
				m.err = fmt.Sprintf("Failed to write config: %v", err)
				return m, nil
			}
			m.state = stateDone
			return m, tea.Quit
		case connect.CancelledMsg:
			// User cancelled the connect wizard; return to project name.
			m.state = stateProjectName
			m.connectModel = nil
			m.textInput.SetValue("")
			m.textInput.Placeholder = filepath.Base(m.workingDir)
			m.textInput.Focus()
			return m, nil
		}
		cm, ccmd := m.connectModel.Update(msg)
		m.connectModel = cm
		return m, ccmd
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit

		case "up":
			if m.state == stateSelectProvider {
				m.providerIndex--
				if m.providerIndex < 0 {
					m.providerIndex = len(m.providers) - 1
				}
				m.selectedProvider = m.providers[m.providerIndex]
			} else if m.state == stateKeyMode {
				if m.keyMode == keyModeInline {
					m.keyMode = keyModeEnvName
				} else {
					m.keyMode = keyModeInline
				}
			} else if m.state == stateModelSelection && len(m.ollamaModels) > 0 {
				m.ollamaIndex--
				if m.ollamaIndex < 0 {
					m.ollamaIndex = len(m.ollamaModels) - 1
				}
			}

		case "down":
			if m.state == stateSelectProvider {
				m.providerIndex++
				if m.providerIndex >= len(m.providers) {
					m.providerIndex = 0
				}
				m.selectedProvider = m.providers[m.providerIndex]
			} else if m.state == stateKeyMode {
				if m.keyMode == keyModeEnvName {
					m.keyMode = keyModeInline
				} else {
					m.keyMode = keyModeEnvName
				}
			} else if m.state == stateModelSelection && len(m.ollamaModels) > 0 {
				m.ollamaIndex++
				if m.ollamaIndex >= len(m.ollamaModels) {
					m.ollamaIndex = 0
				}
			}

		case "enter":
			m.err = ""
			switch m.state {
			case stateSelectProvider:
				m.attempts++
				m.state = stateProjectName
				m.textInput.SetValue("")
				m.textInput.Placeholder = filepath.Base(m.workingDir)
				m.projectNameTouched = false
				m.textInput.Focus()

			case stateProjectName:
				name := strings.TrimSpace(m.textInput.Value())
				if name == "" {
					if m.projectNameTouched {
						m.err = "Project name cannot be empty"
						return m, nil
					}
					name = filepath.Base(m.workingDir)
				}
				m.projectName = name
				// Transition to the connect wizard for provider/model setup.
				m.state = stateConnect
				m.connectModel = connect.New(connect.Opts{
					Cfg:     config.Default(),
					CfgPath: filepath.Join(m.workingDir, ".marshal", "config.toml"),
				})
				return m, m.connectModel.Init()

			case stateConfigureURL:
				m.baseURL = strings.TrimSpace(m.textInput.Value())
				if m.baseURL == "" {
					m.baseURL = "http://localhost:11434/v1"
				}
				m.state = stateModelSelection
				m.loading = true
				m.textInput.SetValue("")
				return m, fetchOllamaModels(m.baseURL)

			case stateKeyMode:
				// keyMode is already set by up/down; proceed to input.
				m.state = stateConfigureKey
				m.textInput.SetValue("")
				if m.keyMode == keyModeEnvName {
					m.textInput.Placeholder = "Env var name (e.g. OPENAI_API_KEY)"
					m.textInput.EchoMode = textinput.EchoPassword
				} else {
					m.textInput.Placeholder = "Paste API key (masked)"
					m.textInput.EchoMode = textinput.EchoPassword
				}
				m.textInput.Focus()

			case stateConfigureKey:
				val := strings.TrimSpace(m.textInput.Value())
				if val == "" {
					m.err = "Value cannot be empty"
					return m, nil
				}
				if m.keyMode == keyModeEnvName {
					m.apiKey = val
				} else {
					m.keySecret = val
					m.apiKey = marshalGlobalAPIKey
				}
				m.state = stateModelSelection
				m.textInput.SetValue("")
				if m.selectedProvider == "OpenAI" {
					m.textInput.Placeholder = "gpt-4o"
				} else {
					m.textInput.Placeholder = "anthropic/claude-3.5-sonnet"
				}

			case stateModelSelection:
				if len(m.ollamaModels) > 0 {
					m.modelName = m.ollamaModels[m.ollamaIndex]
				} else {
					m.modelName = strings.TrimSpace(m.textInput.Value())
					if m.modelName == "" {
						m.modelName = m.textInput.Placeholder
					}
				}

				// Final Save Configuration
				if err := m.saveConfig(); err != nil {
					m.err = fmt.Sprintf("Failed to write config: %v", err)
					return m, nil
				}
				m.state = stateDone
				return m, tea.Quit
			}
		}

	case ollamaModelsLoadedMsg:
		m.loading = false
		m.ollamaModels = msg
		m.ollamaIndex = 0

	case ollamaModelsFailedMsg:
		m.loading = false
		m.ollamaErr = true
		m.textInput.Placeholder = "qwen2.5-coder:7b"
		m.textInput.Focus()
	}

	if m.state == stateProjectName || m.state == stateConfigureURL || m.state == stateConfigureKey || (m.state == stateModelSelection && len(m.ollamaModels) == 0 && !m.loading) {
		oldVal := m.textInput.Value()
		m.textInput, cmd = m.textInput.Update(msg)
		if m.state == stateProjectName && m.textInput.Value() != oldVal {
			m.projectNameTouched = true
		}
	}

	return m, cmd
}

func (m *OnboardingModel) saveConfig() error {
	dir := filepath.Join(m.workingDir, ".marshal")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var tomlContent strings.Builder
	projectName := m.projectName
	if projectName == "" {
		projectName = filepath.Base(m.workingDir)
	}
	tomlContent.WriteString("[project]\n")
	tomlContent.WriteString(fmt.Sprintf("name = %q\n\n", projectName))

	tomlContent.WriteString("[commands]\n")
	tomlContent.WriteString("test = \"go test ./...\"\n\n")

	tomlContent.WriteString("[profile]\n")
	tomlContent.WriteString("default = \"onboarded\"\n\n")

	providerKey := strings.ToLower(strings.Fields(m.selectedProvider)[0])

	if providerKey == "ollama" {
		tomlContent.WriteString(fmt.Sprintf("[providers.%s]\n", providerKey))
		tomlContent.WriteString("type = \"openai_compatible\"\n")
		tomlContent.WriteString(fmt.Sprintf("base_url = %q\n", m.baseURL))
		tomlContent.WriteString("api_key = \"ollama\"\n\n")
	} else {
		switch m.keyMode {
		case keyModeEnvName:
			tomlContent.WriteString(fmt.Sprintf("[providers.%s]\n", providerKey))
			tomlContent.WriteString("type = \"openai_compatible\"\n")
			tomlContent.WriteString(fmt.Sprintf("api_key_env = %q\n\n", m.apiKey))
		case keyModeInline:
			// Persist the raw key to the GLOBAL config only.
			if err := writeGlobalProviderAPIKey(m.globalConfigPath, providerKey, m.keySecret); err != nil {
				return err
			}
			// Deliberately do NOT write a [providers.<name>] block in the
			// project config. The global config has the real api_key; the
			// loader's whole-entry merge (config.go:850-861) preserves the
			// global entry when the project file has no entry for this
			// provider name. See F-UIUX-137.
			tomlContent.WriteString("# Provider config (api_key) lives in the global config\n")
			tomlContent.WriteString("# (~/.config/marshal/config.toml) to keep secrets out of\n")
			tomlContent.WriteString("# this project-tracked file. See F-UIUX-137.\n\n")
		default:
			tomlContent.WriteString(fmt.Sprintf("[providers.%s]\n", providerKey))
			tomlContent.WriteString("type = \"openai_compatible\"\n")
			tomlContent.WriteString("# api_key_env = \"OPENAI_API_KEY\"\n\n")
		}
	}

	tomlContent.WriteString("[models.presets.onboarded_preset]\n")
	tomlContent.WriteString(fmt.Sprintf("provider = %q\n", providerKey))
	tomlContent.WriteString(fmt.Sprintf("model = %q\n", m.modelName))
	if providerKey == "ollama" {
		tomlContent.WriteString("local_only = true\n\n")
	} else {
		tomlContent.WriteString("local_only = false\n\n")
	}

	tomlContent.WriteString("[agent_profiles.onboarded]\n")
	for _, role := range routing.AllRoles {
		tomlContent.WriteString(fmt.Sprintf("%s = \"onboarded_preset\"\n", role))
	}
	tomlContent.WriteString("\n")

	tomlContent.WriteString("[agent]\n")
	tomlContent.WriteString("max_tool_iterations = 32\n")

	return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tomlContent.String()), 0644)
}

// writeGlobalProviderAPIKey writes the raw API key to the user's global
// config file under [providers.<name>].api_key.
// If path is empty, it defaults to ~/.config/marshal/config.toml.
// If the file does not exist, it creates it with a minimal header.
// TODO: The constant MARSHAL_GLOBAL_API_KEY should be documented in
// docs/03-config-and-policy.md (out of scope for this plan).
func writeGlobalProviderAPIKey(path, providerName, key string) error {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("find home dir: %w", err)
		}
		path = config.UserConfigPath(home)
	}
	return config.SaveUserConfigProviderAPIKey(path, providerName, key)
}

func (m *OnboardingModel) View() tea.View {
	return tea.NewView(m.viewString())
}

func (m *OnboardingModel) viewString() string {
	accentColor := lipgloss.Color("38")
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(accentColor).Bold(true).Padding(0, 1)
	header := titleStyle.Render(" MARSHAL ONBOARDING WIZARD ")

	var body strings.Builder
	body.WriteString(header + "\n\n")

	switch m.state {
	case stateSelectProvider:
		body.WriteString("Select your LLM Provider:\n\n")
		for i, prov := range m.providers {
			cursor := " "
			if i == m.providerIndex {
				cursor = ">"
				body.WriteString(lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(fmt.Sprintf("%s %d. %s", cursor, i+1, prov)) + "\n")
			} else {
				body.WriteString(fmt.Sprintf("%s %d. %s\n", cursor, i+1, prov))
			}
		}

	case stateProjectName:
		body.WriteString("Enter a name for this project:\n\n")
		body.WriteString(m.textInput.View() + "\n")

	case stateConfigureURL:
		body.WriteString("Enter Ollama Base URL:\n\n")
		body.WriteString(m.textInput.View() + "\n")

	case stateKeyMode:
		body.WriteString(fmt.Sprintf("API Key Source for %s:\n\n", m.selectedProvider))
		options := []struct {
			kind keyModeKind
			text string
		}{
			{keyModeEnvName, "Use env var (recommended)"},
			{keyModeInline, "Paste key inline (writes to global config)"},
		}
		for _, opt := range options {
			cursor := " "
			if opt.kind == m.keyMode {
				cursor = ">"
				body.WriteString(lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(fmt.Sprintf("%s %s", cursor, opt.text)) + "\n")
			} else {
				body.WriteString(fmt.Sprintf("%s %s\n", cursor, opt.text))
			}
		}
		body.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("Use up/down to select, Enter to confirm") + "\n")

	case stateConfigureKey:
		if m.keyMode == keyModeInline {
			body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("⚠ WARNING: this value will only be written to your global ~/.config/marshal/config.toml, never to a project file.") + "\n\n")
		}
		body.WriteString(fmt.Sprintf("Enter API Key / Environment Variable Name for %s:\n\n", m.selectedProvider))
		body.WriteString(m.textInput.View() + "\n")

	case stateModelSelection:
		if m.loading {
			body.WriteString("Connecting to Ollama to list available models...\n")
		} else if len(m.ollamaModels) > 0 {
			body.WriteString("Select an Ollama model:\n\n")
			for i, modelName := range m.ollamaModels {
				cursor := " "
				if i == m.ollamaIndex {
					cursor = ">"
					body.WriteString(lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(fmt.Sprintf("%s %s", cursor, modelName)) + "\n")
				} else {
					body.WriteString(fmt.Sprintf("%s %s\n", cursor, modelName))
				}
			}
		} else {
			body.WriteString("Enter Model Name (preset default: " + m.textInput.Placeholder + "):\n\n")
			body.WriteString(m.textInput.View() + "\n")
		}

	case stateDone:
		body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("71")).Render("Onboarding Complete!") + "\n")
		body.WriteString("Created `.marshal/config.toml` successfully.\n")
	}

	if m.err != "" {
		body.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Render("Error: "+m.err) + "\n")
	}

	body.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("Press Enter to continue • Esc / Ctrl+C to quit") + "\n")

	return body.String()
}
