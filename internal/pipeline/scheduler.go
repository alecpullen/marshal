// Package pipeline implements the multi-task DAG execution for M9.
package pipeline

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"
)

// TaskState tracks the execution state of a pipeline task.
type TaskState int

const (
	TaskPending TaskState = iota
	TaskRunning
	TaskPassed
	TaskFailed
)

// PipelineTask wraps a planner.Task with runtime state.
type PipelineTask struct {
	ID          string
	Description string
	Files       []string
	DependsOn   []string

	State   TaskState
	Round   int
	MaxRounds int
	Err     error
}

// Scheduler manages the execution of pipeline tasks across tiers.
type Scheduler struct {
	tasks      map[string]*PipelineTask
	tiers      [][]*PipelineTask
	fileLocks  map[string]*sync.Mutex
}

// NewScheduler creates a scheduler from a list of tasks.
func NewScheduler(tasks []*PipelineTask) (*Scheduler, error) {
	s := &Scheduler{
		tasks:     make(map[string]*PipelineTask),
		fileLocks: make(map[string]*sync.Mutex),
	}

	for _, t := range tasks {
		s.tasks[t.ID] = t
	}

	// Build execution tiers via topological sort.
	tiers, err := s.buildTiers()
	if err != nil {
		return nil, err
	}
	s.tiers = tiers

	// Initialize file locks for all files mentioned.
	for _, t := range tasks {
		for _, f := range t.Files {
			if _, ok := s.fileLocks[f]; !ok {
				s.fileLocks[f] = &sync.Mutex{}
			}
		}
	}

	return s, nil
}

// Tiers returns the computed execution tiers.
func (s *Scheduler) Tiers() [][]*PipelineTask {
	return s.tiers
}

// buildTiers performs topological sort to produce execution tiers.
// Tasks in the same tier have no dependencies between them and can run in parallel
// (subject to file-lock serialization).
func (s *Scheduler) buildTiers() ([][]*PipelineTask, error) {
	// Calculate in-degree for each task.
	inDegree := make(map[string]int)
	for id := range s.tasks {
		inDegree[id] = 0
	}
	for _, t := range s.tasks {
		for _, dep := range t.DependsOn {
			if _, ok := s.tasks[dep]; !ok {
				return nil, fmt.Errorf("task %s depends on unknown task %s", t.ID, dep)
			}
			inDegree[t.ID]++
		}
	}

	// Kahn's algorithm for tier construction.
	var tiers [][]*PipelineTask
	remaining := len(s.tasks)
	visited := make(map[string]bool)

	for remaining > 0 {
		var tier []*PipelineTask
		for id, deg := range inDegree {
			if deg == 0 && !visited[id] {
				tier = append(tier, s.tasks[id])
			}
		}

		if len(tier) == 0 && remaining > 0 {
			return nil, fmt.Errorf("circular dependency detected or no tasks ready")
		}

		// Mark tier tasks as visited and reduce in-degrees.
		for _, t := range tier {
			visited[t.ID] = true
			remaining--
			for _, other := range s.tasks {
				for _, dep := range other.DependsOn {
					if dep == t.ID {
						inDegree[other.ID]--
					}
				}
			}
		}

		tiers = append(tiers, tier)
	}

	return tiers, nil
}

// Run executes all tasks tier by tier.
// The runTask function is called for each task and should handle the actual execution.
func (s *Scheduler) Run(ctx context.Context, runTask func(ctx context.Context, t *PipelineTask) error) error {
	for tierIdx, tier := range s.tiers {
		g, ctx := errgroup.WithContext(ctx)

		for _, t := range tier {
			t := t // capture for closure
			g.Go(func() error {
				// Acquire locks for all files this task touches.
				locks := s.acquireLocks(t.Files)
				defer s.releaseLocks(locks)

				t.State = TaskRunning
				err := runTask(ctx, t)
				if err != nil {
					t.State = TaskFailed
					t.Err = err
					return err // fail fast
				}
				t.State = TaskPassed
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return fmt.Errorf("tier %d failed: %w", tierIdx, err)
		}
	}

	return nil
}

// acquireLocks grabs all file locks needed for a task.
// To avoid deadlock, locks are sorted by filename before acquisition.
func (s *Scheduler) acquireLocks(files []string) []*sync.Mutex {
	// Deduplicate and sort to ensure consistent lock ordering.
	seen := make(map[string]bool)
	var unique []string
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			unique = append(unique, f)
		}
	}

	// Simple sort (not lexical, just consistent ordering)
	for i := 0; i < len(unique); i++ {
		for j := i + 1; j < len(unique); j++ {
			if unique[i] > unique[j] {
				unique[i], unique[j] = unique[j], unique[i]
			}
		}
	}

	var locks []*sync.Mutex
	for _, f := range unique {
		if lock, ok := s.fileLocks[f]; ok {
			lock.Lock()
			locks = append(locks, lock)
		}
	}
	return locks
}

func (s *Scheduler) releaseLocks(locks []*sync.Mutex) {
	// Release in reverse order.
	for i := len(locks) - 1; i >= 0; i-- {
		locks[i].Unlock()
	}
}

// AllPassed returns true if all tasks passed.
func (s *Scheduler) AllPassed() bool {
	for _, t := range s.tasks {
		if t.State != TaskPassed {
			return false
		}
	}
	return true
}

// FailedTasks returns all tasks that failed.
func (s *Scheduler) FailedTasks() []*PipelineTask {
	var failed []*PipelineTask
	for _, t := range s.tasks {
		if t.State == TaskFailed {
			failed = append(failed, t)
		}
	}
	return failed
}
