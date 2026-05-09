package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Detector probes the environment to find available model providers.
type Detector struct {
	timeout    time.Duration
	httpClient *http.Client
}

// DetectorOption configures the detector.
type DetectorOption func(*Detector)

// WithTimeout sets the probe timeout.
func WithTimeout(timeout time.Duration) DetectorOption {
	return func(d *Detector) {
		d.timeout = timeout
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) DetectorOption {
	return func(d *Detector) {
		d.httpClient = client
	}
}

// NewDetector creates a new provider detector.
func NewDetector(opts ...DetectorOption) *Detector {
	d := &Detector{
		timeout:    200 * time.Millisecond,
		httpClient: &http.Client{Timeout: 500 * time.Millisecond},
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

// DetectedProvider represents a detected provider.
type DetectedProvider struct {
	Name            string   // e.g., "anthropic", "ollama", "lmstudio"
	Endpoint        string   // Detected endpoint URL
	AuthAvailable   bool     // Whether API key is available
	AvailableModels []string // Models available (populated for local servers)
	Priority        int      // Auto-selection priority
}

// IsCloud returns true if this is a cloud provider.
func (p DetectedProvider) IsCloud() bool {
	switch p.Name {
	case "ollama", "lmstudio", "vllm":
		return false
	default:
		return true
	}
}

// Probe runs detection and returns all available providers.
func (d *Detector) Probe(ctx context.Context) []DetectedProvider {
	var wg sync.WaitGroup
	results := make(chan DetectedProvider, 8)

	// Probe cloud providers via env vars
	cloudProbes := []struct {
		name    string
		envVar  string
		priority int
	}{
		{"anthropic", "ANTHROPIC_API_KEY", 100},
		{"openai", "OPENAI_API_KEY", 80},
		{"openrouter", "OPENROUTER_API_KEY", 70},
		{"fireworks", "FIREWORKS_API_KEY", 60},
		{"runpod", "RUNPOD_API_KEY", 55},
	}

	for _, probe := range cloudProbes {
		wg.Add(1)
		go func(p struct {
			name    string
			envVar  string
			priority int
		}) {
			defer wg.Done()
			if d.detectCloudProvider(ctx, p.name, p.envVar, p.priority, results) {
				// Detected
			}
		}(probe)
	}

	// Probe local servers via HTTP
	localProbes := []struct {
		name     string
		endpoint string
		priority int
	}{
		{"ollama", "http://localhost:11434/v1/models", 50},
		{"lmstudio", "http://localhost:1234/v1/models", 50},
		{"vllm", "http://localhost:8000/v1/models", 50},
	}

	for _, probe := range localProbes {
		wg.Add(1)
		go func(p struct {
			name     string
			endpoint string
			priority int
		}) {
			defer wg.Done()
			if d.detectLocalProvider(ctx, p.name, p.endpoint, p.priority, results) {
				// Detected
			}
		}(probe)
	}

	// Wait for all probes and close channel
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var providers []DetectedProvider
	for provider := range results {
		providers = append(providers, provider)
	}

	// Sort by priority (highest first)
	sortByPriority(providers)

	return providers
}

// detectCloudProvider checks for cloud provider via environment variable.
func (d *Detector) detectCloudProvider(ctx context.Context, name, envVar string, priority int, results chan<- DetectedProvider) bool {
	apiKey := os.Getenv(envVar)
	if apiKey == "" {
		// Also try lowercase version
		apiKey = os.Getenv(strings.ToLower(envVar))
	}

	if apiKey == "" {
		return false
	}

	results <- DetectedProvider{
		Name:          name,
		Endpoint:      d.getDefaultEndpoint(name),
		AuthAvailable: true,
		Priority:      priority,
	}
	return true
}

// detectLocalProvider probes a local server endpoint.
func (d *Detector) detectLocalProvider(ctx context.Context, name, endpoint string, priority int, results chan<- DetectedProvider) bool {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return false
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	// Try to parse available models
	var modelList struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&modelList); err == nil {
		models := make([]string, 0, len(modelList.Data))
		for _, m := range modelList.Data {
			models = append(models, m.ID)
		}

		results <- DetectedProvider{
			Name:            name,
			Endpoint:        endpoint,
			AuthAvailable:   false, // Local providers don't need auth
			AvailableModels: models,
			Priority:        priority,
		}
		return true
	}

	// Endpoint responded but couldn't parse models
	results <- DetectedProvider{
		Name:          name,
		Endpoint:      endpoint,
		AuthAvailable: false,
		Priority:      priority,
	}
	return true
}

// getDefaultEndpoint returns the default endpoint for a provider.
func (d *Detector) getDefaultEndpoint(provider string) string {
	endpoints := map[string]string{
		"anthropic":  "https://api.anthropic.com/v1/messages",
		"openai":     "https://api.openai.com/v1/chat/completions",
		"openrouter": "https://openrouter.ai/api/v1/chat/completions",
		"fireworks":  "https://api.fireworks.ai/inference/v1/chat/completions",
		"runpod":     "https://api.runpod.io/v2/",
		"ollama":     "http://localhost:11434/v1/chat/completions",
		"lmstudio":   "http://localhost:1234/v1/chat/completions",
		"vllm":       "http://localhost:8000/v1/chat/completions",
	}

	if endpoint, ok := endpoints[provider]; ok {
		return endpoint
	}
	return ""
}

// sortByPriority sorts providers by priority (highest first).
func sortByPriority(providers []DetectedProvider) {
	for i := 0; i < len(providers)-1; i++ {
		for j := i + 1; j < len(providers); j++ {
			if providers[j].Priority > providers[i].Priority {
				providers[i], providers[j] = providers[j], providers[i]
			}
		}
	}
}

// ProbeResult is the combined result of provider detection.
type ProbeResult struct {
	Providers   []DetectedProvider
	CloudCount  int
	LocalCount  int
	HasFallback bool // Has at least one local provider for fallback
}

// Analyze provides a summary of detected providers.
func (d *Detector) Analyze(ctx context.Context) ProbeResult {
	providers := d.Probe(ctx)

	result := ProbeResult{
		Providers: providers,
	}

	for _, p := range providers {
		if p.IsCloud() {
			result.CloudCount++
		} else {
			result.LocalCount++
		}
	}

	result.HasFallback = result.LocalCount > 0

	return result
}

// GetRecommendedProfile suggests a profile based on detected providers.
func (d *Detector) GetRecommendedProfile(ctx context.Context) string {
	result := d.Analyze(ctx)

	// Priority-based selection
	hasAnthropic := false
	hasOpenAI := false
	hasLocal := false

	for _, p := range result.Providers {
		switch p.Name {
		case "anthropic":
			hasAnthropic = true
		case "openai", "openrouter":
			hasOpenAI = true
		case "ollama", "lmstudio", "vllm":
			hasLocal = true
		}
	}

	switch {
	case hasAnthropic && hasLocal:
		return "balanced"
	case hasAnthropic:
		return "quality"
	case hasOpenAI && hasLocal:
		return "balanced"
	case hasLocal:
		return "local-only"
	case hasOpenAI:
		return "budget"
	default:
		return ""
	}
}

// FormatDetectionResults formats detection results for display.
func FormatDetectionResults(result ProbeResult) string {
	if len(result.Providers) == 0 {
		return "No providers detected.\n\n" +
			"To get started:\n" +
			"  - Set ANTHROPIC_API_KEY for Claude (recommended)\n" +
			"  - Set OPENAI_API_KEY for GPT-4\n" +
			"  - Install Ollama for local models: https://ollama.com"
	}

	var parts []string
	parts = append(parts, "Detected providers:")

	for _, p := range result.Providers {
		status := "✓"
		if !p.AuthAvailable && p.IsCloud() {
			status = "✗ (no API key)"
		}

		line := fmt.Sprintf("  %s %s", status, p.Name)
		if len(p.AvailableModels) > 0 {
			line += fmt.Sprintf(" (%d models)", len(p.AvailableModels))
		}
		parts = append(parts, line)
	}

	parts = append(parts, "")
	if profile := getRecommendedProfileFromResult(result); profile != "" {
		parts = append(parts, fmt.Sprintf("Recommended profile: %s", profile))
	}

	return strings.Join(parts, "\n")
}

func getRecommendedProfileFromResult(result ProbeResult) string {
	hasAnthropic := false
	hasLocal := false
	for _, p := range result.Providers {
		if p.Name == "anthropic" {
			hasAnthropic = true
		}
		if !p.IsCloud() {
			hasLocal = true
		}
	}

	switch {
	case hasAnthropic && hasLocal:
		return "balanced"
	case hasAnthropic:
		return "quality"
	case hasLocal:
		return "local-only"
	default:
		return ""
	}
}
