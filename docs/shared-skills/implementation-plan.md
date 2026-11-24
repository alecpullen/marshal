# Shared Skills Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans for native execution or superpowers:subagent-driven-development if delegated execution is selected. Track the checkboxes in order.

**Goal** Make the agent user's shared skills available automatically and add a per-name preference to `/skills`.

**Architecture** Keep source precedence and provenance in `internal/skills`. Build the runtime index and the ACP skill listing from the same rules. Persist preference changes on the agent side and keep the TUI on the client seam.

**Tech stack** Go, TOML, ACP, Bubble Tea.

**Spec** [Shared skill discovery](design.md)

## Global constraints

- Default order is project, Marshal global, built-in, shared. A per-name preference swaps the last two.
- Shared files are read-only through Marshal's skill manager.
- An active skill keeps its current instructions until unloaded and loaded again.
- Resolve home on the machine running the agent. Keep project configuration out of user preference writes.

## Review focus

- A missing shared root or broken bundle link leaves existing skills available.
- An invalid preferred shared copy falls back to the built-in.
- Matching directory names across stores retain distinct identities.
- A failed config write leaves the effective preference and active instructions intact.
- A higher-priority copy can mask the saved preference and should be identified in the panel.

## 1. Discover and resolve skills

Files include `internal/skills/loader.go`, `internal/app/runtime.go`, and the skills config in `internal/app/config/types.go`. The resolver should return both candidates with source identities and a winner per declared name.

- [ ] Add fixtures for all four sources, linked shared bundles, missing and invalid shared copies, and duplicate source filenames. Assert the same winner with no preference as before, and assert that preference only changes the built-in versus shared decision.
- [ ] Run `scripts/test.sh quick ./internal/skills ./internal/app/config ./internal/app` and confirm the new expectations fail.
- [ ] Discover the shared root from the runtime home. Apply linked-bundle discovery in that source. Reuse existing parsing and keep removal identity checks for managed stores.
- [ ] Add user-only `prefer_shared` decoding and use the resolver to populate the runtime index, including the inputs consumed by existing named and autoload paths.
- [ ] Rerun the package tests and commit the discovery and resolution change.

## 2. Expose and persist preferences through ACP

Files include `internal/acp/service/skills.go`, `internal/acp/skills.go`, `internal/acpwire`, and `internal/acpclient` and its RPC implementation.

- [ ] Add service and transport tests for source identity, winner, preference and action availability. Cover failed persistence, shared removal rejection, restart persistence, and future loads while a skill with the same name is already active.
- [ ] Run `scripts/test.sh quick ./internal/acp/... ./internal/acpclient/... ./internal/acpwire ./internal/app` and confirm the new expectations fail.
- [ ] Extend the listing with built-in and shared provenance, effective winner and saved preference. Keep managed entry identity scoped to its source. Revalidate the selection when acting on it.
- [ ] Add a typed preference operation taking a declared skill name and a boolean. Update only the user config's preference list via the existing config write path. Serialize concurrent updates, preserve unrelated settings, and publish updated resolution after persistence succeeds. Existing active instructions stay in session state.
- [ ] Rerun the package tests and commit the ACP change.

## 3. Add the action to /skills

Files include `internal/app/tui/skills/panel.go`, its existing panel tests, and user-facing skills documentation.

- [ ] Add a panel test for a built-in and shared pair. Assert action labels reflect the saved preference, success refreshes the winner, a project winner remains visible, and failures display without claiming success.
- [ ] Run `scripts/test.sh quick ./internal/app/tui/skills ./internal/app/tui` and confirm the new expectations fail.
- [ ] Add source and winner indicators and wire **Prefer shared copy** and **Use built-in copy** through ACP. Restrict install and remove actions to managed stores. Document automatic discovery, precedence, the TOML list and the panel action.
- [ ] Rerun the package tests, run `scripts/test.sh quick`, and verify preference changes through the normal `/skills` entry point in embedded and daemon modes. Commit the panel and documentation change.
