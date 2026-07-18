package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"marshal/test/usability/actor"
	"marshal/test/usability/screen"
)

// Config for the LLM actor.
type Config struct {
	BaseURL       string
	Model         string
	MaxIterations int
}

// Client is the LLM completion interface.
type Client interface {
	Complete(ctx context.Context, system, prompt string) (string, error)
}

// LLM is an actor driven by an LLM.
type LLM struct {
	cfg    Config
	client Client
	goal   string
	iter   int
	lastN  []string
}

// New creates an LLM actor. If client is nil, an OllamaClient is constructed from cfg.
func New(cfg Config, client Client) *LLM {
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("USABILITY_LLM_URL")
		if cfg.BaseURL == "" {
			cfg.BaseURL = "http://localhost:11434"
		}
	}
	if cfg.Model == "" {
		cfg.Model = os.Getenv("USABILITY_LLM_MODEL")
		if cfg.Model == "" {
			cfg.Model = "qwen2.5-coder:14b"
		}
	}
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 30
	}
	if client == nil {
		client = &OllamaClient{BaseURL: cfg.BaseURL, Model: cfg.Model}
	}
	return &LLM{cfg: cfg, client: client}
}

// WithGoal sets the task goal before running.
func (l *LLM) WithGoal(goal string) *LLM {
	l.goal = goal
	return l
}

// Act asks the LLM for the next input.
func (l *LLM) Act(ctx context.Context, s screen.Screen) (actor.Action, error) {
	if l.iter >= l.cfg.MaxIterations {
		return actor.Action{Type: actor.ActionDone, Success: false, Notes: "iteration limit reached"}, nil
	}
	l.iter++

	system := `You are a usability tester driving the Marshal terminal coding agent. You see the terminal screen and decide the next keystrokes. Respond with a single JSON object: {"action":"type","text":"..."}, {"action":"key","key":"..."} (enter, esc, y, n, etc.), or {"action":"done","success":true,"notes":"..."}. Be concise.`

	prompt := fmt.Sprintf("Goal: %s\nCurrent screen:\n%s\nPending approval: %v\nPending question: %v\nBusy: %v\nWhat is your next action?",
		l.goal, summarize(s), s.State.PendingApproval, s.State.PendingQuestion, s.State.Busy)

	raw, err := l.client.Complete(ctx, system, prompt)
	if err != nil {
		return actor.Action{}, err
	}
	return parseAction(raw)
}

func summarize(s screen.Screen) string {
	lines := s.Lines
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}

func parseAction(raw string) (actor.Action, error) {
	// Extract JSON if the model wrapped it in markdown.
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end <= start {
		return actor.Action{}, fmt.Errorf("no JSON action found in: %q", raw)
	}
	var ja struct {
		Action  string `json:"action"`
		Text    string `json:"text"`
		Key     string `json:"key"`
		Success bool   `json:"success"`
		Notes   string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &ja); err != nil {
		return actor.Action{}, fmt.Errorf("parse action: %w", err)
	}
	return actor.Action{
		Type:    ja.Action,
		Text:    ja.Text,
		Key:     ja.Key,
		Success: ja.Success,
		Notes:   ja.Notes,
	}, nil
}

// OllamaClient is a minimal Ollama chat client.
type OllamaClient struct {
	BaseURL string
	Model   string
	client  *http.Client
}

// Complete sends a chat request to Ollama and returns the assistant message.
func (o *OllamaClient) Complete(ctx context.Context, system, prompt string) (string, error) {
	if o.client == nil {
		o.client = http.DefaultClient
	}
	body := map[string]any{
		"model": o.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": prompt},
		},
		"stream": false,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", o.BaseURL+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama status %d", resp.StatusCode)
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Message.Content, nil
}
