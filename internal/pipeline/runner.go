// Package pipeline provides sequential and parallel pipeline execution for Marshal.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alecpullen/marshal/internal/agents/planner"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/marshal"
	"github.com/alecpullen/marshal/internal/store"
)

// Runner orchestrates pipeline execution.
type Runner struct {
	marshal *marshal.Marshal
	store   *store.Store
	git     git.Layer
	gitMu   sync.Mutex // serialises git ops (single shared working tree)
	progMu  sync.Mutex // serialises progress callbacks from goroutines
}

// RunResult contains the final outcome of a pipeline run.
type RunResult struct {
	PipelineID        int64
	Status            string // "DONE", "FAILED", "PARTIAL", "INTEGRATION_FAIL"
	TasksTotal        int
	TasksDone         int
	TasksFailed       int
	FailedTaskID      string
	IntegrationIssues []string // populated on INTEGRATION_FAIL
	Duration          time.Duration
}

// NewRunner creates a new pipeline runner.
func NewRunner(m *marshal.Marshal, s *store.Store, g git.Layer) *Runner {
	return &Runner{
		marshal: m,
		store:   s,
		git:     g,
	}
}

// completedTask tracks a successfully executed task and its branch.
type completedTask struct {
	TaskID string
	Branch string
	SHA    string
}

// callProgress emits a progress event, safe to call from any goroutine.
func (r *Runner) callProgress(cb ProgressCallback, event ProgressEvent) {
	r.progMu.Lock()
	defer r.progMu.Unlock()
	cb(event)
}

// gitLocked executes fn while holding the git mutex.
func (r *Runner) gitLocked(fn func() error) error {
	r.gitMu.Lock()
	defer r.gitMu.Unlock()
	return fn()
}

// partitionByFileConflict splits a tier into tasks that can run in parallel and
// tasks that must run serially due to overlapping FilesLikelyAffected.
// The first task that claims a file wins the parallel slot; subsequent tasks
// touching the same file are moved to the serial queue.
func partitionByFileConflict(tasks []planner.Task) (parallel []planner.Task, serial []planner.Task) {
	claimed := make(map[string]bool)
	for _, t := range tasks {
		hasConflict := false
		for _, f := range t.FilesLikelyAffected {
			if claimed[f] {
				hasConflict = true
				break
			}
		}
		if hasConflict {
			serial = append(serial, t)
		} else {
			for _, f := range t.FilesLikelyAffected {
				claimed[f] = true
			}
			parallel = append(parallel, t)
		}
	}
	return
}

// executeOneTask runs a single task on its own isolation branch.
// It handles branch creation, LLM execution, status updates, and cleanup on failure.
// Returns a completedTask on success, or an error on failure.
func (r *Runner) executeOneTask(
	ctx context.Context,
	pipelineID int64,
	task planner.Task,
	progress ProgressCallback,
	tasksTotal int,
	tasksDone *int,
	tasksDoneMu *sync.Mutex,
) (completedTask, error) {
	r.callProgress(progress, ProgressEvent{
		Type:    "task_start",
		TaskID:  task.ID,
		Message: task.Description,
	})

	// Update DB status
	if err := r.store.UpdatePipelineTaskStatus(pipelineID, task.ID, "RUNNING"); err != nil {
		return completedTask{}, fmt.Errorf("update task status: %w", err)
	}

	branch := fmt.Sprintf("marshal/pipeline-%d-task-%s", pipelineID, task.ID)

	// Create isolation branch (git op — serialised)
	if err := r.gitLocked(func() error { return r.git.CreateIsolationBranch(branch) }); err != nil {
		r.store.UpdatePipelineTaskStatus(pipelineID, task.ID, "FAILED")
		r.callProgress(progress, ProgressEvent{
			Type:    "task_failed",
			TaskID:  task.ID,
			Message: fmt.Sprintf("failed to create branch: %v", err),
		})
		return completedTask{}, fmt.Errorf("task %s: create branch: %w", task.ID, err)
	}

	// Record branch
	if err := r.store.UpdatePipelineTaskBranch(pipelineID, task.ID, branch); err != nil {
		return completedTask{}, fmt.Errorf("record task branch: %w", err)
	}

	// Execute the task (LLM calls — can run concurrently across goroutines)
	result, err := r.marshal.ExecutePipelineTask(ctx, task.Description, branch)

	if err != nil || (result != nil && result.Status != "SUCCESS") {
		status := "FAILED"
		if result != nil && result.Status == "EXHAUSTED" {
			status = "EXHAUSTED"
		}
		r.store.UpdatePipelineTaskStatus(pipelineID, task.ID, status)

		// Cleanup branch (git op — serialised)
		r.gitLocked(func() error {
			r.git.CheckoutBranch("main")
			r.git.DeleteBranch(branch)
			return nil
		})

		msg := "task failed"
		if err != nil {
			msg = fmt.Sprintf("error: %v", err)
		} else if result != nil && result.FinalVerdict != nil {
			msg = fmt.Sprintf("exhausted after %d rounds: %s", len(result.Rounds), result.FinalVerdict.Summary)
		}
		r.callProgress(progress, ProgressEvent{
			Type:    "task_failed",
			TaskID:  task.ID,
			Message: msg,
		})
		return completedTask{}, fmt.Errorf("task %s failed: %s", task.ID, msg)
	}

	// Success
	r.store.UpdatePipelineTaskStatus(pipelineID, task.ID, "DONE")

	tasksDoneMu.Lock()
	*tasksDone++
	done := *tasksDone
	tasksDoneMu.Unlock()

	roundsInfo := fmt.Sprintf("%d round", len(result.Rounds))
	if len(result.Rounds) > 1 {
		roundsInfo = fmt.Sprintf("%d rounds", len(result.Rounds))
	}
	r.callProgress(progress, ProgressEvent{
		Type:     "task_complete",
		TaskID:   task.ID,
		Message:  fmt.Sprintf("completed (%s, PASS verdict)", roundsInfo),
		Progress: float64(done) / float64(tasksTotal),
	})

	return completedTask{TaskID: task.ID, Branch: branch, SHA: result.SHA}, nil
}

// Run executes a pipeline with per-tier parallelism, respecting dependencies.
func (r *Runner) Run(ctx context.Context, pipelineID int64, progress ProgressCallback) (*RunResult, error) {
	startTime := time.Now()

	pipelineRun, err := r.store.GetPipelineRun(pipelineID)
	if err != nil {
		return nil, fmt.Errorf("load pipeline run: %w", err)
	}

	var taskGraph planner.TaskGraph
	if err := json.Unmarshal([]byte(pipelineRun.PlanJSON), &taskGraph); err != nil {
		return nil, fmt.Errorf("parse task graph: %w", err)
	}

	if err := r.store.UpdatePipelineRunStatus(pipelineID, "EXECUTING"); err != nil {
		return nil, fmt.Errorf("update pipeline status: %w", err)
	}

	tiers, err := planner.TopologicalSort(&taskGraph)
	if err != nil {
		return nil, fmt.Errorf("topological sort: %w", err)
	}

	tasksTotal := 0
	for _, tier := range tiers {
		tasksTotal += len(tier)
	}

	tasksDone := 0
	tasksDoneMu := sync.Mutex{}
	var completedTasks []completedTask
	var failedTaskID string

	// Parallel execution limits from config (accessed via marshal — fall back to 3).
	maxParallel := 3
	failFast := true // marshal pipeline always fail-fast by default

	for _, tier := range tiers {
		parallel, serial := partitionByFileConflict(tier)

		// ------------------------------------------------------------------
		// Parallel segment: tasks without file conflicts run concurrently.
		// ------------------------------------------------------------------
		if len(parallel) > 0 {
			type outcome struct {
				ct  completedTask
				err error
				id  string
			}
			results := make(chan outcome, len(parallel))

			sem := make(chan struct{}, maxParallel)

			tierCtx, tierCancel := context.WithCancel(ctx)

			var wg sync.WaitGroup
			for _, task := range parallel {
				task := task
				wg.Add(1)
				go func() {
					defer wg.Done()
					// Acquire semaphore or bail on context cancellation.
					select {
					case sem <- struct{}{}:
					case <-tierCtx.Done():
						results <- outcome{id: task.ID, err: tierCtx.Err()}
						return
					}
					defer func() { <-sem }()

					ct, err := r.executeOneTask(tierCtx, pipelineID, task, progress, tasksTotal, &tasksDone, &tasksDoneMu)
					results <- outcome{ct: ct, err: err, id: task.ID}
				}()
			}

			go func() {
				wg.Wait()
				close(results)
			}()

			var tierFailed bool
			for res := range results {
				if res.err != nil {
					if !tierFailed {
						tierFailed = true
						failedTaskID = res.id
						if failFast {
							tierCancel()
						}
					}
				} else {
					completedTasks = append(completedTasks, res.ct)
				}
			}
			tierCancel()

			if tierFailed {
				return r.finalizeRun(pipelineID, tasksTotal, tasksDone, 1, failedTaskID, startTime, progress)
			}
		}

		// ------------------------------------------------------------------
		// Serial segment: tasks with overlapping files run one at a time.
		// ------------------------------------------------------------------
		for _, task := range serial {
			ct, err := r.executeOneTask(ctx, pipelineID, task, progress, tasksTotal, &tasksDone, &tasksDoneMu)
			if err != nil {
				failedTaskID = task.ID
				return r.finalizeRun(pipelineID, tasksTotal, tasksDone, 1, failedTaskID, startTime, progress)
			}
			completedTasks = append(completedTasks, ct)
		}
	}

	// ------------------------------------------------------------------
	// Integration critic: cross-task coherence review before merging.
	// ------------------------------------------------------------------
	if len(completedTasks) > 1 {
		var diffs []string
		for _, ct := range completedTasks {
			var diff string
			r.gitLocked(func() error {
				var e error
				diff, e = r.git.DiffBranch(ct.Branch)
				return e
			})
			if diff != "" {
				diffs = append(diffs, fmt.Sprintf("=== Task %s ===\n%s", ct.TaskID, diff))
			}
		}

		if len(diffs) > 0 {
			combinedDiff := strings.Join(diffs, "\n\n")
			taskDescs := make([]string, len(taskGraph.Tasks))
			for i, t := range taskGraph.Tasks {
				taskDescs[i] = fmt.Sprintf("  - %s: %s", t.ID, t.Description)
			}

			icResult, err := r.marshal.IntegrationCritic(ctx, taskGraph.Feature, taskDescs, combinedDiff)
			if err == nil && icResult.Verdict == "FAIL" {
				r.store.UpdatePipelineRunStatus(pipelineID, "INTEGRATION_FAIL")
				progress(ProgressEvent{
					Type:    "complete",
					Message: fmt.Sprintf("Integration critic FAIL: %s", icResult.Summary),
				})
				return &RunResult{
					PipelineID:        pipelineID,
					Status:            "INTEGRATION_FAIL",
					TasksTotal:        tasksTotal,
					TasksDone:         tasksDone,
					TasksFailed:       0,
					IntegrationIssues: icResult.CrossTaskIssues,
					Duration:          time.Since(startTime),
				}, fmt.Errorf("integration critic: %s", icResult.Summary)
			}
		}
	}

	// ------------------------------------------------------------------
	// Merge all completed branches in execution order.
	// ------------------------------------------------------------------
	progress(ProgressEvent{
		Type:    "merge",
		Message: "Merging branches...",
	})

	for _, ct := range completedTasks {
		mergeMsg := fmt.Sprintf("Merge %s: Task %s completed", ct.Branch, ct.TaskID)
		if err := r.gitLocked(func() error { return r.git.MergeBranch(ct.Branch, mergeMsg) }); err != nil {
			r.store.UpdatePipelineRunStatus(pipelineID, "PARTIAL")
			return &RunResult{
				PipelineID: pipelineID,
				Status:     "PARTIAL",
				TasksTotal: tasksTotal,
				TasksDone:  tasksDone,
				Duration:   time.Since(startTime),
			}, fmt.Errorf("merge branch %s: %w", ct.Branch, err)
		}
		r.gitLocked(func() error { r.git.DeleteBranch(ct.Branch); return nil })
	}

	if err := r.store.UpdatePipelineRunStatus(pipelineID, "DONE"); err != nil {
		return nil, fmt.Errorf("update pipeline status: %w", err)
	}

	duration := time.Since(startTime)
	progress(ProgressEvent{
		Type:    "complete",
		Message: fmt.Sprintf("Pipeline complete: %d/%d tasks succeeded in %v", tasksDone, tasksTotal, duration),
	})

	return &RunResult{
		PipelineID:  pipelineID,
		Status:      "DONE",
		TasksTotal:  tasksTotal,
		TasksDone:   tasksDone,
		TasksFailed: 0,
		Duration:    duration,
	}, nil
}

// finalizeRun handles cleanup and result generation when a pipeline fails.
func (r *Runner) finalizeRun(pipelineID int64, total, done, failed int, failedTaskID string, startTime time.Time, progress ProgressCallback) (*RunResult, error) {
	duration := time.Since(startTime)

	r.store.UpdatePipelineRunStatus(pipelineID, "FAILED")

	progress(ProgressEvent{
		Type:    "complete",
		Message: fmt.Sprintf("Pipeline failed: %d/%d tasks completed, task %s failed", done, total, failedTaskID),
	})

	return &RunResult{
		PipelineID:   pipelineID,
		Status:       "FAILED",
		TasksTotal:   total,
		TasksDone:    done,
		TasksFailed:  failed,
		FailedTaskID: failedTaskID,
		Duration:     duration,
	}, fmt.Errorf("pipeline failed at task %s", failedTaskID)
}
