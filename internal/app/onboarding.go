package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/app/config"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type onboardingState int

const (
	stateSelectProvider onboardingState = iota
	stateConfigureURL
	stateKeyMode
	stateConfigureKey
	stateModelSelection
	stateDone
)

type keyModeKind int

const (
	keyModeUnset   keyModeKind = iota
	keyModeEnvName
	keyModeInline
)

const (
	marshalGlobalAPIKey = "MARSHAL_GLOBAL_API_KEY"
)

type OnboardingModel struct {
	state      onboardingState
	workingDir string

	// Step 1: Provider selection
	providers     []string
	providerIndex int

	// Step 2/3: Input fields
	textInput textinput.Model
	loading   bool
	err       string

	// Collected inputs
	selectedProvider string
	baseURL          string
	apiKey           string
	modelName        string

	// API key mode (F-UIUX-137)
	keyMode   keyModeKind // keyModeEnvName | keyModeInline | keyModeUnset
	keySecret string      // when keyMode == inline, the value the user typed

	// Ollama dynamic models
	ollamaModels []string
	ollamaIndex  int
	ollamaErr    bool
}

type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func NewOnboardingModel(workingDir string) *OnboardingModel {
	ti := textinput.New()
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
		if err != nil {
			// Try without "/v1" if the user entered standard API URL
			reqURL = strings.Replace(reqURL, "/v1/tags", "/api/tags", 1)
			resp, err = client.Get(reqURL)
			if err != nil {
				return ollamaModelsFailedMsg{}
			}
		}
		defer resp.Body.Close()
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

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
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
				if m.selectedProvider == "Ollama (Local)" {
					m.state = stateConfigureURL
					m.textInput.Placeholder = "http://localhost:11434/v1"
					m.textInput.SetValue("http://localhost:11434/v1")
				} else {
					m.state = stateKeyMode
					m.keyMode = keyModeEnvName // default
				}
				m.textInput.Focus()

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
					m.textInput.EchoMode = textinput.EchoNormal
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

	if m.state == stateConfigureURL || m.state == stateConfigureKey || (m.state == stateModelSelection && len(m.ollamaModels) == 0 && !m.loading) {
		m.textInput, cmd = m.textInput.Update(msg)
	}

	return m, cmd
}

func (m *OnboardingModel) saveConfig() error {
	dir := filepath.Join(m.workingDir, ".marshal")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var tomlContent strings.Builder
	tomlContent.WriteString("[project]\n")
	tomlContent.WriteString("name = \"my-project\"\n\n")

	tomlContent.WriteString("[commands]\n")
	tomlContent.WriteString("test = \"go test ./...\"\n\n")

	tomlContent.WriteString("[profile]\n")
	tomlContent.WriteString("default = \"onboarded\"\n\n")

	providerKey := strings.ToLower(strings.Fields(m.selectedProvider)[0])

	tomlContent.WriteString(fmt.Sprintf("[providers.%s]\n", providerKey))
	tomlContent.WriteString("type = \"openai_compatible\"\n")
	if providerKey == "ollama" {
		tomlContent.WriteString(fmt.Sprintf("base_url = %q\n", m.baseURL))
		tomlContent.WriteString("api_key = \"ollama\"\n\n")
	} else {
		switch m.keyMode {
		case keyModeEnvName:
			tomlContent.WriteString(fmt.Sprintf("api_key_env = %q\n\n", m.apiKey))
		case keyModeInline:
			// Persist the raw key to the GLOBAL config only.
			if err := writeGlobalProviderAPIKey(providerKey, m.keySecret); err != nil {
				return err
			}
			// Project config records only the env-var-style reference.
			tomlContent.WriteString(fmt.Sprintf("api_key_env = %q\n\n", marshalGlobalAPIKey))
		default:
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
	for _, role := range []string{"router", "knowledge", "summarizer", "repo_scout", "tester", "planner", "implementer", "reviewer", "security_reviewer"} {
		tomlContent.WriteString(fmt.Sprintf("%s = \"onboarded_preset\"\n", role))
	}
	tomlContent.WriteString("\n")

	tomlContent.WriteString("[agent]\n")
	tomlContent.WriteString("max_tool_iterations = 32\n")

	return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tomlContent.String()), 0644)
}

// writeGlobalProviderAPIKey writes the raw API key to the user's global
// config file (~/.config/marshal/config.toml) under [providers.<name>].api_key.
// If the file does not exist, it creates it with a minimal header.
// TODO: The constant MARSHAL_GLOBAL_API_KEY should be documented in
// docs/03-config-and-policy.md (out of scope for this plan).
func writeGlobalProviderAPIKey(providerName, key string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home dir: %w", err)
	}
	path := filepath.Join(home, ".config", "marshal", "config.toml")
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
