# Task 3 Report: Config File Loading and Precedence

## What I implemented
- Added `config.Load(LoadOptions)` to load config from:
  - `$HOME/.config/marshal/config.toml`
  - `$WORKING_DIR/.marshal/config.toml`
- Kept `config.Default()` as the base config and merged file values on top in the required precedence order.
- Treated missing config files as non-errors.
- Returned path-aware errors for read and parse failures.
- Added TOML parsing via `github.com/pelletier/go-toml/v2`.
- Added tests for:
  - missing config files
  - project config overriding global config
  - malformed config reporting the file path

## What I tested and test results
- `GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app/config -count=1`
- Result: PASS

## TDD Evidence
### RED
- Command:
  - `GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app/config -count=1`
- Output:
  - `undefined: Load`
  - `undefined: LoadOptions`
  - `FAIL marshal/internal/app/config [build failed]`

### GREEN
- Command:
  - `GOCACHE=/private/tmp/coder-agent-gocache go test ./internal/app/config -count=1`
- Output:
  - `ok   marshal/internal/app/config`

## Files changed
- `internal/app/config/config.go`
- `internal/app/config/config_test.go`
- `go.mod`
- `go.sum`

## Self-review findings
- Merge behavior matches the brief’s precedence rules.
- Missing files are ignored instead of failing load.
- Parse errors include the config path, which makes troubleshooting usable.
- Tests cover the required surface, but only the config package was run.

## Any issues or concerns
- None.
