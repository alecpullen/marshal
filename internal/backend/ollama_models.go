package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ListModelsResponse from /api/tags endpoint
type ListModelsResponse struct {
	Models []OllamaModel `json:"models"`
}

// OllamaModel represents a model available in the local Ollama instance
type OllamaModel struct {
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	ModifiedAt time.Time `json:"modified_at"`
	Size       int64     `json:"size"`
	Digest     string    `json:"digest"`
	Details    struct {
		Format            string   `json:"format"`
		Family            string   `json:"family"`
		Families          []string `json:"families"`
		ParameterSize     string   `json:"parameter_size"`
		QuantizationLevel string   `json:"quantization_level"`
	} `json:"details"`
}

// PullModelRequest for /api/pull endpoint
type PullModelRequest struct {
	Name   string `json:"name"`
	Stream bool   `json:"stream"`
}

// PullModelProgress from streaming pull response
type PullModelProgress struct {
	Status    string `json:"status"`    // "downloading", "verifying", "complete"
	Completed int64  `json:"completed"` // bytes downloaded
	Total     int64  `json:"total"`     // total bytes
}

// ListModels fetches available models from the Ollama instance
func (b *OllamaBackend) ListModels(ctx context.Context) ([]OllamaModel, error) {
	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		b.baseURL+"/api/tags",
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var result ListModelsResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Models, nil
}

// PullModel downloads a model from Ollama Hub with progress callbacks
func (b *OllamaBackend) PullModel(ctx context.Context, name string, onProgress func(PullModelProgress)) error {
	reqBody := PullModelRequest{
		Name:   name,
		Stream: true,
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		b.baseURL+"/api/pull",
		io.NopCloser(bytes.NewReader(bodyJSON)),
	)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	// Parse NDJSON stream
	decoder := json.NewDecoder(resp.Body)
	for {
		var progress PullModelProgress
		if err := decoder.Decode(&progress); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decode progress: %w", err)
		}

		if onProgress != nil {
			onProgress(progress)
		}

		if progress.Status == "complete" {
			break
		}
	}

	return nil
}

// HasModel checks if a specific model is available locally
func (b *OllamaBackend) HasModel(ctx context.Context, name string) (bool, error) {
	models, err := b.ListModels(ctx)
	if err != nil {
		return false, err
	}

	for _, m := range models {
		if m.Name == name {
			return true, nil
		}
	}

	return false, nil
}

// FormatModelInfo returns a human-readable description of the model
func (m OllamaModel) FormatModelInfo() string {
	paramSize := m.Details.ParameterSize
	if paramSize == "" {
		paramSize = "unknown"
	}

	quant := m.Details.QuantizationLevel
	if quant == "" {
		quant = "unknown"
	}

	sizeStr := formatBytes(m.Size)

	return fmt.Sprintf("%s (%s, %s, %s)", m.Name, paramSize, quant, sizeStr)
}

// formatBytes converts bytes to human-readable string
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
