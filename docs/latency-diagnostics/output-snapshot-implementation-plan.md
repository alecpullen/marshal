# Output and snapshot reuse implementation plan

**Goal:** reduce repeated output cleaning and snapshot allocations without
changing command lifecycles, redaction or snapshot protection.

**Design:** implement the contracts in
`output-and-snapshot-followup-2026-11-14.md`. Keep a bounded, synchronized,
exact-input cleaned-tail cache on each work coordinator. Keep codecs and
scratch buffers on each snapshot object writer; compare buffered source bytes
directly when verifying existing objects.

**Execution:** native implementation in the existing isolated worktree, then
one independent whole-change review. User approved proceeding with the design.

## Constraints and review focus

- No daemon transport, installed binary, live configuration or capture-frequency changes.
- Full-input redaction precedes clipping. Fresh lifecycle/action/progress fields.
- Cache bounded to 64 entries / 4 MiB of owned key, revision, raw and cleaned strings.
- Exact source identity/length, corruption checks, Git agreement, storage budgets,
  cancellation and durability/publication order remain enforced.
- No global mutable factories, plaintext buffers or verified-object caches.
- Review same-revision output corrections, concurrent reconciles, dropped history,
  codec reset after failures, and corrupt existing objects after a prior success.

## Task 1: Completed output projection

Files: new `internal/app/work_output_cache.go` and tests; modify coordinator
projection in `internal/app/runtime.go`.

- [x] Write tests for bounded exact-input memo behavior (same revision changes,
  epochs/identities, oversized inputs, owned strings, pruning and concurrency),
  terminal projection with fresh capability/progress/save-error fields, and
  skipped shadowed retained output. Observe the expected failure first.
- [x] Implement zero-value-safe synchronized cache; compute cleaning outside
  its lock; own retained strings and evict least-recently-used entries. Only
  settled tails use it. Prune identities absent from the command projection.
- [x] Move the existing durable-winner guard before retained item construction.
  Keep all other precedence/settling rules.
- [x] Run app tests and focused race checks. Commit the component.

## Task 2: Per-capture snapshot scratch state

Files: new `internal/snapshot/object_codec.go` and tests; modify
`internal/snapshot/object_writer.go`.

- [x] Write tests proving reset/reuse preserves Git object bytes, resets after
  errors, compares already-hashed buffered source exactly, rejects corrupt,
  truncated, longer and wrong-header objects, and observes corruption after a
  prior successful verification. Add allocation benchmarks; observe failure.
- [x] Reuse writer/reader codecs, source buffer (up to 8 MiB) and 64 KiB copy
  buffer within each object writer. Release codec file references after use.
- [x] Use direct header/body comparison for already-buffered blob sources;
  retain hash verification for streaming sources. Keep all store-level proofs.
- [x] Run snapshot suite and focused race checks. Commit the component.

## Task 3: Validation and evidence

Files: real-repository measurement artifacts and updated result report under
`docs/latency-diagnostics/`.

- [x] Run component benchmarks and complete default Go suite, required embedded
  test binary and vet. Report any failures explicitly.
- [x] Repeat three sequential real-client runs with LSP off / snapshots on,
  plus a separate quiet profile, against a fixed private Marshal source tree.
  Compare workload/audit/snapshot assertions and resources with prior evidence.
- [x] Independent review; address material findings with reproducing tests.
- [x] Commit verified code and evidence; keep this branch separate from main.

## Progress

Base: `797a7f5a` (investigation), main merge `cef1e353`.
Tasks share no production interfaces. Validation consumes both components.

Task 1 complete: `8a306691`, app suite 10.005s; focused Go 1.27 race 16.897s.
Task 2 complete: `e7be9c13`, snapshot suite 107.768s; focused Go 1.27 race 3.599s.
RED evidence: settled projection 252,144 allocations/op; unchanged blob 210,072 bytes/op before fixes. Both resource regressions passed after fixes.

Task 3 complete: integrated projection benchmark; 3 preliminary and 6 paired real-client runs; 2 separate profiles. Full Go suite passed 123 packages, no failures; full/tagged vet passed. Independent production review found dynamic-secret registration invalidation; reproducing test RED->GREEN and full app/redact/race checks passed after fix 43c2a590. Independent evidence review found no issues.
