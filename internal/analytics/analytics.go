// Package analytics provides opt-in analytics tracking for marshal.
// It logs command names and token spend only - never prompts or file contents.
// Compatible with PostHog and other analytics providers.
package analytics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/alecpullen/marshal/internal/config"
)

// Event represents a single analytics event.
type Event struct {
	Event      string                 `json:"event"`
	DistinctID string                 `json:"distinct_id,omitempty"`
	Timestamp  time.Time              `json:"timestamp"`
	Properties map[string]interface{} `json:"properties"`
}

// Client represents an analytics client.
type Client struct {
	enabled    bool
	apiKey     string
	baseURL    string
	distinctID string
	client     *http.Client
	queue      chan Event
	stopChan   chan struct{}
	version    string
}

// NewClient creates a new analytics client.
// Analytics is disabled by default unless explicitly enabled via config.
func NewClient(cfg *config.Config, version string) *Client {
	// Check if analytics is enabled via environment variable or config
	enabled := os.Getenv("MARSHAL_ANALYTICS") == "1" ||
		os.Getenv("MARSHAL_ANALYTICS") == "true"

	// Get API key from environment (PostHog or custom)
	apiKey := os.Getenv("MARSHAL_ANALYTICS_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("POSTHOG_API_KEY")
	}

	// Get base URL
	baseURL := os.Getenv("MARSHAL_ANALYTICS_URL")
	if baseURL == "" {
		baseURL = os.Getenv("POSTHOG_HOST")
		if baseURL == "" {
			baseURL = "https://app.posthog.com"
		}
	}

	// Generate distinct ID from machine info (hashed)
	distinctID := generateDistinctID()

	client := &Client{
		enabled:    enabled && apiKey != "",
		apiKey:     apiKey,
		baseURL:    baseURL,
		distinctID: distinctID,
		client:     &http.Client{Timeout: 10 * time.Second},
		queue:      make(chan Event, 100),
		stopChan:   make(chan struct{}),
		version:    version,
	}

	if client.enabled {
		go client.processQueue()
	}

	return client
}

// IsEnabled returns true if analytics is enabled.
func (c *Client) IsEnabled() bool {
	return c.enabled
}

// TrackCommand tracks a command execution.
func (c *Client) TrackCommand(command string, args ...string) {
	if !c.enabled {
		return
	}

	// Only track command name, never arguments which may contain sensitive data
	c.trackEvent("command_executed", map[string]interface{}{
		"command":    command,
		"args_count": len(args),
	})
}

// TrackTask tracks a task execution with token usage.
func (c *Client) TrackTask(prompt string, model string, promptTokens, completionTokens int, duration time.Duration, passed bool) {
	if !c.enabled {
		return
	}

	// Track token usage but NOT the prompt content
	c.trackEvent("task_completed", map[string]interface{}{
		"model":             model,
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
		"duration_ms":       duration.Milliseconds(),
		"passed":            passed,
		// Track prompt length but not content
		"prompt_length": len(prompt),
	})
}

// TrackSession tracks session lifecycle.
func (c *Client) TrackSession(event string, sessionID string, taskCount int) {
	if !c.enabled {
		return
	}

	c.trackEvent("session_"+event, map[string]interface{}{
		"task_count": taskCount,
	})
}

// TrackError tracks an error (without sensitive details).
func (c *Client) TrackError(errorType string) {
	if !c.enabled {
		return
	}

	c.trackEvent("error", map[string]interface{}{
		"error_type": errorType,
	})
}

// trackEvent adds an event to the queue.
func (c *Client) trackEvent(eventName string, properties map[string]interface{}) {
	// Add common properties
	if properties == nil {
		properties = make(map[string]interface{})
	}
	properties["version"] = c.version
	properties["os"] = runtime.GOOS
	properties["arch"] = runtime.GOARCH

	event := Event{
		Event:      eventName,
		DistinctID: c.distinctID,
		Timestamp:  time.Now().UTC(),
		Properties: properties,
	}

	select {
	case c.queue <- event:
		// Event queued successfully
	default:
		// Queue is full, drop the event
	}
}

// processQueue processes analytics events in the background.
func (c *Client) processQueue() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var batch []Event

	for {
		select {
		case event := <-c.queue:
			batch = append(batch, event)

			// Send if batch is full
			if len(batch) >= 10 {
				c.sendBatch(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			// Send any pending events
			if len(batch) > 0 {
				c.sendBatch(batch)
				batch = batch[:0]
			}

		case <-c.stopChan:
			// Send remaining events before stopping
			if len(batch) > 0 {
				c.sendBatch(batch)
			}
			return
		}
	}
}

// sendBatch sends a batch of events to the analytics API.
func (c *Client) sendBatch(events []Event) {
	if len(events) == 0 {
		return
	}

	// Format for PostHog capture API
	payload := map[string]interface{}{
		"api_key": c.apiKey,
		"batch":   events,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return
	}

	url := c.baseURL + "/capture"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")

	// Send asynchronously - don't block on analytics
	go func() {
		resp, err := c.client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
	}()
}

// Close shuts down the analytics client.
func (c *Client) Close() error {
	if !c.enabled {
		return nil
	}

	close(c.stopChan)
	return nil
}

// generateDistinctID generates a unique but anonymous ID for this machine.
func generateDistinctID() string {
	// Use hostname + username hash for a stable but anonymous ID
	hostname, _ := os.Hostname()
	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("USERNAME")
	}

	// Simple hash of machine identifiers
	data := hostname + "-" + username
	hash := 0
	for i, c := range data {
		hash = (hash + int(c)*(i+1)) % 1000000007
	}

	return fmt.Sprintf("anon-%x", hash)
}

// Config represents analytics configuration.
type Config struct {
	Enabled  bool   `toml:"enabled"`
	Provider string `toml:"provider"` // "posthog" or "custom"
	APIKey   string `toml:"api_key"`
	BaseURL  string `toml:"base_url"`
}

// DefaultConfig returns the default (disabled) analytics config.
func DefaultConfig() Config {
	return Config{
		Enabled:  false,
		Provider: "posthog",
	}
}
