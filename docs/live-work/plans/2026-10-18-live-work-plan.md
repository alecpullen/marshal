# Live work — implementation plan

Date: 2026-10-18. Status: ready for plan review; production implementation not started.
Base: `5b71f0297205325600ef0319a28dcf3b34524ed5`.
Approved specification: [feature-spec.md](../feature-spec.md).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md), [work-decomposition](../../../internal/skills/builtin/work-decomposition.md).

**Goal:** give subagents, detached commands and watches one pinned gutter-card surface, durable result access and optional measured progress that continues while the agent waits.

**Non-goals:** another execution supervisor; new deadlines/retries; arbitrary agent-rendered UI/actions; standalone widgets; retroactive decoration of external processes; universal meter parsers; fleet SPA redesign; narration deduplication.

**Context delivery:** launch and terminal summaries only. Intermediate progress stays in the presentation snapshot; this plan does not add progress to model context or prompts.

**Prerequisites:** reliable background execution and access approval are merged. Execute this plan after the submitted [Send now plan](../../send-now/plans/2026-10-18-send-now-plan.md) lands, so prompt identities/composer routing have one implementation. Its names may be adapted to the equivalent merged declarations; do not reimplement its controller. The baseline already contains mid-turn effort; keep its /effort, Alt+E and route/prompt behavior.

All tasks run sequentially. They are interface-, file- or build-coupled; none is parallel-safe. Each task runs its checks and commits before the next: `live-work: task N — <title>`. Do not commit unrelated untracked files. Authoring this plan changes documentation only.

Before each task's tests/commit, run gofmt on that task's edited Go files and git diff --check. Do not format or stage unrelated concurrent work.

**Integration clarification:** source inspection found durable subagent report messages but no restored durable SubagentView registry. Add an optional `agentResults` index to `work_view_v1`, holding stable identity/origin and report-message references, not another full report. This fulfills the spec's retained-result guarantee. Store/restore missing full reports as explicitly unavailable; never infer identity from `subagent N` across restarts.

## Contracts

Declarations below pin every cross-task boundary. The complete file in C1 is pasted when first created. Later judgment tasks extend that existing file at the named symbols; signatures elsewhere are implementation contracts, not stubs to paste.

### C1. Progress values and configuration — Task 1

Create `internal/progress/progress.go` with this complete file:

```go
// Package progress holds bounded display data. It never owns execution.
package progress

import (
    "crypto/sha256"
    "encoding/base64"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "strings"
    "time"
    "unicode"
    "unicode/utf8"

    "github.com/charmbracelet/x/ansi"
    "marshal/internal/redact"
)

const (
    Version = 1
    Prefix = "@@marshal-progress/v1 "
    MaxNumber = float64(1<<53 - 1)
    MaxEventBytes = 16 << 10
    MaxGoLineBytes = 64 << 10
    MaxDisplayBytes = 8 << 10
    MaxCases = 20000
    FreshFor = 30 * time.Second
)
var ErrInvalid = errors.New("invalid progress data")
var ErrSequence = errors.New("stale progress sequence")

type Config struct {
    Profile string `json:"profile,omitempty"`
    Source string `json:"source,omitempty"`
    Title string `json:"title,omitempty"`
}
type Phase struct {
    ID string `json:"id"`
    Label string `json:"label"`
    Index int `json:"index,omitempty"`
    Total int `json:"total,omitempty"`
}
type Metric struct {
    ID string `json:"id"`
    Label string `json:"label"`
    Unit string `json:"unit"`
    Current float64 `json:"current"`
    Total *float64 `json:"total,omitempty"`
    Rate *float64 `json:"rate,omitempty"`
}
type Row struct {
    ID string `json:"id"`
    Label string `json:"label"`
    State string `json:"state"`
}
// Frame is the producer's complete snapshot. Runtime facts are excluded.
type Frame struct {
    Version int `json:"version"`
    Sequence uint64 `json:"sequence"`
    Phase *Phase `json:"phase,omitempty"`
    PrimaryMetricID string `json:"primaryMetricId,omitempty"`
    Metrics []Metric `json:"metrics,omitempty"`
    Rows []Row `json:"rows,omitempty"`
    Detail string `json:"detail,omitempty"`
}
type Snapshot struct {
    Frame
    ObservedAt time.Time `json:"observedAt"`
    MeasuredAt time.Time `json:"measuredAt,omitempty"`
    Partial bool `json:"partial,omitempty"`
    Warning string `json:"warning,omitempty"`
    ReportedError bool `json:"reportedError,omitempty"`
}
type Estimate struct {
    Fraction *float64
    Rate *float64
    ETA *time.Duration
    Stale bool
}

// Clean is the common display/persistence boundary; callers keep raw identity.
func Clean(s string, limit int, multiline bool) string {
    s = ansi.Strip(s)
    s = strings.Map(func(r rune) rune {
        if r == '\n' || r == '\t' {
            if multiline { return r }
            return ' '
        }
        if r == '\r' { return -1 }
        if unicode.IsControl(r) { return -1 }
        return r
    }, strings.ToValidUTF8(s, "�"))
    s = redact.Secrets(s)
    if limit > 0 && utf8.RuneCountInString(s) > limit {
        s = string([]rune(s)[:limit])
    }
    return s
}
func NormalizeConfig(c Config) (Config, error) {
    if c.Profile == "" { c.Profile = "generic" }
    if c.Source == "" { c.Source = "none" }
    switch c.Profile {
    case "generic", "tests", "download", "transfer", "custom":
    default: return Config{}, fmt.Errorf("%w: profile", ErrInvalid)
    }
    switch c.Source {
    case "none":
    case "events-v1":
        if c.Profile == "generic" { return Config{}, fmt.Errorf("%w: events profile", ErrInvalid) }
    case "go-test-json":
        if c.Profile != "tests" { return Config{}, fmt.Errorf("%w: test source/profile", ErrInvalid) }
    default: return Config{}, fmt.Errorf("%w: source", ErrInvalid)
    }
    if !utf8.ValidString(c.Title) || utf8.RuneCountInString(c.Title) > 80 {
        return Config{}, fmt.Errorf("%w: title", ErrInvalid)
    }
    c.Title = Clean(c.Title, 80, false)
    return c, nil
}
func Clone(s *Snapshot) *Snapshot {
    if s == nil { return nil }
    out := *s
    if s.Phase != nil { p := *s.Phase; out.Phase = &p }
    out.Metrics = append([]Metric(nil), s.Metrics...)
    for i := range out.Metrics {
        if p := s.Metrics[i].Total; p != nil { v := *p; out.Metrics[i].Total = &v }
        if p := s.Metrics[i].Rate; p != nil { v := *p; out.Metrics[i].Rate = &v }
    }
    out.Rows = append([]Row(nil), s.Rows...)
    return &out
}
func WorkKey(conversation, epoch, kind, sourceID string) string {
    b, _ := json.Marshal([4]string{conversation, epoch, kind, sourceID})
    return "work:" + base64.RawURLEncoding.EncodeToString(b)
}
func ResultRevision(key, state, outcome string, exitCode *int, endedAt time.Time) string {
    b, _ := json.Marshal([]any{key, state, outcome, exitCode, endedAt.UTC().Format(time.RFC3339Nano)})
    h := sha256.Sum256(b)
    return hex.EncodeToString(h[:])
}
```

Task 2–3 add the following declarations/behavior to that file:

```go
// package progress
func DecodeEvent(line []byte, previous uint64, observed time.Time) (*Snapshot, error)
func EstimateFor(previous, current *Snapshot, metricID string, now time.Time, stopping bool) Estimate

type Stream uint8
const ( Stdout Stream = iota; Stderr )
// Observer serializes writers; dirty MUST be a nonblocking wake, not a broker.
type Observer struct {
    // Private fields: mutex; normalized config; clock; dirty callback;
    // independent bounded stdout/stderr framers; latest valid/previous snapshot;
    // display tail; last sequence; Go case/package state; closed flag.
}
func NewObserver(config Config, now func() time.Time, dirty func()) (*Observer, error)
func (o *Observer) Write(stream Stream, p []byte)
func (o *Observer) Snapshot() *Snapshot
func (o *Observer) DisplayTail() string
func (o *Observer) Close()
// Task 3's adapter state is private to Observer, never a second goroutine.
```

### C2. Evidence and stored work view — Tasks 4–8

Add these exact fields to the existing `registry.CommandExecution`, `native.JobInfo` and `session.JobExit`; DB tags below apply to `db.JobExitRecord`. Keep the outer execution version at 1 because these fields are additive.

```go
ProgressConfig *progress.Config `json:"progress_config,omitempty"`
Progress *progress.Snapshot `json:"progress,omitempty"`
ResultRevision string `json:"result_revision,omitempty"`
SourceEpoch string `json:"source_epoch,omitempty"`
```

LaunchSpec gains `Progress *progress.Config`. JobInfo additionally gains `DisplayTail string` (not a second raw output buffer exposed on every broker event).

Append these complete declarations to existing `internal/db/jobexits.go`:

```go
type WorkReference struct {
    MessageID int64 `json:"messageId,omitempty"`
    NodeKey string `json:"nodeKey,omitempty"`
    ChildSessionID string `json:"childSessionId,omitempty"`
}
type WorkResultRecord struct {
    TaskKey string `json:"taskKey"`
    SourceID string `json:"sourceId"`
    RuntimeEpoch string `json:"runtimeEpoch"`
    Kind string `json:"kind"`
    Label string `json:"label"`
    Owner string `json:"owner,omitempty"`
    TurnID string `json:"turnId,omitempty"`
    StepID string `json:"stepId,omitempty"`
    ToolCallID string `json:"toolCallId,omitempty"`
    State string `json:"state"`
    Outcome string `json:"outcome,omitempty"`
    StartedAt time.Time `json:"startedAt"`
    EndedAt time.Time `json:"endedAt,omitempty"`
    ResultRevision string `json:"resultRevision,omitempty"`
    Detail string `json:"detail,omitempty"`
    Reference WorkReference `json:"reference"`
}
type WorkAcknowledgment struct {
    TaskKey string `json:"taskKey"`
    ResultRevision string `json:"terminalResultRevision"`
}
type WorkViewRecord struct {
    Version int `json:"version"`
    WatchResults []WorkResultRecord `json:"watchResults"`
    AgentResults []WorkResultRecord `json:"agentResults,omitempty"`
    Acknowledged []WorkAcknowledgment `json:"acknowledged"`
}
func (db *DB) SaveWorkView(sessionID string, record WorkViewRecord) error
func (db *DB) LoadWorkView(sessionID string) (WorkViewRecord, error)
func (db *DB) WorkReport(sessionID string, messageID int64) (string, error)
```

Session's shared methods/fields are:

```go
// package session
func (s *State) WorkView() db.WorkViewRecord
func (s *State) SaveWatchWorkResult(record db.WorkResultRecord) error
func (s *State) AcknowledgeWork(taskKey, resultRevision string) error
func (s *State) RetryWorkViewSave() error
func (s *State) BindSubagentReport(id int64, report string) error
func (s *State) JobExitSnapshot() []JobExit

type SubagentOrigin struct { TurnID, StepID, ToolCallID string }
type SubagentWorkFacts struct {
    WorkKey string
    RuntimeEpoch string
    Origin SubagentOrigin
    ResultRevision string
    Report db.WorkReference
    StopRequested bool
    Outcome string
}
type SubagentSnapshot struct {
    Epoch string
    Revision uint64
    Views []SubagentView
    Settling []string
    SaveErrors map[string]string
}
func (s *State) SubagentSnapshot() SubagentSnapshot
// Embed SubagentWorkFacts in existing SubagentView.
// Replace the existing SubagentMeta and SubagentEvent with:
type SubagentMeta struct {
    Role routing.AgentRole
    Provider string
    Model string
    Fallback bool
    Origin SubagentOrigin
}
type SubagentEvent struct {
    View SubagentView
    Epoch string
    Revision uint64
}
```

Append to existing registry/types.go in Task 4. The notice data key is exactly "subagent_work"; Task 8 writes it, Task 15 reads it through the codec, including after JSON persistence.

```go
type SubagentWorkReference struct {
    TaskKey string `json:"taskKey"`
    RuntimeEpoch string `json:"runtimeEpoch"`
    AgentID int64 `json:"agentId"`
    ChildSessionID string `json:"childSessionId,omitempty"`
}
const NoticeSubagentWork = "subagent_work"
func (n *ToolNotice) SubagentWork() (SubagentWorkReference, bool)
```

Do not put full agent reports in work_view_v1. BindSubagentReport finds the exact already-appended `ContentTypeSubagentReport` message for that live ID, at/after its registration time, and records its persisted DBID. A missing/unpersisted message is explicitly unavailable, not a fabricated reference.

### C3. Source snapshots — Tasks 9 and 11

```go
// package native
type JobSnapshot struct {
    Epoch string
    Revision uint64
    Jobs []JobInfo
    Settling []string
    SaveErrors map[string]string
}
func (m *JobManager) Snapshot() JobSnapshot
// Add Epoch/Revision to JobEvent. Revision versions lifecycle/membership;
// Progress.Sequence versions progress independently.

// package watch
type WorkFacts struct {
    Epoch string
    Revision uint64
    Active bool
    EndedAt time.Time
    TerminalReason string
    ResultRevision string
}
type Snapshot struct {
    Epoch string
    Revision uint64
    Active []Info
    Terminal []Info
    Settling []string
    SaveErrors map[string]string
}
func (m *Manager) Snapshot() Snapshot
func (m *Manager) RetryTerminalSaves() error
// Embed WorkFacts in existing Info and Event.
// Exact added Event field: Info *Info.
// Exact added Deps field: OnTerminal func(Info) error.
// Exact added Deps field: ConversationID string.
```

Source constructors create a crypto/rand.Text epoch once per source lifetime; reload retains a source's epoch when it retains the service. Snapshots are deep copies. Broker events wake the collector; it re-reads current snapshots rather than applying potentially late event copies.

Settling contains source IDs (ExecutionID for commands, registration ID for watches) whose initial durable-result sink is in flight. SaveErrors contains bounded errors after a failed attempt. The coordinator holds back these new terminal facts while Settling, then publishes either durable completion or a truthful terminal result with save-failure attention. This also gates a matching in-memory JobExit from the same polling cycle; an AddJobExit memory update cannot bypass its durable sink.

### C4. Work projection and runtime facade — Tasks 12–14

Append these declarations to existing `internal/viewmodel/band.go`; add the needed context/pubsub/progress imports. These are complete new type declarations, not replacements for existing BandInfo.

```go
type WorkOrigin struct {
    TurnID string `json:"turnId,omitempty"`
    StepID string `json:"stepId,omitempty"`
    ToolCallID string `json:"toolCallId,omitempty"`
}
type WorkReference struct {
    MessageID int64 `json:"messageId,omitempty"`
    NodeKey string `json:"nodeKey,omitempty"`
    ChildSessionID string `json:"childSessionId,omitempty"`
}
type WorkCapabilities struct {
    Inspect bool `json:"inspect"`
    Stop bool `json:"stop"`
    Acknowledge bool `json:"acknowledge"`
    ResolvePrompt bool `json:"resolvePrompt"`
}
type WorkPrompt struct {
    Kind string `json:"kind"`
    RequestID string `json:"requestId"`
    AgentID int64 `json:"agentId,omitempty"`
}
type WorkItem struct {
    TaskKey string `json:"taskKey"`
    DisplayID string `json:"displayId"`
    ConversationID string `json:"conversationId"`
    RuntimeEpoch string `json:"runtimeEpoch"`
    SourceID string `json:"sourceId"`
    SourceRevision uint64 `json:"sourceRevision"`
    Kind string `json:"kind"`
    Label string `json:"label"`
    Owner string `json:"owner,omitempty"`
    Origin WorkOrigin `json:"origin"`
    State string `json:"state"`
    Outcome string `json:"outcome,omitempty"`
    ExitCode *int `json:"exitCode,omitempty"`
    Attachment string `json:"attachment,omitempty"`
    StartedAt time.Time `json:"startedAt"`
    EndedAt time.Time `json:"endedAt,omitempty"`
    ResultRevision string `json:"terminalResultRevision,omitempty"`
    Activity string `json:"activity,omitempty"`
    OutputTail string `json:"outputTail,omitempty"`
    Warning string `json:"warning,omitempty"`
    Attention string `json:"attention,omitempty"`
    Reference WorkReference `json:"reference"`
    Prompt *WorkPrompt `json:"prompt,omitempty"`
    Capabilities WorkCapabilities `json:"capabilities"`
    ProgressConfig *progress.Config `json:"progressConfig,omitempty"`
    Progress *progress.Snapshot `json:"progress,omitempty"`
}
type WorkSnapshot struct {
    Version int `json:"version"`
    ConversationID string `json:"conversationId"`
    RuntimeEpoch string `json:"runtimeEpoch"`
    Revision uint64 `json:"revision"`
    HistoryRevision uint64 `json:"historyRevision"`
    ObservedAt time.Time `json:"observedAt"`
    Items []WorkItem `json:"items"`
    ActiveCount int `json:"activeCount"`
    AttentionCount int `json:"attentionCount"`
    SuccessReceiptCount int `json:"successReceiptCount"`
    StoppedReceiptCount int `json:"stoppedReceiptCount"`
}
// Exact field additions used by Tasks 15/18:
// Snapshot.LiveWork bool; Node.WorkKey string.
// WireTool.WorkKey string has json:"workKey,omitempty".
type WorkQuery struct {
    ConversationID string
    View string
    Query string
    Cursor string
    Limit int
}
type WorkPage struct {
    RuntimeEpoch string `json:"runtimeEpoch"`
    Revision uint64 `json:"revision"`
    Items []WorkItem `json:"items"`
    NextCursor string `json:"nextCursor,omitempty"`
}
type WorkTarget struct { ConversationID, TaskKey string }
type WorkAck struct { WorkTarget; ResultRevision string }
type WorkDetail struct {
    Item WorkItem `json:"item"`
    Output string `json:"output"`
    Raw bool `json:"raw"`
    Report string `json:"report"`
    Unavailable string `json:"unavailable"`
}
type WorkStopResult struct {
    TaskKey string `json:"taskKey"`
    State string `json:"state"`
    Summary string `json:"summary"`
    Accepted bool `json:"accepted"`
}
type WorkService interface {
    WorkSnapshot() WorkSnapshot
    SubscribeWork(context.Context) <-chan pubsub.Event[WorkSnapshot]
    ListWork(context.Context, WorkQuery) (WorkPage, error)
    InspectWork(context.Context, WorkTarget, bool) (WorkDetail, error)
    StopWork(context.Context, WorkTarget, string) (WorkStopResult, error)
    AcknowledgeWork(context.Context, WorkAck) error
}
func WorkKey(conversation, epoch, kind, sourceID string) string
func WorkResultRevision(item WorkItem) string
func CloneWorkItem(item WorkItem) WorkItem
func CloneWorkSnapshot(snapshot WorkSnapshot) WorkSnapshot
func WorkActive(item WorkItem) bool
func WorkNeedsAttention(item WorkItem) bool
func WorkReceipts(item WorkItem, now time.Time) (success, stopped bool)
func PageWork(items []WorkItem, epoch string, revision uint64, query WorkQuery) (WorkPage, error)
var ErrWorkStaleCursor = errors.New("work history changed; refresh the list")
var ErrWorkStaleTarget = errors.New("work target is unavailable in this conversation")
var ErrWorkUnsupported = errors.New("work action is unavailable")

// package app; Runtime implements WorkService.
func (rt *Runtime) WorkSnapshot() viewmodel.WorkSnapshot
func (rt *Runtime) SubscribeWork(context.Context) <-chan pubsub.Event[viewmodel.WorkSnapshot]
func (rt *Runtime) ListWork(context.Context, viewmodel.WorkQuery) (viewmodel.WorkPage, error)
func (rt *Runtime) InspectWork(context.Context, viewmodel.WorkTarget, bool) (viewmodel.WorkDetail, error)
func (rt *Runtime) StopWork(context.Context, viewmodel.WorkTarget, string) (viewmodel.WorkStopResult, error)
func (rt *Runtime) AcknowledgeWork(context.Context, viewmodel.WorkAck) error

// package tui; Task 16 supplies the option, Task 20 wires it.
func WithWork(service viewmodel.WorkService) Option
```

Runtime keeps a private conversation-owned `workCoordinator` in runtime.go. Fields: mutex, captured State/manager/watch pointers, epoch, latest snapshot, history index, acknowledgment map, source epochs/revisions, history revision, dirty wake, cancel/join and a nonterminal WorkSnapshot broker. No process handles or second stop registry. Runtime captures this pointer briefly under Runtime.mu and delegates outside it.

Task 13 defines these app-private contracts consumed by lifecycle wiring in Task 14:

```go
type workSources struct {
    Jobs *native.JobManager
    Watches *watch.Manager
    JobEvents *pubsub.Broker[native.JobEvent]
    WatchEvents *pubsub.Broker[watch.Event]
    SubagentEvents *pubsub.Broker[session.SubagentEvent]
    SessionEvents *pubsub.Broker[session.Event]
}
func newWorkCoordinator(ctx context.Context, state *session.State, sources workSources, now func() time.Time) *workCoordinator
```

`WorkKey` is `"work:" + base64.RawURLEncoding(JSON([conversation, epoch, kind, sourceID]))`. Result revision is the lower-case hex SHA-256 of JSON([taskKey,state,outcome,exitCode,endedAt.UTC().Format(time.RFC3339Nano)]); it excludes labels, progress, observation time and publication revision. Persist it; never recompute using now on resume.

viewmodel.WorkKey and WorkResultRevision delegate to progress.WorkKey/ResultRevision. Source packages use those dependency-neutral helpers, avoiding session → viewmodel import cycles. Commands persist SourceEpoch at admission. Legacy command history uses epoch "legacy" and JobExit.Identity() as source ID; it never receives stop capability.

WorkPage cursors encode epoch/history revision/view/normalized query/offset as base64url JSON. Default limit 50, max 100; query ≤200 runes; exact cursor mismatch returns ErrWorkStaleCursor. Only membership, order, safe label/owner or attention changes advance HistoryRevision; progress/age updates cannot invalidate a reader's page.

WorkSnapshot.RuntimeEpoch identifies this conversation's projection lifetime; WorkItem.RuntimeEpoch identifies the captured owning source epoch used in its key. Epochs are opaque, not sortable. Clients bind an epoch from a fresh snapshot/list or explicit service rebind before accepting its events.

### C5. UI and transport contracts

Task 16 adds private Model fields `workService viewmodel.WorkService`, `workSnapshot viewmodel.WorkSnapshot`, `workExpanded string`, and stable per-frame work hit targets keyed by TaskKey. Task 17 changes `tasksPanel` to a private wrapper implementing dock.Panel over existing docpanel/listpanel; it never performs runtime calls.

Task 18 uses these exact internal message shapes in existing events.go:

```go
type workMsg struct {
    snapshot viewmodel.WorkSnapshot
    subscriptionID uint64
}
type workRequestMsg struct {
    conversationID, panelID, requestID string
    kind, taskKey, resultRevision string
    query viewmodel.WorkQuery
    raw bool
}
type workPageMsg struct {
    conversationID, panelID, requestID string
    query viewmodel.WorkQuery
    page viewmodel.WorkPage
    err error
}
type workDetailMsg struct {
    conversationID, panelID, requestID string
    detail viewmodel.WorkDetail
    err error
}
type workActionMsg struct {
    conversationID, taskKey, resultRevision, requestID string
    kind string
    stop viewmodel.WorkStopResult
    err error
}
```

ACP's new wire DTOs duplicate all JSON fields in C4 explicitly in `internal/acp`, using primitive strings/numbers/bools/time strings and independently declared progress DTOs. No aliases to viewmodel/progress. C6 provides the complete initial wire file for Task 21; Task 22 sends those DTOs as `work_state` updates. Public methods remain the four session/work_* names in the spec.

Task 21 adds this controller in existing acp/watch.go; Task 22 uses its forwarders. Its lookup resolves a service directly from SessionManager.Get, without the prompt lookup's requirement for an available Runner.

```go
// package acp
type WorkManagerACP struct {
    lookup func(string) (viewmodel.WorkService, bool)
    notify NotifyFunc
    mu sync.Mutex
    forwarders map[string]*workForwarder
    closed bool
}
type workForwarder struct {
    cancel context.CancelFunc
    done chan struct{}
    epoch string
}
func NewWorkManagerACP(lookup func(string) (viewmodel.WorkService, bool), notify NotifyFunc) *WorkManagerACP
func (m *WorkManagerACP) List(context.Context, json.RawMessage) (any, error)
func (m *WorkManagerACP) Inspect(context.Context, json.RawMessage) (any, error)
func (m *WorkManagerACP) Stop(context.Context, json.RawMessage) (any, error)
func (m *WorkManagerACP) Acknowledge(context.Context, json.RawMessage) (any, error)
func (m *WorkManagerACP) Close()
```

### C6. Complete initial ACP wire file — Tasks 21–22

Create internal/acp/work.go in Task 21; Task 22 consumes these same declarations.

```go
package acp

import "encoding/json"

type WorkConfigWire struct {
    Profile string `json:"profile,omitempty"`
    Source string `json:"source,omitempty"`
    Title string `json:"title,omitempty"`
}
type WorkPhaseWire struct {
    ID string `json:"id"`
    Label string `json:"label"`
    Index int `json:"index,omitempty"`
    Total int `json:"total,omitempty"`
}
type WorkMetricWire struct {
    ID string `json:"id"`
    Label string `json:"label"`
    Unit string `json:"unit"`
    Current float64 `json:"current"`
    Total *float64 `json:"total,omitempty"`
    Rate *float64 `json:"rate,omitempty"`
}
type WorkRowWire struct {
    ID string `json:"id"`
    Label string `json:"label"`
    State string `json:"state"`
}
type WorkProgressWire struct {
    Version int `json:"version"`
    Sequence uint64 `json:"sequence"`
    Phase *WorkPhaseWire `json:"phase,omitempty"`
    PrimaryMetricID string `json:"primaryMetricId,omitempty"`
    Metrics []WorkMetricWire `json:"metrics,omitempty"`
    Rows []WorkRowWire `json:"rows,omitempty"`
    Detail string `json:"detail,omitempty"`
    ObservedAt string `json:"observedAt"`
    MeasuredAt string `json:"measuredAt,omitempty"`
    Partial bool `json:"partial,omitempty"`
    Warning string `json:"warning,omitempty"`
    ReportedError bool `json:"reportedError,omitempty"`
}
type WorkOriginWire struct {
    TurnID string `json:"turnId,omitempty"`
    StepID string `json:"stepId,omitempty"`
    ToolCallID string `json:"toolCallId,omitempty"`
}
type WorkReferenceWire struct {
    MessageID int64 `json:"messageId,omitempty"`
    NodeKey string `json:"nodeKey,omitempty"`
    ChildSessionID string `json:"childSessionId,omitempty"`
}
type WorkCapabilitiesWire struct {
    Inspect bool `json:"inspect"`
    Stop bool `json:"stop"`
    Acknowledge bool `json:"acknowledge"`
    ResolvePrompt bool `json:"resolvePrompt"`
}
type WorkPromptWire struct {
    Kind string `json:"kind"`
    RequestID string `json:"requestId"`
    AgentID int64 `json:"agentId,omitempty"`
}
type WorkItemWire struct {
    TaskKey string `json:"taskKey"`
    DisplayID string `json:"displayId"`
    ConversationID string `json:"conversationId"`
    RuntimeEpoch string `json:"runtimeEpoch"`
    SourceID string `json:"sourceId"`
    SourceRevision uint64 `json:"sourceRevision"`
    Kind string `json:"kind"`
    Label string `json:"label"`
    Owner string `json:"owner,omitempty"`
    Origin WorkOriginWire `json:"origin"`
    State string `json:"state"`
    Outcome string `json:"outcome,omitempty"`
    ExitCode *int `json:"exitCode,omitempty"`
    Attachment string `json:"attachment,omitempty"`
    StartedAt string `json:"startedAt"`
    EndedAt string `json:"endedAt,omitempty"`
    ResultRevision string `json:"terminalResultRevision,omitempty"`
    Activity string `json:"activity,omitempty"`
    OutputTail string `json:"outputTail,omitempty"`
    Warning string `json:"warning,omitempty"`
    Attention string `json:"attention,omitempty"`
    Reference WorkReferenceWire `json:"reference"`
    Prompt *WorkPromptWire `json:"prompt,omitempty"`
    Capabilities WorkCapabilitiesWire `json:"capabilities"`
    ProgressConfig *WorkConfigWire `json:"progressConfig,omitempty"`
    Progress *WorkProgressWire `json:"progress,omitempty"`
}
type WorkSnapshotWire struct {
    Version int `json:"version"`
    ConversationID string `json:"conversationId"`
    RuntimeEpoch string `json:"runtimeEpoch"`
    Revision uint64 `json:"revision"`
    HistoryRevision uint64 `json:"historyRevision"`
    ObservedAt string `json:"observedAt"`
    Items []WorkItemWire `json:"items"`
    ActiveCount int `json:"activeCount"`
    AttentionCount int `json:"attentionCount"`
    SuccessReceiptCount int `json:"successReceiptCount"`
    StoppedReceiptCount int `json:"stoppedReceiptCount"`
}
type WorkPageWire struct {
    RuntimeEpoch string `json:"runtimeEpoch"`
    Revision uint64 `json:"revision"`
    Items []WorkItemWire `json:"items"`
    NextCursor string `json:"nextCursor,omitempty"`
}
type WorkStateUpdateWire struct {
    Kind string `json:"kind"`
    WorkSnapshotWire
}
type WorkDetailWire struct {
    Item WorkItemWire `json:"item"`
    Output string `json:"output"`
    Raw bool `json:"raw"`
    Report string `json:"report"`
    Unavailable string `json:"unavailable"`
}
type WorkStopWire struct {
    TaskKey string `json:"taskKey"`
    State string `json:"state"`
    Summary string `json:"summary"`
    Accepted bool `json:"accepted"`
}
type workListParams struct {
    SessionID string `json:"sessionId"`
    View string `json:"view,omitempty"`
    Query string `json:"query,omitempty"`
    Cursor string `json:"cursor,omitempty"`
    Limit int `json:"limit,omitempty"`
}
type workTargetParams struct {
    SessionID string `json:"sessionId"`
    TaskKey string `json:"taskKey"`
    ResultRevision string `json:"terminalResultRevision,omitempty"`
    Reason string `json:"reason,omitempty"`
    Raw bool `json:"raw,omitempty"`
}
func workWire[T any](value any) (T, error) {
    var result T
    b, err := json.Marshal(value)
    if err != nil { return result, err }
    err = json.Unmarshal(b, &result)
    return result, err
}
```

## Task 1: Establish progress values, safe text and configuration

**Goal:** all packages can consume one dependency-neutral, validated configuration and deep-copyable progress value.

**Files:** new `internal/progress/progress.go` (complete file in C1); new `internal/progress/progress_test.go` (complete file below).

**Steps**

1. Paste C1's complete file. There are no worker/process/provider dependencies.
2. Add named tests with channel-free table fixtures:
   - `TestNormalizeConfigCombinations`: defaults, every valid source/profile pair, invalid pairs and title limit.
   - `TestProgressCleanRemovesControlsAndSecrets`: CSI/OSC, C0/C1, UTF-8, redaction and line/rune limits.
   - `TestProgressCloneOwnsAllNestedValues`: mutate phase/metric pointers/rows in a copy; original unchanged.
3. Paste this complete initial progress_test.go; later tasks extend this existing test file at these anchors.

```go
package progress

import (
    "strings"
    "testing"
)

func TestNormalizeConfigCombinations(t *testing.T) {
    got, err := NormalizeConfig(Config{})
    if err != nil || got.Profile != "generic" || got.Source != "none" { t.Fatalf("defaults: %#v %v", got, err) }
    for _, profile := range []string{"generic", "tests", "download", "transfer", "custom", "bogus"} {
        for _, source := range []string{"none", "events-v1", "go-test-json", "bogus"} {
            valid := profile != "bogus" && (source == "none" ||
                source == "events-v1" && profile != "generic" ||
                source == "go-test-json" && profile == "tests")
            _, err := NormalizeConfig(Config{Profile: profile, Source: source})
            if (err == nil) != valid { t.Fatalf("%s/%s: %v", profile, source, err) }
        }
    }
    for _, title := range []string{strings.Repeat("界", 81), string([]byte{0xff})} {
        if _, err := NormalizeConfig(Config{Title: title}); err == nil { t.Fatal("accepted invalid title") }
    }
}
func TestProgressCleanRemovesControlsAndSecrets(t *testing.T) {
    secret := "AKIA" + "1234567890ABCDEF"
    got := Clean("\x1b[31mred\x1b[0m\x1b]8;;https://example.invalid\x07link\x1b]8;;\x07\x00\u0085\r\n"+secret, 0, false)
    if strings.Contains(got, secret) || strings.ContainsAny(got, "\x1b\x00\u0085\r\n") { t.Fatalf("unsafe display: %q", got) }
    if !strings.Contains(got, "redlink") { t.Fatalf("lost text: %q", got) }
    if got := Clean("界界界", 2, false); got != "界界" { t.Fatal(got) }
    if got := Clean("a\nb\tc", 0, true); got != "a\nb\tc" { t.Fatal(got) }
}
func TestProgressCloneOwnsAllNestedValues(t *testing.T) {
    total, rate := 10.0, 2.0
    original := &Snapshot{Frame: Frame{Version: 1, Phase: &Phase{ID: "a"},
        Metrics: []Metric{{ID: "bytes", Total: &total, Rate: &rate}},
        Rows: []Row{{ID: "row", State: "running"}}}}
    copied := Clone(original)
    copied.Phase.ID = "b"
    *copied.Metrics[0].Total = 20
    *copied.Metrics[0].Rate = 4
    copied.Rows[0].State = "done"
    if original.Phase.ID != "a" || total != 10 || rate != 2 || original.Rows[0].State != "running" { t.Fatal("clone aliases source") }
    if Clone(nil) != nil { t.Fatal("nil clone") }
}
```

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/progress -count=1
```

Expected: all combinations and deep-copy/redaction boundaries pass.

## Task 2: Decode bounded custom progress and estimate measurements

**Goal:** events-v1 validates complete snapshots, survives arbitrary write chunks and never turns display progress into lifecycle.

**Files:** existing `internal/progress/progress.go` from Task 1 and `internal/progress/progress_test.go`.

**Steps**

1. Add DecodeEvent at the C1 anchor after Clone. Decode only Frame, not Snapshot/runtime fields. Require Prefix, one UTF-8 JSON object, version 1, increasing sequence in 1..2^53-1; reject trailing JSON. Unknown fields are ignored, including attempts to supply lifecycle/identity/actions.
2. Validate all spec limits before sanitization. Use private `validateFrame(frame *Frame) error`: ≤4 unique metrics, ≤8 unique rows, bounded IDs/labels/unit/detail, valid row states, valid phase bounds, primary ID resolution, finite numeric ranges and integer count units. Current exceeding total clears total and adds one bounded outdated-denominator warning, while retaining measured current.
3. Sanitize text with Clean; cap detail by UTF-8 byte boundary at 1,024 bytes after redaction. Copy before storing. Complete-frame replacement clears omitted fields. Compute ReportedError from reported error rows and the tests profile adapter later, never runtime state.
   Recheck uniqueness/nonempty IDs and primary references after sanitization so two escaped/redacted producer IDs cannot collide in the rendered snapshot.
4. Implement Observer's exact methods from C1 in this existing file. Normalize config once; use a mutex, injected clock (default time.Now), separate stdout/stderr framers, latest/previous snapshots, bounded display tail and closed flag. For this task reject go-test-json construction as unsupported until Task 3 adds its adapter; config normalization itself already accepts it. Production wiring does not consume Observer before Task 3.
5. Add private `feedLine(stream Stream, line []byte, oversized bool)` and a bounded framer that splits LF, handles CRLF/split UTF-8, retains at most the source limit and discards oversized bytes until LF. Custom decoding is stdout-only. Ordinary text from either stream goes to the 8 KiB display tail; valid custom records do not. Invalid candidate records remain output and set one diagnostic until recovery. A final unterminated control record is diagnostic/output, not an accepted snapshot.
   ErrSequence is an ignored replay, not a malformed record: omit it from the display tail and do not advance observations or emit another warning.
6. Write serializes decoding and tail updates. Call dirty outside the observer mutex only when semantic data changed; its caller must provide a nonblocking wake. Close is idempotent, flushes text/diagnostics, prevents further updates and does not invoke process control. Snapshot/DisplayTail return copies. Bound any pending custom snapshot to the newest complete record; synchronous decoding is acceptable, an unbounded queue is not.
   Collect malformed-record warnings separately even before a first valid snapshot, and expose them in a bounded partial Snapshot with Sequence=0; it contains no invented measurements. Split-line tails are assembled before sanitization so escape-bearing text or a secret split across Write calls cannot evade the display boundary.
7. Implement EstimateFor by primary/stable metric ID. Use same phase/unit only; accept a valid supplied rate or positive delta over observation time. Reset on counter/phase/unit changes; suppress rate/ETA when >30s stale or stopping; no denominator means no fraction/ETA. Reject nonfinite/division-overflow estimates, cap displayed ETA at 365 days and keep 100% separate from process completion.
8. Add:
   - `TestProgressFramesAcrossChunksAndCRLF`: every split point, no duplicate frame.
   - `TestProgressInvalidFieldsCannotSetRuntimeFacts`: versions, IDs, forged state/actions and oversized values.
   - `TestProgressOversizeAndMalformedRecoverWithoutGrowth`: skip oversize then accept next record; bounded buffers/tail.
   - `TestProgressSequenceAndReplacement`: replay ignored, omission clears, partial old state not merged.
   - `TestProgressEstimateUnknownChangedAndStale`: missing/changing totals, counter reset, phase reset, stale data, tiny rates.
   - `TestProgressObserverCloseAndConcurrentStreams`: deterministic barriers, no callback under lock or after close.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/progress -count=1
```

Expected: bounded framing/estimates and independent immutable snapshots; no process operations.

## Task 3: Aggregate Go JSON tests before coalescing

**Goal:** the tests profile reports actual observed cases/packages without invented totals or dropped action counts.

**Files:** `internal/progress/progress.go`, `internal/progress/progress_test.go`.

**Steps**

1. Extend NewObserver/feedLine for go-test-json stdout. Decode the installed toolchain's TestEvent fields Time/Action/Package/Test/Elapsed/Output/FailedBuild, using a 64 KiB framer. Non-JSON/oversized records mark coverage partial and remain diagnostic output; valid Output fields feed the filtered display tail.
2. Add private adapter state keyed by package/test and invocation number, not current narration or output string. start initializes a package; run opens a test invocation; pause/cont change activity; pass/fail/skip settle once; a subsequent run starts the next invocation. Cached terminal records lacking run/Time create one settled state. Exact repeated timestamped events are ignored with a bounded latest-event fingerprint per case, not an unbounded event log.
   Maintain per-identity timestamp high-water marks for run/terminal and per-action latest fingerprints. An old timestamped run cannot reopen a later invocation; repeated terminal events cannot recount it. Unexpected out-of-order events that cannot be assigned safely mark coverage partial rather than inventing another invocation. A missing-Time terminal settles once until a new run explicitly opens an invocation.
3. Empty Test is package state and never an extra test. FailedBuild is package failure. bench/output never imply a pass. Keep at most 20,000 identities across case/package state; on excess, preserve already-known counts, mark partial and stop claiming complete coverage. Do not queue actions for later lossy processing.
4. Emit monotonically sequenced aggregate Snapshot values with observed pass/fail/skip/running counts, current package/test and ReportedError. Use four metrics max: passed, failed, skipped, running. No denominator/percentage for plain Go JSON. Declared package totals can come through events-v1 using the tests profile; do not add another tool argument or discovery command.
5. Add `TestGoProgressPackagesAndTestsStayDistinct`, `TestGoProgressCachedRepeatedAndDuplicateActions`, `TestGoProgressPauseBenchmarkAndBuildFailure`, `TestGoProgressInvalidAndIdentityCapIsPartial` and `TestGoProgressBurstCountsBeforeCoalescing`. Assert actual counts, filtered output and zero fabricated totals.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/progress -run 'GoProgress|ProgressObserver' -count=1
```

Expected: interlaced/repeated/cached packages work; counts remain bounded and honest.

## Task 4: Carry optional progress through execution evidence

**Goal:** immutable launch/final progress survives notice encoding without changing execution or legacy versions.

**Files:** `internal/tools/registry/types.go`, `internal/tools/registry/types_test.go`, `internal/tools/registry/notice_test.go`.

**Steps**

1. At CommandExecution add C2's optional fields. Add private `cloneExecution(ex CommandExecution) CommandExecution`: copy ExitCode, config and progress.Clone. Use it in NewCommandExecutionNotice, WithExecution and every successful commandExecutionFromValue branch.
2. The map/JSON path decodes optional progress and preserves version-0/1 compatibility. Invalid future progress versions are omitted with bounded diagnostic metadata; never reject an otherwise valid actual execution record or manufacture a zero exit.
3. Add `TestExecutionProgressNoticeOwnsNestedSnapshots` and `TestExecutionProgressRoundTripAndLegacy` to existing registry tests. Assert mutation isolation for struct/pointer/map/JSON forms and unchanged legacy state/outcome.
4. Add SubagentWorkReference/NoticeSubagentWork and SubagentWork from C2. Decode only the notice's subagent_work value via struct/pointer/JSON-map paths, require nonempty TaskKey/RuntimeEpoch and positive AgentID; nil/invalid notices return false. Add `TestSubagentWorkReferenceNoticeRoundTrip`. The codec is inert until Task 8 emits the metadata.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/tools/registry -run 'Execution|Notice' -count=1
```

Expected: actual command evidence stays readable; progress is additive.

## Task 5: Persist versioned work-view indices and progress evidence

**Goal:** SQLite retains final progress, lightweight agent/watch references and acknowledgments without a schema migration.

**Files:** `internal/db/jobexits.go`, `internal/db/audits_test.go`, `internal/db/sessions.go` and `internal/db/sessions_test.go`.

**Steps**

1. Add C2 fields to JobExitRecord and C2's complete Work* declarations to jobexits.go (implement methods, do not paste signature-only declarations). Import progress. Keep existing job_exits JSON save/load behavior.
2. Implement SaveWorkView/LoadWorkView with the same session_state upsert/read pattern as SaveJobExits/LoadJobExits. Key is exactly `work_view_v1`; record.Version must be 1. A missing row returns `WorkViewRecord{Version:1}`. Unknown versions return an explicit error, not a blank successful read; preserve existing stored data.
3. Save marshals a copied immutable record. DB methods do not serialize callers; State's Task 6 owns that. JSON keys/zero-value omissions follow C2 exactly.
4. Add `func (db *DB) WorkReport(sessionID string, messageID int64) (string, error)` in sessions.go beside GetMessages. Query one row with both session_id and id, select content/content_type, require the subagent-report content type. Missing/wrong-session/wrong-type returns an unavailable error. Do not load all message history or parse narration to locate a report.
5. Add `TestWorkViewRoundTripVersionAndIsolation`, `TestWorkViewMissingAndFutureVersion` and `TestJobExitProgressRoundTripLegacy` in audits_test.go beside TestJobExitRecordsRoundTripStructuredEvidence/TestLoadJobExitsLegacyRowStaysLegacy. Add `TestWorkReportRequiresExactSessionAndReportType` in sessions_test.go. Use the package's existing isolated database fixtures.

The new DB methods are mechanical; append these complete bodies (existing imports cover them):

```go
func (db *DB) SaveWorkView(sessionID string, record WorkViewRecord) error {
    if record.Version != 1 { return fmt.Errorf("unsupported work view version %d", record.Version) }
    data, err := json.Marshal(record)
    if err != nil { return fmt.Errorf("marshal work view: %w", err) }
    _, err = db.sqlDB.Exec(
        `INSERT INTO session_state (session_id, key, value) VALUES (?, 'work_view_v1', ?)
         ON CONFLICT(session_id, key) DO UPDATE SET value=excluded.value`,
        sessionID, string(data))
    if err != nil { return fmt.Errorf("save work view: %w", err) }
    return nil
}
func (db *DB) LoadWorkView(sessionID string) (WorkViewRecord, error) {
    var raw string
    err := db.sqlDB.QueryRow(
        `SELECT value FROM session_state WHERE session_id=? AND key='work_view_v1'`,
        sessionID).Scan(&raw)
    if err == sql.ErrNoRows { return WorkViewRecord{Version: 1}, nil }
    if err != nil { return WorkViewRecord{}, fmt.Errorf("load work view: %w", err) }
    var record WorkViewRecord
    if err := json.Unmarshal([]byte(raw), &record); err != nil {
        return WorkViewRecord{}, fmt.Errorf("decode work view: %w", err)
    }
    if record.Version != 1 { return WorkViewRecord{}, fmt.Errorf("unsupported work view version %d", record.Version) }
    return record, nil
}
```

Append this complete method beside GetMessages in sessions.go:

```go
func (db *DB) WorkReport(sessionID string, messageID int64) (string, error) {
    if sessionID == "" || messageID <= 0 { return "", fmt.Errorf("work report unavailable") }
    var content string
    var contentType sql.NullString
    err := db.sqlDB.QueryRow(
        `SELECT content, content_type FROM messages WHERE session_id=? AND id=?`,
        sessionID, messageID).Scan(&content, &contentType)
    if err != nil { return "", fmt.Errorf("work report unavailable: %w", err) }
    if !contentType.Valid || contentType.String != "subagent_report" {
        return "", fmt.Errorf("work report unavailable: message is not a subagent report")
    }
    return content, nil
}
```

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/db -run 'WorkView|WorkReport|JobExit' -count=1
```

Expected: no migration/new table; old job records still decode.

## Task 6: Serialize work-view persistence on session state

**Goal:** source results and acknowledgments persist in order without holding State.mu across live database writes.

**Files:** `internal/app/session/session.go`, `internal/app/session/jobexit.go`, `internal/app/session/jobexit_test.go`, `internal/app/session/session_test.go`.

**Steps**

1. Add State fields `workView db.WorkViewRecord`, `workViewSaveMu sync.Mutex`, dirty flag and pending acknowledgment bookkeeping. Initialize version 1; load it beside existing loadJobExitsLocked startup restoration, before publishing the live State. Unknown/corrupt version is a diagnostic; never overwrite it automatically.
2. Implement C2 WorkView/SaveWatchWorkResult/AcknowledgeWork/RetryWorkViewSave in jobexit.go next to the existing serialized result persistence. Lock order is workViewSaveMu → State.mu; snapshot under State.mu, release it for DB I/O, then mark the saved revision. Copy every nested slice/pointer. With no DB, the in-memory path settles normally.
3. Watch results upsert by canonical key and stable result revision. AcknowledgeWork stores key+revision after runtime validation; it never accepts a display ID or edits a source result. An old acknowledgment cannot acknowledge a newer terminal revision.
   Add private `safeWorkResult(record db.WorkResultRecord) db.WorkResultRecord`, used by watch and later agent-index writes: clean label/owner to 80 runes and detail to 1,024 UTF-8 bytes after control stripping/redaction. Identity/origin/reference keys are retained exactly and never presented as arbitrary terminal text.
4. Failed result save retains the in-memory result/attention and dirty flag. Failed acknowledgment save keeps its attention visible and returns error; only a successful serialized save commits the acknowledgment. Retry uses the newest snapshot, not a cached older JSON value.
5. Extend JobExit, jobExitRecord/jobExitFromRecord/JobExitFromExecution with C2's evidence fields using progress.Clone. Preserve AddJobExit's durable-before-announcement behavior and RetryJobExitSaves, including final progress on a retried save.
   Implement JobExitSnapshot under State.mu, returning deep copies including ExitCode/config/progress. Extend lostExit at its existing recovery anchor to retain source epoch/config/key evidence from the launch notice; use original stable start facts for unknown/lost result revision, not restart time.
6. Add `TestWorkViewConcurrentSaveKeepsNewest`, `TestWorkAckFailureDoesNotHideAttention`, `TestWorkAckTargetsTerminalRevision`, `TestWorkViewResumeCopiesAndDoesNotOverwriteFutureData` and `TestJobExitFinalProgressBeforeAnnouncement`. Use DB errors/barriers, not ambient credentials/services.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/app/session -run 'WorkView|WorkAck|JobExit' -count=1
```

Expected: no lost acknowledgment/result, DB I/O outside State.mu.

## Task 7: Give subagents stable source snapshots and report references

**Goal:** live/retired agent identities and result references survive restart without retaining every Child State.

**Files:** `internal/app/session/subagents.go`, `internal/app/session/session.go`, `internal/app/session/subagents_test.go`, `internal/app/session/subagents_meta_test.go`, `internal/app/session/subagents_async_test.go`.

**Steps**

1. Add C2's SubagentOrigin/Snapshot declarations and fields. State has one crypto/rand.Text subagent epoch and a source revision under State.mu. In registerSubagent capture epoch, opaque WorkKey, origin and start facts, then keep the existing bindSubagentWork/WorkID contract exactly once.
2. Register/update/finish/cancel/token/provenance changes increment revision and publish copies after releasing locks. SubagentSnapshot copies views under State.mu, then reads Child activity/prompt facts under the child's own locks outside the parent lock. Do not publish Cancel callbacks or mutable Child pointers in the work projection.
3. CancelSubagent records accepted StopRequested only after locating the exact running child and its cancel/group. Retain stop-before-child-context-cancel ordering. In FinishSubagent set Outcome completed when err==nil, stopped only for an accepted request with errors.Is(err,context.Canceled), otherwise failed. A late request cannot relabel a successful result, unrelated failure or panic. Keep existing SubagentStatus compatibility; the projection reads the additional outcome. Do not turn detached child-owned jobs into stop targets.
4. Upsert a lightweight AgentResults entry at registration and settlement, using Task 6's serialized work view. Store stable key/epoch/origin/timing/summary preview/reference, not a duplicate full report. Preserve maxRetainedChildStates GC policy.
   Set a private per-ID settling flag before terminal view mutation, and clear it after the first serialized final-index save attempt. Expose it plus bounded save errors in SubagentSnapshot, using decimal agent IDs as source IDs. A failed save keeps actual final state/report and attention. RetryWorkViewSave clears only errors covered by the successfully saved revision and publishes a source wake. No State.mu spans this I/O.
5. Implement BindSubagentReport: find the already-appended exact report text and ContentTypeSubagentReport for that live ID within its registration interval under State.mu; capture Message.DBID (not transient ID) and parent/child reference. Persist the index. If persistence was enabled but DBID is zero, record unavailable evidence and return/log the error; do not claim a durable report link.
6. Restore these indices as history only. A stored running entry with no terminal result becomes unknown/owner-gone using its original start time as stable fallback, not time.Now; it never becomes a live SubagentView or stop capability. Existing old messages stay readable; no cross-runtime ID parsing/matching.
7. Add `TestSubagentWorkIdentityAndSnapshotVersions`, `TestSubagentReportBindingUsesExactPersistedMessage`, `TestSubagentWorkResumeNeverResurrectsChild`, `TestSubagentWorkStopCompletionRace` and `TestSubagentWorkHistorySurvivesChildGC`. Retain existing end-gate/depth/await tests.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/app/session -run 'Subagent|WorkView|WorkAck' -count=1
```

Expected: one WorkID per child, immutable source snapshots and inspectable report references.

## Task 8: Bind actual agent reports to the retained index

**Goal:** normal and panic completion point at their real durable report messages before child completion is announced.

**Files:** `internal/agent/subagent.go`, `internal/agent/subagent_test.go`, `internal/agent/subagent_async_test.go`.

**Steps**

1. In NewSubagentTool's registration path populate SubagentMeta.Origin from session.ExecutionOriginFromContext(ctx), call.ID and the actual originating step. Do not derive keys from a provider call ID alone or mutate the child creation/end WorkID.
2. In both normal completion and panic recovery, immediately after the existing AddMessage(RoleUser, report, ContentTypeSubagentReport), call State.BindSubagentReport(view.ID, report). Log an index-save failure as evidence, keeping report delivery/PushSubagentReport/ExitSubagentWork/FinishSubagent in their existing happens-before order.
3. Do not add another report message, rewrite its body or count progress as narration. Preserve salvage markers, actual failure causes, stop-group cleanup and watch transfer.
   Add a ToolNotice with Kind registry.NoticeSubagentWork, empty Text and Data["subagent_work"] = registry.SubagentWorkReference to the existing successful dispatch ToolResult, using the registered view's canonical key/epoch/ID and actual child session ID. Preserve Summary/Content and agent.await's numeric handle; no duplicate dispatch result is emitted.
4. Add `TestAgentWorkReportIndexPrecedesAwaitCompletion`, `TestAgentWorkPanicReportHasReference` and `TestAgentWorkBindingFailurePreservesDelivery`. Assert one persisted report and correct origin/reference rather than testing private formatting.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/agent -run 'AgentWork|Subagent|AgentAwait' -count=1
```

Expected: no extra model/history messages and no orphaned child report.

## Task 9: Observe progress inside the existing command service

**Goal:** one managed execution retains progress across attachment changes and settles its final snapshot before result publication.

**Files:** `internal/tools/native/jobs_manager.go`, `internal/tools/native/jobs_test.go`, `internal/tools/native/jobs_wait_test.go`.

**Steps**

1. Add C2/C3 fields and JobManager.Snapshot. Manager epoch is created once and copied into JobInfo.SourceEpoch/execution evidence at admission; lifecycle/membership revision is captured under its mutex. Reuse current manager ownership/OnChange/OnExit callbacks. Do not install another owner callback from a tool registry.
2. Launch validates/copies optional progress config before reserving admission and builds a progress.Observer with an injected/default clock. Add the observer to private job; jobSink receives stream kind and that observer. Its Write first feeds the observer independently of attached forwarding/tail truncation, then preserves current foreground forwarding and raw tail behavior. No broker, DB or slow callback is invoked from that observer; the collector polls current progress.
3. Snapshot copies current JobInfo plus config/progress/display tail while respecting lock order manager → job → observer. Source revision versions lifecycle/membership; Progress.Sequence/ObservedAt represent independent progress changes. A broker event is only a wake; it must not overwrite a newer observed snapshot with an older progress copy.
4. In runJob preserve the existing immediate job-lock boundary after runner.Run: record processExited/completedAt first, then Close/drain observer under the established job→observer lock order, before copying final progress/classifySettlement/ResultRevision. Do not insert decoding ahead of processExited where a late stop could relabel a natural exit. Report 100% independently from lifecycle; only actual outcome ends the command.
   Add a private atomic settling flag to job. For detached settlement set it before terminal becomes observable; clear it after reportSettlement's first sink attempt has returned and its existing pending-settlement ledger is updated. Snapshot exposes Settling and SaveErrors from that boundary. Foreground settlement needs no detached sink flag. No source lock spans the sink/DB write.
5. Retain raw output unchanged. For opted-in tool results use DisplayTail plus a bounded progress summary; raw Output remains available explicitly. In JobInfo.Execution copy optional fields to CommandExecution. Snapshot/Output/terminal retention and notice paths never share mutable progress pointers.
6. notifyChange adds epoch/revision and still omits heavy output. Progress-only output does not Publish through a possibly blocking terminal subscriber; the 200ms collector poll handles it. Terminal/attachment/stop membership events continue their current immediate path.
7. Add `TestCommandProgressSurvivesDetachAndOutputCap`, `TestCommandProgressFinalFlushPrecedesExitSink`, `TestCommandProgressDoesNotCompleteOrStopProcess`, `TestCommandProgressConcurrentSnapshotOwnsData` and `TestCommandProgressReloadKeepsCapturedProfile`. Include a fake runner writing split records on both streams and then stopping through RequestStop.
   Add `TestCommandSnapshotGatesInFlightSettlementAndRetainsSaveError` with a blocked then failed exit sink, and `TestCommandProgressCompletionStopRaceKeepsActualOutcome` with channel barriers.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/tools/native -run 'CommandProgress|CommandSnapshot|Job.*(Wait|Foreground|Stop|Snapshot)' -count=1
```

Expected: same PID/execution/owner, continuing measured data after detach, no new execution timers.

## Task 10: Expose compact opt-in progress on shell and test tools

**Goal:** agents can configure one command's card at launch without another tool, command rewrite or permission bypass.

**Files:** `internal/tools/native/command.go`, `internal/tools/native/command_test.go`, `internal/tools/native/schemas_test.go`, `internal/tools/native/native_test.go`.

**Steps**

1. Add `Progress *progress.Config` to shellRunArgs and the test.run decode arguments. Both schemas add one optional progress object with profile/source/title only, enums/80-character title and additionalProperties:false. Preserve every existing top-level argument and risk/guardrail path.
   In each schema's properties map, insert a progress property with this exact value; leave required and all existing properties unchanged:

```json
{"type":"object","properties":{"profile":{"type":"string","enum":["generic","tests","download","transfer","custom"]},"source":{"type":"string","enum":["none","go-test-json","events-v1"]},"title":{"type":"string","maxLength":80}},"additionalProperties":false}
```
2. Keep runManagedCommand's current signature as a delegating compatibility helper. Add private `runManagedCommandWithProgress(ctx context.Context, callID, command string, wait time.Duration, background bool, cfg *progress.Config) (registry.ToolResult, error)`. Normalize/reject before guardrail/admission; copy into LaunchSpec.Progress. Shell/test handlers use it; other ordinary callers keep their current helper.
   A nil cfg remains nil with no observer allocation or tool-result filtering; explicit source none keeps ordinary text. Validate supplied configuration before the existing launch/approval boundary, but never replace that boundary.
3. Existing explicit-background and continuation results carry the config/identity via info.Execution. Terminal results retain filtered diagnostics/final progress in one existing notice/summary. Do not append progress records as chat messages or schema updates.
4. No implicit -json flags, discovery command, wrapper generation, new manager, retry or default runtime limit. Source none with any profile remains generic display.
5. Add `TestShellProgressConfigRejectedBeforeLaunch`, `TestTestRunProgressKeepsCommandOverride`, `TestShellProgressOneExecutionNoticeThroughHandoff` and `TestShellProgressSourceNoneIsPlainCommand`. Re-run schema/unknown-property and repository tool-schema budget checks; keep schema prose short rather than relaxing a budget.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/tools/native -run 'Progress|Schemas|ShellToolText|TestRunTool|RegistersExpectedTools' -count=1
```

Expected: static compact schema, actual command unchanged and one execution/card.

## Task 11: Make watch membership authoritative and retain terminal facts

**Goal:** repeat/error recovery stays live, while actual once/removal/failure retires membership with recoverable final evidence.

**Files:** `internal/watch/watch.go`, `internal/watch/own.go`, `internal/watch/watch_test.go`, `internal/watch/own_test.go`, `internal/watch/waitfire_test.go`, `internal/watch/resume_test.go`.

**Steps**

1. Add C3's Snapshot fields, manager epoch/revision and a retained terminal map keyed by registration ID. Deps.OnTerminal is optional; no app/session/viewmodel import is added. Copy sample rings and event Info pointers.
   Use Deps.ConversationID with progress.WorkKey/ResultRevision to derive the canonical stable result revision; standalone watch fixtures may supply an empty conversation, production wiring supplies the captured owning session. No runtime lookup is done by watch.
2. Refactor Start/sampleOnce/handleSampleError/autoStop/fire/Stop/removeWatch so active membership and terminal reason are explicit. Once fire removes; repeat fire retains watching; recoverable StateError stays Active true. On final removal set EndedAt/reason/result revision and retain terminal Info before deleting active membership/publishing.
   Pin TerminalReason to completed for once fire, stopped for explicit Stop, failed for error-budget autoStop, shutdown for manager close; keep human diagnostics in LastError/final detail. In Close, after canceling/joining source goroutines, finalize remaining registrations with shutdown once. A join timeout keeps unfinished registrations active until a later successful join; no close-time OnFire or resume is generated.
3. Preserve fired-report/resume/owner-transfer/waiter semantics. A source terminal record is independent from OnFire's model-report queue; do not generate another model turn or machine report merely to retire a card.
   In TransferFromSubagent (own.go), read/mutate owner with manager→watch lock order, increment source revision for actual reparenting, and publish a copied wake after releasing locks. Repeat stops continue through Stop, collecting terminal evidence once. Add `TestWatchSnapshotReparentPreservesIdentityAndRevisions` in own_test.go.
4. Call OnTerminal outside manager/watch locks. Record the registration in Snapshot.Settling before releasing the terminal-membership boundary; clear it only after the initial callback attempt. Failed durable save retains pending terminal evidence/SaveErrors, publishes a diagnostic/actual inactive state and retries through RetryTerminalSaves. A successful retry clears SaveErrors but does not fire/report/announce completion twice. Snapshot includes retained terminal facts, so a dropped removal event is repairable.
5. Add `TestWatchSnapshotOnceRemovalAndRepeatLiveness`, `TestWatchSnapshotErrorRecoveryIsNotTerminal`, `TestWatchTerminalSaveRetryKeepsMembershipTruthful`, `TestWatchLateSampleCannotResurrectRemovedRegistration` and `TestWatchSnapshotEpochAndCopies`. Keep existing once/repeat/owner/resume/wait-fire tests.
   Add `TestWatchSnapshotCloseFinalizesOnlyJoinedRegistrations`, including a timed-out first join followed by successful close and no duplicate terminal receipt.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/watch -run 'Snapshot|TerminalSave|LateSample|FireOnce|FireRepeat|ErrorBudget|WaitFire|Resume|Owner' -count=1
```

Expected: disappearance is actual membership, not a generic fired/error filter.

## Task 12: Add pure work projection, retirement and history paging

**Goal:** clients share immutable work values, stable identities and deterministic paging/receipt rules.

**Files:** `internal/viewmodel/band.go`, `internal/viewmodel/band_test.go`.

**Steps**

1. Append C4 declarations and implement the pure helpers beside BandOf. Imports may include progress/pubsub/context/errors/encoding packages; never native managers or app.Runtime. Deep-copy all slices, ExitCode, Prompt, config and progress.
2. WorkActive accepts running/stopping/watching/blocked only, excluding command items still foreground-attached from pinned membership (they remain inspectable in Work history). WorkNeedsAttention accepts terminal failure/startup_failure/lost/unknown, terminal nonempty Attention (including save failure), and terminal semantic ReportedError, with no acknowledgment suppression inside this pure predicate. Running error rows appear on their live card without becoming acknowledgeable results. Explicit stopped outcomes without another error are not failures. WorkReceipts returns success or stopped only for nonattention terminal results with nonzero EndedAt and now before EndedAt+5s; expected watch shutdown is stopped, while failed command shutdown remains failure. Old/missing timestamps cannot restart a receipt.
3. WorkResultRevision delegates to the neutral helper using actual outcome/exit/time. Semantic progress errors affect Attention, not the process state/exit. Source-provided persisted result revisions win; legacy fallback is computed once from stable stored facts.
4. Implement PageWork: validate view attention/history, query UTF-8 and ≤200 runes, limit 0→50 and otherwise 1..100. Case-fold query against safe label/kind/owner. History sorts newest StartedAt first then TaskKey; attention filters unacknowledged inputs supplied by the coordinator and uses the same order. Decode a bounded cursor (≤2 KiB), reject mismatched epoch/revision/view/query or negative/out-of-range offset. Copy returned items. Progress, observation time and elapsed display never enter the cursor.
5. Add `TestWorkIdentitySeparatesConversationEpochAndSource`, `TestWorkCloneOwnsNestedValues`, `TestWorkReceiptsDoNotReplayOrHideFailure`, `TestWorkSemanticErrorPreservesActualExit`, `TestWorkPagingBoundsSearchAndStaleCursor` and `TestWorkPagingStableAcrossProgressOnlyUpdates`. Include time-zone equivalent EndedAt values and equal starts.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/viewmodel -run 'Work|Band' -count=1
```

Expected: deterministic keys/pages, no runtime or UI side effects.

## Task 13: Build the conversation projection and action facade

**Goal:** Runtime exposes one recoverable view over existing owners and routes exact actions without supervising executions.

**Files:** `internal/app/runtime.go`, `internal/app/runtime_test.go`.

**Steps**

1. Add private workCoordinator and the C4 Runtime methods. Constructor newWorkCoordinator from C4 captures immutable conversation ownership and a new coordinator epoch. Do not use rt.State dynamically inside collector callbacks. This task tests construction directly; Task 14 wires lifecycle.
2. Subscribe nonterminal source brokers before the initial source snapshots, then reconcile current JobManager.Snapshot, SubagentSnapshot, watch.Snapshot and State.WorkView/JobExitSnapshot. Read source snapshots without the coordinator mutex; build a new map, then swap atomically. Read source lifecycle fingerprints again after the memory indices; if epoch/revision, status/attachment, Settling or SaveErrors changed, retry up to twice and otherwise defer to the next wake/tick. This closes a source/index race without acquiring cross-owner locks. Progress sequence changes do not force retries. Late events are wakes, never authoritative replacements. Reconcile every 200ms even without wakes. Epoch replacement is accepted only from an explicitly rebound source pointer.
3. Normalize commands from full ExecutionID+SourceEpoch; use captured conversation and origin, actual lifecycle/outcome, config/progress/display tail. Attached foreground commands stay in history/tool details but do not enter pinned live membership until detached. Normalize subagents from their saved WorkKey; child prompt/activity reads occur outside parent/coordinator locks. Normalize watches by registration ID+epoch and explicit Active. Preserve repeat activity and recoverable error as live; terminal reasons success/explicit stop/failure map separately.
   Apply progress.Clean at the projection boundary to all display strings, including generic command/watch/subagent data: label/owner 80 runes, activity 160, warning/attention 256, tail ≤8 KiB at a UTF-8 boundary. Canonical identities remain exact opaque data; do not render them as uncontrolled text.
   Normalized states are running/stopping/watching/blocked/completed/stopped/failed/lost/unknown. Native killed+stopped maps stopped; native failed/historical timed_out maps failed while retaining actual Outcome/ExitCode. SubagentDone/Failed use C2 Outcome to distinguish explicit cancellation. Watch completed/stopped/failed/shutdown map completed/stopped/failed/stopped respectively, preserving the terminal reason in Outcome. Live recoverable watch errors remain watching with Warning. Canonical pending requests map a live agent to blocked. These display mappings do not rewrite source/audit evidence.
   Gate new terminal facts and matching memory indices on each source's Settling. Retain the prior published item while that first save is in flight; do not synthesize success from absence. After an attempt fails, show actual terminal outcome plus result-save attention/diagnostic and no acknowledgment capability until retry succeeds. SaveErrors clearing cannot restart the original five-second receipt deadline.
4. Add private `reconcileWork(now time.Time)` and `publishWork(urgent bool)`. Lifecycle/membership/blocked-prompt changes publish immediately; cosmetic changes publish ≤5Hz. Idle identical polls do not advance Revision. Revision increases per published semantic snapshot; HistoryRevision changes only when ordering/membership/search/attention changes. A local UI age tick is not a new revision.
5. Keep all active items in the snapshot. Include at most 32 newest terminal details, independently compute all receipt/attention counts from the full conversation history index, and keep pages available for omitted results. Acknowledgments match both key and terminal result revision. No result is evicted merely because its card is hidden.
6. ListWork validates requested conversation and delegates to PageWork over captured history. InspectWork resolves canonical key in that same history, returns up to 32 KiB safe output/report plus an explicit unavailable reason when evidence has expired, and uses DB.WorkReport for exact persisted agent references. Validate command ExecutionID against the current source before using a display ID to call Output; never use a reused job-N alone. Raw selects existing raw retained output, still redacted/terminal-safe and bounded; filtered is default.
7. StopWork checks context/conversation/key/current source epoch and capability, then delegates outside the coordinator mutex: exact command RequestStop, exact child CancelSubagent, exact watch Stop. Preserve existing authorization/admission and truthful accepted/no-longer-running results. No parent cancel fallback, process handles, execution timer or config reload. AcknowledgeWork validates terminal/current revision, serializes through State.AcknowledgeWork and publishes only after durable success; repeated same acknowledgment is idempotent.
8. Resolve blocked prompts from the existing canonical session request APIs (including merged Send now). Expose only kind/request ID/agent ID and ResolvePrompt capability. A stale prompt ID never settles a newer prompt. No permissions are decided by this projection.
9. Add `TestWorkCoordinatorSeedsAndRepairsDroppedEvents`, `TestWorkCoordinatorAllActiveAndBoundedTerminalDetails`, `TestWorkCoordinatorProgressThrottleAndUrgentSettlement`, `TestWorkCoordinatorLegacyHistoryCannotStopNewJob`, `TestWorkFacadeExactChildAndWatchStop`, `TestWorkFacadeAckFailureAndRevisionCAS` and `TestWorkInspectUsesExactReportAndBoundedOutput`. Use real isolated State plus fake clocks/channel runners; assert no provider call or per-progress persistence.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/app -run 'WorkCoordinator|WorkFacade|WorkInspect' -count=1
```

Expected: same owners as current execution, complete active membership and stale-target rejection.

## Task 14: Wire projection into startup, reload, session change and exit

**Goal:** projection lifetime follows its captured conversation and finishes before persistence closes.

**Files:** `internal/app/app.go`, `internal/app/runtime.go`, `internal/app/app_test.go`, `internal/app/runtime_test.go`.

**Steps**

1. In newConversationCommandService/buildAgentRunnerWithLock, retain the existing command service and durable OnExit sink. Set watch.Deps.ConversationID to the captured State.SessionID; wire OnTerminal to convert the supplied terminal Info into db.WorkResultRecord with that session/epoch/registration key and call State.SaveWatchWorkResult. It must not dereference a later rt.State. The watch constructor's existing callbacks/resume paths remain single-installed.
2. In startRuntime create/start the coordinator after source owners/State exist and before exposing Runtime to transports. Populate workSources from the already-created managers and typed job/watch/subagent/session brokers; no new source brokers. Initial WorkSnapshot is synchronously seeded. Start one background collector with cancel/join owned by Runtime; no terminal broker subscription can block process output.
3. reloadAgentRuntime retains coordinator/conversation/command identity. If the watch manager is replaced, explicitly bind the new pointer/epoch after joining the old manager and reconciling its terminal snapshot; old results stay history. Do not infer that old registrations belong to the replacement merely because numeric IDs match.
4. In newSessionLocked, settle/join old sources through the existing end-work path, reconcile/final-save old history, close its coordinator, then create the replacement for the new State. Old queued callbacks/subscriptions cannot publish into it. New session starts with no old attention/receipt counters.
5. In Quiesce/Close, keep source settlement and existing active-work warning ahead of optional postmortem. After owners finish, drain/reconcile final facts and retry State.RetryWorkViewSave/watch.RetryTerminalSaves alongside existing job-exit/JobManager.RetrySettlements retries; then close coordinator broker/join before DB closes. Retry failures surface through existing diagnostics without announcing a durable acknowledgment. In normal runtime retry dirty persistence at most once per second, outside source locks; no progress tick writes clean state.
6. Add `TestWorkRuntimeReloadRetainsExecutionIdentity`, `TestWorkRuntimeReplacementCannotReceiveOldEpoch`, `TestWorkRuntimeCloseFlushesBeforeDBClose`, `TestWorkRuntimeStartupRestoresHistoryWithoutLiveOwners` and `TestWorkRuntimeDirtyRetryDoesNotWriteProgressTicks`. Preserve existing active-work exit/replacement and postmortem toggle tests.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app -run 'WorkRuntime|NewSession|Close|Quiesce|Reload|Postmortem' -count=1
```

Expected: no phantom work, no duplicate ownership callbacks, no changed exit semantics.

## Task 15: Separate chronological dispatch from live cards

**Goal:** the transcript retains each actual dispatch/result once while the pinned surface owns live presentation.

**Files:** `internal/viewmodel/stack.go`, `internal/viewmodel/cards.go`, `internal/viewmodel/band.go`, `internal/viewmodel/wire.go`, `internal/viewmodel/stack_test.go`, `internal/viewmodel/cards_test.go`, `internal/viewmodel/wire_test.go`.

**Steps**

1. Add `LiveWork bool` to Snapshot; false retains legacy projection. Add `WorkKey string` to Node and WireTool (WireTool JSON tag workKey,omitempty). Existing NodeID/step identity remains unchanged. These are exact additive fields; no new kind or fabricated chronology node.
2. In Build's initial agent.run audit filter and buildTurn's active agent.run filter, add !s.LiveWork to the suppression conditions. In buildTurn's KindSubagent/card insertion path, omit parent child boxes when LiveWork is true. Read dispatch keys only through Notice.SubagentWork. Change Mergeable to return false for agent.run, so restored dispatches remain individually reachable. Do not delete report messages from history or change drilled-child transcript behavior.
3. In indexCards/settleJob, preserve execution-identity pairing and one existing command dispatch/result row. Set WorkKey from structured execution metadata, never summary parsing for new records. A detach changes attachment/card placement on that same execution; it cannot create a second dispatch. Legacy summaries continue their existing fallback.
4. In BandOf's LiveWork branch omit duplicated entity listings, retaining todo/task/workflow/browser summary data. ProjectBand retains optional entities for legacy clients. WireTool includes stable WorkKey and existing execution/progress detail; apply bounded safe text before export. Exclude clock-only changes from node hashes.
5. Add `TestLiveWorkDispatchShownOnceWithoutChildBox`, `TestLiveWorkDetachedCommandKeepsOneDispatch`, `TestLiveWorkReportAndDrilledTranscriptRemainReachable` and `TestLiveWorkWireOptInPreservesLegacyBand`. Verify real persisted audits/reports, not invented placeholder rows.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/viewmodel -run 'LiveWork|Card|Wire|Band|Subagent' -count=1
```

Expected: dispatch chronology remains truthful in both legacy and opted-in projections.

## Task 16: Render bounded gutter cards in the existing now bar

**Goal:** all live work and profile variants share one bounded, terminal-safe row plan and hit map.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/nowbar.go`, `internal/app/tui/view.go`, `internal/app/tui/jobs.go`, `internal/app/tui/nowbar_test.go`, `internal/app/tui/jobs_test.go`.

**Steps**

1. Add WithWork/C5 fields in model.go. WithWork(nil) leaves the existing now bar fallback intact. The live-work branch reads only immutable snapshots, not agent/command/watch owners. Task 18 connects the event pump; tests inject snapshots directly meanwhile.
2. Extend nowBarInput/nowBarPlan at planNowBar with WorkSnapshot, selected/expanded TaskKey, injected now and typed hit targets. Keep one planner responsible for lines and coordinates. Add private `planWorkRows(in nowBarInput, budget int) nowBarPlan`; return rows and matching TaskKey/action targets from that same result.
3. Height <30 uses one summary row. Otherwise initial budget is min(10,height/4), further reduced by existing chrome to preserve minTranscriptRows=4. All todo/workflow/browser/attention/receipt/overflow rows count inside it. Allocate one aggregate attention row when needed, one compact success/stopped receipt summary, then live cards ordered selected first, blocked next, StartedAt/TaskKey stable. If content overflows, reserve one row showing exact hidden-live count and Work action. Summary rows cannot reserve more than the budget; a one-row budget combines counts.
4. Collapsed cards take two rows; explicit expansion takes up to six, only one card expanded. While expanded, other cards use one row. Expansion never steals composer/prompt focus or stops work. When selected item disappears/settles, clear its expansion. Use ellipsis/cell width after sanitization; no producer-controlled color/layout/actions.
5. Add private `workCardRows(item viewmodel.WorkItem, previous *progress.Snapshot, now time.Time, width, maxRows int) []string`. Header shows kind/title, owner and actual lifecycle/elapsed. Detail uses profile-selected primary metric/phase, measured rate/approximate ETA and bounded checklist/activity/tail. Unknown total omits percent/ETA; show phase-local label, partial/stale/outdated warning. Go JSON shows observed counts, no suite denominator. At 100% while active say reported work complete; process still running. Failed test rows with exit zero show attention without changing actual exit.
6. Retain theme/glyph/no-color/transparency conventions. Use IEC byte formatting and cell-aware truncation. Do not add animation/provider work/SQLite writes. Existing session-sheet and browser summary still fit the same budget.
7. Add `TestWorkNowBarBudgetAndExactOverflow` (80×24/120×40/200×60/narrow resize), `TestWorkNowBarExpandedAndBlockedOrdering`, `TestWorkCardsGenericTestsDownloadTransferCustom`, `TestWorkCardsUnknownStalePartialAndStopping` and `TestWorkNowBarHitTargetsMatchRenderedRows`. Old nil-service now-bar tests must pass.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/tui -run 'WorkNowBar|WorkCards|NowBar|RunningJobs' -count=1
```

Expected: no clipped composer/transcript, phantom percentage or duplicated live entity list.

## Task 17: Extend Tasks with Plan and Work tabs

**Goal:** users can search/inspect all conversation work while existing todo statistics and child Plan scope remain intact.

**Files:** `internal/app/tui/todos.go`, `internal/app/tui/model.go`, `internal/app/tui/todos_test.go`.

**Steps**

1. Replace tasksPanel's concrete *docpanel.Panel with private `tasksPanelModel` in todos.go implementing dock.Panel. Reuse docpanel for the existing tasksDoc Plan and listpanel.FieldList/PaneStack for Work; no new panel host. Add fields panelID, activeTab, previousTab, selectedTaskKey, query textfield.Model, view, cursor, pages/items, detail, pending request IDs and error. New panel IDs use a monotonic model counter; never labels.
   Keep `SetSource(fn func() commands.Doc)` on the wrapper, delegating to its Plan docpanel, so view.go's existing live todo refresh still builds. Use the verified NewFieldList constructor; no changes to the shared list engine are needed.
2. First Ctrl+T opening defaults Plan when viewedTodos nonempty, otherwise Work; subsequent openings restore last tab. Ctrl+T while open closes. Plan continues viewedTodos/taskStats and viewed-child behavior; Work is explicitly labeled current root conversation, including owner badges. Tab switching preserves each selection and text editing state.
   An available empty Work view says "No work yet"; a nil service says "Work is unavailable in this runtime." Do not confuse no results with unsupported service.
3. Work initially shows live membership/attention summary, plus actions for attention/history. History uses server paging; "/" focuses a ≤200-rune textfield with paste support. Enter applies the query and requests page 1; next-page row requests its opaque cursor. A stale cursor displays refresh and requests page 1; never silently concatenate incompatible pages. Reconcile selection by TaskKey, not list index.
4. Work list keys j/k/up/down select; left/right switches Plan/Work only outside text editing/detail. Enter emits inspect; Space emits expansion for selected live item. Esc exits detail/search before closing; detail has jump-to-origin, output/raw, stop and Mark reviewed/Dismiss only when capability allows. Both attention actions persist the same revision acknowledgment; opening/inspecting does not acknowledge.
5. Panel callbacks emit C5 workRequestMsg, never call WorkService; kind is list/inspect/stop/ack/expand/origin/prompt. For this task declare its complete C5 struct beside tasksPanelModel and move it unchanged to events.go in Task 18. No duplicate definition.
6. Add `TestWorkTasksInitialAndRememberedTab`, `TestWorkTasksPlanKeepsChildStats`, `TestWorkTasksSearchPagingAndSelection`, `TestWorkTasksInspectNeverAcknowledges` and `TestWorkTasksKeyboardEmitsExactTargets`. Verify callback messages without starting runtime or model.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/tui -run 'WorkTasks|Tasks|Todo' -count=1
```

Expected: one Tasks host, preserved Plan behavior and bounded searchable Work pages.

## Task 18: Connect TUI updates, async actions and canonical prompt navigation

**Goal:** work stays current without blocking input, stealing steering focus or accepting stale action replies.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/events.go`, `internal/app/tui/pump.go`, `internal/app/tui/keypress.go`, `internal/app/tui/browse.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/viewport_render.go`, `internal/app/tui/pump_test.go`, `internal/app/tui/todos_test.go`, `internal/app/tui/commands_dispatch_test.go`.

**Steps**

1. Add C5 messages to events.go and move workRequestMsg there unchanged. Add private `pumpWorkEvents(ch <-chan pubsub.Event[viewmodel.WorkSnapshot], subscriptionID uint64) tea.Cmd` beside pumpSubagentEvents. Subscribe once with a child cancel context; seed WorkSnapshot after subscribing. Init starts one pump; Update rearms that same channel. Bind allowed conversation/coordinator epoch from service snapshot, not an arbitrary late event.
2. Ignore older revisions, wrong conversation and superseded subscription IDs. Increment subscriptionID on every explicit rebind and capture it in each pump. Session rebind cancels/drains the old subscription, clears cards/panel requests, seeds the new epoch and rearms. A terminated pump does not resubscribe itself.
3. Include work messages in the existing Update branches that allow asynchronous background updates while panels/approvals are open. Use stable scroll anchoring; update rows/hit targets only through the planner. Model-local age/receipt ticks repaint against EndedAt; they never extend receipts or persist anything.
4. Handle list/inspect/stop/ack as tea.Cmd closures calling WorkService outside Update. Capture exact conversation/key/revision/request IDs and validate replies against current panel/request. Closing/replacing a panel cannot resurrect it when a slow response returns. Stop acknowledgments only describe the accepted request; source snapshots settle actual state. Errors stay visible, with actual capability/attention unchanged.
5. Origin action resolves the actual stored node/step/report reference; missing nodes open the bounded retained detail with unavailable text. Ctrl+F still drills the actual child when available. Prompt action navigates to the existing request by canonical request ID/owner; it cannot approve/answer. Keep merged Send now composer ownership and canceled-prompt behavior, system-access routing, Ctrl+B, Ctrl+C, Alt+E and mouse-release selection.
6. In viewport_render.go's refreshViewport Snapshot literal set LiveWork to m.workService!=nil. Handle /work by opening Tasks directly on Work (the registration lands in Task 19). Existing Ctrl+T toggle works even during idle. Do not add live boxes in steps.go; the shared tree already omits them in this mode.
7. Add `TestWorkPumpSingleSubscriptionAndSessionRebind`, `TestWorkUpdateAcceptsNewRevisionWhilePromptOpen`, `TestWorkAsyncReplyCannotReopenClosedPanel`, `TestWorkStopAndAckStayAsyncAndExact`, `TestWorkPromptNavigationDoesNotResolveOrStealSteering` and `TestWorkScrollAndMouseTargetsSurviveRefresh`.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/tui -run 'Work|Steer|SendNow|Approval|Access|Tasks|Mouse' -count=1
```

Expected: responsive composer and no cross-session/stale-target UI actions.

## Task 19: Register the Work shortcut

**Goal:** /work is a discoverable TUI-only shortcut to Tasks → Work.

**Files:** `internal/commands/commands.go`, `internal/commands/commands_test.go`.

**Steps**

1. Insert this complete entry into RegisterAll's command list alongside the other groupChat TUI surfaces:

```go
{
    Name: "work",
    Description: "Open running work, results and attention",
    Group: groupChat,
    TUIOnly: true,
},
```

2. Add `TestWorkCommandIsTUIOnlyAndListed`, asserting lookup/group/description and no executable Handler. Preserve existing command registration uniqueness and ACP TUI-only handling.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/commands -run 'WorkCommand|Register|List' -count=1
```

Expected: one shortcut, no parallel command implementation.

## Task 20: Supply the runtime service to the TUI

**Goal:** production TUI uses the same facade tested by runtime and later consumed over ACP.

**Files:** `internal/app/app.go`, `internal/app/app_test.go`, `internal/app/runtime_test.go`.

**Steps**

1. In Run's existing tuiOpts construction beside WithJobBroker/WithWatchBroker, add `tui.WithWork(rt)`. No new manager, closure over initial State or TUI import of internal/agent. The Runtime pointer delegates to the current coordinator after explicit session replacement.
2. Add `TestRunWiresSharedWorkService` using WithProgramRunner and the existing fake runtime/config seams: drive a WindowSizeMsg and Ctrl+T into the empty Work tab, assert "No work yet" rather than the nil-service unavailable notice, then exit through the fixture. Add `TestWorkTUIFacadeReplacementUsesNewConversation` through the runtime facade. Update injected minimal app fixtures only when they actually exercise work; nil remains the existing unsupported fallback.
3. Run the prompt-budget guard from prompt_budget_test.go: progress schema adds bounded static fields, and card updates add no prompt-prefix content or per-tick model calls.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app -run 'RunWiresSharedWork|WorkTUIFacade|Prompt' -count=1
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/agent -run 'PromptPrefixStableAcrossSteps' -count=1
```

Expected: runtime-backed TUI and unchanged cache-stable prompt behavior.

## Task 21: Expose shared work over explicit ACP DTOs

**Goal:** the four work methods provide the same identities, pages and actions without leaking internal Go types.

**Files:** new `internal/acp/work.go` (complete file in C6); existing `internal/acp/watch.go`, `internal/acp/host.go`, `internal/acp/watch_test.go`, `internal/acp/host_test.go`.

**Steps**

1. Create work.go with C6's complete initial wire types and conversion helper. Runtime detail/stop structs receive exact lower-camel JSON tags in Task 12 (item/output/raw/report/unavailable and taskKey/state/summary/accepted respectively), so conversion is explicit and lossless. workWire serializes into independently declared DTOs, never a type alias or a returned internal struct.

The complete initial wire file is in C6; paste it unchanged.

2. Implement C5 WorkManagerACP in watch.go alongside, but independent from, WatchManagerACP. Lookup returns Runtime as viewmodel.WorkService from manager.Get even if Runner is nil. A missing session/service returns the existing server/invalid-params style; a real nil service fails closed. Do not start forwarders yet (Task 22).
3. List decodes workListParams: require sessionId; view omitted/live returns WorkSnapshotWire; attention/history delegates ListWork and returns WorkPageWire. Live rejects query/cursor/limit rather than ignoring history arguments. Inspect/Stop/Acknowledge require exact taskKey, and acknowledgment requires terminalResultRevision. Bound stop reason to 1,024 runes; pass captured conversation plus reason to facade without command/permission payload. Unknown view/bad limit/stale cursor use explicit invalid-param/stale-data errors, never reinterpret as success.
4. Inspect returns WorkDetailWire, Stop WorkStopWire, successful acknowledgment `{"acknowledged":true}` only after facade success. Map ErrWorkStaleTarget/Unsupported into explicit error messages. Preserve existing stop authorization; these handlers cannot grant system/edit access.
5. Register session/work_list, work_inspect, work_stop, work_acknowledge beside session/watch_* in agentHost.registerHandlers, using one controller per connection. Change registerHandlers to return a cleanup func and capture/defer it in serveConn, its sole current caller. Close is idempotent and initially has no active forwarders.
6. Add `TestWorkACPListLiveAndPagedShareRuntime`, `TestWorkACPMethodsRejectMissingStaleAndForeignTargets`, `TestWorkACPInspectStopAckPreserveOutcome`, `TestWorkACPWireRoundTripKeepsProgressAndRevision` and `TestWorkACPAvailableWithoutModelRunner` in existing watch_test.go/host_test.go. Compare JSON field names, nested copies and exact raw/filter behavior.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/acp -run 'WorkACP|Watch|Host' -count=1
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./web/bridge -run '^TestWebIsStdlibOnly$' -count=1
```

Expected: additive wire contract and no web/internal import.

## Task 22: Forward recoverable work updates and preserve legacy ACP

**Goal:** work_state streams share snapshot revisions and recover through work_list without duplicate subscriptions.

**Files:** `internal/acp/watch.go`, `internal/acp/host.go`, `internal/acp/stack.go`, `internal/acp/watch_test.go`, `internal/acp/stack_test.go`, `internal/acp/host_test.go`. The final docs task records wire compatibility.

**Steps**

1. Add private `ensureWorkForwarder(sessionID string, service viewmodel.WorkService)` on WorkManagerACP. First valid List/Inspect starts one subscription before seeding its snapshot; additional requests reuse it. Forward session/update {sessionId,update:{kind:"work_state",...snapshot fields}} using WorkSnapshotWire. Do not subscribe in any process-output callback or perform Notify under the controller mutex.
   Use WorkStateUpdateWire{Kind:"work_state", WorkSnapshotWire:wire} inside an explicitly tagged sessionId/update envelope passed to NotifyFunc. No JSON-to-map round trip that turns revision integers into float64.
2. Forwarders use connection lifetime contexts, not request contexts. Retire on session removal, service epoch replacement or connection close; cancel/join the old subscription before rebinding. Reconciliation/work_list is the recovery path for dropped updates; identical/older revision delivery must not recreate receipts. Close cancels all forwarders and joins them outside the mutex.
   Each map entry carries its cancel/done/epoch. Cleanup deletes only its own entry, closes done, then may rebind a still-existing session to a different epoch after a fresh lookup. The controller's closed flag blocks new/restarted subscriptions once connection cleanup starts.
3. Keep existing stack/watch/job notifications and WireBand.Entities fields for unmodified clients. Existing ACP stack requests retain their legacy default projection; work-aware clients render work_state as the authoritative live surface and ignore legacy entity listings. Document this explicit compatibility behavior; no fleet SPA changes. Work detail references use the same node/report keys as stack_node.
4. An opted-in work snapshot never changes stack root ordering or message hashes on age/progress-only ticks. Source settlement precedes final work_state; session close drains the coordinator's final snapshot before disconnect where transport remains available, then cancels the forwarder. No guarantee is inferred from an already-closed transport.
5. Add `TestWorkACPForwarderSingleSubscriptionAndRecovery`, `TestWorkACPFinalUpdateBeforeClose`, `TestWorkACPEpochReplacementRejectsLateUpdate`, `TestWorkACPLegacyStackAndWatchRemainCompatible` and `TestWorkACPProgressDoesNotChurnStackHashes`. Use in-process transports/fake service and barriers; no real endpoint/provider.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/acp -run 'WorkACP|Stack|Watch' -count=1
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./web/bridge -run '^TestWebIsStdlibOnly$' -count=1
```

Expected: one independent recoverable stream and unchanged legacy clients.

## Task 23: Ship measured producer examples and protocol usage

**Goal:** agents have runnable download, SSH-input and phased-work examples using the same bounded event protocol.

**Files:** new `docs/live-work/examples/progress.py`, `docs/live-work/examples/download.py`, `docs/live-work/examples/transfer.py`, `docs/live-work/examples/custom.py`, `docs/live-work/examples/README.md`. These examples are documentation, not new native executors.

**Steps**

1. Create each file with the complete contents below. The examples do actual measured work. They do not generate elapsed-time percentages, alter SSH trust/auth, add a watchdog or claim remote receipt from local pipe writes.

progress.py:

```python
"""Small events-v1 producer; run under shell.run with source events-v1."""
import json
import sys
import time

class Progress:
    def __init__(self):
        self.sequence = 0
        self.last = None

    def emit(self, metrics, *, phase=None, rows=None, detail="", force=False):
        now = time.monotonic()
        if not force and self.last is not None and now - self.last < 0.2:
            return
        self.sequence += 1
        if self.sequence > 2**53 - 1:
            raise ValueError("sequence exhausted")
        detail = detail.encode("utf-8")[:1024].decode("utf-8", errors="ignore")
        frame = {"version": 1, "sequence": self.sequence, "metrics": metrics,
                 "detail": detail}
        if metrics:
            frame["primaryMetricId"] = metrics[0]["id"]
        if phase is not None:
            frame["phase"] = phase
        if rows is not None:
            frame["rows"] = rows
        payload = json.dumps(frame, ensure_ascii=False, allow_nan=False)
        line = "@@marshal-progress/v1 " + payload
        if len(line.encode("utf-8")) > 16384:
            raise ValueError("progress record too large")
        print(line, file=sys.stdout, flush=True)
        self.last = now
```

download.py:

```python
"""Download URL to a new destination, counting bytes actually received."""
import argparse
import time
import urllib.request
from progress import Progress

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("url")
    parser.add_argument("destination")
    args = parser.parse_args()
    meter = Progress()
    received = 0
    started = time.monotonic()
    with urllib.request.urlopen(args.url) as response, open(args.destination, "xb") as output:
        header = response.headers.get("Content-Length", "")
        total = int(header) if header.isdigit() and 0 < int(header) <= 2**53-1 else None
        def report(force=False):
            elapsed = time.monotonic() - started
            metric = {"id": "received", "label": "Bytes received", "unit": "bytes",
                      "current": received, "rate": received / elapsed if elapsed > 0 else 0}
            if total is not None:
                metric["total"] = total
            meter.emit([metric], detail=args.destination, force=force)
        report(True)
        while True:
            chunk = response.read(256 * 1024)
            if not chunk:
                break
            output.write(chunk)
            received += len(chunk)
            report()
        output.flush()
        report(True)
    print(f"Downloaded {received} bytes to {args.destination}")

if __name__ == "__main__":
    main()
```

transfer.py:

```python
"""Send one file to SSH stdin; only the SSH exit confirms remote completion."""
import argparse
import os
import shlex
import subprocess
import time
from progress import Progress

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source")
    parser.add_argument("target", help="Configured SSH host or user@host")
    parser.add_argument("destination", help="Remote file path; may overwrite that file")
    args = parser.parse_args()
    if not args.target or args.target.startswith("-"):
        parser.error("target must be a host, not an option")
    if not args.destination or "\x00" in args.destination:
        parser.error("invalid remote file path")
    meter = Progress()
    sent = 0
    started = time.monotonic()
    with open(args.source, "rb") as source:
        total = os.fstat(source.fileno()).st_size
        remote = subprocess.Popen(
            ["ssh", "-T", args.target, "cat > " + shlex.quote(args.destination)],
            stdin=subprocess.PIPE)
        def report(force=False):
            elapsed = time.monotonic() - started
            metric = {"id": "supplied", "label": "Bytes supplied to SSH", "unit": "bytes",
                      "current": sent, "rate": sent / elapsed if elapsed > 0 else 0}
            if total > 0:
                metric["total"] = total
            meter.emit([metric], detail=args.destination, force=force)
        try:
            report(True)
            while True:
                chunk = source.read(256 * 1024)
                if not chunk:
                    break
                remote.stdin.write(chunk)
                sent += len(chunk)
                report()
            remote.stdin.flush()
            report(True)
        finally:
            try:
                remote.stdin.close()
            finally:
                code = remote.wait()
        if code:
            raise SystemExit(code)
    print(f"SSH completed successfully after accepting {sent} bytes")

if __name__ == "__main__":
    main()
```

custom.py:

```python
"""Hash actual files and write a new manifest, reporting two measured phases."""
import argparse
import hashlib
import json
import os
from progress import Progress

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest")
    parser.add_argument("files", nargs="+")
    args = parser.parse_args()
    meter = Progress()
    rows = [{"id": "hash", "label": "Hash files", "state": "running"},
            {"id": "manifest", "label": "Write manifest", "state": "pending"}]
    size = sum(os.path.getsize(path) for path in args.files)
    done = 0
    entries = []
    def report(detail="", force=False):
        metric = {"id": "hashed", "label": "Bytes hashed", "unit": "bytes", "current": done}
        if size > 0:
            metric["total"] = size
        meter.emit([metric], phase={"id": "hash", "label": "Hash files", "index": 1, "total": 2},
                   rows=rows, detail=detail, force=force)
    report(force=True)
    for path in args.files:
        digest = hashlib.sha256()
        with open(path, "rb") as source:
            while True:
                chunk = source.read(256 * 1024)
                if not chunk:
                    break
                digest.update(chunk)
                done += len(chunk)
                report(path)
        entries.append({"path": path, "sha256": digest.hexdigest()})
    rows[0]["state"] = "done"
    rows[1]["state"] = "running"
    meter.emit([], phase={"id": "manifest", "label": "Write manifest", "index": 2, "total": 2},
               rows=rows, detail=args.manifest, force=True)
    with open(args.manifest, "x", encoding="utf-8") as output:
        json.dump(entries, output, ensure_ascii=False, indent=2)
        output.write("\n")
    rows[1]["state"] = "done"
    meter.emit([{"id": "files", "label": "Files recorded", "unit": "files",
                 "current": len(entries), "total": len(entries)}],
               phase={"id": "manifest", "label": "Write manifest", "index": 2, "total": 2},
               rows=rows, detail=args.manifest, force=True)
    print(f"Wrote {len(entries)} manifest entries")

if __name__ == "__main__":
    main()
```

README.md:

```markdown
# Progress producers

These Python 3 examples emit real measurements to stdout. Start them with
shell.run's optional progress object, for example:

{"command":"python3 docs/live-work/examples/download.py URL NEW_FILE","background":true,"progress":{"profile":"download","source":"events-v1","title":"Download data"}}

Replace URL/NEW_FILE with the intended arguments and shell-quote paths. The
download refuses to overwrite an existing local file. It leaves partial
output on failure; Marshal does not automatically retry or remove it.

transfer.py SOURCE HOST REMOTE_FILE uses existing SSH configuration and may
overwrite REMOTE_FILE. It reports bytes supplied to SSH, not verified remote
bytes. A full counter still waits for the actual SSH exit. Choose profile
transfer, source events-v1. No host-key or credential checks are bypassed.

custom.py NEW_MANIFEST FILE... hashes the supplied local files and creates a
new JSON manifest. Choose profile custom, source events-v1. Its phase-local
byte counter does not claim an overall duration percentage.

For Go tests, run the intended go test -json command with profile tests and
source go-test-json. Marshal does not add flags or discover a total suite.
Other test runners can emit events-v1 with profile tests.

The stdout prefix is exactly "@@marshal-progress/v1 ". Each LF-terminated
record is a complete version-1 snapshot with an increasing sequence.
See ../feature-spec.md for numeric, text, line and cardinality limits.
stderr is ordinary diagnostic output. Keep producer diagnostics off the
control prefix. Unknown totals omit total; never fabricate a denominator.

Records describe measurements/checklists. Only the real process result can
complete or stop a card. No example is a production download/transfer backend.
```

2. Validate examples using Python's AST compile and temporary local fixtures. Execute custom.py over two files in a temp directory and verify manifest hashes plus strictly increasing valid records/final rows. For download use mocked urllib.request.urlopen with BytesIO/context-manager responses for known/unknown Content-Length. For transfer replace subprocess.Popen with an in-memory stdin/wait fixture for success/nonzero exit; no real SSH or network. Keep fixture scripts in temporary author/test directories, not new ambient-dependent repository tests.

**Verify**

```sh
python3 -c 'import ast,pathlib; [ast.parse(p.read_text(), filename=str(p)) for p in pathlib.Path("docs/live-work/examples").glob("*.py")]'
python3 docs/live-work/examples/download.py --help
python3 docs/live-work/examples/transfer.py --help
python3 docs/live-work/examples/custom.py --help
```

Run this complete local smoke fixture as well:

```sh
python3 - <<'PY'
import contextlib, hashlib, io, json, pathlib, runpy, sys, tempfile
from unittest import mock
examples = pathlib.Path("docs/live-work/examples").resolve()
sys.path.insert(0, str(examples))
prefix = "@@marshal-progress/v1 "

def run(name, args):
    output = io.StringIO()
    code = 0
    with mock.patch.object(sys, "argv", [name, *map(str, args)]), contextlib.redirect_stdout(output):
        try:
            runpy.run_path(str(examples / name), run_name="__main__")
        except SystemExit as exc:
            code = exc.code
    text = output.getvalue()
    frames = [json.loads(line[len(prefix):]) for line in text.splitlines() if line.startswith(prefix)]
    assert frames and [f["sequence"] for f in frames] == list(range(1, len(frames)+1))
    assert all(f["version"] == 1 for f in frames)
    assert all(len(line.encode()) <= 16384 for line in text.splitlines() if line.startswith(prefix))
    return frames, text, code

class Response(io.BytesIO):
    def __init__(self, data, known):
        super().__init__(data)
        self.headers = {"Content-Length": str(len(data))} if known else {}

class Pipe(io.BytesIO):
    def close(self):
        self.was_closed = True

class Remote:
    def __init__(self, code):
        self.stdin, self.code = Pipe(), code
    def wait(self):
        return self.code

with tempfile.TemporaryDirectory() as temporary:
    root = pathlib.Path(temporary)
    first, second = root/"one", root/"two"
    first.write_bytes(b"one"*500)
    second.write_bytes(b"two"*300)
    manifest = root/"manifest.json"
    frames, _, code = run("custom.py", [manifest, first, second])
    entries = json.loads(manifest.read_text())
    assert code == 0 and entries[0]["sha256"] == hashlib.sha256(first.read_bytes()).hexdigest()
    assert entries[1]["sha256"] == hashlib.sha256(second.read_bytes()).hexdigest()
    assert all(row["state"] == "done" for row in frames[-1]["rows"])
    for known in (False, True):
        destination = root/("known" if known else "unknown")
        with mock.patch("urllib.request.urlopen", return_value=Response(first.read_bytes(), known)):
            frames, _, code = run("download.py", ["https://fixture.invalid/data", destination])
        metric = frames[-1]["metrics"][0]
        assert code == 0 and destination.read_bytes() == first.read_bytes()
        assert metric["current"] == len(first.read_bytes()) and ("total" in metric) == known
    for status in (0, 9):
        remote = Remote(status)
        with mock.patch("subprocess.Popen", return_value=remote) as launch:
            frames, text, code = run("transfer.py", [first, "fixture-host", "/fixture path/result"])
        assert code == status and remote.stdin.getvalue() == first.read_bytes()
        assert frames[-1]["metrics"][0]["label"] == "Bytes supplied to SSH"
        assert ("SSH completed successfully" in text) == (status == 0)
        assert launch.call_args.args[0][-1] == "cat > '/fixture path/result'"
print("Measured producer fixtures passed; no network or SSH used.")
PY
```

Expected: three usable producers, measured local smoke fixtures pass without a LAN host or download service. Do not run the README placeholders as actual commands.

## Task 24: Complete integration verification and update the shipped contract

**Goal:** the feature passes repository checks and its final documentation matches implemented behavior.

**Files:** `docs/live-work/feature-spec.md`, this plan, `docs/ARCHITECTURE.md`; any defect found returns to its owning task before this task completes.

**Steps**

1. Update spec Implementation/status only after all preceding tasks pass. Link the examples and final plan; document the optional agentResults report-reference index and ACP legacy listing compatibility. Add internal/progress and the runtime projection to the architecture's existing layer table/narrative; identify existing source managers as sole lifecycle owners.
2. Run the full checks below. A package with no matching focused tests is not verification: confirm each task's newly named tests actually ran. Do not enable integration/live-credential flags or use developer services. Keep testenv isolation and explicit auth resolver fixtures.
3. Build the binary in a temporary location; pass USABILITY_MARSHAL_BINARY rather than replacing the developer's root binary. Add no production clocks/timeouts to appease a flaky fixture. If the known baseline find-highlighting test still fails, record it separately with baseline reproduction; do not silently skip it in the full gate.
4. Manually exercise hermetic fixtures in 80×24, 120×40 and 200×60: mixed owners/profiles, narrow resize, transcript scroll, one expansion, history search, failures/ack persistence, prompt/composer focus and exit warning with postmortem both enabled/disabled. Check no-color/16-color and mouse-release behavior. Run existing scripted usability tests after the build.
5. Record the commands/results and remaining actual limitations in the execution handoff. Stop for review if a prerequisite contract changed, a check fails without an identified baseline cause, or a required behavior is missing. Plan approval does not authorize relaxing gates or redesigning the feature.

**Verify**

```sh
git diff --check
gofmt -l internal/progress internal/app internal/agent internal/tools/native internal/tools/registry internal/watch internal/viewmodel internal/acp internal/db internal/commands
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./...
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/progress ./internal/app/session ./internal/tools/native ./internal/tools/registry ./internal/watch ./internal/viewmodel ./internal/app ./internal/acp
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go vet ./...
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go build -o /private/tmp/marshal-live-work ./cmd/marshal
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-live-work GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./test/usability/... -run TestScripted -v -timeout 5m
```

Expected: no formatting output, all required tests/race/vet/build/usability pass, web boundary remains intact.

## Integration and execution handoff

Execute in order; each task remains independently buildable. Tasks 1–11 establish values and source evidence, 12–14 add projection/actions, 15–20 deliver the pinned TUI, 21–22 add ACP, and 23 supplies measured examples. The complete feature includes all stages; an intermediate commit is not a claim that the approved feature is done.

If Send now or other concurrent work changes an anchor, rebase and adapt only the equivalent integration call while preserving the pinned contracts. Do not copy its controller into this feature. Production implementation and task commits begin only after this written plan is approved.

## Plan-authoring validation

Validated against the base commit above:

- All 24 tasks have Goal/Files/Steps/Verify; existing task paths and relative links resolve. Every newly created source/example file has complete embedded contents. The source/value contracts and task dependencies were reviewed inline.
- C1's complete source and three tests were extracted to a temporary Go overlay, compiled and passed. The new package has no physical directory yet, so its test binary was compiled with -c -vet=off and run from the temporary directory. Normal vet remains a required execution gate.
- C2/Task 5's complete DB declarations/methods compiled through an overlay. Temporary fixture tests passed for versioned save/load, rejected future versions, exact report identity/type/ownership and legacy job-exit compatibility.
- C6's complete ACP DTO file compiled through an overlay; its nested progress/revision round-trip fixture passed.
- All four Python files parsed; the complete Task 23 fixture passed for real local hashes, known/unknown download totals and successful/failed mocked SSH. It used no external service or network.
- JSON schema content, Markdown fences and whitespace were checked. The full project suite and future feature tests are execution work, not claims made by this document-only validation.

Only this plan and the approved specification were edited. No production implementation, task commit or deployment was performed.
