// Package loop implements the single-task executor–critic round loop.
package loop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/config"
	"github.com/alec/marshal/internal/edit"
	"github.com/alec/marshal/internal/git"
	"github.com/alec/marshal/internal/prompts"
	"github.com/alec/marshal/internal/session"
)

// executorSystemPrompt / criticSystemPrompt are loaded from the embedded base
// markdown files. The output-format instructions are injected into the user
// message so they can be overridden per skill in M8.
var (
	executorSystemPrompt = prompts.Executor
	criticSystemPrompt   = prompts.Critic
)

const executorOutputInstructions = `Output any files you create or modify using this format — the filename on its own line immediately before a fenced code block containing the complete file content:

main.go
` + "```" + `go
package main
// full file content here
` + "```" + `

Output ONLY files that changed. Do not truncate or summarise file contents.`

const criticOutputInstructions = `Respond with ONLY this JSON object (no markdown, no prose):
{"verdict":"PASS","summary":"one sentence","issue":"","fix":"","concerns":[]}

Use "PASS" if the task is correctly and completely implemented. Use "FAIL" otherwise.
"issue" and "fix" must be non-empty on FAIL.`

// ErrTaskFailed is returned by Engine.Run when all rounds are exhausted.
var ErrTaskFailed = errors.New("task failed after all rounds")

// Config controls Engine behaviour.
type Config struct {
	MaxRounds  int
	SessionID  string
	GitEnabled bool // when false all git operations are skipped
}

// txer abstracts the task-branch lifecycle so the engine can run with or
// without git.  *git.TaskTx satisfies this; noopTx is used when git is
// disabled.
type txer interface {
	Commit(message string) error
	Diff() (string, error)
	Merge(message string) error
	Abandon() error
}

// noopTx is a no-op txer used when git integration is disabled.
type noopTx struct{}

func (noopTx) Commit(string) error   { return nil }
func (noopTx) Diff() (string, error) { return "", nil }
func (noopTx) Merge(string) error    { return nil }
func (noopTx) Abandon() error        { return nil }

// Engine orchestrates the executor–critic round loop for a single task.
type Engine struct {
	repo    *git.Repo
	gitSess *git.Session
	store   *session.Store
	reg     *backend.Registry
	cfg     Config
	sink    Sink
}

// New creates an Engine. All parameters are required.
func New(
	cfg Config,
	repo *git.Repo,
	gitSess *git.Session,
	store *session.Store,
	reg *backend.Registry,
	sink Sink,
) *Engine {
	if cfg.MaxRounds == 0 {
		cfg.MaxRounds = 3
	}
	return &Engine{
		repo:    repo,
		gitSess: gitSess,
		store:   store,
		reg:     reg,
		cfg:     cfg,
		sink:    sink,
	}
}

// Run executes one task for prompt.
//   - On PASS the task branch is squash-merged into the session staging branch;
//     the ledger row is updated with status=passed.
//   - On FAIL (all rounds exhausted) the task branch is abandoned unchanged;
//     the ledger row is updated with status=failed.
//     ErrTaskFailed is returned so callers can distinguish task failure from
//     infrastructure errors.
//
// The target branch is never touched by Run.
func (e *Engine) Run(ctx context.Context, prompt string) error {
	taskID := newTaskID()

	// 1. Create task isolation branch (or no-op when git is disabled).
	var tx txer
	var parentStagingSHA string
	if e.cfg.GitEnabled {
		realTx, err := e.gitSess.BeginTask(taskID)
		if err != nil {
			return fmt.Errorf("begin task: %w", err)
		}
		parentStagingSHA = realTx.ParentStagingSHA
		tx = realTx
	} else {
		tx = noopTx{}
	}

	// 2. Insert task row.
	now := time.Now()
	if err := e.store.InsertTask(&session.Task{
		ID:               taskID,
		SessionID:        e.cfg.SessionID,
		Prompt:           prompt,
		ParentStagingSHA: parentStagingSHA,
		Status:           session.StatusRunning,
		StartedAt:        now,
	}); err != nil {
		_ = tx.Abandon()
		return fmt.Errorf("ledger insert task: %w", err)
	}

	executorB, err := e.reg.For(config.RoleExecutor)
	if err != nil {
		_ = tx.Abandon()
		return err
	}
	criticB, err := e.reg.For(config.RoleCritic)
	if err != nil {
		_ = tx.Abandon()
		return err
	}

	// 3. Round loop.
	var issue, fix string

	for round := 1; round <= e.cfg.MaxRounds; round++ {
		e.sink.RoundStart(taskID, round, e.cfg.MaxRounds)
		roundStart := time.Now()

		// a. Build executor messages.
		execMsgs := e.buildExecutorMessages(prompt, issue, fix, round)

		// b. Stream executor; collect full text for diff + critic.
		execContent, execPToks, execCToks, streamErr := e.streamToSink(ctx, executorB, execMsgs)
		if streamErr != nil {
			_ = tx.Abandon()
			return fmt.Errorf("executor stream: %w", streamErr)
		}

		// c. Apply whole-file edits from executor response.
		e.applyEdits(execContent)

		// d. Commit all changes on the task branch.
		commitMsg := fmt.Sprintf("marshal: task %s round %d", taskID, round)
		if commitErr := tx.Commit(commitMsg); commitErr != nil && !errors.Is(commitErr, git.ErrNothingToCommit) {
			_ = tx.Abandon()
			return fmt.Errorf("commit: %w", commitErr)
		}

		// e. Diff against parent staging SHA for critic context.
		diff, _ := tx.Diff()

		// f. Call critic (non-streaming for reliable JSON extraction).
		criticMsgs := e.buildCriticMessages(prompt, execContent, diff)
		criticContent, criticPToks, criticCToks, criticErr := e.callBackend(ctx, criticB, criticMsgs)
		if criticErr != nil {
			_ = tx.Abandon()
			return fmt.Errorf("critic: %w", criticErr)
		}

		// g. Parse verdict (strips think blocks).
		verdict, thinks, parseErr := ParseVerdict(criticContent)
		if parseErr != nil {
			// Treat unparseable verdict as FAIL so we can retry.
			verdict = &Verdict{Verdict: "FAIL", Summary: "critic returned unparseable response", Issue: parseErr.Error()}
		}

		durationMS := time.Since(roundStart).Milliseconds()

		// h. Record executor round.
		e.recordRound(&session.Round{
			SessionID:        e.cfg.SessionID,
			TaskID:           taskID,
			Round:            round,
			Role:             config.RoleExecutor,
			Model:            executorB.Model(),
			PromptTokens:     execPToks,
			CompletionTokens: execCToks,
			DurationMS:       durationMS,
			Content:          execContent,
		})
		// Record critic round with verdict + think blocks.
		thinkJSON, _ := json.Marshal(thinks)
		verdictJSON, _ := json.Marshal(verdict)
		thinkStr := string(thinkJSON)
		verdictStr := string(verdictJSON)
		e.recordRound(&session.Round{
			SessionID:        e.cfg.SessionID,
			TaskID:           taskID,
			Round:            round,
			Role:             config.RoleCritic,
			Model:            criticB.Model(),
			PromptTokens:     criticPToks,
			CompletionTokens: criticCToks,
			DurationMS:       durationMS,
			Content:          criticContent,
			VerdictJSON:      &verdictStr,
			ThinkBlocks:      &thinkStr,
		})

		e.sink.VerdictBadge(taskID, verdict.Verdict, verdict.Summary)

		// i. PASS: merge to staging (no-op when git is disabled).
		if verdict.IsPASS() {
			shortPrompt := truncate(prompt, 60)
			mergeMsg := fmt.Sprintf("task %s: %s", taskID, shortPrompt)
			if mergeErr := tx.Merge(mergeMsg); mergeErr != nil {
				if errors.Is(mergeErr, git.ErrAlreadyUpToDate) {
					// Task branch has no commits relative to staging — treat
					// this as a FAIL so the executor retries.
					issue = "the executor made no file changes"
					fix = "output the complete content of any files that need to change"
					continue
				}
				return fmt.Errorf("merge to staging: %w", mergeErr)
			}
			var newSHA string
			if e.cfg.GitEnabled {
				newSHA, _ = e.gitSess.StagingHEAD()
			}
			endedAt := time.Now()
			_ = e.store.UpdateTask(taskID, session.TaskUpdate{
				Status:     session.StatusPassed,
				StagingSHA: &newSHA,
				EndedAt:    &endedAt,
				Summary:    &verdict.Summary,
			})
			e.sink.TaskMerged(taskID, newSHA)
			return nil
		}

		// FAIL: carry issue/fix into next round.
		issue = verdict.Issue
		fix = verdict.Fix
	}

	// 4. All rounds exhausted.
	_ = tx.Abandon()
	endedAt := time.Now()
	_ = e.store.UpdateTask(taskID, session.TaskUpdate{
		Status:  session.StatusFailed,
		EndedAt: &endedAt,
		Summary: &issue,
	})
	e.sink.TaskFailed(taskID, issue)
	return ErrTaskFailed
}

// --- Prompt builders ---------------------------------------------------------

func (e *Engine) buildExecutorMessages(prompt, issue, fix string, round int) []backend.Message {
	var sb strings.Builder

	if round == 1 {
		sb.WriteString("Task: ")
		sb.WriteString(prompt)
		sb.WriteString("\n\n")
		sb.WriteString(executorOutputInstructions)
	} else {
		sb.WriteString("Task: ")
		sb.WriteString(prompt)
		sb.WriteString("\n\n")
		sb.WriteString("Your previous attempt was rejected by the code reviewer.\n\n")
		if issue != "" {
			sb.WriteString("Issue: ")
			sb.WriteString(issue)
			sb.WriteString("\n")
		}
		if fix != "" {
			sb.WriteString("Suggested fix: ")
			sb.WriteString(fix)
			sb.WriteString("\n")
		}
		sb.WriteString("\nPlease try again.\n\n")
		sb.WriteString(executorOutputInstructions)
	}

	return []backend.Message{
		{Role: backend.MessageRoleSystem, Content: executorSystemPrompt},
		{Role: backend.MessageRoleUser, Content: sb.String()},
	}
}

func (e *Engine) buildCriticMessages(prompt, execContent, diff string) []backend.Message {
	var sb strings.Builder
	sb.WriteString("Task: ")
	sb.WriteString(prompt)
	sb.WriteString("\n\n")

	if diff != "" {
		sb.WriteString("Git diff (what changed):\n```diff\n")
		sb.WriteString(diff)
		sb.WriteString("\n```\n\n")
	} else {
		sb.WriteString("No changes were made to the repository.\n\n")
	}

	sb.WriteString("Executor response:\n")
	sb.WriteString(execContent)
	sb.WriteString("\n\n")
	sb.WriteString(criticOutputInstructions)

	return []backend.Message{
		{Role: backend.MessageRoleSystem, Content: criticSystemPrompt},
		{Role: backend.MessageRoleUser, Content: sb.String()},
	}
}

// --- Backend helpers ---------------------------------------------------------

// streamToSink streams an executor request, forwarding each token to the sink.
// Returns the full concatenated content and token counts.
func (e *Engine) streamToSink(ctx context.Context, b backend.Backend, msgs []backend.Message) (content string, promptToks, completionToks int, err error) {
	ch, err := b.Stream(ctx, backend.Request{Messages: msgs})
	if err != nil {
		return "", 0, 0, err
	}
	var sb strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return sb.String(), 0, 0, chunk.Err
		}
		sb.WriteString(chunk.Content)
		e.sink.Token(chunk.Content)
		completionToks++
	}
	// Token counts from the backend are approximate here; M5 will add accurate
	// counting via the tokens package.
	return sb.String(), 0, completionToks, nil
}

// callBackend performs a non-streaming completion (used for the critic so that
// we get the full JSON in one shot before trying to parse it).
func (e *Engine) callBackend(ctx context.Context, b backend.Backend, msgs []backend.Message) (content string, promptToks, completionToks int, err error) {
	resp, err := b.Complete(ctx, backend.Request{Messages: msgs})
	if err != nil {
		return "", 0, 0, err
	}
	return resp.Content, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, nil
}

// --- Edit application --------------------------------------------------------

func (e *Engine) applyEdits(content string) {
	edits := edit.ParseWhole(content)
	for _, fe := range edits {
		// Resolve path relative to repo root; reject traversals.
		rel := filepath.Clean(fe.Path)
		if strings.HasPrefix(rel, "..") {
			continue
		}
		abs := filepath.Join(e.repo.Root(), rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			continue
		}
		_ = os.WriteFile(abs, []byte(fe.Content), 0o644)
	}
}

// --- Ledger helpers ----------------------------------------------------------

func (e *Engine) recordRound(r *session.Round) {
	_ = e.store.InsertRound(r) // best-effort; failures are non-fatal
}

// --- Misc helpers ------------------------------------------------------------

func newTaskID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
