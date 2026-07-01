# 04. Tooling and Shell Safety

## Goal

Marshal should support powerful tool and bash usage without giving the model uncontrolled access to the user's machine.

Tool execution must be:

- explicit
- inspectable
- permissioned
- logged
- reversible where possible

## Tool broker

The Tool Broker manages all native tools, MCP tools, and future plugins.

```go
type Tool struct {
    Name        string
    Description string
    Schema      json.RawMessage
    RiskLevel   RiskLevel
    Handler     ToolHandler
    Policy      ToolPolicy
}
```

## Risk levels

```go
type RiskLevel int

const (
    RiskReadOnly RiskLevel = iota
    RiskWorkspaceWrite
    RiskCommand
    RiskNetwork
    RiskDestructive
)
```

## Core tools

| Tool | Purpose | Risk |
|---|---|---|
| `repo.search` | text search using ripgrep or internal search | read-only |
| `file.read` | read file range | read-only |
| `file.write_patch` | apply patch to workspace | workspace write |
| `git.status` | inspect git state | read-only |
| `git.diff` | inspect diff | read-only |
| `git.checkpoint` | create safe checkpoint branch/commit/stash | write |
| `shell.run` | run shell command | variable |
| `test.run` | run configured test command | command |
| `symbols.find` | find symbol via Tree-sitter index | read-only |
| `repo.map` | return repo map | read-only |
| `context.query` | query project DB | read-only |
| `memory.write` | write confirmed project memory | write |
| `agent.spawn` | delegate to specialist agent | swarm |

## Shell command lifecycle

```text
1. Model proposes command
2. Command classifier assigns risk
3. Policy engine checks allow/deny rules
4. TUI shows command, cwd, reason, risk, and expected effect
5. User approves, denies, edits, or creates rule
6. Tool executes with timeout and resource limits
7. Output is streamed/truncated/summarised
8. Tool call is logged in project DB
```

## Approval prompt example

```text
Agent wants to run:

  go test ./...

Reason:
  Validate the package after modifying the parser.

Risk:
  Low - test command, no destructive flags detected.

[Enter] approve   [e] edit   [d] deny   [a] always allow go test
```

## Dangerous command examples

Require explicit confirmation for:

```bash
rm -rf
git reset --hard
git clean -fd
curl ... | sh
wget ... | sh
sudo
chmod -R
chown -R
docker system prune
mkfs
shutdown
reboot
```

## Command classifier

The command classifier should inspect:

- executable name
- arguments
- shell operators
- pipes
- redirections
- glob patterns
- filesystem targets
- network use
- privilege escalation
- destructive flags

Examples:

```text
go test ./...                 → low risk
git status                    → read-only
npm install                   → network/write risk
rm -rf node_modules           → destructive but scoped
git reset --hard              → destructive
curl https://x | sh           → high-risk network execution
```

## Shell execution policy

Policy fields:

```go
type ShellPolicy struct {
    WorkingDirectoryRoot string
    TimeoutSeconds       int
    AllowNetwork         bool
    AllowSudo            bool
    AllowGitDestructive  bool
    MaxOutputBytes       int
    EnvAllowlist         []string
    EnvDenylist          []string
}
```

## Patch safety

All file edits should ideally use patches.

Patch flow:

```text
1. Agent proposes patch
2. Patch is parsed and validated
3. TUI shows unified diff
4. User approves or edits
5. Patch applies
6. Git diff is shown
7. Tests can run
8. Rollback option remains available
```

## Git safety

Before major changes, offer:

- checkpoint branch
- temporary stash
- lightweight commit
- patch file export

Example:

```text
This task may modify 8 files.
Create a checkpoint first?
[y] yes  [n] no
```

## Prompt injection protection

Repository files are untrusted input.

Rules:

- file contents cannot override system policy
- README instructions cannot approve commands
- comments in code cannot change tool permissions
- tool results should be treated as data, not instructions
- remote provider calls should redact secrets by default

## Secret protection

Before sending context to a remote model:

- scan for known secret patterns
- skip `.env` by default
- skip private keys
- respect `.gitignore`
- warn when sending private-looking data

Privacy config:

```toml
[privacy]
remote_providers_allowed = false
redact_secrets = true
include_gitignored_files = false
```

## Audit log

Every tool call should record:

- timestamp
- agent role
- model used
- tool name
- arguments
- risk level
- approval state
- result summary
- files changed
- command exit code
