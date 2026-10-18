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
