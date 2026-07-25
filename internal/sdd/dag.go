package sdd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LoadDAG reads <ws.Dir()>/dag.json if it exists; otherwise returns an empty
// DAG (no error). Callers that need to distinguish "never written" from
// "written empty" can check len(dag.Tasks) == 0 and the workspace's Resume().
func LoadDAG(ws *Workspace) (*DAG, error) {
	path := filepath.Join(ws.Dir(), "dag.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &DAG{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sdd dag: read: %w", err)
	}
	var dag DAG
	if err := json.Unmarshal(data, &dag); err != nil {
		return nil, fmt.Errorf("sdd dag: parse: %w", err)
	}
	return &dag, nil
}

// Save writes the DAG as indented JSON to <ws.Dir()>/dag.json with 0644
// perms. Ensures the workspace exists first.
func (d *DAG) Save(ws *Workspace) error {
	if _, err := ws.Ensure(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("sdd dag: marshal: %w", err)
	}
	path := filepath.Join(ws.Dir(), "dag.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("sdd dag: write: %w", err)
	}
	return nil
}

// SetTaskStatus mutates the named task's Status field. Returns an error if
// no task with that ID exists. This is the only allowed Status mutator in P2
// so P3 (which will add Progress logging here) has a single chokepoint.
func (d *DAG) SetTaskStatus(taskID string, status TaskStatus) error {
	for i := range d.Tasks {
		if d.Tasks[i].ID == taskID {
			d.Tasks[i].Status = status
			return nil
		}
	}
	return fmt.Errorf("sdd dag: unknown task %q", taskID)
}

// ReadyTasks returns the pending tasks whose Deps are all satisfied
// (every dep id appears in the merged-set, computed inline from
// Status == TaskMerged). Tasks in any other state (in_progress, blocked,
// merged) are not returned. Stable order: input order.
func (d *DAG) ReadyTasks() []DAGTask {
	merged := make(map[string]bool, len(d.Tasks))
	for _, t := range d.Tasks {
		if t.Status == TaskMerged {
			merged[t.ID] = true
		}
	}
	var out []DAGTask
	for _, t := range d.Tasks {
		if t.Status != TaskPending {
			continue
		}
		allOK := true
		for _, dep := range t.Deps {
			if !merged[dep] {
				allOK = false
				break
			}
		}
		if allOK {
			out = append(out, t)
		}
	}
	return out
}

// TaskByID is a linear-scan lookup helper. The DAG is small (one entry per
// plan task, typically <100), so O(n) is fine. Returns (task, true) on hit
// and (zero, false) on miss.
func (d *DAG) TaskByID(id string) (DAGTask, bool) {
	for _, t := range d.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return DAGTask{}, false
}
