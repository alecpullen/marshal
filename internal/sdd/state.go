package sdd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// LoadRepoState reads <ws.RepoPath()> if it exists; otherwise returns an
// empty RepoState (no error). Same convention as LoadDAG.
func LoadRepoState(ws *Workspace) (*RepoState, error) {
	data, err := os.ReadFile(ws.RepoPath())
	if os.IsNotExist(err) {
		return &RepoState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sdd state: read: %w", err)
	}
	var rs RepoState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("sdd state: parse: %w", err)
	}
	return &rs, nil
}

// Save writes RepoState as indented JSON to <ws.RepoPath()> with 0644 perms.
// Ensures the workspace exists first.
func (rs *RepoState) Save(ws *Workspace) error {
	if _, err := ws.Ensure(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return fmt.Errorf("sdd state: marshal: %w", err)
	}
	if err := os.WriteFile(ws.RepoPath(), data, 0644); err != nil {
		return fmt.Errorf("sdd state: write: %w", err)
	}
	return nil
}

// MarkMerged records that a task has been merged into the pipeline branch.
// Idempotent: a second call with the same taskID does not duplicate the
// entry but does refresh LastMergeAt. This is the only mutator for Merged
// and MergedAt in P2.
func (rs *RepoState) MarkMerged(taskID string) error {
	if rs.MergedAt == nil {
		rs.MergedAt = map[string]string{}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rs.MergedAt[taskID] = now
	rs.LastMergeAt = now
	for _, id := range rs.Merged {
		if id == taskID {
			return nil
		}
	}
	rs.Merged = append(rs.Merged, taskID)
	return nil
}

// Repair syncs Head and Merged against git:
//  1. resolve the pipeline branch's current SHA via GitOps.RevParse and set rs.Head
//  2. drop any rs.Merged entries whose task branch (sdd/<id>) no longer exists
//  3. save the corrected state
//
// Returns an error if rs.Branch is empty.
func (rs *RepoState) Repair(ws *Workspace, git GitOps) error {
	if rs.Branch == "" {
		return fmt.Errorf("sdd state: branch not set")
	}
	head, err := git.RevParse(rs.Branch)
	if err != nil {
		return fmt.Errorf("sdd state: rev-parse %s: %w", rs.Branch, err)
	}
	rs.Head = head

	kept := rs.Merged[:0:0]
	for _, id := range rs.Merged {
		if git.BranchExists("sdd/" + id) {
			kept = append(kept, id)
		} else {
			delete(rs.MergedAt, id)
		}
	}
	rs.Merged = kept
	return rs.Save(ws)
}
