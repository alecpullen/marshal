# ADR-001: `db.FileIndex` and `db.Symbol` live in `internal/db/`

## Context

F-POL-133 in `docs/14-codebase-improvement-audit-2026-07-14.md` notes that
`db.FileIndex` and `db.Symbol` are defined in `internal/db/` but are
heavily used by `internal/repo/` and `internal/tools/native/`, creating
a `db → repo → db` import graph that is theoretically reversible (a
neutral `internal/repoindex/` package would break the cycle). The audit
offers two options: move the types or accept the coupling.

## Decision

We accept the coupling. The marshal schema is small (one file index,
one symbol table) and the alternative — a third package with no
behaviour and only types — adds an import alias layer without buying
any decoupling in practice. If a future need (e.g. a different
storage backend) requires the split, this ADR is the place to revisit.

## Consequences

- The `internal/repo` package will continue to import `internal/db`.
- Tests for `internal/repo` may continue to use `db` helpers.
- New DB-backed fields on `FileIndex` / `Symbol` do not require
  changes in `internal/repo`.
