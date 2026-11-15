#!/usr/bin/env bash
# Browser checks for the Fleet-first Studio shell and the project/factory
# settings surfaces.
#
# Builds the SPA and `webbridge`, starts the bridge against a throwaway project
# with an isolated HOME/config/data dir and a fixture config, then runs the
# Playwright suite at desktop (1440x900), tablet (768x1024) and phone (390x844).
#
# Two fixture styles run here and they are not interchangeable:
#   - real bridge: the shell's routes, deep links, route guard, roadmap and the
#     project/factory settings reads and writes, driven against the API this
#     bridge really serves (e2e/studio-shell.pw.ts, e2e/project-settings.pw.ts);
#   - mocked presentation: e2e/studio-fleet.pw.ts answers the fleet, inbox and
#     factory reads from e2e/fixtures.ts to render states this credential-free
#     bridge cannot hold. That is evidence about rendering only, never that the
#     bridge produced the state.
#
# Neither style creates, approves, retries or recovers factory work: there is no
# GitHub credential here and factoryAPI.preflight requires one. No provider is
# reachable and no credentials are read.
#
#   scripts/e2e/run_browser_checks.sh
#   PW_PORT=7801 KEEP=1 scripts/e2e/run_browser_checks.sh   # keep the fixture
#
# Environment:
#   PW_PORT      port the fixture bridge listens on (default 7799)
#   PW_TOKEN     bearer token the bridge and the suite use (default e2e-token)
#   PW_WORKDIR   scratch directory for the fixture, bridge log and binary
#                (default a fresh temp dir). Unless KEEP=1, it is deleted on
#                exit, so do not point it at a real directory.
#   KEEP=1       keep the fixture config, project and bridge log afterwards
#   SKIP_BUILD=1 reuse an existing SPA build and skip building webbridge
#
# Requires `npm install` in web/ui and the browser:
#   cd web/ui && npx playwright install chromium
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
port="${PW_PORT:-7799}"
token="${PW_TOKEN:-e2e-token}"
workdir="${PW_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/marshal-browser-checks.XXXXXX")}"
project="$workdir/proj"
bridge_pid=""

cleanup() {
  if [[ -n "$bridge_pid" ]]; then
    kill "$bridge_pid" 2>/dev/null || true
    wait "$bridge_pid" 2>/dev/null || true
  fi
  if [[ "${KEEP:-0}" == "1" ]]; then
    echo "kept: $workdir (bridge log: $workdir/bridge.log)"
  else
    rm -rf "$workdir"
  fi
}
trap cleanup EXIT

mkdir -p "$project" "$workdir/config" "$workdir/data" "$workdir/home"
git init -q "$project"

# A provider that is never called, plus three agent profiles so the role
# pickers have real options to follow and override.
cat > "$workdir/config/config.toml" <<'TOML'
[providers.mock]
type = "openai_compatible"
base_url = "http://127.0.0.1:9/v1"
api_key = "mock-key"

[models.presets."mock/alpha"]
[models.presets."mock/beta"]
[models.presets."mock/gamma"]

[agent_profiles.alpha]
implementer = "mock/alpha"
reviewer = "mock/alpha"
planner = "mock/alpha"
tester = "mock/alpha"
repo_scout = "mock/alpha"

[agent_profiles.beta]
implementer = "mock/beta"
reviewer = "mock/beta"
planner = "mock/beta"
tester = "mock/beta"
repo_scout = "mock/beta"

[agent_profiles.gamma]
implementer = "mock/gamma"
reviewer = "mock/gamma"
planner = "mock/gamma"
tester = "mock/gamma"
repo_scout = "mock/gamma"
TOML

if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  echo "==> building the SPA"
  (cd "$repo_root/web/ui" && npm run build >/dev/null)
  echo "==> building webbridge"
  (cd "$repo_root" && CGO_ENABLED=1 go build -o "$workdir/webbridge" ./cmd/webbridge)
fi

echo "==> starting the fixture bridge on 127.0.0.1:$port"
HOME="$workdir/home" \
MARSHAL_CONFIG_DIR="$workdir/config" \
MARSHAL_DATA_DIR="$workdir/data" \
XDG_CONFIG_HOME="$workdir/config" \
XDG_DATA_HOME="$workdir/data" \
  "$workdir/webbridge" \
    --profile server \
    --addr "127.0.0.1:$port" \
    --token "$token" \
    --project "$project" \
    --cwd-root "$project" \
    --state-dir "$workdir/state" \
    --workspace "$workdir/state/workspace.json" \
    --egress off \
    >"$workdir/bridge.log" 2>&1 &
bridge_pid=$!

base="http://127.0.0.1:$port"
ready=0
for _ in $(seq 1 50); do
  if curl -fsS -H "Authorization: Bearer $token" "$base/api/projects/settings?root=$(printf %s "$project" | sed 's|/|%2F|g')" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "$bridge_pid" 2>/dev/null; then
    echo "bridge exited; log:" >&2
    cat "$workdir/bridge.log" >&2
    exit 1
  fi
  sleep 0.2
done

# The loop can also end on the last probe's failure, with the bridge still
# alive but not answering — a bad config, or a machine slower than the budget.
# Without this check Playwright would run against an unready bridge and report
# the resulting browser failures as suite bugs.
if [[ "$ready" != "1" ]]; then
  echo "bridge did not answer on $base within 10s; log:" >&2
  cat "$workdir/bridge.log" >&2
  exit 1
fi

echo "==> running the browser checks"
cd "$repo_root/web/ui"
PW_BASE_URL="$base" PW_TOKEN="$token" PW_PROJECT_ROOT="$project" npx playwright test "$@"
