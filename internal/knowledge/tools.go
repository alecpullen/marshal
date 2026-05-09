package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alecpullen/marshal/internal/agent"
	agttools "github.com/alecpullen/marshal/internal/agent/tools"
	"github.com/alecpullen/marshal/pkg/protocol"
)

// CtxFetchTool provides exact context retrieval (Layer A).
type CtxFetchTool struct {
	tier *KnowledgeTier
}

// NewCtxFetchTool creates a new ctx_fetch tool.
func NewCtxFetchTool(tier *KnowledgeTier) *CtxFetchTool {
	return &CtxFetchTool{tier: tier}
}

func (t *CtxFetchTool) Name() string        { return "ctx_fetch" }
func (t *CtxFetchTool) Description() string { return "Retrieve exact context by reference key" }
func (t *CtxFetchTool) IsReadOperation() bool { return true }
func (t *CtxFetchTool) IsMutating() bool    { return false }
func (t *CtxFetchTool) RequiresReadBeforeEdit() bool { return false }

func (t *CtxFetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"ref": {"type": "string", "description": "Context reference (e.g., files/main.go@sha256:abc123)"}
		},
		"required": ["ref"]
	}`)
}

func (t *CtxFetchTool) Invoke(ctx context.Context, args json.RawMessage) (*agttools.Result, error) {
	var params struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	ref, err := protocol.ParseContextRef(params.Ref)
	if err != nil {
		return nil, fmt.Errorf("invalid ref: %w", err)
	}

	entry, err := t.tier.Fetch(ctx, ref)
	if err != nil {
		return nil, err
	}

	return &agttools.Result{
		Content:     string(entry.Content),
		ContextRef:  ref,
		ReadHash:    entry.ContentHash,
		ContentSize: len(entry.Content),
		ReadPath:    entry.Key.Path(),
	}, nil
}

// CtxListTool provides structural listing (Layer A).
type CtxListTool struct {
	tier *KnowledgeTier
}

// NewCtxListTool creates a new ctx_list tool.
func NewCtxListTool(tier *KnowledgeTier) *CtxListTool {
	return &CtxListTool{tier: tier}
}

func (t *CtxListTool) Name() string        { return "ctx_list" }
func (t *CtxListTool) Description() string { return "List context entries by tags, kinds, or path prefix" }
func (t *CtxListTool) IsReadOperation() bool { return true }
func (t *CtxListTool) IsMutating() bool    { return false }
func (t *CtxListTool) RequiresReadBeforeEdit() bool { return false }

func (t *CtxListTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"kinds": {"type": "array", "items": {"type": "string"}, "description": "Entry kinds (files, outputs, etc.)"},
			"tags": {"type": "array", "items": {"type": "string"}, "description": "Tags to filter by"},
			"path_prefix": {"type": "string", "description": "Path prefix filter"},
			"limit": {"type": "integer", "default": 10}
		}
	}`)
}

func (t *CtxListTool) Invoke(ctx context.Context, args json.RawMessage) (*agttools.Result, error) {
	var params struct {
		Kinds      []string `json:"kinds"`
		Tags       []string `json:"tags"`
		PathPrefix string   `json:"path_prefix"`
		Limit      int      `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}
	if params.Limit == 0 {
		params.Limit = 10
	}

	// Convert string kinds to EntryKind
	var kinds []protocol.EntryKind
	for _, k := range params.Kinds {
		kinds = append(kinds, protocol.EntryKind(k))
	}

	query := protocol.ListQuery{
		Kinds:      kinds,
		Tags:       params.Tags,
		PathPrefix: params.PathPrefix,
		Limit:      params.Limit,
		LatestOnly: true,
	}

	entries, err := t.tier.List(ctx, query)
	if err != nil {
		return nil, err
	}

	// Format as readable list
	var content strings.Builder
	for _, e := range entries {
		content.WriteString(fmt.Sprintf("- %s (%d tokens, %s)\n", e.Key, e.SizeTokens, e.Ref))
	}

	data, _ := json.Marshal(entries)
	return &agttools.Result{
		Content: content.String(),
		Data:    data,
	}, nil
}

// CtxSearchTool provides BM25 search (Layer B).
type CtxSearchTool struct {
	tier *KnowledgeTier
}

// NewCtxSearchTool creates a new ctx_search tool.
func NewCtxSearchTool(tier *KnowledgeTier) *CtxSearchTool {
	return &CtxSearchTool{tier: tier}
}

func (t *CtxSearchTool) Name() string        { return "ctx_search" }
func (t *CtxSearchTool) Description() string { return "Search context entries using BM25 full-text search" }
func (t *CtxSearchTool) IsReadOperation() bool { return true }
func (t *CtxSearchTool) IsMutating() bool    { return false }
func (t *CtxSearchTool) RequiresReadBeforeEdit() bool { return false }

func (t *CtxSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"mode": {"type": "string", "enum": ["exact", "fuzzy"], "default": "exact"},
			"scope": {"type": "string", "description": "Scope filter (backend, frontend, docs, tests, all)"},
			"limit": {"type": "integer", "default": 10}
		},
		"required": ["query"]
	}`)
}

func (t *CtxSearchTool) Invoke(ctx context.Context, args json.RawMessage) (*agttools.Result, error) {
	var params struct {
		Query string `json:"query"`
		Mode  string `json:"mode"`
		Scope string `json:"scope"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	if params.Mode == "" {
		params.Mode = "exact"
	}
	if params.Limit == 0 {
		params.Limit = 10
	}

	mode := SearchMode(params.Mode)
	if !mode.IsValid() {
		return nil, fmt.Errorf("invalid search mode: %s", params.Mode)
	}

	results, err := t.tier.Search(ctx, params.Query, mode, params.Scope)
	if err != nil {
		return nil, err
	}

	content := FormatResults(results, params.Limit)
	data, _ := json.Marshal(results)

	return &agttools.Result{
		Content: content,
		Data:    data,
	}, nil
}

// QueryKnowledgeTool provides natural-language queries (Layer C).
type QueryKnowledgeTool struct {
	knowledgeAgent *KnowledgeAgent
}

// NewQueryKnowledgeTool creates a new query_knowledge tool.
func NewQueryKnowledgeTool(ka *KnowledgeAgent) *QueryKnowledgeTool {
	return &QueryKnowledgeTool{knowledgeAgent: ka}
}

func (t *QueryKnowledgeTool) Name() string        { return "query_knowledge" }
func (t *QueryKnowledgeTool) Description() string { return "Ask a natural-language question to the knowledge system" }
func (t *QueryKnowledgeTool) IsReadOperation() bool { return true }
func (t *QueryKnowledgeTool) IsMutating() bool    { return false }
func (t *QueryKnowledgeTool) RequiresReadBeforeEdit() bool { return false }

func (t *QueryKnowledgeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"question": {"type": "string", "description": "The question to answer"},
			"scope": {"type": "string", "enum": ["backend", "frontend", "docs", "tests", "all", "auto"], "default": "auto"}
		},
		"required": ["question"]
	}`)
}

func (t *QueryKnowledgeTool) Invoke(ctx context.Context, args json.RawMessage) (*agttools.Result, error) {
	var params struct {
		Question string `json:"question"`
		Scope    string `json:"scope"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	// Run knowledge agent
	answer, err := t.knowledgeAgent.Run(ctx, params.Question, params.Scope)
	if err != nil {
		return nil, err
	}

	// Format answer with citations (short refs + inline links + full refs section)
	content := formatAnswerWithCitations(answer)
	data, _ := json.Marshal(answer)

	return &agttools.Result{
		Content: content,
		Data:    data,
	}, nil
}

// KnowledgeAgentFactory creates knowledge agents for tools.
type KnowledgeAgentFactory struct {
	agentFactory  func(manifest string) (*agent.Agent, error)
	tier          *KnowledgeTier
	cache         *QueryCache
}

// NewKnowledgeAgentFactory creates a factory for knowledge agents.
func NewKnowledgeAgentFactory(agentFactory func(manifest string) (*agent.Agent, error), tier *KnowledgeTier, cache *QueryCache) *KnowledgeAgentFactory {
	return &KnowledgeAgentFactory{
		agentFactory: agentFactory,
		tier:         tier,
		cache:        cache,
	}
}

// CreateKnowledgeAgent creates a new knowledge agent instance.
func (f *KnowledgeAgentFactory) CreateKnowledgeAgent() (*KnowledgeAgent, error) {
	inner, err := f.agentFactory("knowledge")
	if err != nil {
		return nil, fmt.Errorf("creating inner agent: %w", err)
	}

	runtime := agent.NewRuntime(inner)
	return NewKnowledgeAgent(runtime, f.tier, f.cache), nil
}

func formatAnswerWithCitations(answer *KnowledgeAnswer) string {
	var content strings.Builder

	content.WriteString(fmt.Sprintf("**Answer:** %s\n\n", answer.Answer))
	content.WriteString(fmt.Sprintf("**Confidence:** %s\n", answer.Confidence))

	if len(answer.Citations) > 0 {
		content.WriteString("\n**Citations (Short):**\n")
		for _, ref := range answer.Citations {
			short := formatShortRef(ref)
			content.WriteString(fmt.Sprintf("- [%s](%s)\n", short, ref))
		}

		content.WriteString("\n**Full References:**\n")
		for _, ref := range answer.Citations {
			content.WriteString(fmt.Sprintf("- `%s`\n", ref))
		}
	}

	if len(answer.Followups) > 0 {
		content.WriteString("\n**Suggested Follow-ups:**\n")
		for _, q := range answer.Followups {
			content.WriteString(fmt.Sprintf("- %s\n", q))
		}
	}

	return content.String()
}

// RegisterKnowledgeTools registers all knowledge tools with a registry.
func RegisterKnowledgeTools(registry interface{ Register(tool agttools.Tool) }, tier *KnowledgeTier, knowledgeAgent *KnowledgeAgent) {
	registry.Register(NewCtxFetchTool(tier))
	registry.Register(NewCtxListTool(tier))
	registry.Register(NewCtxSearchTool(tier))
	registry.Register(NewQueryKnowledgeTool(knowledgeAgent))
}
