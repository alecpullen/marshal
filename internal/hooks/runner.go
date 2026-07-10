package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"marshal/internal/app/config"
)

type Runner struct {
	cfg Config
}

func NewRunner(cfg Config) *Runner {
	return &Runner{cfg: cfg}
}

func NewRunnerFromConfig(cfg config.HooksConfig) *Runner {
	entries := make([]HookEntry, 0, len(cfg.Entries))
	for _, entry := range cfg.Entries {
		entries = append(entries, HookEntry{
			Event:     entry.Event,
			Matcher:   entry.Matcher,
			Command:   entry.Command,
			TimeoutMS: entry.TimeoutMS,
		})
	}
	return NewRunner(Config{FailClosed: cfg.FailClosed, Entries: entries})
}

func (r *Runner) RunPreToolUse(ctx context.Context, input PreToolUseInput) (Output, error) {
	input.Event = EventPreToolUse
	return r.runEvent(ctx, EventPreToolUse, input.ToolName, input)
}

func (r *Runner) RunTurnEnd(ctx context.Context, input TurnEndInput) (Output, error) {
	input.Event = EventTurnEnd
	return r.runEvent(ctx, EventTurnEnd, "", input)
}

func (r *Runner) runEvent(ctx context.Context, event, matcherValue string, payload any) (Output, error) {
	matched := r.matchingEntries(event, matcherValue)
	out := Output{Decision: DecisionAllow, HookCount: len(matched)}

	for i := range matched {
		entry := matched[i]
		timeout := time.Duration(entry.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 5 * time.Second
		}

		hookCtx, cancel := context.WithTimeout(ctx, timeout)
		stdout, runErr := runHook(hookCtx, entry.Command, payload)
		cancel()

		if runErr != nil {
			if r.cfg.FailClosed {
				return Output{
					Decision:  DecisionBlock,
					Reason:    fmt.Sprintf("hook %q failed: %v", entry.Command, runErr),
					HookCount: len(matched),
				}, nil
			}
			out.FailedOpen = true
			continue
		}

		trimmed := bytes.TrimSpace(stdout)
		if len(trimmed) == 0 {
			continue
		}

		var parsed Output
		if err := json.Unmarshal(trimmed, &parsed); err != nil {
			if r.cfg.FailClosed {
				return Output{
					Decision:  DecisionBlock,
					Reason:    fmt.Sprintf("hook %q produced invalid JSON: %v", entry.Command, err),
					HookCount: len(matched),
				}, nil
			}
			out.FailedOpen = true
			continue
		}

		if parsed.Decision == DecisionBlock || parsed.Decision == DecisionHalt {
			return Output{
				Decision:   parsed.Decision,
				Reason:     parsed.Reason,
				Continue:   parsed.Continue,
				Message:    parsed.Message,
				HookCount:  len(matched),
				FailedOpen: out.FailedOpen,
			}, nil
		}

		if len(parsed.Rewrite) > 0 {
			return Output{
				Decision:   out.Decision,
				Rewrite:    parsed.Rewrite,
				Continue:   parsed.Continue,
				Message:    parsed.Message,
				HookCount:  len(matched),
				FailedOpen: out.FailedOpen,
			}, nil
		}
	}

	return out, nil
}

func (r *Runner) matchingEntries(event, value string) []HookEntry {
	var matched []HookEntry
	for _, entry := range r.cfg.Entries {
		if entry.Event != event {
			continue
		}
		if event != EventPreToolUse {
			matched = append(matched, entry)
			continue
		}
		if match(entry.Matcher, value) {
			matched = append(matched, entry)
		}
	}
	return matched
}

func match(pattern, value string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	ok, err := filepath.Match(pattern, value)
	return err == nil && ok
}

func runHook(ctx context.Context, command string, payload any) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdout := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = &bytes.Buffer{}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	if err := json.NewEncoder(stdin).Encode(payload); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return nil, err
	}
	_ = stdin.Close()

	if err := cmd.Wait(); err != nil {
		return stdout.Bytes(), err
	}

	return stdout.Bytes(), nil
}
