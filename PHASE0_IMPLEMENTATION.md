# Phase 0 Implementation: Foundation Alignment

## Summary

Phase 0 establishes the foundational types and infrastructure for the evolution from Marshal to Swarm. This phase creates:

1. **Protocol types** (`pkg/protocol/`): SwarmEvent, ContextRef, ContextEntry
2. **Event bus** (`internal/events/`): Pub/sub system for SwarmEvent streaming
3. **Task specification** (`internal/pipeline/`): Extended TaskSpec for swarm features
4. **Bridge layer** (`internal/bridge/`): Maps Marshal types to Swarm protocol

## Files Created

### Protocol Package
```
pkg/protocol/
├── events.go          # SwarmEvent, EventKind, all payload types
├── events_test.go     # Comprehensive tests for events
├── context.go         # ContextRef, ContextEntry, EntryKind
└── context_test.go    # Tests for context types
```

### Events Package
```
internal/events/
├── bus.go             # Event bus with pub/sub
└── bus_test.go        # Tests for event bus functionality
```

### Pipeline Extension
```
internal/pipeline/
└── taskspec.go        # TaskSpec, ContextPolicy, GraphMutation
```

### Bridge Package
```
internal/bridge/
└── marshal_bridge.go    # Maps Marshal types to SwarmEvents
```

## Key Types

### SwarmEvent
The core event type used throughout the swarm system:
```go
type SwarmEvent struct {
    ID        string          // Unique identifier
    Kind      EventKind       // Event category
    SessionID string          // Owning session
    AgentID   string          // Source agent
    TaskID    string          // Related task
    ParentID  string          // Causal parent
    Timestamp time.Time       // Creation time
    Payload   json.RawMessage // Event-specific data
}
```

### ContextRef
Content-addressed reference format: `<kind>/<path>@sha256:<hash>`
```go
type ContextRef string

// Example: "files/cmd/main.go@sha256:abc123..."
ref := NewContextRef(EntryKindFile, "cmd/main.go", content)
```

### TaskSpec
Extended task specification for swarm:
```go
type TaskSpec struct {
    ID            string
    Role          string           // Agent role (e.g., "codegen")
    Goal          string           // Natural language goal
    DependsOn     []string         // Task dependencies
    OutputSchema  json.RawMessage  // Expected output structure
    ContextPolicy ContextPolicy    // Context assembly rules
    MaxIterations int              // Tool-call round limit
    Timeout       time.Duration    // Execution timeout
    Status        TaskState        // Current state
}
```

## Event Bus

The event bus provides publish/subscribe for SwarmEvents:

```go
bus := events.NewBus(100)  // Buffer size 100

// Subscribe to specific session and kinds
sub := bus.Subscribe("sess_123", 
    []protocol.EventKind{protocol.EventTaskSpawned}, 10)
defer sub.Close()

// Publish events
publisher := events.NewPublisher(bus, "sess_123", "agent_456")
publisher.TaskSpawned("task_1", "codegen", "Add feature", []string{})

// Receive events
for event := range sub.C() {
    // Handle event
}
```

## Bridge Layer

The bridge maps Marshal's internal types to SwarmEvents:

```go
bridge := marshalbridge.NewMarshalBridge(bus, sessionID)

// Convert and publish Marshal task
bridge.PublishTask(marshalTask)

// Convert and publish Marshal round
bridge.PublishRound(marshalRound)
```

## Tests

All new code includes comprehensive tests:
- Protocol types: 19 test cases covering all event kinds
- Context types: Tests for parsing, validation, hash matching
- Event bus: Tests for pub/sub, filtering, buffering, backpressure

Run tests:
```bash
go test ./pkg/protocol/... ./internal/events/... ./internal/pipeline/... ./internal/bridge/...
```

## Backward Compatibility

All Marshal functionality continues to work:
- Existing CLI commands unchanged
- Existing config format unchanged
- Existing database schema unchanged
- New packages are additive only

## Next Steps

Phase 0 is complete. Phase 1 will:
1. Refactor `internal/backend/` to `internal/gateway/`
2. Add Anthropic adapter
3. Create provider detection
4. Add router with role binding resolution

See EVOLUTION_PLAN.md for the full roadmap.
