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

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type onboardingState int

const (
	stateSelectProvider onboardingState = iota
	stateConfigureURL
	stateConfigureKey
	stateModelSelection
	stateDone
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
					m.state = stateConfigureKey
					m.textInput.Placeholder = "API Key or Env Var name (e.g. OPENROUTER_API_KEY)"
					m.textInput.SetValue("")
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

			case stateConfigureKey:
				m.apiKey = strings.TrimSpace(m.textInput.Value())
				if m.apiKey == "" {
					m.err = "API Key cannot be empty"
					return m, nil
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
	} else if strings.Contains(m.apiKey, "_") {
		// Env var
		tomlContent.WriteString(fmt.Sprintf("api_key_env = %q\n\n", m.apiKey))
	} else {
		// Raw key
		tomlContent.WriteString(fmt.Sprintf("api_key = %q\n\n", m.apiKey))
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

	case stateConfigureKey:
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
