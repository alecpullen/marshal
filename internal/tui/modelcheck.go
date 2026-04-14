// internal/tui/modelcheck.go
// Startup model-readiness check.
//
// For each configured agent role (executor, critic, marshal, planner) we verify
// that the specified model is actually available:
//
//   - ollama:  GET /api/tags → confirm the model name appears in the list.
//             If missing, suggest ":pull <model>".
//   - cloud (fireworks, openai, …):
//             GET /models (or /v1/models) with Bearer auth → 2xx means the key
//             is accepted; we also check if the model ID appears in the response.
//             No API key → reported as "unconfigured key".
//
// Results arrive as a modelReadyMsg and are emitted as log lines in the main panel.

package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Types ─────────────────────────────���────────────────────��──────────────────

type readyStatus int

const (
	readyOK           readyStatus = iota // model confirmed available
	readyFail                            // server reachable but model not found / auth failed
	readyUnreachable                     // server not responding
	readyUnconfigured                    // missing URL / model / key
)

type modelResult struct {
	role    string
	model   string
	status  readyStatus
	message string // human-readable detail shown in the log
}

// modelReadyMsg is delivered to Update() once all checks complete.
type modelReadyMsg []modelResult

// ── Check command ──────────────────────────────���──────────────────────────────

// doModelReadyCheck launches per-role model checks concurrently.
func doModelReadyCheck(cfg *config.Config) tea.Cmd {
	if cfg == nil {
		return nil
	}
	return func() tea.Msg {
		type roleCheck struct {
			role     string
			agentCfg config.AgentConfig
		}

		// Build the list of roles to check. Marshal falls back to Executor if not
		// explicitly configured, matching the runtime behaviour.
		roles := []roleCheck{
			{"executor", cfg.Executor},
			{"critic", cfg.Critic},
			{"marshal", cfg.GetMarshalConfig()},
		}
		if cfg.Planner.Model != "" {
			roles = append(roles, roleCheck{"planner", cfg.Planner})
		}

		results := make(chan modelResult, len(roles))
		for _, r := range roles {
			r := r
			go func() {
				results <- checkOneModel(r.role, r.agentCfg)
			}()
		}

		out := make(modelReadyMsg, 0, len(roles))
		for range roles {
			out = append(out, <-results)
		}
		// Sort by canonical role order for predictable log output.
		order := map[string]int{"executor": 0, "critic": 1, "marshal": 2, "planner": 3}
		for i := 0; i < len(out)-1; i++ {
			for j := i + 1; j < len(out); j++ {
				if order[out[i].role] > order[out[j].role] {
					out[i], out[j] = out[j], out[i]
				}
			}
		}
		return out
	}
}

// checkOneModel performs the appropriate check for a single agent role.
func checkOneModel(role string, ac config.AgentConfig) modelResult {
	base := strings.TrimRight(ac.BaseURL, "/")
	model := ac.Model
	provider := ac.GetProvider()

	if base == "" || model == "" {
		return modelResult{
			role:    role,
			model:   model,
			status:  readyUnconfigured,
			message: "base_url or model not set",
		}
	}

	client := &http.Client{Timeout: 6 * time.Second}

	if strings.EqualFold(provider, "ollama") {
		return checkOllamaModel(client, role, base, model)
	}
	return checkCloudModel(client, role, base, model, ac.APIKey)
}

// checkOllamaModel queries /api/tags and verifies the model is in the local library.
func checkOllamaModel(client *http.Client, role, base, model string) modelResult {
	url := base + "/api/tags"
	resp, err := client.Get(url)
	if err != nil {
		return modelResult{
			role:    role,
			model:   model,
			status:  readyUnreachable,
			message: fmt.Sprintf("ollama unreachable (%v)", trimErr(err)),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return modelResult{
			role:    role,
			model:   model,
			status:  readyUnreachable,
			message: fmt.Sprintf("ollama /api/tags returned %d", resp.StatusCode),
		}
	}

	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return modelResult{role: role, model: model, status: readyFail,
			message: "could not parse /api/tags response"}
	}

	// Normalize: Ollama names are like "qwen2.5-coder:7b"; match on exact name or
	// the part before any colon (family match) as a fallback.
	wantName := strings.ToLower(model)
	wantFamily := strings.SplitN(wantName, ":", 2)[0]
	for _, m := range payload.Models {
		got := strings.ToLower(m.Name)
		if got == wantName || strings.SplitN(got, ":", 2)[0] == wantFamily {
			return modelResult{role: role, model: model, status: readyOK,
				message: "available in local Ollama library"}
		}
	}

	return modelResult{
		role:    role,
		model:   model,
		status:  readyFail,
		message: fmt.Sprintf("model not found — run: :pull %s", model),
	}
}

// checkCloudModel hits GET {base}/models with the Bearer token.
// A 2xx means the key is valid; we also scan the response for the model ID.
func checkCloudModel(client *http.Client, role, base, model, apiKey string) modelResult {
	if apiKey == "" {
		return modelResult{
			role:    role,
			model:   model,
			status:  readyUnconfigured,
			message: "api_key not set",
		}
	}

	// OpenAI-compatible /models endpoint.
	url := base + "/models"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return modelResult{role: role, model: model, status: readyFail,
			message: "could not build request"}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return modelResult{
			role:    role,
			model:   model,
			status:  readyUnreachable,
			message: fmt.Sprintf("unreachable (%v)", trimErr(err)),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return modelResult{role: role, model: model, status: readyFail,
			message: "API key rejected (401/403)"}
	}
	if resp.StatusCode >= 500 {
		return modelResult{role: role, model: model, status: readyUnreachable,
			message: fmt.Sprintf("server error %d", resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Unexpected non-2xx that isn't auth/server — treat as unconfigured.
		return modelResult{role: role, model: model, status: readyFail,
			message: fmt.Sprintf("unexpected status %d from /models", resp.StatusCode)}
	}

	// Parse the model list and check if the configured model is present.
	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Data) == 0 {
		// Couldn't parse or empty list — key works, model existence unknown.
		return modelResult{role: role, model: model, status: readyOK,
			message: "API key accepted (model list unavailable)"}
	}

	wantID := strings.ToLower(model)
	for _, m := range payload.Data {
		if strings.ToLower(m.ID) == wantID {
			return modelResult{role: role, model: model, status: readyOK,
				message: "model confirmed in provider catalog"}
		}
	}

	return modelResult{
		role:    role,
		model:   model,
		status:  readyFail,
		message: fmt.Sprintf("model %q not found in provider catalog", model),
	}
}

// ── Log-line helpers ─────────────────────────────��────────────────────────────

// modelResultLines converts a slice of results into LogLine values for the main panel.
func modelResultLines(results modelReadyMsg) []LogLine {
	lines := []LogLine{
		{Kind: lineSystem, Content: "── model readiness ─────────────────────────"},
	}
	for _, r := range results {
		lines = append(lines, modelResultLine(r))
	}
	return lines
}

func modelResultLine(r modelResult) LogLine {
	label := fmt.Sprintf("  %-9s %s", r.role, r.model)
	switch r.status {
	case readyOK:
		return LogLine{Kind: lineSuccess, Content: fmt.Sprintf("%s  ✓  %s", label, r.message)}
	case readyFail:
		return LogLine{Kind: lineError, Content: fmt.Sprintf("%s  ✗  %s", label, r.message)}
	case readyUnreachable:
		return LogLine{Kind: lineError, Content: fmt.Sprintf("%s  ✗  %s", label, r.message)}
	default: // readyUnconfigured
		return LogLine{Kind: lineWarning, Content: fmt.Sprintf("%s  ⚠  %s", label, r.message)}
	}
}

// trimErr shortens error strings for display.
func trimErr(err error) string {
	s := err.Error()
	// Strip noisy URL scaffolding from net/url errors.
	if i := strings.LastIndex(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}
