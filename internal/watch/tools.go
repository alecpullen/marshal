package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/tools/registry"
)

// watchStartArgs is the decoded argument set for watch.start.
type watchStartArgs struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Command   string `json:"command"`
	JobID     string `json:"job_id"`
	Path      string `json:"path"`
	Condition string `json:"condition"`
	Mode      string `json:"mode"`
	Notify    *bool  `json:"notify"`
	Interval  string `json:"interval"`
}

// watchIDArgs is the decoded argument set for watch.status and watch.stop.
type watchIDArgs struct {
	WatchID string `json:"watch_id"`
}

// RegisterTools registers the four watch.* tools into reg. The manager must
// be non-nil. It is called from app.go after native.RegisterAll because the
// watch package imports internal/tools/native (for JobEvent/JobInfo), so the
// native toolset cannot import watch without an import cycle.
func RegisterTools(reg *registry.Registry, m *Manager) error {
	tools := []registry.Tool{
		watchStartTool(m),
		watchListTool(m),
		watchStatusTool(m),
		watchStopTool(m),
	}
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

// JobWatchingAvailable reports whether job watches can be registered. It is
// false when no job event broker was wired (e.g. headless tests), in which
// case watch.start rejects job watches with a clear error rather than
// registering an inert watch.
func (m *Manager) JobWatchingAvailable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deps.SubscribeJobs != nil
}

func watchStartTool(m *Manager) registry.Tool {
	tool := registry.Tool{
		Name: "watch.start",
		Description: "Register a background watch that samples a source (command, job, or file) " +
			"and fires when a condition trips. kind is one of command, job, file. " +
			"For command watches, command is the shell command to sample. For job watches, " +
			"job_id is the background job to watch (fires on terminal state). For file watches, " +
			"path is a file path or glob. condition defaults to \"change\"; other forms: " +
			"\"exit_code N\", \"regex PATTERN\", \"json PATH OP VALUE\". mode is once (default) " +
			"or repeat. interval is a duration string like \"5s\" (clamped to a 2s floor).",
		Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"kind":{"type":"string","enum":["command","job","file"]},"command":{"type":"string"},"job_id":{"type":"string"},"path":{"type":"string"},"condition":{"type":"string"},"mode":{"type":"string","enum":["once","repeat"]},"notify":{"type":"boolean"},"interval":{"type":"string"}},"required":["name","kind"],"additionalProperties":false}`),
		Risk:   registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[watchStartArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}

		var kind Kind
		switch args.Kind {
		case string(KindCommand):
			kind = KindCommand
		case string(KindJob):
			kind = KindJob
		case string(KindFile):
			kind = KindFile
		default:
			return registry.ToolResult{}, fmt.Errorf("unknown watch kind %q", args.Kind)
		}

		// Job watches require the job event broker; reject with a clear error
		// when it is not wired rather than registering an inert watch.
		if kind == KindJob && !m.JobWatchingAvailable() {
			return registry.ToolResult{}, fmt.Errorf("job watching unavailable: no job event broker is wired")
		}

		var mode Mode
		switch args.Mode {
		case "", string(ModeOnce):
			mode = ModeOnce
		case string(ModeRepeat):
			mode = ModeRepeat
		default:
			return registry.ToolResult{}, fmt.Errorf("unknown watch mode %q", args.Mode)
		}

		var interval time.Duration
		if args.Interval != "" {
			interval, err = time.ParseDuration(args.Interval)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("invalid interval %q: %v", args.Interval, err)
			}
		}

		spec := Spec{
			Name:      args.Name,
			Kind:      kind,
			Command:   args.Command,
			JobID:     args.JobID,
			Path:      args.Path,
			Condition: args.Condition,
			Mode:      mode,
			Notify:    args.Notify,
			Interval:  interval,
			// A subagent's watch.start calls carry the owner tag on the
			// context (set by the agent.run handler via watch.WithOwner);
			// the parent's calls carry none and stay owner "".
			Owner: OwnerFromContext(ctx),
		}
		id, note, err := m.Start(spec)
		if err != nil {
			return registry.ToolResult{}, err
		}
		content := fmt.Sprintf("watch_id: %s\nkind: %s\nname: %s", id, kind, spec.Name)
		if note != "" {
			content += "\nnote: " + note
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("started watch %s (%s)", id, kind),
			Content: content,
		}, nil
	}
	return tool
}

func watchListTool(m *Manager) registry.Tool {
	tool := registry.Tool{
		Name:        "watch.list",
		Description: "List all registered background watches and their states.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		watches := m.List()
		var b strings.Builder
		for _, w := range watches {
			line := fmt.Sprintf("%s  %s  kind=%s  state=%s", w.ID, w.Name, w.Kind, w.State)
			if w.Interval > 0 {
				line += fmt.Sprintf("  interval=%s", w.Interval)
			}
			if w.Condition != "" {
				line += fmt.Sprintf("  cond=%s", w.Condition)
			}
			line += fmt.Sprintf("  fires=%d", w.FireCount)
			b.WriteString(line + "\n")
		}
		content := b.String()
		if content == "" {
			content = "no watches"
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("%d watch(es)", len(watches)),
			Content: content,
		}, nil
	}
	return tool
}

func watchStatusTool(m *Manager) registry.Tool {
	tool := registry.Tool{
		Name:        "watch.status",
		Description: "Return the current state and history for a single watch.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"watch_id":{"type":"string"}},"required":["watch_id"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[watchIDArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		info, err := m.Status(args.WatchID)
		if err != nil {
			return registry.ToolResult{}, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "watch_id: %s\nname: %s\nkind: %s\nstate: %s\n", info.ID, info.Name, info.Kind, info.State)
		fmt.Fprintf(&b, "fires: %d\n", info.FireCount)
		if info.Condition != "" {
			fmt.Fprintf(&b, "condition: %s\n", info.Condition)
		}
		if info.Interval > 0 {
			fmt.Fprintf(&b, "interval: %s\n", info.Interval)
		}
		if info.LastSample != "" {
			fmt.Fprintf(&b, "last_sample: %s\n", info.LastSample)
		}
		if info.LastError != "" {
			fmt.Fprintf(&b, "last_error: %s\n", info.LastError)
		}
		if !info.CreatedAt.IsZero() {
			fmt.Fprintf(&b, "created_at: %s\n", info.CreatedAt.Format(time.RFC3339))
		}
		if !info.LastFiredAt.IsZero() {
			fmt.Fprintf(&b, "last_fired_at: %s\n", info.LastFiredAt.Format(time.RFC3339))
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("watch %s is %s", info.ID, info.State),
			Content: b.String(),
		}, nil
	}
	return tool
}

func watchStopTool(m *Manager) registry.Tool {
	tool := registry.Tool{
		Name:        "watch.stop",
		Description: "Stop a background watch by watch ID. Idempotent: stopping an unknown or already-stopped watch succeeds with a note.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"watch_id":{"type":"string"}},"required":["watch_id"],"additionalProperties":false}`),
		Risk:        registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[watchIDArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		note, err := m.Stop(args.WatchID)
		if err != nil {
			return registry.ToolResult{}, err
		}
		content := fmt.Sprintf("watch_id: %s stopped", args.WatchID)
		if note != "" {
			content += "\nnote: " + note
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("stopped watch %s", args.WatchID),
			Content: content,
		}, nil
	}
	return tool
}

// decodeArgs validates and decodes a tool call's arguments against the tool's
// schema. It mirrors the native package's unexported decodeArgs helper.
func decodeArgs[T any](tool registry.Tool, raw json.RawMessage) (T, error) {
	var zero T
	if err := registry.ValidateArgs(tool, raw); err != nil {
		return zero, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, &zero); err != nil {
		return zero, fmt.Errorf("decode %s arguments: %w", tool.Name, err)
	}
	return zero, nil
}
