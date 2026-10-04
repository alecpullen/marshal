package acp

import (
	"context"
	"encoding/json"

	"marshal/internal/viewmodel"
)

// StepDiffJSON is one step's diff for the review page's by-step view.
type StepDiffJSON struct {
	StepNode string   `json:"stepNode"`
	TurnNode string   `json:"turnNode"`
	TaskNode string   `json:"taskNode,omitempty"`
	Headline string   `json:"headline"`
	At       int64    `json:"at,omitempty"`
	Files    []string `json:"files"`
	Diff     string   `json:"diff"`
}

// StepDiffs handles session/step_diffs.
func (m *TurnManager) StepDiffs(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if err := decodeParams(params, &p, "session/step_diffs"); err != nil {
		return nil, err
	}
	_, _, tree, err := m.treeFor(p.SessionID, 0)
	if err != nil {
		return nil, err
	}
	steps := []StepDiffJSON{}
	for _, d := range viewmodel.StepDiffs(tree) {
		files := d.Files
		if files == nil {
			files = []string{}
		}
		steps = append(steps, StepDiffJSON{StepNode: d.StepNode, TurnNode: d.TurnNode, TaskNode: d.TaskNode,
			Headline: d.Headline, At: ms(d.At), Files: files, Diff: d.Diff})
	}
	return map[string]any{"steps": steps}, nil
}
