package sdd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StateDump is the JSON the controller writes and the orchestrator reads at
// the start of each drain iteration. It is the orchestrator's memory across
// iterations (the orchestrator's conversation context resets each iteration).
type StateDump struct {
	PipelineBranch string     `json:"pipeline_branch"`
	PipelineHead   string     `json:"pipeline_head"`
	TargetBranch   string     `json:"target_branch"`
	Tasks          []TaskDump `json:"tasks"`
	Merged         []string   `json:"merged"`
	LastBatchID    int        `json:"last_batch_id"`
}

// TaskDump is one task in the state-dump. Ready is precomputed by the
// controller (the orchestrator does not recompute deps-merged logic).
type TaskDump struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Deps   []string `json:"deps"`
	Ready  bool     `json:"ready"`
}

// WriteStateDump builds a StateDump from the DAG + RepoState, writes it as
// JSON to <ws.Dir()>/state-dump.json, and returns the path.
func WriteStateDump(ws *Workspace, dag *DAG, rs *RepoState, lastBatchID int) (string, error) {
	if _, err := ws.Ensure(); err != nil {
		return "", err
	}
	ready := dag.ReadyTasks()
	readySet := make(map[string]bool, len(ready))
	for _, t := range ready {
		readySet[t.ID] = true
	}
	sd := StateDump{
		PipelineBranch: rs.Branch,
		PipelineHead:   rs.Head,
		TargetBranch:   rs.TargetBranch,
		Merged:         rs.Merged,
		LastBatchID:    lastBatchID,
	}
	for _, t := range dag.Tasks {
		sd.Tasks = append(sd.Tasks, TaskDump{
			ID:     t.ID,
			Title:  t.Title,
			Status: string(t.Status),
			Deps:   t.Deps,
			Ready:  readySet[t.ID],
		})
	}
	data, err := json.MarshalIndent(sd, "", "  ")
	if err != nil {
		return "", fmt.Errorf("sdd state-dump: marshal: %w", err)
	}
	path := filepath.Join(ws.Dir(), "state-dump.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("sdd state-dump: write: %w", err)
	}
	return path, nil
}

// ReadStateDump reads + parses the state-dump JSON from the given path.
func ReadStateDump(path string) (*StateDump, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sdd state-dump: read: %w", err)
	}
	var sd StateDump
	if err := json.Unmarshal(data, &sd); err != nil {
		return nil, fmt.Errorf("sdd state-dump: parse: %w", err)
	}
	return &sd, nil
}
