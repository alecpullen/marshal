package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// openAIEmbedder talks to an OpenAI-compatible /v1/embeddings endpoint. This
// path also works against Ollama's /v1 endpoint, LM Studio, and llama.cpp
// servers.
type openAIEmbedder struct {
	client  *http.Client
	baseURL string
	apiKey  string
	model   string
	dims    int
}

func newOpenAIEmbedder(baseURL, apiKey, model string) *openAIEmbedder {
	return &openAIEmbedder{
		client:  &http.Client{Timeout: 60 * time.Second},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
	}
}

func (e *openAIEmbedder) Model() string { return e.model }
func (e *openAIEmbedder) Dims() int     { return e.dims }

type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
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

func (e *openAIEmbedder) embedOnce(ctx context.Context, batch []string) ([][]float32, error) {
	body, err := json.Marshal(openAIEmbedRequest{Model: e.model, Input: batch})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
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
		return nil, retryable(fmt.Errorf("embeddings: status %d", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: status %d", resp.StatusCode)
	}
	var decoded openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Data) != len(batch) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(decoded.Data), len(batch))
	}
	sort.Slice(decoded.Data, func(i, j int) bool { return decoded.Data[i].Index < decoded.Data[j].Index })
	vecs := make([][]float32, len(decoded.Data))
	for i, d := range decoded.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}
