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

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/edit"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/linter"
	"github.com/alecpullen/marshal/internal/models"
	"github.com/alecpullen/marshal/internal/prompts"
	"github.com/alecpullen/marshal/internal/repomap"
	"github.com/alecpullen/marshal/internal/session"
)


// executorOutputInstructions returns format-specific instructions for the executor.
func executorOutputInstructions(fmt config.EditFormat) string {
	switch fmt {
	case config.EditFormatSearchReplace:
		return `Output changes using SEARCH/REPLACE blocks. For each change, write the filename on its own line, then a block like this:

main.go
<<<<<<< SEARCH
old content to find (must match exactly)
=======
new content to replace it with
>>>>>>> REPLACE

To create a new file, leave the SEARCH section empty. Output ONLY changed files.`

	case config.EditFormatUdiff:
		return `Output changes as a unified diff. Use standard unified-diff format:

` + "```diff" + `
--- a/main.go
+++ b/main.go
@@ -1,4 +1,4 @@
 context line
-old line
+new line
 context line
` + "```" + `

Include 2–3 lines of context around each change. Output ONLY changed files.`

	default: // EditFormatWholeFile
		return `Output any files you create or modify using this format — the filename on its own line immediately before a fenced code block containing the complete file content:

main.go
` + "```" + `go
package main
// full file content here
` + "```" + `

Output ONLY files that changed. Do not truncate or summarise file contents.`
	}
}

const criticOutputInstructions = `Respond with ONLY this JSON object (no markdown, no prose):
{"verdict":"PASS","summary":"one sentence","issue":"","fix":"","concerns":[]}

Use "PASS" if the task is correctly and completely implemented. Use "FAIL" otherwise.
"issue" and "fix" must be non-empty on FAIL.`

// ErrTaskFailed is returned by Engine.Run when all rounds are exhausted.
var ErrTaskFailed = errors.New("task failed after all rounds")

// Config controls Engine behaviour.
type Config struct {
	MaxRounds     int
	CompactAfter  int                 // call compactor after this many consecutive FAIL rounds (0 = disabled)
	SessionID     string
	GitEnabled    bool                // when false all git operations are skipped
	ChatFiles     []string            // recently referenced files for repo-map personalization
	ReadOnlyFiles []string            // read-only files to include in context but never write
	EditFormat    config.EditFormat   // controls executor output format
	LinterCfg     config.LinterConfig // linter commands; zero value disables linting
	ExecutorExtra string              // appended to executor system prompt (from active skill)
	CriticExtra   string              // appended to critic system prompt (from active skill)
	LinterIsCritic bool               // auto-PASS when linter is clean
	CriticMode     config.CriticMode   // "separate" or "self"
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
	repo            *git.Repo
	gitSess         *git.Session
	store           *session.Store
	reg             *backend.Registry
	cfg             Config
	sink            Sink
	repoMap         string       // pre-built repo map text, injected into executor prompt
	repoMapM        *repomap.Map // cached map for file content injection
	lint            *linter.Linter
	execSysPrompt   string // assembled executor system prompt (base + skill extra)
	criticSysPrompt string // assembled critic system prompt (base + skill extra)
	executorModel   string // model name for context window lookup
	executorSubtype   backend.ProviderSubtype // for grammar constraint selection
	criticSubtype   backend.ProviderSubtype // for grammar constraint selection
}

// fileInjectionBudget returns the character budget for file content injection,
// derived from the executor model's context window. We reserve space for:
//   - system prompt (~2K tokens)
//   - output (~max_tokens)
//   - repo map (~2K tokens)
//   - task + instructions (~1K tokens)
//   - round-2+ issue/fix (~1K tokens)
// Budget is in characters, assuming ~4 chars/token.
func (e *Engine) fileInjectionBudget() int {
	ctxWindow := models.ContextWindowFor(e.executorModel)
	reserveTokens := 2048 + 4096 + 2048 + 1024 + 1024 // ~10K safety margin
	budgetTokens := ctxWindow - reserveTokens
	if budgetTokens < 2048 {
		budgetTokens = 2048 // floor at 2K tokens (8K chars)
	}
	return budgetTokens * 4 // chars per token heuristic
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
	e := &Engine{
		repo:            repo,
		gitSess:         gitSess,
		store:           store,
		reg:             reg,
		cfg:             cfg,
		sink:            sink,
		execSysPrompt:   prompts.Assemble(prompts.Executor, cfg.ExecutorExtra),
		criticSysPrompt: prompts.Assemble(prompts.Critic, cfg.CriticExtra),
	}
	// Build the repo map eagerly if we have a repo root. Errors are
	// non-fatal: the executor still runs, just without symbol context.
	if repo != nil {
		ig, _ := git.LoadMarshalIgnore(repo.Root())
		m, err := repomap.Build(repo.Root(), ig, repomap.Options{
			ChatFiles: cfg.ChatFiles,
		})
		if err == nil {
			e.repoMap = m.String()
			e.repoMapM = m
		}

		// Create linter if any command is configured.
		lc := cfg.LinterCfg
		if lc.Go != "" || lc.Python != "" || lc.JS != "" || lc.TS != "" {
			e.lint = linter.New(lc, repo.Root())
		}
	}
	return e
}

// SetExecutorModel sets the executor model name for context window lookups.
// Call this before Run if the model name is known (e.g., from config).
func (e *Engine) SetExecutorModel(model string) {
	e.executorModel = model
}

// SetCriticSubtype sets the critic provider subtype for grammar constraint selection.
// Call this before Run if using a local model backend.
func (e *Engine) SetCriticSubtype(subtype backend.ProviderSubtype) {
	e.criticSubtype = subtype
}

// SetExecutorSubtype sets the executor provider subtype for grammar constraint
// selection and cache hints. Call this before Run if using a local model backend.
func (e *Engine) SetExecutorSubtype(subtype backend.ProviderSubtype) {
	e.executorSubtype = subtype
}

// canUseSelfCritique returns true when self-critique mode is enabled and viable:
//   - CriticMode is "self"
//   - Executor backend supports JSON mode (for grammar constraint)
//   - Executor subtype supports grammar mode (local models)
func (e *Engine) canUseSelfCritique(executorB backend.Backend) bool {
	if e.cfg.CriticMode != config.CriticModeSelf {
		return false
	}
	if !executorB.SupportsJSONMode() {
		return false
	}
	// Grammar mode is required for reliable self-critique on local models.
	if e.executorSubtype != backend.SubtypeLlamaCPP {
		return false
	}
	return true
}

// executorCacheHints returns ExtraBody fields for KV-cache warming based on
// the executor backend subtype. These are passed to local servers to enable
// prefix caching across rounds.
func (e *Engine) executorCacheHints() map[string]any {
	if e.executorSubtype == backend.SubtypeLlamaCPP {
		return map[string]any{"cache_prompt": true}
	}
	return nil
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
	compactorB, _ := e.reg.For(config.RoleCompactor) // nil ok; compaction is best-effort

	// 3. Round loop.
	var issue, fix string
	var history []roundResult // failure history fed to the compactor
	var tc taskContext        // immutable context, built on round 1

	for round := 1; round <= e.cfg.MaxRounds; round++ {
		e.sink.RoundStart(taskID, round, e.cfg.MaxRounds)
		roundStart := time.Now()

		// a. Build executor messages.
		execMsgs := e.buildExecutorMessages(prompt, issue, fix, round, &tc)

		// b. Execute via tool-use (if supported) or stream + edit formats.
		var execContent string
		var execPToks, execCToks int
		var changedFiles []string
		var execErr error

		if executorB.SupportsTools() {
			// Tool-use path: multi-turn tool calling with compact prompts (for 16K context models)
			toolUseMsgs := e.buildToolUseMessages(prompt, issue, fix, round)
			execContent, execPToks, execCToks, execErr = e.runToolUseRound(ctx, executorB, toolUseMsgs)
			if execErr != nil {
				_ = tx.Abandon()
				return fmt.Errorf("tool-use round: %w", execErr)
			}
			// Get changed files from git status after tool execution
			changedFiles = e.getChangedFilesFromGit()
		} else {
			// Edit-format path: stream response and apply edits
			execContent, execPToks, execCToks, execErr = e.streamToSink(ctx, executorB, execMsgs)
			if execErr != nil {
				_ = tx.Abandon()
				return fmt.Errorf("executor stream: %w", execErr)
			}
			// c. Apply edits from executor response; capture changed file paths.
			changedFiles = e.applyEdits(execContent)
		}

		// d. Commit all changes on the task branch.
		commitMsg := fmt.Sprintf("marshal: task %s round %d", taskID, round)
		if commitErr := tx.Commit(commitMsg); commitErr != nil && !errors.Is(commitErr, git.ErrNothingToCommit) {
			_ = tx.Abandon()
			return fmt.Errorf("commit: %w", commitErr)
		}

		// e. Diff against parent staging SHA for critic context.
		diff, _ := tx.Diff()

		// f. Run linter; on failures short-circuit to next round.
		var lintPassed bool
		if e.lint != nil && len(changedFiles) > 0 {
			lintIssues, _ := e.lint.Run(ctx, changedFiles)
			if len(lintIssues) > 0 {
				summary := linter.Format(lintIssues)
				e.sink.LintErrors(taskID, summary)
				issue = "linter reported the following errors that must be fixed:\n" + summary
				fix = "Fix every linter error listed above. Do not proceed until the code is lint-clean."
				continue
			}
			lintPassed = true
		}

		// f.1 Linter-is-critic mode: when linter passes and diff is non-empty, auto-PASS
		// without a critic round-trip (PR-3 3.1). This saves latency for local models.
		if e.cfg.LinterIsCritic && lintPassed && diff != "" {
			// Synthetic PASS verdict from linter.
			verdict := &Verdict{
				Verdict: "PASS",
				Summary: "Linter passed; auto-approved in local-profile mode.",
			}
			durationMS := time.Since(roundStart).Milliseconds()

			// Record executor round (no critic round needed).
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
			// Record a synthetic critic round with the linter verdict.
			verdictJSON, _ := json.Marshal(verdict)
			verdictStr := string(verdictJSON)
			e.recordRound(&session.Round{
				SessionID:        e.cfg.SessionID,
				TaskID:           taskID,
				Round:            round,
				Role:             config.RoleCritic,
				Model:            "linter",
				PromptTokens:     0,
				CompletionTokens: 0,
				DurationMS:       0,
				Content:          verdict.Summary,
				VerdictJSON:      &verdictStr,
			})

			e.sink.VerdictBadge(taskID, verdict.Verdict, verdict.Summary)
			e.sink.RoundEnd(taskID, round, execPToks, execCToks)

			// Merge to staging.
			shortPrompt := truncate(prompt, 60)
			mergeMsg := fmt.Sprintf("task %s: %s", taskID, shortPrompt)
			if mergeErr := tx.Merge(mergeMsg); mergeErr != nil {
				if errors.Is(mergeErr, git.ErrAlreadyUpToDate) {
					// Task branch has no commits relative to staging — treat as FAIL.
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

		// g. Self-critique mode (PR-3 3.2): when executor==critic model and the backend
		// supports grammar constraints, emit verdict as part of executor output instead
		// of a separate round-trip. This halves per-round latency for local models.
		var verdict *Verdict
		var thinks []string
		var criticPToks, criticCToks int
		var criticContent string // content recorded for critic round
		if e.canUseSelfCritique(executorB) {
			var scErr error
			execContent, verdict, execPToks, execCToks, scErr = e.runSelfCritique(ctx, executorB, execMsgs, diff)
			if scErr != nil {
				_ = tx.Abandon()
				return fmt.Errorf("self-critique: %w", scErr)
			}
			// Re-apply edits from the stripped executor content.
			changedFiles = e.applyEdits(execContent)
			// Self-critique uses same token count for both phases.
			criticPToks, criticCToks = 0, 0
			// Record the verdict as critic content.
			vd, _ := json.Marshal(verdict)
			criticContent = string(vd)
		} else {
			// Standard critic call (non-streaming for reliable JSON extraction).
			// Use grammar-constrained output for local models (llama.cpp) to eliminate
			// "unparseable verdict" failures.
			criticMsgs := e.buildCriticMessages(prompt, execContent, diff)
			var criticErr error
			criticContent, criticPToks, criticCToks, criticErr = e.callCritic(ctx, criticB, criticMsgs, e.criticSubtype)
			if criticErr != nil {
				_ = tx.Abandon()
				return fmt.Errorf("critic: %w", criticErr)
			}
			// Parse verdict (strips think blocks).
			var parseErr error
			verdict, thinks, parseErr = ParseVerdict(criticContent)
			if parseErr != nil {
				// Treat unparseable verdict as FAIL so we can retry.
				verdict = &Verdict{Verdict: "FAIL", Summary: "critic returned unparseable response", Issue: parseErr.Error()}
			}
		}

		if verdict == nil {
			// Should not happen, but guard against nil pointer.
			verdict = &Verdict{Verdict: "FAIL", Summary: "no verdict produced"}
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
		e.sink.RoundEnd(taskID, round, execPToks+criticPToks, execCToks+criticCToks)

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

		// FAIL: record this round's result and carry issue/fix forward.
		issue = verdict.Issue
		fix = verdict.Fix
		history = append(history, roundResult{
			Round: round,
			Issue: issue,
			Fix:   fix,
			Diff:  diff,
		})

		// After compact_after consecutive failures, call the compactor to
		// synthesize the history into a fresh issue/fix for the next round.
		ca := e.cfg.CompactAfter
		if ca > 0 && len(history) >= ca && compactorB != nil {
			if ci, cf, cerr := e.compact(ctx, compactorB, prompt, history); cerr == nil {
				issue, fix = ci, cf
				history = nil // reset; compacted summary starts a new window
			}
			// Compaction errors are non-fatal: the executor retries with the
			// last raw issue/fix instead.
		}
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

// taskContext holds the immutable task description and output instructions.
// It is constructed once at round 1 and reused on subsequent rounds to
// preserve the KV cache prefix.
type taskContext struct {
	Prompt             string
	OutputInstructions string
	RepoMap            string // skeleton view, computed once
	FileContext        string // skeleton or full body, computed once
}

// buildExecutorMessages assembles messages with a stable prefix contract.
//
// Round 1: [system] + [user: repo_map + file_context + task + instructions]
// Round 2+: [system] + [user: repo_map + file_context + task + instructions]
//            + [user: previous attempt was rejected + issue + fix]
//
// The first user message (immutable context) is identical on every round,
// so local-server KV caches hit for the expensive prefix. Only the final
// user message (the retry prompt) changes.
func (e *Engine) buildExecutorMessages(prompt, issue, fix string, round int, tc *taskContext) []backend.Message {
	outputInstructions := executorOutputInstructions(e.cfg.EditFormat)

	// Round 1: build and cache the immutable context block.
	if round == 1 {
		tc.Prompt = prompt
		tc.OutputInstructions = outputInstructions
		tc.RepoMap = e.buildRepoMapContext()
		tc.FileContext = e.buildFileContext(prompt) // skeleton-first, prompt-relative expansion
	}

	// Assemble the immutable context message (same on every round).
	var ctxBuilder strings.Builder
	if tc.RepoMap != "" {
		ctxBuilder.WriteString(tc.RepoMap)
		ctxBuilder.WriteString("\n\n")
	}
	if tc.FileContext != "" {
		ctxBuilder.WriteString(tc.FileContext)
		ctxBuilder.WriteString("\n\n")
	}
	ctxBuilder.WriteString("Task: ")
	ctxBuilder.WriteString(tc.Prompt)
	ctxBuilder.WriteString("\n\n")
	ctxBuilder.WriteString(tc.OutputInstructions)

	messages := []backend.Message{
		{Role: backend.MessageRoleSystem, Content: e.execSysPrompt},
		{Role: backend.MessageRoleUser, Content: ctxBuilder.String()},
	}

	// Round 2+: append the mutable retry prompt as a separate user message.
	// This keeps the prefix stable while isolating the changing retry signal.
	if round > 1 {
		var retryBuilder strings.Builder
		retryBuilder.WriteString("Your previous attempt was rejected by the code reviewer.\n\n")
		if issue != "" {
			retryBuilder.WriteString("Issue: ")
			retryBuilder.WriteString(issue)
			retryBuilder.WriteString("\n")
		}
		if fix != "" {
			retryBuilder.WriteString("Suggested fix: ")
			retryBuilder.WriteString(fix)
			retryBuilder.WriteString("\n")
		}
		retryBuilder.WriteString("\nPlease try again, taking the issue and fix into account.")
		messages = append(messages, backend.Message{
			Role:    backend.MessageRoleUser,
			Content: retryBuilder.String(),
		})
	}

	return messages
}

// buildRepoMapContext returns the repo map formatted for prompt injection.
// Returns empty string if no repo map is available.
func (e *Engine) buildRepoMapContext() string {
	if e.repoMap == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Repository map (ranked by relevance):\n```\n")
	sb.WriteString(e.repoMap)
	sb.WriteString("```")
	return sb.String()
}

// buildToolUseMessages creates compact prompts for tool-use models with limited context (e.g., 16K).
// It excludes the repo map and file context to save tokens, since the model can use read_file tool.
func (e *Engine) buildToolUseMessages(prompt, issue, fix string, round int) []backend.Message {
	var sb strings.Builder

	sb.WriteString("You have access to tools: read_file, write_file, and run_command.\n")
	sb.WriteString("Use these tools to complete the task. Prefer reading files before modifying them.\n\n")

	if round == 1 {
		sb.WriteString("Task: ")
		sb.WriteString(prompt)
	} else {
		sb.WriteString("Task: ")
		sb.WriteString(prompt)
		sb.WriteString("\n\nYour previous attempt was rejected.\n")
		if issue != "" {
			sb.WriteString("Issue: ")
			sb.WriteString(issue)
			sb.WriteString("\n")
		}
		if fix != "" {
			sb.WriteString("Fix: ")
			sb.WriteString(fix)
			sb.WriteString("\n")
		}
		sb.WriteString("\nPlease try again using the available tools.")
	}

	return []backend.Message{
		{Role: backend.MessageRoleSystem, Content: e.execSysPrompt},
		{Role: backend.MessageRoleUser, Content: sb.String()},
	}
}

// fileMatchesPrompt returns true if the file path or any of its symbols
// appear in the task prompt (case-insensitive).
func fileMatchesPrompt(path string, symbols []string, prompt string) bool {
	lowerPrompt := strings.ToLower(prompt)
	if strings.Contains(lowerPrompt, strings.ToLower(path)) {
		return true
	}
	for _, sym := range symbols {
		if strings.Contains(lowerPrompt, strings.ToLower(sym)) {
			return true
		}
	}
	return false
}

// buildSkeleton returns a skeleton view of a file: path + symbol list.
// Does not read file contents; uses the repo map's extracted symbols.
func (e *Engine) buildSkeleton(sec repomap.Section) string {
	var sb strings.Builder
	sb.WriteString(sec.Path)
	sb.WriteString(":\n")
	for _, sym := range sec.Symbols {
		sb.WriteString("  - ")
		sb.WriteString(sym)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// buildFileContext returns file context for the executor prompt using a
// skeleton-first approach (PR-1 variant a). Files are shown as skeleton views
// (path + symbol list) unless the task prompt explicitly references the file
// path or a symbol defined in that file, in which case the full body is shown.
//
// This keeps the prompt compact for small-context local models while still
// providing full file contents when the task clearly targets specific files.
func (e *Engine) buildFileContext(prompt string) string {
	if e.repo == nil {
		return ""
	}

	budget := e.fileInjectionBudget()
	wrote := 0

	var sb strings.Builder
	sb.WriteString("File context (skeletons shown; full body if mentioned in task):\n\n")

	// Helper to check if path is in read-only list.
	isReadOnly := func(path string) bool {
		for _, rof := range e.cfg.ReadOnlyFiles {
			if path == rof {
				return true
			}
		}
		return false
	}

	// First, include read-only files with full content (high priority).
	for _, path := range e.cfg.ReadOnlyFiles {
		abs := filepath.Join(e.repo.Root(), filepath.Clean(path))
		if !strings.HasPrefix(abs, e.repo.Root()) {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > budget {
			continue
		}
		budget -= len(content)

		sb.WriteString(path)
		sb.WriteString(" (read-only)\n```\n")
		sb.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```\n\n")
		wrote++

		if budget <= 0 {
			break
		}
	}

	// Then, include repo map ranked files using skeleton-first approach.
	if e.repoMapM != nil {
		for _, sec := range e.repoMapM.Sections {
			// Skip if already included as read-only.
			if isReadOnly(sec.Path) {
				continue
			}

			// Decide: skeleton or full body?
			// Full body only if task mentions the file path or its symbols.
			showFullBody := fileMatchesPrompt(sec.Path, sec.Symbols, prompt)

			if showFullBody {
				abs := filepath.Join(e.repo.Root(), sec.Path)
				data, err := os.ReadFile(abs)
				if err != nil {
					continue
				}
				content := string(data)
				if len(content) > budget {
					continue
				}
				budget -= len(content)

				sb.WriteString(sec.Path)
				sb.WriteString("\n```\n")
				sb.WriteString(content)
				if !strings.HasSuffix(content, "\n") {
					sb.WriteString("\n")
				}
				sb.WriteString("```\n\n")
			} else {
				// Skeleton view is compact (~200 chars typical).
				skeleton := e.buildSkeleton(sec)
				if len(skeleton) > budget {
					continue
				}
				budget -= len(skeleton)
				sb.WriteString(skeleton)
				sb.WriteString("\n")
			}
			wrote++

			if budget <= 0 {
				break
			}
		}
	}

	if wrote == 0 {
		return ""
	}
	return sb.String()
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
		{Role: backend.MessageRoleSystem, Content: e.criticSysPrompt},
		{Role: backend.MessageRoleUser, Content: sb.String()},
	}
}

// --- Backend helpers ---------------------------------------------------------

// streamToSink streams an executor request, forwarding each token to the sink.
// Returns the full concatenated content and token counts.
func (e *Engine) streamToSink(ctx context.Context, b backend.Backend, msgs []backend.Message) (content string, promptToks, completionToks int, err error) {
	req := backend.Request{Messages: msgs}
	// Pass cache hints for local-server KV prefix caching (PR-2 feature).
	// PR-3 will wire executorSubtype to populate these hints.
	if extra := e.executorCacheHints(); extra != nil {
		req.ExtraBody = extra
	}
	ch, err := b.Stream(ctx, req)
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

// callCritic calls the critic backend with grammar-constrained output when
// supported (local models). This dramatically reduces "unparseable verdict"
// failures on llama.cpp servers.
func (e *Engine) callCritic(ctx context.Context, b backend.Backend, msgs []backend.Message, subtype backend.ProviderSubtype) (content string, promptToks, completionToks int, err error) {
	req := backend.Request{
		Messages: msgs,
		JSONMode: backend.JSONLoose,
	}

	// Only set JSON response format if the backend supports it.
	if b.SupportsJSONMode() {
		req.ResponseFormat = &backend.ResponseFormat{Type: "json_object"}
	}

	// Enable grammar mode for local models that support it.
	if subtype == backend.SubtypeLlamaCPP {
		req.JSONMode = backend.JSONGrammar
		req.Grammar = backend.VerdictGrammar()
	}

	resp, err := b.Complete(ctx, req)
	if err != nil {
		return "", 0, 0, err
	}
	return resp.Content, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, nil
}

// selfCritiqueInstructions is appended to executor output instructions in self-critique mode.
// The executor outputs code changes, then emits a verdict JSON block at the end.
const selfCritiqueInstructions = `

After completing the code changes, output a verdict block at the very end:

<verdict>
{"verdict":"PASS|FAIL","summary":"one sentence","issue":"","fix":"","concerns":[]}
</verdict>

Use PASS if the task is correctly and completely implemented. Use FAIL otherwise.
"issue" and "fix" must be non-empty on FAIL.`

// runSelfCritique calls the executor with grammar-constrained output that includes
// both the code changes and a verdict JSON block. This halves per-round latency
// when executor and critic are the same model (PR-3 3.2).
func (e *Engine) runSelfCritique(ctx context.Context, b backend.Backend, execMsgs []backend.Message, diff string) (execContent string, verdict *Verdict, execPToks, execCToks int, err error) {
	// Append self-critique instructions and diff to the last user message.
	msgs := make([]backend.Message, len(execMsgs))
	copy(msgs, execMsgs)

	// Find the last user message and append self-critique instructions.
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == backend.MessageRoleUser {
			msgs[i].Content = msgs[i].Content + "\n\n" + selfCritiqueInstructions + "\n\nGit diff:\n" + diff
			break
		}
	}

	// Request grammar-constrained output for reliable verdict parsing.
	req := backend.Request{
		Messages:  msgs,
		JSONMode:  backend.JSONGrammar,
		Grammar:   backend.VerdictGrammar(),
		ExtraBody: e.executorCacheHints(),
	}

	// Only set JSON response format if the backend supports it.
	if b.SupportsJSONMode() {
		req.ResponseFormat = &backend.ResponseFormat{Type: "json_object"}
	}

	resp, err := b.Complete(ctx, req)
	if err != nil {
		return "", nil, 0, 0, err
	}

	content := resp.Content
	// Try to extract verdict from <verdict>...</verdict> tags.
	verdict, parseErr := extractVerdictFromContent(content)
	if parseErr != nil {
		// If extraction fails, treat as unparseable but still return the content.
		verdict = &Verdict{
			Verdict: "FAIL",
			Summary: "self-critique returned unparseable response",
			Issue:   parseErr.Error(),
		}
	}

	// Strip the verdict block from content for edit parsing.
	execContent = stripVerdictBlock(content)
	return execContent, verdict, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, nil
}

// extractVerdictFromContent extracts the verdict JSON from <verdict> tags.
func extractVerdictFromContent(content string) (*Verdict, error) {
	start := strings.Index(content, "<verdict>")
	end := strings.Index(content, "</verdict>")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no <verdict> block found")
	}
	jsonStr := content[start+len("<verdict>") : end]
	jsonStr = strings.TrimSpace(jsonStr)
	verdict, _, err := ParseVerdict(jsonStr)
	return verdict, err
}

// stripVerdictBlock removes the <verdict>...</verdict> block from content.
func stripVerdictBlock(content string) string {
	start := strings.Index(content, "<verdict>")
	if start == -1 {
		return content
	}
	end := strings.Index(content, "</verdict>")
	if end == -1 {
		return content
	}
	// Return content before <verdict> and after </verdict>
	before := content[:start]
	after := content[end+len("</verdict>"):]
	return strings.TrimSpace(before) + "\n" + strings.TrimSpace(after)
}

// --- Edit application --------------------------------------------------------

// applyEdits writes files from the executor response and returns the relative
// paths of files that were successfully written.
func (e *Engine) applyEdits(content string) []string {
	switch e.cfg.EditFormat {
	case config.EditFormatSearchReplace:
		return e.applySearchReplace(content)
	case config.EditFormatUdiff:
		return e.applyUdiff(content)
	default:
		return e.applyWhole(content)
	}
}

func (e *Engine) applyWhole(content string) []string {
	edits := edit.ParseWhole(content)
	var written []string
	for _, fe := range edits {
		rel := filepath.Clean(fe.Path)
		if strings.HasPrefix(rel, "..") {
			continue
		}
		abs := filepath.Join(e.repo.Root(), rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(abs, []byte(fe.Content), 0o644); err == nil {
			written = append(written, rel)
		}
	}
	return written
}

func (e *Engine) applySearchReplace(content string) []string {
	edits := edit.ParseSearchReplace(content)
	var written []string
	for _, se := range edits {
		rel := filepath.Clean(se.Path)
		if strings.HasPrefix(rel, "..") {
			continue
		}
		abs := filepath.Join(e.repo.Root(), rel)
		existing, _ := os.ReadFile(abs)
		updated, ok := se.ApplyToContent(string(existing))
		if !ok {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(abs, []byte(updated), 0o644); err == nil {
			written = append(written, rel)
		}
	}
	return written
}

func (e *Engine) applyUdiff(content string) []string {
	edits := edit.ParseUdiff(content)
	var written []string
	for _, ue := range edits {
		rel := filepath.Clean(ue.Path)
		if strings.HasPrefix(rel, "..") {
			continue
		}
		abs := filepath.Join(e.repo.Root(), rel)
		existing, _ := os.ReadFile(abs)
		updated, ok := ue.ApplyToContent(string(existing))
		if !ok {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(abs, []byte(updated), 0o644); err == nil {
			written = append(written, rel)
		}
	}
	return written
}

// getChangedFilesFromGit returns a list of changed files after tool execution.
// It uses git status to find modified files.
func (e *Engine) getChangedFilesFromGit() []string {
	if e.repo == nil {
		return nil
	}

	// Use DirtyFiles to get list of changed files
	files, err := e.repo.DirtyFiles()
	if err != nil {
		return nil
	}
	return files
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
