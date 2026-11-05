package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/schema"
)

// ollamaThinkingMetadata retains booleans as well as model-defined names.
type ollamaThinkingMetadata struct {
	Values  []json.RawMessage `json:"values"`
	Default json.RawMessage   `json:"default"`
}

func (m *ollamaThinkingMetadata) options() *schema.ThinkingOptions {
	options := &schema.ThinkingOptions{Mode: "effort"}
	for _, value := range m.Values {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		var name string
		if json.Unmarshal(value, &name) == nil && name != "" {
			options.Levels = append(options.Levels, name)
			continue
		}
		var enabled bool
		if json.Unmarshal(value, &enabled) == nil {
			options.Mode = "toggle"
			if enabled {
				options.Levels = append(options.Levels, "on")
			} else {
				options.Levels = append(options.Levels, "off")
			}
		}
	}
	// A model advertising false alone has no thinking control to offer.
	if len(options.Levels) == 1 && options.Levels[0] == "off" {
		options.Levels = nil
	}
	if len(m.Default) == 0 || bytes.Equal(bytes.TrimSpace(m.Default), []byte("null")) {
		return options
	}
	var name string
	if json.Unmarshal(m.Default, &name) == nil {
		options.Default = name
	} else {
		var enabled bool
		if json.Unmarshal(m.Default, &enabled) == nil {
			if enabled {
				options.Default = "on"
			} else {
				options.Default = "off"
			}
		}
	}
	return options
}

func isOllamaEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return u.Hostname() == "ollama.com" || u.Port() == "11434"
}

// Compatible Ollama endpoints expose native metadata outside /v1. A failed
// enrichment never prevents the model list from being returned.
func (p *OpenAICompatible) ollamaThinkingOptions(ctx context.Context, model string) *schema.ThinkingOptions {
	body, _ := json.Marshal(map[string]string{"model": model})
	endpoint := strings.TrimSuffix(p.baseURL, "/v1") + "/api/show"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	p.setHeaders(req)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var parsed ollamaShowResponse
	if json.NewDecoder(resp.Body).Decode(&parsed) != nil {
		return nil
	}
	if parsed.Thinking != nil {
		return parsed.Thinking.options()
	}
	if parsed.Capabilities != nil {
		for _, capability := range parsed.Capabilities {
			if capability == "thinking" {
				return nil
			}
		}
		return &schema.ThinkingOptions{Mode: "toggle"}
	}
	return nil
}

// enrichThinkingOptions limits concurrent /api/show requests and bounds each
// request. Models remain usable if capability discovery is unavailable.
func enrichThinkingOptions(ctx context.Context, models []schema.ModelInfo, fetch func(context.Context, string) *schema.ThinkingOptions) {
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(4, len(models)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				models[i].Thinking = fetch(probeCtx, models[i].ID)
				cancel()
			}
		}()
	}
	for i := range models {
		if models[i].Thinking == nil {
			jobs <- i
		}
	}
	close(jobs)
	workers.Wait()
}

func openAIThinkingEffort(req schema.ChatRequest) string {
	effort := req.Thinking
	if effort == "default" {
		return ""
	}
	// A discovered named value is already a wire value. Preserve it exactly.
	if req.ThinkingOptions != nil && req.ThinkingOptions.Mode == "effort" {
		for _, level := range req.ThinkingOptions.Levels {
			if effort == level {
				return effort
			}
		}
	}
	switch effort {
	case "off":
		return "none"
	case "on":
		return "high"
	}
	return effort
}

// ThinkingOptionsFor resolves the effort options for one provider/model
// pair from the model cache, the same source the model panel uses.
func ThinkingOptionsFor(dataDir, providerName string, pc config.ProviderConfig, model string, override *schema.ThinkingOptions) *schema.ThinkingOptions {
	var models []schema.ModelInfo
	if dataDir != "" {
		if cached, ok := modelcache.Load(dataDir).Lookup(providerName, pc, modelcache.DefaultTTL, time.Now()); ok {
			models = cached
		}
	}
	return schema.ResolveThinkingOptions(pc.Type, model, override, models)
}

// ValidEffortLevel reports whether level may be requested for a model with
// opts. "" (clear) is always valid. Unknown options (nil) accept any level,
// matching preset thinking today; otherwise the level must be listed,
// including "off".
func ValidEffortLevel(opts *schema.ThinkingOptions, level string) bool {
	if level == "" || opts == nil {
		return true
	}
	return slices.Contains(opts.Levels, level)
}
