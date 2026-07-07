# Novelty-Aware Stall Detection — SDD Progress

Plan: docs/superpowers/plans/2026-07-07-novelty-aware-stall-detection.md
Branch: novelty-aware-stall-detection
Base at start: 92a7e5e (plan doc commit on top of 4a19c95 main)

(Reset 2026-07-07 for stall-detection run; prior content was TUI single-column redesign, merged.)

Tasks: 1=commit in-flight finalize+tui fixes, 2=novelty-aware tracker, 3=specific nudge, 4=e2e regression tests.

Task 1: complete (commits 92a7e5e..010bd8d + review-fix 9820b71). Review found 2 Important
(cursor Background-vs-Foreground under reverse video; misnamed extractUsefulProse test case)
+ 1 Minor (dead correction computation) — all fixed in 9820b71. User switched execution to
inline (no more subagents) from Task 2 onward.

Task 2: complete inline (adb3020, TDD red→green, full agent+swarm suites pass).
Task 3: complete inline (2e5a8ab, TDD red→green; note: plan wrongly claimed runner_test.go
  already imported fmt — added it).
Task 4: complete inline (907974f, both regression tests passed first run; full repo suite
  passes; gofmt clean; go vet: only pre-existing app.go:463 mutex-copy on main).
All tasks done. Follow-up candidates: app.go:463 `*runner = *newRunner` copies mutexes/tracker
  (pre-existing, latent race on config reload during an in-flight turn).
