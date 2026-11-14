#!/usr/bin/env bash
# Browser checks for the project/factory settings surfaces.
#
# Builds the SPA and `webbridge`, starts the bridge against a throwaway project
# with an isolated HOME/config/data dir and a fixture config, then runs the
# Playwright suite at desktop and phone widths. No provider is reachable and no
# credentials are read: every case reads and writes settings and drafts.
#
#   scripts/e2e/run_browser_checks.sh
#   PW_PORT=7801 KEEP=1 scripts/e2e/run_browser_checks.sh   # keep the fixture
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
for _ in $(seq 1 50); do
  if curl -fsS -H "Authorization: Bearer $token" "$base/api/projects/settings?root=$(printf %s "$project" | sed 's|/|%2F|g')" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$bridge_pid" 2>/dev/null; then
    echo "bridge exited; log:" >&2
    cat "$workdir/bridge.log" >&2
    exit 1
  fi
  sleep 0.2
done

echo "==> running the browser checks"
cd "$repo_root/web/ui"
PW_BASE_URL="$base" PW_TOKEN="$token" PW_PROJECT_ROOT="$project" npx playwright test "$@"
