#!/usr/bin/env bash
#
# scripts/studio-smoke.sh — serve the Studio from a real daemon, and check it.
#
# The Studio is files a daemon serves, not a bundle webbridge embeds: the app
# job builds it into share/marshal/studio, and this proves the daemon finds it
# there and serves it through plan 3's request guard. It starts a real webbridge
# with a unix socket and a free loopback web listener, in directories of its own
# under a short temporary directory, and asks the web listener what a browser
# would ask it.
#
# What it checks, failing with a message on the first that does not hold:
#
#   1. GET / answers 200, and the document names flutter_bootstrap.js — the
#      entry point of a Flutter 3.50 web build, not the no-files page;
#   2. GET /api/agents answers 200 with a JSON array;
#   3. POST /api/agents with Sec-Fetch-Site: cross-site answers 403: the guard's
#      refusal of a request another site's page made on the browser's behalf;
#   4. GET /api/agents with Host: evil.example answers 403: the daemon answers
#      only to the address it publishes.
#
# It needs two things in the repository root, which
# `scripts/dev-daemon.sh --studio --no-restart` makes together:
#
#   ./webbridge                      # go build -o webbridge ./cmd/webbridge
#   share/marshal/studio/index.html  # the Studio's build, as an install ships it
#
# The daemon is stopped on exit, whatever happened, and the temporary directory
# goes with it: no port, no socket and no process outlive the run.

set -euo pipefail

# -P: the physical path, so it matches where the daemon looks even from a
# symlinked checkout.
repo_root=$(cd -P "$(dirname "$0")/.." && pwd -P)
cd "$repo_root"

bridge=./webbridge
studio=share/marshal/studio

if [ ! -x "$bridge" ]; then
	echo "studio-smoke.sh: no webbridge at $bridge; build it: go build -o $bridge ./cmd/webbridge" >&2
	exit 1
fi
if [ ! -f "$studio/index.html" ]; then
	echo "studio-smoke.sh: no Studio build at $studio; make one: scripts/dev-daemon.sh --studio --no-restart" >&2
	exit 1
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/marshal-studio-smoke.XXXXXX")
daemon_pid=

cleanup() {
	if [ -n "$daemon_pid" ]; then
		kill "$daemon_pid" 2>/dev/null || true
		wait "$daemon_pid" 2>/dev/null || true
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# fail names what did not hold and prints what the daemon said: a daemon that
# would not start and one that answered wrongly read the same otherwise.
fail() {
	echo "studio-smoke.sh: $*" >&2
	if [ -s "$tmp/daemon.log" ]; then
		echo "--- webbridge said:" >&2
		cat "$tmp/daemon.log" >&2
	fi
	exit 1
}

# status_of <curl args...>: the status code of a request, its body in $tmp/body.
status_of() {
	curl -sS --max-time 5 -o "$tmp/body" -w '%{http_code}' "$@"
}

# The daemon's state, its fleet file and its data (the trust store, the model
# cache) are all under $tmp, so it reads and writes nothing of the developer's.
MARSHAL_DATA_DIR="$tmp/data" "$bridge" \
	--socket "$tmp/s.sock" \
	--state-dir "$tmp/state" \
	--workspace "$tmp/fleet.json" \
	--web-listen 127.0.0.1:0 \
	--studio-dir "$studio" \
	>"$tmp/daemon.log" 2>&1 &
daemon_pid=$!

info=
deadline=$((SECONDS + 20))
while [ "$SECONDS" -lt "$deadline" ]; do
	if info=$(curl -fsS --max-time 2 --unix-socket "$tmp/s.sock" http://d/api/daemon 2>/dev/null); then
		break
	fi
	info=
	kill -0 "$daemon_pid" 2>/dev/null || fail "the daemon exited before it answered on the socket"
	sleep 0.1
done
[ -n "$info" ] || fail "the daemon did not answer GET /api/daemon within 20s"

web_listen=$(printf '%s' "$info" | python3 -c 'import json, sys; print(json.load(sys.stdin).get("webListen") or "")') ||
	fail "GET /api/daemon did not answer JSON with a webListen"
if [ -z "$web_listen" ]; then
	web_error=$(printf '%s' "$info" | python3 -c 'import json, sys; print(json.load(sys.stdin).get("webError") or "")')
	fail "the daemon bound no web listener: ${web_error:-webListen is empty}"
fi
web="http://$web_listen"

status=$(status_of "$web/") || fail "GET / did not answer on $web_listen"
[ "$status" = 200 ] || fail "GET / answered HTTP $status, want 200"
grep -q 'flutter_bootstrap\.js' "$tmp/body" ||
	fail "GET / does not name flutter_bootstrap.js, so the daemon is not serving the Studio build: $(head -c 200 "$tmp/body")"

status=$(status_of "$web/api/agents") || fail "GET /api/agents did not answer"
[ "$status" = 200 ] || fail "GET /api/agents answered HTTP $status, want 200"
python3 -c 'import json, sys; sys.exit(0 if isinstance(json.load(open(sys.argv[1])), list) else 1)' "$tmp/body" ||
	fail "GET /api/agents is not a JSON array: $(head -c 200 "$tmp/body")"

status=$(status_of -H 'Sec-Fetch-Site: cross-site' -H 'Content-Type: application/json' --data '{}' "$web/api/agents") ||
	fail "POST /api/agents did not answer"
[ "$status" = 403 ] || fail "a cross-site POST /api/agents answered HTTP $status, want 403 from the web listener's guard"

status=$(status_of -H 'Host: evil.example' "$web/api/agents") || fail "GET /api/agents did not answer"
[ "$status" = 403 ] || fail "GET /api/agents with Host: evil.example answered HTTP $status, want 403"

echo "studio-smoke.sh: the Studio is served from $web_listen, and the guard holds"
