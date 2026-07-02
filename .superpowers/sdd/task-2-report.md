Task 2 Report: Parse Routing Config

Summary
- Added TDD coverage for routing config parsing and project-overrides-global merge behavior in `internal/app/config/config_test.go`.
- Extended `internal/app/config/config.go` to parse `[models.presets.*]`, `[agent_profiles.*]`, and `[agents.<role>.context]` into routing-backed config fields.
- Kept the change scoped to the two owned files and preserved legacy `[agent]` parsing.

TDD Record
1. Added `TestLoadParsesRoutingConfig` and `TestLoadRoutingConfigProjectOverridesGlobalByKey`.
2. Ran:
   - `go test ./internal/app/config -run 'TestLoadParsesRoutingConfig|TestLoadRoutingConfigProjectOverridesGlobalByKey' -v`
3. Observed RED:
   - Build failed because `Config` did not yet expose `Models`, `AgentProfiles`, or `Agents`.
4. Implemented the minimal config parsing and merge logic.
5. Re-ran the focused test command and got GREEN.

Implementation Notes
- Added `Config.Models`, `Config.AgentProfiles`, and `Config.Agents`.
- Added local TOML decode structs for model presets and context budgets so snake_case keys decode correctly without changing the routing package.
- Added conversion helpers from config-local decode structs into `routing.ModelPreset`, `routing.AgentProfile`, and `routing.ContextBudget`.
- Merge semantics follow the brief:
  - presets overwrite by preset name
  - agent profiles overwrite by profile name
  - agent role configs overwrite by role key

Verification
- `go test ./internal/app/config -run 'TestLoadParsesRoutingConfig|TestLoadRoutingConfigProjectOverridesGlobalByKey' -v`
- `go test ./internal/app/config -v`
- `go test ./...`

Self-Review
- Confirmed the first red failure happened before implementation.
- Confirmed project config overwrites global routing entries by top-level key, matching provider merge behavior.
- Confirmed legacy `[agent]` parsing remains intact and full repo tests stay green.

Concerns
- None.
