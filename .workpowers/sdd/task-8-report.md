# Task 8 Report: App wiring (role resolver + swarm runner factory)

## Summary

Modified `internal/app/app.go` to make the routed provider resolver concurrency-safe, expose `ResolveRole`, and wire the Milestone O swarm orchestrator via a new `buildSwarmRunner` factory.

## Changes Made

- `routedProviderResolver` now guards its provider cache with `sync.Mutex`.
- Added `ResolveRole(role routing.AgentRole) (routing.Route, provider.Provider, error)`.
- Extracted shared provider construction into `providerFor`.
- Changed `buildAgentRunner` return signature to `(*agent.Runner, *registry.Registry, *swarm.Orchestrator, error)`.
- Added `buildSwarmRunner` that:
  - Uses `registry.ReadOnlyView(reg)` for read-only roles.
  - Shares one `*swarm.WriteLock` across all role runners.
  - Resolves per-role provider/model via `resolver.ResolveRole`.
  - Configures each role runner with shared state, policy, memory provider, project ID, and a forced "question" class.
- Updated `Run()` to capture the new `swarmRunner` return value; added temporary `_ = swarmRunner` to avoid the unused-variable error until Task 9 adds `tui.WithSwarmRunner`.

## Verification

```bash
gofmt -w internal/app
go vet ./internal/app/...
go build ./...
go test ./internal/app/...
```

Result: PASS.

Full suite (`go test ./... 2>&1 | tail -5`): all packages PASS.

## Commit

```bash
git add internal/app/app.go
git commit -m "feat(app): wire swarm orchestrator with per-role routing and shared write lock"
```
