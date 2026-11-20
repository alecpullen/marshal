# Marshal web-bridge end-to-end suite

Drives the live bridge over HTTP the way the SPA does, across every route
group, and reports defects with the request that produced them. Standard
library only, so it runs anywhere the bridge does.

```bash
python3 scripts/e2e/run_e2e.py \
    --base unix:/run/user/1000/marshal/daemon/web.sock \
    --project /tmp/w5proj --project2 /tmp/w5proj2 \
    --repo w5forge --workspace w5prev --provider ollama-cloud-2
```

`--base` names the daemon's web listener: `unix:/path/web.sock` dials that
socket, and `http://127.0.0.1:PORT` (the `[daemon] web_listen` a loopback
address gives) is plain HTTP. The listener asks for no credential, so the
suite sends none; the routes that belong to the daemon's own socket — project
attach, pin, restart, hosts report, enrollment — answer 404 there, and one
group H check pins that.

Every flag also reads an `E2E_*` environment variable (`E2E_BASE`,
`E2E_PROJECT`, …). `--groups` selects a subset, e.g. `ABH`; `--json
report.json` writes a machine-readable report; `-v` prints each check as it
runs. The exit code is non-zero when any check fails.

## Groups

| Group | Covers |
|---|---|
| A sessions | config, projects, session create/list/load/delete, prompt, cancel, steer, mode, stack, nodes, roster, last-request, step-diffs |
| B agents | spawn, list, diff, files, file, commit-draft, gate, verify, patch, merge, discard, exit |
| C terminal | terminal open/input/resize/release/close, events, preview port policy and origin |
| D ops | recipes, schedules, notifications, status links, library, models, budgets, usage, watches, prompts |
| E workspaces | templates, drafts, patches, publish, builds, diff, pool, CA rotation, test shell |
| F secrets | secrets, credentials, network view, egress decisions |
| G forge | repos, issues, review drafts, CI history, webhook secrets |
| H errors | socket-only routes, malformed and oversize bodies, unknown routes, traversal, SSE, MCP, clients, pending |
| I studio shell | the Studio directory the daemon names, the shell it serves from it, its bootstrap and cache policy, client-route fallback, and that the engine is not fetched from a CDN |

## Writing a check

A check is a function that exercises one behaviour and records a verdict.
Raise `Skip` when a precondition the environment lacks makes the check
meaningless; raise `Failure` (or use the `expect*` helpers) to record a
defect. The harness keeps going after a failure and prints every defect
with the request that produced it.

```python
def register(s: Suite) -> None:
    s.area("Z example")

    def my_check(c: Ctx) -> None:
        r = c.client.get("/api/thing")
        body = s.expect_ok(r, request="GET /api/thing")
        s.expect_keys(body, ["id"], request="GET /api/thing")

    s.check("thing round trip", my_check)
```

State passes between checks through `c.set(key, value)` / `c.get(key)`.

## Notes

- Mutations write to the bridge's own state directory and its isolated
  config, never the operator's real config. Point `--state-dir` at a
  throwaway location if you are unsure.
- Checks that spawn agents consume concurrency slots. The suite discards
  what it creates, but a crashed run can leave agents behind; discard them
  before re-running if the pool fills.
- A group that needs a precondition the environment lacks skips its
  checks rather than failing, so a green run on a partial environment is
  not the same as a green run on a full one. Read the skip list.
- The daemon under test needs its Studio files, or group I's shell checks
  skip: `scripts/dev-daemon.sh --studio` builds and serves them in a
  checkout.
