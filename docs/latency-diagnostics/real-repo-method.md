# Real Marshal repository latency probe

The synthetic-tree probes do not reproduce working inside Marshal. This pass
uses the tracked source at `fd328c3e` in private Git checkouts and launches the
real binary through `--embedded`, its normal RPC client seam and an 80x24 PTY.
A test-owned OpenAI-compatible endpoint immediately returns scripted native tool
calls. No configured provider, real home, credentials or running daemon is used.

The workload measures startup, a cold turn, a repeated warm turn and a longer
read sequence. It reads real source, searches the actual tree, invokes Git and
runs a real Go test package with JSON output. Request timing/size, persisted tool
audits, per-call phase logs, UI completion, sampled client CPU/RSS and descendant
RSS are recorded separately. A direct execution of the same test command uses
the same isolated environment and warm build/module caches. Its wait4 CPU/RSS
and elapsed time provide command-cost context, not a different-client comparison.

Fresh process samples repeat sequentially. A second tree shape adds disposable
sparse files under ignored directory names, based on the usual checkout's major
ignored-directory counts/bytes without copying private file contents or names.
This is an ignored-directory sensitivity control, not a byte-for-byte
copy of the user's working directory. Background indexing, allocation profiles
and quiet/active resource use are investigated if the first pass identifies a
material contributor. No production optimization is part of this measurement
pass; observations determine the next change.

Final results, limitations and priorities are in
[the real-repository report](real-repo-results-2026-11-14.md).

## Reproducing a source-tree run

Run from the investigation branch. Use a fresh fixture for each invocation;
the probe deliberately requires exactly one session's expected audits and logs.

```sh
probe_tmp=$(mktemp -d /tmp/marshal-probe.XXXXXX)
mkdir "$probe_tmp/repo"
git archive fd328c3e | tar -xf - -C "$probe_tmp/repo"
git -C "$probe_tmp/repo" init -q
git -C "$probe_tmp/repo" add -A
git -C "$probe_tmp/repo" -c user.name='Latency Probe' \
  -c user.email='latency@invalid' -c core.hooksPath=/dev/null \
  commit -qm 'Disposable source fixture'
printf 'disposable Marshal latency fixture\n' \
  > "$probe_tmp/repo/.git/marshal-latency-fixture"
CGO_ENABLED=1 go build -o "$probe_tmp/marshal" ./cmd/marshal
CGO_ENABLED=1 go test -c -tags=latencyprobe \
  -o "$probe_tmp/usability.test" ./test/usability
MARSHAL_LATENCY_REPO="$probe_tmp/repo" \
  USABILITY_MARSHAL_BINARY="$probe_tmp/marshal" USABILITY_REQUIRE_BINARY=1 \
  "$probe_tmp/usability.test" -test.run '^TestMarshalRepoLatency$' \
  -test.v -test.timeout 8m
```

`MARSHAL_LATENCY_LSP=off` and `MARSHAL_LATENCY_SNAPSHOTS=off` select the
experimental controls in that invocation's private config. They never edit
the developer's configuration. The harness uses an explicit safe environment,
the same PATH-resolved Bash/pipefail invocation for both command paths, shared
warm Go build/module caches and an offline module policy. Ambient shell startup
scripts, provider credentials and language-server remote settings are dropped.
The PTY harness supports the standard DCH redraw sequence the fast control
triggered; this change affects the test decoder only.

Each successful invocation verifies 29 model requests, 26 successful persisted
tool calls, completed commands with zero exit codes, 26 timing records, six
persisted pre-write snapshots (zero in the disabled control), and usable
resource samples in startup, ready-idle, active and quiet-idle windows. Any
resource sampling failure fails the run. Failure cleanup kills only descendants
of the still-live fixture client before closing it.

For separate profiles, build the binary with `-tags=latencyprobe` and supply a
fresh `MARSHAL_LATENCY_PROFILE_PREFIX` under the temporary directory. Full
profiling starts before main; `MARSHAL_LATENCY_PROFILE_MODE=quiet` profiles
only the ten-second post-work quiet window. The harness signals its own client
PID to start/stop, waits for profile completion and then quits cleanly. The
tagged helper is absent from normal builds. No profiling is enabled in the
unprofiled timing baselines.
