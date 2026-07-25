package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ollamaEmbedder talks to Ollama's native /api/embed endpoint, which accepts
// an array input and returns embeddings in input order.
type ollamaEmbedder struct {
	client  *http.Client
	baseURL string
	apiKey  string
	model   string
	dims    int
}

func newOllamaEmbedder(baseURL, apiKey, model string) *ollamaEmbedder {
	return &ollamaEmbedder{
		client:  &http.Client{Timeout: 60 * time.Second},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
	}
}

func (e *ollamaEmbedder) Model() string { return e.model }
func (e *ollamaEmbedder) Dims() int     { return e.dims }

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (e *ollamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	out := make([][]float32, 0, len(texts))
	for _, batch := range splitBatches(texts, defaultBatchSize) {
		var vecs [][]float32
		err := withRetry(ctx, func() error {
			v, e2 := e.embedOnce(ctx, batch)
			vecs = v
			return e2
		})
		if err != nil {
			return nil, err
		}
		dims, err := checkDims(vecs, e.dims)
		if err != nil {
			return nil, err
		}
		e.dims = dims
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *ollamaEmbedder) embedOnce(ctx context.Context, batch []string) ([][]float32, error) {
	body, err := json.Marshal(ollamaEmbedRequest{Model: e.model, Input: batch})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, retryable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, retryable(fmt.Errorf("embed: status %d", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: status %d", resp.StatusCode)
	}
	var decoded ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Embeddings) != len(batch) {
		return nil, fmt.Errorf("embed: got %d vectors for %d inputs", len(decoded.Embeddings), len(batch))
	}
	return decoded.Embeddings, nil
}
