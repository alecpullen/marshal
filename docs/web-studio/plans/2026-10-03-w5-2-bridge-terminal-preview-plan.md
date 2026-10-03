# W5.2 · Bridge: terminal and preview — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w5-automations-and-ops-design.md`](../specs/2026-10-03-w5-automations-and-ops-design.md) §5.1–§5.2, §6.1 (Test shell)
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Runs after:** W1–W4 and [W5.1](2026-10-03-w5-1-engine-plan.md).
**Base:** the branch once W5.1 is complete. Bridge anchors were checked on
`2ddc09e`. The following are as their plans define them:
- from W2–W4: `Registry.call`, `ErrUnsupported`, `ContainerConfig.ExtraEnv`,
  `workspaceNetworkEnv`, `ResolveWorkspace`, the egress sidecar and
  control socket;
- `Agent.ContainerName`, and `fleet.json` v10.

**Plan slug:** `w5-2-bridge-terminal-preview`. Commit each task as
`w5-2-bridge-terminal-preview: task N — <title>`.

## Goal

- Terminals into agents, with hold and hand-back, recording, and audit.
- A shell in a one-off workspace container, for "Test shell".
- Preview reverse proxying for declared ports, through the sidecar in
  container mode or directly in process mode.
- `MARSHAL_WORKSPACE` in agent env.

## Non-goals

- UI (W5.5).

## Assumptions

- W5.1 agents implement `session/hold` and emit `hold` updates.
- The runtime seam `commandRunner` (`container.go:71`) only returns
  buffered output. Task 1 adds a streaming seam beside it.

---

## Task 1: Streaming exec seam and `MARSHAL_WORKSPACE`

**Goal:** a testable way to start a long-lived runtime process with
stdin and stdout, plus the workspace env var.

**Files:**
- `web/bridge/execstream.go` (new), `execstream_test.go` (new)
- `web/bridge/wsspawn.go` (W4.2)

**Steps:**

1. Define `type streamProc interface { Stdin() io.WriteCloser; Stdout() io.Reader; Wait() error; Kill() }`
   and `type streamStarter func(dir string, name string, args ...string) (streamProc, error)`.
   - The production implementation uses `exec.Command` with
     `cmd.Env = clientEnv()`, combines stdout and stderr through a pipe,
     and sets `cmd.Dir` when `dir` is given.
   - `Fleet` gets a `streamer streamStarter` field. Nil means production,
     mirroring `f.runner`.
2. In W4.2's spawn path, add `MARSHAL_WORKSPACE=<name>` to `ExtraEnv`
   when the agent has a workspace.
3. Tests:
   - a fake `streamStarter` that echoes input;
   - the production starter running `cat` locally (skipped when `cat` is
     missing);
   - the env var is present in the run args for a workspace spawn.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestExecStream|TestSpawnWorkspace' -v
```

---

## Task 2: Terminal sessions

**Goal:** open, stream, input, resize and close terminals, with hold,
auto-release, recording and audit (spec §5.1).

**Files:**
- `web/bridge/terminal.go` (new), `terminal_test.go` (new)
- `web/bridge/http.go`, `web/bridge/audit.go`

**Steps:**

1. Define `terminal{id, agentID string; proc streamProc; log *os.File; held bool; lastInput time.Time; bytesIn, bytesOut int64; events *EventLog key}`.
   `Fleet` tracks terminals in a map from agent ID to terminals, with a
   maximum of 2 per agent (`ErrTooManyTerminals`, which becomes 429).
2. **Open** (`POST /api/agents/{id}/terminal {cols, rows}`):
   - **Container mode:** start
     `exec -i -w /work -e TERM=xterm-256color <containerName> script -qfc "stty rows R cols C; exec ${SHELL:-sh}" /dev/null`
     through `streamer` with `dir` empty. Get `containerName` from
     `a.ContainerName`, or from `containerNameFor`.
   - **Process mode:** run the same `script` command with `dir` set to the
     agent's active root. Get the root from the `root` field of W2.1's
     `Fleet.Files(ctx, id, "")`.
   - Create `<state>/terminal/<agentId>/<unix>.log`, mode `0600`.
   - Audit `terminal_opened`.
3. **Output:** a goroutine reads `Stdout()` in chunks of up to 32 KiB. It
   appends `{data: base64}` to `f.termLog` (a dedicated `EventLog`) under
   the key `term:<tid>`, and writes the raw bytes to the log file. On EOF
   it appends `{exit: code}`. Serve
   `GET …/terminal/{tid}/events` with `f.termLog.ServeSSEKey`.
4. **Input** (`POST …/input {data}`):
   - decode it, enforcing the 64 KiB limit;
   - write it to `Stdin()` and to the log file;
   - on the first byte, call `rt.reg`/`call` with `session/hold {on:true}`
     and append the fleet delta
     `{kind:"hold", sessionId, held:true, by:"terminal"}`;
   - update `lastInput`.
5. **Resize** (`POST …/resize {cols, rows}`) writes `stty rows R cols C\n`
   to stdin. This is documented as best effort.
6. **Hand back** (`POST /api/agents/{id}/terminal/{tid}/release`), and
   **close** (`DELETE …`), and a 2-minute idle ticker, all call
   `session/hold {on:false}` and append the delta. Close also kills the
   process, closes the log, and audits `terminal_closed` with
   `Detail: in=N out=M`.
7. Tests, with the fake streamer:
   - the exec args in container mode;
   - process mode uses the root;
   - output arrives on the SSE key;
   - the first input sends hold;
   - release sends unhold;
   - the idle timer releases after 2m (injected clock);
   - the limit of two;
   - the recording file contains the input and output;
   - audit entries.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestTerminal' -race -v && go test ./
```

---

## Task 3: Test shell for workspaces

**Goal:** `POST /api/workspaces/{name}/shell` opens a terminal in a
one-off container of the template's image (spec §6.1).

**Files:**
- `web/bridge/terminal.go`, `terminal_test.go`

**Steps:**

1. Resolve `<name>@<published>`, which must be built (`ErrWorkspaceNotBuilt`,
   W4.2).
2. Start
   `run --rm -i --name marshal-shell-<rand> --network marshal-agents <workspace mounts and env> <image> script -qfc "exec sh" /dev/null`
   through `streamer`. Reuse W4.2's `workspaceMounts` and
   `workspaceNetworkEnv`, using a synthetic agent ID of `shell-<rand>` so
   the proxy issues it a policy.
3. Register it as a terminal under owner `workspace:<name>`. The routes
   are the same as Task 2, under `/api/workspaces/{name}/shell/{tid}/…`.
   There's no hold, because there's no agent. Closing kills the process,
   and `--rm` removes the container.
4. Tests: the run args include the image and mounts, and closing kills
   the process.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestTestShell' -v
```

---

## Task 4: Preview tokens and routing

**Goal:** `POST /api/agents/{id}/preview/{port}` issues a token, and
`/preview/<agent>/<port>/…` authenticates by token or cookie
(spec §5.2).

**Files:**
- `web/bridge/preview.go` (new), `preview_test.go` (new)
- `web/bridge/http.go` (`ServeHTTP` `:91`)

**Steps:**

1. **Issuing.** `POST /api/agents/{id}/preview/{port}` checks that the
   port is in the agent's resolved workspace `preview.ports` (403
   otherwise). It creates 24 random bytes as base64url, stores the token
   in memory with the agent, port and expiry (12h), and returns
   `{url: "/preview/<id>/<port>/?t=<token>"}`.
2. **Routing.** In `ServeHTTP`, before the static handler, route paths
   starting with `/preview/` to `s.preview`. It:
   - parses `<id>/<port>/<rest>`;
   - accepts `?t=` when the token matches: it sets the cookie
     `mp_<id>_<port>` (HttpOnly, SameSite=Strict, `Path` set to the
     prefix, `Secure` when the request is TLS) and redirects to the same
     path without `t`;
   - otherwise requires the cookie to match a live token;
   - returns 404 on any failure.
3. Tests:
   - an undeclared port gives 403;
   - token, then cookie, then a request reaches the forwarder (stubbed in
     this task);
   - an expired token gives 404;
   - a path for another port with the same cookie gives 404.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestPreviewAuth' -v
```

---

## Task 5: Preview forwarding

**Goal:** forward preview traffic to the agent: through the sidecar in
container mode, directly in process mode (spec §5.2).

**Files:**
- `web/bridge/preview.go`, `preview_test.go`
- `web/bridge/egress_proxy.go`, `egresshost.go` (W4.3)

**Steps:**

1. **Sidecar.** `webbridge egress` gains `--preview-listen :8081`. Its
   handler parses `/<agentId>/<port>/…`, looks the agent up in the current
   policy, and refuses ports not in its `PreviewPorts`. Add `PreviewPorts []int`
   to `EgressAgentPolicy`, filled at spawn from the workspace doc. It
   forwards with `httputil.ReverseProxy` to `http://<agent IP>:<port>/<rest>`.
   `Upgrade` (WebSocket) passes through.
2. **Sidecar start** (W4.3 Task 7) adds `-p 127.0.0.1::8081`. The bridge
   reads the chosen host port with `port marshal-egress 8081` and stores
   it.
3. **Bridge forwarder:**
   - in container mode, it reverse-proxies `/preview/<id>/<port>/…` to
     `http://127.0.0.1:<sidecarPort>/<id>/<port>/…`;
   - in process mode, it goes to `http://127.0.0.1:<port>/…`;
   - it strips the `mp_` cookies and `Authorization` before forwarding.
4. Tests:
   - the sidecar handler with a policy containing a port forwards to an
     `httptest` upstream, and refuses other ports;
   - the bridge forwarder in process mode reaches an `httptest` server
     on its port;
   - a WebSocket upgrade passes through, using a raw upgrade handshake
     against a minimal test upgrader written with `net/http` hijack.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestPreview|TestEgress' -race -v && go test ./... && go vet ./...
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./... && go build ./cmd/webbridge
cd web/bridge && go test ./... -race && go vet ./...
cd ../.. && CGO_ENABLED=1 go test ./... ; gofmt -l .
```

Expected:
- the bridge passes, including `TestWebIsStdlibOnly`;
- the repo-wide run fails only on the five known tests.

Check by hand: in container mode, open a terminal and run `ls`. The agent
shows held, and **Hand back** resumes it. Start `python3 -m http.server 3000`
in a workspace that declares port 3000, and open the preview.

## Integration notes

- `script` must exist in the agent image. It's part of util-linux, which
  Debian-based images ship. Workspace builds (W4.2) use Debian bases, and
  the default agent image is checked in the manual step. Terminal open
  returns a clear error ("script not found in image") when it's missing.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. |
| Anchors verified? | Checked on `2ddc09e`: `commandRunner` (`container.go:71`), `containerNameFor` (`:366`), `ServeHTTP` (`http.go:91`), `clientEnv` (`env.go:39`). Later symbols are as defined. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §5.1–§5.2, plus the Test shell from §6.1. |
