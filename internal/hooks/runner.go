package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/sandbox/envutil"
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

		if parsed.Continue {
			out.Continue = true
			out.Message = parsed.Message
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

// maxHookOutputBytes caps the bytes captured from a single hook's
// stdout. A hook that exceeds the cap is treated as a hook error
// (subject to fail-open/fail-closed), preventing an unbounded buffer
// from OOMing the Marshal process. 1 MiB is generous for a JSON
// decision payload while bounding the worst case.
const maxHookOutputBytes = 1 << 20

func runHook(ctx context.Context, command string, payload any) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = scrubHookEnv(os.Environ())

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	cmd.Stderr = &limitedBuffer{max: maxHookOutputBytes}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	if err := json.NewEncoder(stdin).Encode(payload); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return nil, err
	}
	_ = stdin.Close()

	stdout := &limitedBuffer{max: maxHookOutputBytes}
	_, _ = io.Copy(stdout, stdoutPipe)

	if err := cmd.Wait(); err != nil {
		return stdout.bytes(), err
	}

	if stdout.truncated {
		return stdout.bytes(), fmt.Errorf("hook output exceeded %d bytes", maxHookOutputBytes)
	}
	return stdout.bytes(), nil
}

// scrubHookEnv returns a copy of parentEnv with secret-bearing vars
// removed. Mirrors the sandbox's nil-allowlist scrub so hooks and
// sandboxed shell commands agree on what counts as a secret. Hooks are
// project-config files that may be committed to a repo, so they are not
// necessarily authored by the user running Marshal — scrubbing prevents
// a hook from reading provider API keys via $OPENAI_API_KEY etc.
func scrubHookEnv(parentEnv []string) []string {
	out := make([]string, 0, len(parentEnv))
	for _, kv := range parentEnv {
		key := envutil.EnvKey(kv)
		if envutil.IsSecretBearer(key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// limitedBuffer is a bytes.Buffer wrapper that stops accepting bytes
// once max is reached, recording that it was truncated. Write calls
// after the cap are silently dropped (the subprocess keeps running but
// its output is bounded).
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	remaining := l.max - l.buf.Len()
	if remaining <= 0 {
		l.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		l.buf.Write(p[:remaining])
		l.truncated = true
		return len(p), nil
	}
	return l.buf.Write(p)
}

func (l *limitedBuffer) bytes() []byte {
	return l.buf.Bytes()
}
