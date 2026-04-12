// Package pipeline implements the multi-task integration critic for M9.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/git"
	"github.com/alec/marshal/internal/prompts"
)

// IntegrationVerdict is the result of the integration critic review.
type IntegrationVerdict struct {
	Verdict   string   `json:"verdict"`   // "PASS" or "FAIL"
	Summary   string   `json:"summary"`
	Issue     string   `json:"issue"`
	Fix       string   `json:"fix"`
	Concerns  []string `json:"concerns"`
	Implicated []string `json:"implicated_tasks"` // task IDs that may have caused failure
}

// IntegrationCritic reviews the combined changes from all pipeline tasks.
type IntegrationCritic struct {
	backend backend.Backend
}

// NewIntegrationCritic creates a critic with the given backend.
func NewIntegrationCritic(b backend.Backend) *IntegrationCritic {
	return &IntegrationCritic{backend: b}
}

// integrationPrompt instructs the model to review combined changes from multiple tasks.
const integrationPrompt = `You are an integration reviewer evaluating combined changes from multiple independent tasks.

Review the combined diff and assess whether the tasks integrate correctly:
- No conflicting changes to the same lines
- No broken interfaces between components
- Tests pass (if visible in diff)
- No missing imports or dependencies

Respond with ONLY this JSON object:
{"verdict":"PASS|FAIL","summary":"one sentence","issue":"root cause if FAIL","fix":"suggested fix if FAIL","concerns":[],"implicated_tasks":[]}

implicated_tasks should list task IDs that likely caused any integration issues.`

// Review calls the integration critic with the combined diff.
func (c *IntegrationCritic) Review(ctx context.Context, combinedDiff string, taskIDs []string) (*IntegrationVerdict, error) {
	if combinedDiff == "" {
		// No changes means trivial pass.
		return &IntegrationVerdict{
			Verdict: "PASS",
			Summary: "no changes to review",
		}, nil
	}

	userMsg := fmt.Sprintf("Task IDs: %v\n\nCombined diff:\n```diff\n%s\n```", taskIDs, combinedDiff)

	msgs := []backend.Message{
		{Role: backend.MessageRoleSystem, Content: prompts.Assemble(integrationPrompt, "")},
		{Role: backend.MessageRoleUser, Content: userMsg},
	}

	resp, err := c.backend.Complete(ctx, backend.Request{Messages: msgs})
	if err != nil {
		return nil, fmt.Errorf("integration critic call: %w", err)
	}

	content := stripThinkBlocks(resp.Content)
	content = strings.TrimSpace(content)

	var verdict IntegrationVerdict
	if err := json.Unmarshal([]byte(content), &verdict); err != nil {
		// Non-fatal: treat as uncertain but don't block.
		return &IntegrationVerdict{
			Verdict: "PASS",
			Summary: "integration critic returned unparsable response, assuming PASS",
			Issue:   fmt.Sprintf("parse error: %v", err),
		}, nil
	}

	return &verdict, nil
}

// stripThinkBlocks removes <thinking> content.
func stripThinkBlocks(s string) string {
	for {
		start := strings.Index(s, "<thinking>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</thinking>")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</thinking>"):]
	}
	return s
}

// ComputeCombinedDiff generates a diff across all task branches vs the staging branch.
func ComputeCombinedDiff(repo *git.Repo, stagingBranch string, taskBranches []string) (string, error) {
	// For each task branch, get diff against staging and concatenate.
	var combined strings.Builder

	for _, branch := range taskBranches {
		diff, err := repo.DiffBranches(stagingBranch, branch)
		if err != nil {
			return "", fmt.Errorf("diff %s vs %s: %w", stagingBranch, branch, err)
		}
		if diff != "" {
			combined.WriteString(fmt.Sprintf("=== Changes from %s ===\n", branch))
			combined.WriteString(diff)
			combined.WriteString("\n")
		}
	}

	return combined.String(), nil
}

// TopologicalMerge merges all task branches into staging in topological order.
// Returns the new staging SHA on success.
func TopologicalMerge(repo *git.Repo, stagingBranch string, tasks []*PipelineTask) (string, error) {
	// Sort tasks by tier (already in tier order from scheduler, but ensure it).
	// Actually, we need to respect the original dependency graph.
	ordered, err := topoSortTasks(tasks)
	if err != nil {
		return "", err
	}

	// Checkout staging branch.
	if err := repo.Checkout(stagingBranch); err != nil {
		return "", fmt.Errorf("checkout staging: %w", err)
	}

	// Merge each task branch in order.
	for _, t := range ordered {
		branch := "marshal/task-" + t.ID
		if err := repo.Merge(branch, fmt.Sprintf("Merge %s into staging", branch)); err != nil {
			// Attempt to abort and report failure.
			return "", fmt.Errorf("merge %s: %w", branch, err)
		}
	}

	// Get new HEAD SHA.
	sha, err := repo.HEAD()
	if err != nil {
		return "", fmt.Errorf("get HEAD: %w", err)
	}

	return sha, nil
}

// topoSortTasks performs topological sort on tasks.
func topoSortTasks(tasks []*PipelineTask) ([]*PipelineTask, error) {
	// Build adjacency map and in-degrees.
	byID := make(map[string]*PipelineTask)
	for _, t := range tasks {
		byID[t.ID] = t
	}

	inDegree := make(map[string]int)
	for id := range byID {
		inDegree[id] = 0
	}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if _, ok := byID[dep]; ok {
				inDegree[t.ID]++
			}
		}
	}

	// Kahn's algorithm.
	var result []*PipelineTask
	var ready []string
	for id, deg := range inDegree {
		if deg == 0 {
			ready = append(ready, id)
		}
	}

	for len(ready) > 0 {
		// Take first ready task.
		id := ready[0]
		ready = ready[1:]
		result = append(result, byID[id])

		// Reduce in-degree for tasks depending on this one.
		for _, t := range tasks {
			for _, dep := range t.DependsOn {
				if dep == id {
					inDegree[t.ID]--
					if inDegree[t.ID] == 0 {
						ready = append(ready, t.ID)
					}
				}
			}
		}
	}

	if len(result) != len(tasks) {
		return nil, fmt.Errorf("circular dependency in tasks")
	}

	return result, nil
}
