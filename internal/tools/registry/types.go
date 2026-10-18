package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type RiskLevel string

const (
	RiskReadOnly       RiskLevel = "read_only"
	RiskWorkspaceWrite RiskLevel = "workspace_write"
	RiskCommand        RiskLevel = "command"
	RiskNetwork        RiskLevel = "network"
	RiskDestructive    RiskLevel = "destructive"
	// RiskEnvironment marks commands that mutate state OUTSIDE the
	// working directory (build/package caches, global config, system
	// package managers). It always requires explicit approval, even in
	// auto-approve modes: those modes own the workspace, not the machine.
	RiskEnvironment RiskLevel = "environment"
)

func (r RiskLevel) Valid() bool {
	switch r {
	case RiskReadOnly, RiskWorkspaceWrite, RiskCommand, RiskNetwork, RiskDestructive, RiskEnvironment:
		return true
	default:
		return false
	}
}

type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Risk        RiskLevel
	Cacheable   bool
	Deferred    bool
	// Internal tools are dispatchable by the runtime (envelope-mode patch
	// actions, role views that re-expose them) but are never offered to the
	// model: no tool definition, no prompt listing, no deferred announcement.
	Internal bool
	Handler  ToolHandler

	// validator is the compiled form of Schema, built once by Register.
	// Shared by pointer through cloneTool: jsonschema.Schema is immutable
	// after compilation and safe for concurrent use.
	validator *Validator
}

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

// SymbolRef identifies one symbol a tool call touched.
//
// Line and Col are the position of the symbol's *name*, in LSP's 0-based
// convention, so a references query can be issued without re-deriving it.
// Resolved is false when the position could not be determined; consumers
// must then use the name for display only and must NOT issue a query — a
// query at a guessed position returns confidently wrong results.
type SymbolRef struct {
	File     string
	Name     string
	Kind     string // "function", "method", "type"
	Receiver string // e.g. "*Scanner"; empty for non-methods
	Line     int    // 0-based (LSP)
	Col      int    // 0-based
	Resolved bool
}

// ToolNotice is a structured, machine-readable companion to a tool result's
// human-readable footer. Kind is a stable string so consumers can switch on
// it without parsing Text.
type ToolNotice struct {
	Kind string         `json:"kind"`
	Text string         `json:"text"`
	Data map[string]any `json:"data,omitempty"`
}

const (
	NoticeOversizeFallback = "oversize_fallback"
	NoticeSliceTruncated   = "slice_truncated"
	NoticeZeroMatchCoach   = "zero_match_coach"
	NoticeCappedResults    = "capped_results"
	// NoticeContextClamped marks a parameter-validation clamp: the caller
	// asked for a context window outside the supported range and the tool
	// silently coerced it into range. Distinct from NoticeCappedResults,
	// which marks result-set truncation, so consumers can switch cleanly.
	NoticeContextClamped = "context_clamped"
	// NoticeCommandExecution marks a structured command-execution record: a
	// managed launch or wait continuation carries its execution identity,
	// origin call, attachment and (once settled) outcome in Data rather than
	// in prose. Its Text is deliberately empty — this is metadata, not
	// coaching — so no footer is appended to the model's tool result.
	NoticeCommandExecution = "command_execution"
)

// CommandExecutionKey is the ToolNotice.Data key holding the structured
// command-execution payload. One key keeps the data map forward-compatible:
// a consumer that does not know the key ignores the payload rather than
// misreading it.
const CommandExecutionKey = "execution"

// CommandExecutionVersion is the schema version written into every
// CommandExecution payload, so a later shape can be recognized rather than
// guessed at.
const CommandExecutionVersion = 1

// ExecutionAttachment is how the launching caller related to a command
// execution when its record was written. It mirrors native.Attachment — this
// package cannot import native, which imports it — and the values are the
// stable strings the payload and its consumers switch on.
type ExecutionAttachment string

const (
	// ExecutionAttachmentForeground means the launching caller was still
	// waiting for the command's outcome.
	ExecutionAttachmentForeground ExecutionAttachment = "foreground"
	// ExecutionAttachmentBackground means the caller stopped waiting and the
	// command continues as a detached job owned by the conversation service.
	ExecutionAttachmentBackground ExecutionAttachment = "background"
)

// CommandExecution is the structured, machine-readable record of one managed
// command execution: what was launched, who launched it and how it ended. It
// rides in ToolNotice.Data under CommandExecutionKey, which is what lets a
// continuation, its eventual completion, and a persisted audit row be paired
// by identity rather than by timestamp order — display job IDs alone repeat
// across resumes.
//
// Every field is optional except the identity ones. An empty StartedAt or
// ExitCode means "not known", never a zero-valued fact: a start-up failure has
// no exit code, and a running command has no end.
type CommandExecution struct {
	// Version is the payload schema version (CommandExecutionVersion).
	Version int `json:"version"`
	// ExecutionID is the manager's execution identity, stable across a
	// foreground wait that hands off to the background.
	ExecutionID string `json:"execution_id,omitempty"`
	// DisplayID is the user-facing job ID ("job-4"). It is not an identity:
	// it repeats in every process, so it is display and legacy-fallback only.
	DisplayID string `json:"display_id,omitempty"`
	// ConversationID is the owning conversation, which outlives the turn.
	ConversationID string `json:"conversation_id,omitempty"`
	// Actor is the parent agent, a named subagent, or a pipeline worker.
	Actor string `json:"actor,omitempty"`
	// TurnID, StepID and ToolCallID identify the originating call.
	TurnID     string `json:"turn_id,omitempty"`
	StepID     string `json:"step_id,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Cwd and Generation are the captured working directory and runtime
	// config generation at admission.
	Cwd        string `json:"cwd,omitempty"`
	Generation int    `json:"generation,omitempty"`
	// PID is the process identity, published when the process starts.
	PID int `json:"pid,omitempty"`
	// StartedAt is the admission time; EndedAt is the settlement time.
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	// State is the lifecycle status ("running", "stopping", "completed",
	// "failed", "killed", "timed_out"); Outcome is the finer cause
	// ("nonzero_exit", "startup_failure", "stopped", "resource_limit", …).
	State   string `json:"state,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Wait    string `json:"wait,omitempty"`
	// Attachment is "foreground" while the caller waits, "background" once
	// it detached.
	Attachment ExecutionAttachment `json:"attachment,omitempty"`
	// DetachReason and DetachedAt record why and when a wait handed off.
	DetachReason string    `json:"detach_reason,omitempty"`
	DetachedAt   time.Time `json:"detached_at,omitempty"`
	// StopCause and StopRequestedAt record an accepted explicit stop
	// request, separately from the outcome it produced.
	StopCause       string    `json:"stop_cause,omitempty"`
	StopRequestedAt time.Time `json:"stop_requested_at,omitempty"`
	// ExitCode is the actual exit code when one is meaningful. Nil means the
	// command never produced one (a start-up failure) or has not settled.
	ExitCode *int `json:"exit_code,omitempty"`
	// Truncated reports whether retained output hit its cap.
	Truncated bool `json:"truncated,omitempty"`
}

// NewCommandExecutionNotice builds the notice that carries a structured
// execution record. Its Text is empty by construction: an execution record is
// metadata, and the summariser only appends a coaching footer for a non-empty
// Text.
func NewCommandExecutionNotice(ex CommandExecution) *ToolNotice {
	ex.Version = CommandExecutionVersion
	return &ToolNotice{
		Kind: NoticeCommandExecution,
		Data: map[string]any{CommandExecutionKey: ex},
	}
}

// WithExecution attaches (or replaces) the structured execution record on a
// notice, preserving the notice's own Kind and Text.
func (n *ToolNotice) WithExecution(ex CommandExecution) *ToolNotice {
	if n == nil {
		return NewCommandExecutionNotice(ex)
	}
	ex.Version = CommandExecutionVersion
	if n.Data == nil {
		n.Data = make(map[string]any, 1)
	}
	n.Data[CommandExecutionKey] = ex
	return n
}

// Execution decodes the structured execution record a notice carries, if any.
// It is the read side of WithExecution and tolerates every form the payload
// takes: the in-memory struct, a pointer to it, and the map (with JSON
// numbers as float64) a persisted notice decodes to.
func (n *ToolNotice) Execution() (CommandExecution, bool) {
	return DecodeCommandExecution(n)
}

// DecodeCommandExecution reads the structured execution record from a notice.
// A nil notice, a missing key, an unrecognised shape or a payload claiming a
// version this build does not know returns ok=false so callers fall back to
// their legacy path instead of misreading the data.
func DecodeCommandExecution(n *ToolNotice) (CommandExecution, bool) {
	if n == nil || len(n.Data) == 0 {
		return CommandExecution{}, false
	}
	return commandExecutionFromValue(n.Data[CommandExecutionKey])
}

// commandExecutionFromValue decodes one stored payload value in any of its
// forms: the in-memory struct (what EncodeExecution writes), a pointer to it,
// or the map form a JSON round trip produces.
func commandExecutionFromValue(v any) (CommandExecution, bool) {
	switch t := v.(type) {
	case nil:
		return CommandExecution{}, false
	case CommandExecution:
		return t, t.Version == CommandExecutionVersion || t.Version == 0
	case *CommandExecution:
		if t == nil {
			return CommandExecution{}, false
		}
		return *t, t.Version == CommandExecutionVersion || t.Version == 0
	case map[string]any:
		ex := CommandExecution{
			Version:         noticeInt(t, "version"),
			ExecutionID:     noticeString(t, "execution_id"),
			DisplayID:       noticeString(t, "display_id"),
			ConversationID:  noticeString(t, "conversation_id"),
			Actor:           noticeString(t, "actor"),
			TurnID:          noticeString(t, "turn_id"),
			StepID:          noticeString(t, "step_id"),
			ToolCallID:      noticeString(t, "tool_call_id"),
			Cwd:             noticeString(t, "cwd"),
			Generation:      noticeInt(t, "generation"),
			PID:             noticeInt(t, "pid"),
			StartedAt:       noticeTime(t, "started_at"),
			EndedAt:         noticeTime(t, "ended_at"),
			State:           noticeString(t, "state"),
			Outcome:         noticeString(t, "outcome"),
			Wait:            noticeString(t, "wait"),
			Attachment:      ExecutionAttachment(noticeString(t, "attachment")),
			DetachReason:    noticeString(t, "detach_reason"),
			DetachedAt:      noticeTime(t, "detached_at"),
			StopCause:       noticeString(t, "stop_cause"),
			StopRequestedAt: noticeTime(t, "stop_requested_at"),
			Truncated:       noticeBool(t, "truncated"),
		}
		if code, ok := noticeOptionalInt(t, "exit_code"); ok {
			ex.ExitCode = &code
		}
		if ex.Version != CommandExecutionVersion && ex.Version != 0 {
			return CommandExecution{}, false
		}
		return ex, true
	default:
		return CommandExecution{}, false
	}
}

// noticeString reads a string field from a decoded payload map.
func noticeString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// noticeInt reads an integer field from a decoded payload map. JSON numbers
// arrive as float64, and an in-memory writer may have stored any int width, so
// all numeric forms are accepted.
func noticeInt(m map[string]any, key string) int {
	n, _ := noticeOptionalInt(m, key)
	return n
}

func noticeOptionalInt(m map[string]any, key string) (int, bool) {
	switch v := m[key].(type) {
	case nil:
		return 0, false
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	case string:
		// A persisted payload may carry a numeric string (a JSON writer
		// configured for strings); it is still a number, not a fact we
		// should guess at.
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// noticeBool reads a boolean field from a decoded payload map.
func noticeBool(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	case float64:
		return v != 0
	default:
		return false
	}
}

// noticeTime reads a timestamp field, accepting the timestamp itself, its
// RFC3339 text form (what this package's JSON tags write), and unix seconds or
// milliseconds as numbers.
func noticeTime(m map[string]any, key string) time.Time {
	switch v := m[key].(type) {
	case nil:
		return time.Time{}
	case time.Time:
		return v
	case *time.Time:
		if v == nil {
			return time.Time{}
		}
		return *v
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
		return time.Time{}
	default:
		n, ok := noticeOptionalInt(m, key)
		if !ok || n == 0 {
			return time.Time{}
		}
		return unixTime(n)
	}
}

// unixTime converts a numeric timestamp to a time, guessing the unit the way a
// reader must: a value that fits in 1e11 is seconds (year 5138 is still
// seconds), anything larger is milliseconds.
func unixTime(n int) time.Time {
	if n < 1e11 {
		return time.Unix(int64(n), 0).UTC()
	}
	return time.UnixMilli(int64(n)).UTC()
}

// encodeExecutionPayload validates and copies a payload for storage. It exists
// so an execution record is only ever written through one path, which stamps
// the current version and reports rather than stores an unusable identity.
func encodeExecutionPayload(ex CommandExecution) (CommandExecution, error) {
	if strings.TrimSpace(ex.ExecutionID) == "" && strings.TrimSpace(ex.DisplayID) == "" {
		return CommandExecution{}, fmt.Errorf("command execution payload needs an execution or display id")
	}
	ex.Version = CommandExecutionVersion
	if ex.ExitCode != nil {
		code := *ex.ExitCode
		ex.ExitCode = &code
	}
	return ex, nil
}

// EncodeCommandExecution validates a structured execution record and returns
// the ToolNotice.Data map that carries it. It is the one write path for the
// payload: the version is stamped here, and a payload with no identity at all
// is rejected rather than stored as an unpairable record.
func EncodeCommandExecution(ex CommandExecution) (map[string]any, error) {
	encoded, err := encodeExecutionPayload(ex)
	if err != nil {
		return nil, err
	}
	return map[string]any{CommandExecutionKey: encoded}, nil
}

// CommandExecutionNotice encodes ex and wraps it in a notice in one step.
func CommandExecutionNotice(ex CommandExecution) (*ToolNotice, error) {
	data, err := EncodeCommandExecution(ex)
	if err != nil {
		return nil, err
	}
	return &ToolNotice{Kind: NoticeCommandExecution, Data: data}, nil
}

type ToolResult struct {
	Summary         string
	Content         string
	Error           string
	FilesChanged    []string
	CommandExitCode *int
	Sandbox         SandboxMeta
	// Notice carries optional machine-readable guidance about a soft-failure
	// or fallback the tool applied (e.g. oversized-file head fallback,
	// zero-match coaching, capped results). Nil on the happy path. The Text
	// mirrors whatever human-readable footer was appended to Content; Data
	// holds kind-specific structured facts so consumers do not parse prose.
	Notice *ToolNotice
	// Symbols are the symbols this call touched, when the tool and the
	// file's language support attribution. Always safe to be empty.
	Symbols []SymbolRef
}

// SandboxMeta records how a command was executed under the Milestone Q
// sandbox. It is populated by sandbox backends (restricted, container,
// passthrough) and persisted into the audit trail so users can see, per
// command, which backend ran it and whether network/filesystem were
// actually isolated. Honest capability reporting matters more than
// claiming isolation that isn't enforced (e.g. restricted mode cannot
// block network cross-platform).
type SandboxMeta struct {
	Enabled            bool
	Backend            string
	NetworkIsolated    bool
	FilesystemIsolated bool
	ResourceLimits     bool
	MemoryLimitBytes   int64
	CPUSeconds         int
	MaxProcesses       int
	KilledReason       string
	DurationMS         int64
	OutputTruncated    bool
}

func (m SandboxMeta) LimitsJSON() (string, error) {
	limits := map[string]any{"backend": m.Backend}
	if m.MemoryLimitBytes > 0 {
		limits["memory_limit_bytes"] = m.MemoryLimitBytes
	}
	if m.CPUSeconds > 0 {
		limits["cpu_seconds"] = m.CPUSeconds
	}
	if m.MaxProcesses > 0 {
		limits["max_processes"] = m.MaxProcesses
	}
	if m.NetworkIsolated {
		limits["network_isolated"] = true
	}
	if m.FilesystemIsolated {
		limits["filesystem_isolated"] = true
	}
	if m.KilledReason != "" {
		limits["killed_reason"] = m.KilledReason
	}
	if m.OutputTruncated {
		limits["output_truncated"] = true
	}
	b, err := json.Marshal(limits)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
