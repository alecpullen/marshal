package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"marshal/internal/llm/schema"
)

// ResolveThinkingOptions keeps capability policy out of the rendering layer.
// Explicit configuration wins; discovery wins over conservative fallbacks.
// nil means unknown, and an empty Levels list means known unsupported.
func ResolveThinkingOptions(providerType, model string, override *schema.ThinkingOptions, models []schema.ModelInfo) *schema.ThinkingOptions {
	if override != nil {
		return cloneThinkingOptions(override)
	}
	for _, info := range models {
		if info.ID == model && info.Thinking != nil {
			return cloneThinkingOptions(info.Thinking)
		}
	}
	// The Codex transport always uses OpenAI's effort control. Without catalog
	// metadata, only offer the established three levels, never guess off support.
	if providerType == "openai_codex" {
		return &schema.ThinkingOptions{Levels: []string{"low", "medium", "high"}, Mode: "effort"}
	}
	return nil
}

func cloneThinkingOptions(options *schema.ThinkingOptions) *schema.ThinkingOptions {
	if options == nil {
		return nil
	}
	copy := *options
	copy.Levels = nil
	seen := map[string]bool{"": true, "default": true}
	for _, level := range options.Levels {
		if !seen[level] {
			copy.Levels = append(copy.Levels, level)
			seen[level] = true
		}
	}
	return &copy
}

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

// PreserveThinkingOptions retains cached metadata when a model's enrichment
// fails. Explicit empty controls are authoritative and are never replaced.
func PreserveThinkingOptions(models, cached []schema.ModelInfo) []schema.ModelInfo {
	out := append([]schema.ModelInfo(nil), models...)
	for i := range out {
		if out[i].Thinking != nil {
			continue
		}
		for _, previous := range cached {
			if previous.ID == out[i].ID && previous.Thinking != nil {
				out[i].Thinking = cloneThinkingOptions(previous.Thinking)
				break
			}
		}
	}
	return out
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
