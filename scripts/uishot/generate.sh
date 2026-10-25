#!/usr/bin/env bash
# Regenerate the README's UI images.
#
#   docs/ui/tui-session.svg   the TUI answering a question, with tool calls
#   docs/ui/web-fleet.png     the Web Studio dashboard
#
# Requires: go (cgo), python3, node, plus `npm install` and
# `npx playwright install chromium` in this directory. Run from the repo root.
#
# Everything runs against throwaway state: a temp MARSHAL_DATA_DIR, a temp demo
# project, and the bridge's own --state-dir. Your real config, database and
# trust store are never touched, and no capture writes into the repository.

set -euo pipefail

# Go needs HOME to find its module cache; some agent shells start without one.
# Only set it if missing, so a normal shell is untouched.
if [ -z "${HOME:-}" ]; then
  export HOME="$(getent passwd "$(id -u)" | cut -d: -f6)"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HERE="$ROOT/scripts/uishot"
OUT="$ROOT/docs/ui"

WORK="$(mktemp -d)"
cleanup() {
  [ -n "${BRIDGE:-}" ] && kill "$BRIDGE" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

FONT="$ROOT/web/ui/node_modules/@fontsource-variable/geist-mono/files/geist-mono-latin-wght-normal.woff2"
MODEL="${UISHOT_MODEL:-}"

echo "==> building marshal"
( cd "$ROOT" && go build -o "$WORK/marshal" ./cmd/marshal )

# A tiny throwaway project so the transcript shows real tool calls on real
# files. It carries no .marshal/config.toml, so the folder-trust prompt that
# marshal shows for a config-bearing project never appears.
echo "==> staging demo project"
mkdir -p "$WORK/proj"
cp "$ROOT/test/usability/fixtures/go-calc/calc.go" "$WORK/proj/"
cp "$ROOT/test/usability/fixtures/go-calc/calc_test.go" "$WORK/proj/"
printf 'module democalc\n\ngo 1.21\n' > "$WORK/proj/go.mod"
# Make it a real repository: a coding agent runs in one, and a shell-out to git
# should report the project's state rather than "not a git repository".
( cd "$WORK/proj" && git init -q && git add -A \
  && git -c user.name=uishot -c user.email=uishot@example.invalid \
       commit -qm "demo project" )

# ---------------------------------------------------------------------------
# TUI -> SVG
# ---------------------------------------------------------------------------
mkdir -p "$WORK/cfg"
cp "$HERE/config.example.toml" "$WORK/cfg/config.toml"
if [ -n "$MODEL" ]; then
  # BSD sed (macOS) requires an explicit backup suffix for -i; the .bak file is
  # discarded. This form works with both BSD and GNU sed.
  sed -i.bak "s|^model = .*|model = '$MODEL'|" "$WORK/cfg/config.toml"
  rm -f "$WORK/cfg/config.toml.bak"
fi
CAPTURE_MODEL="$(sed -n "s/^model = '\(.*\)'/\1/p" "$WORK/cfg/config.toml")"

echo "==> capturing TUI (model: $CAPTURE_MODEL)"
echo "    a local provider must be reachable, or the transcript will show an error"
# The first delay has to outlast startup (config load, DB open, provider
# probe), which takes a few seconds; keystrokes sent before the input box is
# live are dropped and the transcript stays empty. Override with UISHOT_KEYS
# for a different question or a slower machine.
KEYS="${UISHOT_KEYS:-6.0:Explain what this code does,0.8:\\r}"
python3 "$HERE/capture.py" "$WORK/marshal" "$WORK/tui.bin" \
  --width 120 --height 34 --seconds "${UISHOT_SECONDS:-30}" \
  --workdir "$WORK/proj" \
  --config-dir "$WORK/cfg" \
  --data-dir "$WORK/data" \
  --keys "$KEYS"

echo "==> rendering TUI SVG"
mkdir -p "$OUT"
if [ -f "$FONT" ]; then
  node "$HERE/render.mjs" "$WORK/tui.bin" "$OUT/tui-session.svg" \
    --cols 120 --rows 34 --font "$FONT"
else
  echo "    (font not found — rendering unembedded; columns may drift)"
  node "$HERE/render.mjs" "$WORK/tui.bin" "$OUT/tui-session.svg" \
    --cols 120 --rows 34
fi

# ---------------------------------------------------------------------------
# Web Studio -> PNG
# ---------------------------------------------------------------------------
echo "==> capturing Web Studio"
( cd "$ROOT" && go build -o "$WORK/webbridge" ./cmd/webbridge )

TOKEN="uishot-$$-$RANDOM"
"$WORK/webbridge" \
  --addr 127.0.0.1:7719 \
  --token "$TOKEN" \
  --state-dir "$WORK/wbstate" \
  --project "$WORK/proj" \
  > "$WORK/bridge.log" 2>&1 &
BRIDGE=$!

for _ in $(seq 1 40); do
  if curl -fsS -o /dev/null "http://127.0.0.1:7719/" 2>/dev/null; then break; fi
  sleep 0.5
done

node "$HERE/shot.mjs" http://127.0.0.1:7719 "$OUT/web-fleet.png" \
  --token "$TOKEN" --width 1440 --height 900

kill "$BRIDGE" 2>/dev/null || true
wait "$BRIDGE" 2>/dev/null || true
BRIDGE=""

echo
echo "==> wrote:"
ls -la "$OUT"
