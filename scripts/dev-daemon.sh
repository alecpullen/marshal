#!/usr/bin/env bash
#
# scripts/dev-daemon.sh — put this checkout's build in front of the local
# daemon, without cutting a release.
#
# The daemon's unit pins binary *paths*, not contents: `marshal daemon install`
# resolves ./marshal and the webbridge beside it once, and systemd/launchd then
# re-executes whatever sits at those paths. So the loop for testing a working
# tree on this box is: rebuild in place, restart the service. The unit is
# written once and stays valid across rebuilds.
#
#   scripts/dev-daemon.sh                 # rebuild both binaries, restart, print status
#   scripts/dev-daemon.sh --studio        # also build the Studio into share/marshal/studio
#   scripts/dev-daemon.sh --no-build      # restart the binaries already on disk
#   scripts/dev-daemon.sh --no-restart    # build only; the daemon keeps its old binary
#   scripts/dev-daemon.sh --install       # rewrite the unit, then restart
#   scripts/dev-daemon.sh --status        # print status; build and restart nothing
#   scripts/dev-daemon.sh --dev-image     # also refresh the agent image from ./marshal (fast)
#   scripts/dev-daemon.sh --agent-image   # also rebuild the container agent image (slow)
#
#   --studio         build the Studio, before the Go build. The daemon reads
#                    those files on each request, so a Studio-only change needs
#                    `--studio --no-build --no-restart`: no relink, no restart
#   --timeout SECS   how long to wait for the daemon to answer (default 20)
#   --force          install even though a location variable points outside $HOME
#   -h, --help       this text
#
# Restart is the verb, not `daemon start`: on Linux `start` is `systemctl --user
# start`, which is a no-op while the unit is active, and the running process
# keeps the binary it exec'd. There is no `marshal daemon restart`; when idle
# restart lands, this switch becomes it.
#
# The browser Studio is this daemon's web listener, set with `[daemon]
# web_listen`: the daemon serves share/marshal/studio from that address on each
# request, and `marshal studio` opens it. There is no second service and no
# second unit — this script only ever touches marshal-daemon.
#
# The Studio is a Flutter build, so --studio needs `flutter` on PATH, and its
# files are a second artifact beside the two binaries. It is built into
# share/marshal/studio, which is where the daemon looks for it: beside the
# binaries, and under the prefix for an installed one.
#
# The agent image is a third: an agent inside a container runs the marshal
# baked into the image, not this checkout's, so a host rebuild does not reach
# it.
#
#   --dev-image   builds build/Dockerfile.dev, which COPYs the host-built
#                 ./marshal into a base matching this machine's release. The
#                 copy makes it seconds, and it is tagged :latest, which is what
#                 a source build's default project settings resolve to — this is
#                 the one to use on the dev loop.
#   --agent-image builds build/Dockerfile, which compiles marshal in a golang
#                 builder stage. Portable, publishable, and slow: it recompiles
#                 the module and runs on the release base, whose glibc this host
#                 cannot link against.
#
# The status every run ends with says when the image is missing or older than
# ./marshal, and names the origins another machine can reach the web listener
# on.
#
# A restart ends running turns. A session screen reconnects for up to a minute
# and keeps its draft, and project children are stopped with the daemon and
# respawned on the next attach, so a stale child cannot outlive the restart.
# Nothing here watches the tree — an automatic trigger would restart the daemon
# mid-turn — so run it between turns.
#
# `go build ./cmd/marshal ./cmd/webbridge` without -o only type-checks the two
# mains and writes nothing; `-o .` is what puts both binaries in the repo root,
# which is the path the unit holds. CGO is required: tree-sitter
# (internal/repo/symbols.go) links a C library.
#
# One thing this cannot prove on its own: two `+dirty` builds of the same
# commit share a version string, so `daemon status` cannot tell them apart.
# The daemon's "started" time is what proves the restart happened; the version
# line moves on commit.

set -euo pipefail

# -P: the physical path, so it matches what the unit holds even from a
# symlinked checkout.
repo_root=$(cd -P "$(dirname "$0")/.." && pwd -P)
cd "$repo_root"

build=1
studio=0
restart=1
install=0
force=0
timeout=20
agent_image=0
dev_image=0

# agent_image_ref is the tag the daemon resolves for a source build: a dev
# version has no published tag to pin to, so defaultAgentImageFor answers
# :latest. Both image builds tag this, so a project's default agent settings
# resolve to whatever was built here.
agent_image_ref=ghcr.io/marshal/agent:latest

# runtime is the container runtime the image flags need, empty when neither is
# installed: without one there is no image to build and nothing to report on.
runtime=$(command -v podman || command -v docker || true)

# usage prints the header comment: every leading `#` line after the shebang,
# stopping at the first line that is not one.
usage() {
	awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
}

while [ $# -gt 0 ]; do
	case $1 in
	--no-build) build=0 ;;
	--studio) studio=1 ;;
	--no-restart) restart=0 ;;
	--install) install=1 ;;
	--status)
		build=0
		restart=0
		;;
	--agent-image) agent_image=1 ;;
	--dev-image) dev_image=1 ;;
	--force) force=1 ;;
	--timeout)
		shift
		if [ $# -eq 0 ]; then
			echo "dev-daemon.sh: --timeout wants whole seconds" >&2
			exit 2
		fi
		case $1 in
		'' | *[!0-9]*)
			echo "dev-daemon.sh: --timeout wants whole seconds, got '$1'" >&2
			exit 2
			;;
		esac
		timeout=$1
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "dev-daemon.sh: unknown option '$1' (try --help)" >&2
		exit 2
		;;
	esac
	shift
done

case $(uname -s) in
Linux)
	unit_path="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/marshal-daemon.service"
	;;
Darwin)
	unit_path="$HOME/Library/LaunchAgents/dev.marshal.daemon.plist"
	;;
*)
	echo "dev-daemon.sh: the local daemon runs on macOS and Linux only (this is $(uname -s))" >&2
	exit 1
	;;
esac

# The daemon's directory mirrors config.DataDir: MARSHAL_DATA_DIR outright,
# else XDG_DATA_HOME/marshal, else ~/.local/share/marshal.
if [ -n "${MARSHAL_DATA_DIR:-}" ]; then
	daemon_log="$MARSHAL_DATA_DIR/daemon/webbridge.log"
elif [ -n "${XDG_DATA_HOME:-}" ]; then
	daemon_log="$XDG_DATA_HOME/marshal/daemon/webbridge.log"
else
	daemon_log="$HOME/.local/share/marshal/daemon/webbridge.log"
fi

# under_home reports whether $1 is $HOME or below it.
under_home() {
	case $1 in
	"$HOME" | "$HOME"/*) return 0 ;;
	*) return 1 ;;
	esac
}

# check_location_env guards the one irreversible half: `daemon install` copies
# the location variables that are set into the unit, so an install from a test
# shell would pin the daemon to a temporary directory for good. A value under
# $HOME is a layout someone chose deliberately and only gets a note.
check_location_env() {
	local var value outside="" inside=""
	for var in MARSHAL_CONFIG_DIR MARSHAL_DATA_DIR XDG_CONFIG_HOME XDG_DATA_HOME; do
		value=${!var:-}
		[ -n "$value" ] || continue
		if under_home "$value"; then
			inside="$inside $var=$value"
		else
			outside="$outside $var=$value"
		fi
	done
	if [ -n "$outside" ] && [ "$force" -eq 0 ]; then
		printf 'dev-daemon.sh: refusing to write a unit that pins these paths:%s\n' "$outside" >&2
		printf 'dev-daemon.sh: daemon install copies the location variables into the unit,\n' >&2
		printf 'dev-daemon.sh: so the service would outlive the directory. Unset them, or pass --force.\n' >&2
		exit 1
	fi
	if [ -n "$inside" ]; then
		printf 'note: the unit will pin%s\n' "$inside"
	fi
}

# restart_service restarts the unit the path above names.
restart_service() {
	if [ "$(uname -s)" = "Darwin" ]; then
		launchctl kickstart -k "gui/$(id -u)/dev.marshal.daemon"
		return
	fi
	systemctl --user restart marshal-daemon.service
}

# wait_ready polls until the daemon answers on its socket. `daemon status`
# exits non-zero exactly when the socket is silent, so it is the probe; a
# restart stops the old process before starting the new one, so an answer
# here is always the new binary's.
wait_ready() {
	local deadline
	deadline=$(($(date +%s) + timeout))
	while :; do
		if ./marshal daemon status >/dev/null 2>&1; then
			return 0
		fi
		if [ "$(date +%s)" -ge "$deadline" ]; then
			return 1
		fi
		sleep 0.2
	done
}

# ---------------------------------------------------------------- build ----

# flutter_ready names the toolchain the Studio build needs. There is no npm
# step and no web/ui to install into: the Studio is the Flutter app in app/,
# built with `flutter build web` run from there.
flutter_ready() {
	if ! command -v flutter >/dev/null 2>&1; then
		echo "dev-daemon.sh: flutter is missing, so the Studio cannot be built" >&2
		echo "dev-daemon.sh: install Flutter 3.50+, or drop --studio to rebuild the binaries only" >&2
		exit 1
	fi
	if [ ! -f app/pubspec.yaml ]; then
		echo "dev-daemon.sh: app/pubspec.yaml is missing, so this checkout has no Studio to build" >&2
		exit 1
	fi
}

if [ "$studio" -eq 1 ]; then
	flutter_ready
	# --base-href / is where the daemon serves the Studio, and
	# --no-web-resources-cdn is not optional: without it the build fetches the
	# CanvasKit engine from gstatic, and a private network that cannot reach
	# that CDN never starts the Studio. --output is relative to app/, which is
	# where the command runs, and share/marshal/studio is one of the two places
	# the daemon looks — beside the binaries, which is what this checkout is.
	echo "building the Studio into share/marshal/studio"
	(cd app && flutter build web --base-href / --no-web-resources-cdn --output ../share/marshal/studio)
fi

if [ "$build" -eq 1 ]; then
	echo "building marshal and webbridge"
	CGO_ENABLED=1 go build -o . ./cmd/marshal ./cmd/webbridge
	version=$(./marshal --version)
	version=${version#marshal }
	echo "built marshal ${version%% *}"
fi

# host_base_image names the container base whose glibc matches this machine's.
# The fast image copies a host-built binary, and marshal links tree-sitter
# through cgo, so that binary needs a libc at least as new as the one it was
# linked against — debian:bookworm-slim is too old on a Fedora/Ubuntu host
# newer than bookworm, and the mismatch surfaces as "GLIBC_2.x not found".
host_base_image() {
	local os_id os_ver
	os_id=$(. /etc/os-release 2>/dev/null && printf '%s' "${ID:-}")
	os_ver=$(. /etc/os-release 2>/dev/null && printf '%s' "${VERSION_ID:-}")
	case $os_id in
	fedora | debian | ubuntu)
		[ -n "$os_ver" ] || return 1
		printf 'docker.io/library/%s:%s\n' "$os_id" "$os_ver"
		return 0
		;;
	esac
	return 1
}

# build_dev_image refreshes the agent image from the host build. It copies the
# binary instead of compiling it in the image, so a rebuild is a copy rather
# than a module compile — which is what makes it usable on every commit.
build_dev_image() {
	if [ -z "$runtime" ]; then
		echo "dev-daemon.sh: --dev-image needs podman or docker on PATH" >&2
		exit 1
	fi
	if [ ! -x ./marshal ]; then
		echo "dev-daemon.sh: --dev-image needs ./marshal; drop --no-build" >&2
		exit 1
	fi
	local base ctx stamp
	if ! base=$(host_base_image); then
		base=docker.io/library/debian:stable-slim
		echo "note: no base could be derived from /etc/os-release" >&2
		echo "note: building on ${base}; if the agent reports 'GLIBC_2.x not found'," >&2
		echo "note: rebuild with --build-arg BASE_IMAGE=<an image matching this host>" >&2
	fi
	# The repository root as a build context would hand app/build, the Flutter
	# tree's own artifacts, and .marshal/worktrees to the runtime; the image
	# needs the one binary.
	ctx=$(mktemp -d)
	cp ./marshal "$ctx/marshal"
	# The label records which ./marshal went in, so the status can tell a stale
	# image from a current one by comparing integers.
	stamp=$(stat -c %Y ./marshal)
	echo "building the dev agent image from ./marshal on ${base}"
	if ! "$runtime" build --build-arg "BASE_IMAGE=$base" \
		--label "marshal.dev.bin-mtime=$stamp" \
		-f build/Dockerfile.dev -t "$agent_image_ref" "$ctx"; then
		rm -rf "$ctx"
		return 1
	fi
	rm -rf "$ctx"
}

if [ "$dev_image" -eq 1 ]; then
	build_dev_image
fi

if [ "$agent_image" -eq 1 ]; then
	if [ -z "$runtime" ]; then
		echo "dev-daemon.sh: --agent-image needs podman or docker on PATH" >&2
		exit 1
	fi
	# A source build's default image is the untagged-build fallback
	# (defaultAgentImageFor), so tagging :latest is what makes a project's
	# default agent settings resolve to the image built here. It is the slow
	# step: it compiles marshal inside the image, on the release base.
	echo "building the agent image (slow; the agent binary is baked in)"
	"$runtime" build -f build/Dockerfile -t "$agent_image_ref" .
fi

if [ "$install" -eq 1 ]; then
	if [ ! -x "$repo_root/webbridge" ]; then
		echo "dev-daemon.sh: $repo_root/webbridge is missing; drop --no-build or build it first" >&2
		exit 1
	fi
fi

if [ "$restart" -eq 1 ] || [ "$install" -eq 1 ]; then
	if [ ! -x "$repo_root/marshal" ]; then
		echo "dev-daemon.sh: $repo_root/marshal is missing; drop --no-build" >&2
		exit 1
	fi
fi

# ------------------------------------------------------- install / restart --

restarted=0
if [ "$install" -eq 1 ]; then
	check_location_env
	echo "rewriting the unit at $unit_path"
	./marshal daemon install
	restarted=1
elif [ "$restart" -eq 1 ]; then
	if [ -e "$unit_path" ]; then
		echo "restarting the daemon"
		restart_service
		restarted=1
	else
		# Nothing to restart, and the unit has to name this checkout's
		# binaries anyway, so the first run installs.
		check_location_env
		echo "unit not installed; installing it for this checkout"
		./marshal daemon install
		restarted=1
	fi
fi

if [ "$restarted" -eq 1 ] && ! wait_ready; then
	echo "dev-daemon.sh: the daemon did not answer within ${timeout}s of the restart" >&2
	echo "dev-daemon.sh: last lines of $daemon_log" >&2
	tail -n 20 "$daemon_log" >&2 || true
	echo "dev-daemon.sh: systemctl --user status marshal-daemon.service says why" >&2
	exit 1
fi

# Only --no-restart leaves a *freshly built* binary out of the daemon's reach;
# a plain --status is a query and needs no such warning.
if [ "$build" -eq 1 ] && [ "$restart" -eq 0 ] && [ "$install" -eq 0 ]; then
	echo "note: the daemon was not restarted; it keeps the binary it launched with"
fi

# ---------------------------------------------------------------- status ---

# print_agent_image_notes says when the agent image is missing or older than
# ./marshal. The agent's marshal comes from the image, so a host rebuild does
# not reach a container: this is the third artifact to refresh beside the two
# binaries and the Studio. Nothing prints without a container runtime, because
# nothing could use the image.
print_agent_image_notes() {
	local bin_epoch stamp
	[ -n "$runtime" ] || return 0
	if ! "$runtime" image inspect "$agent_image_ref" >/dev/null 2>&1; then
		printf 'note:    no %s image; spawning an agent needs one\n' "$agent_image_ref"
		printf 'note:    scripts/dev-daemon.sh --dev-image builds it from ./marshal in seconds\n'
		return 0
	fi
	# build_dev_image stamps ./marshal's mtime into the image, so staleness is
	# a comparison of two integers. The image's own created time cannot be
	# used: podman prints it with nanoseconds, which date -d refuses, and it is
	# not what the agent runs.
	bin_epoch=$(stat -c %Y ./marshal 2>/dev/null || true)
	stamp=$("$runtime" image inspect -f '{{index .Labels "marshal.dev.bin-mtime"}}' "$agent_image_ref" 2>/dev/null || true)
	case $stamp in
	'' | *[!0-9]*) return 0 ;;
	esac
	if [ -n "$bin_epoch" ] && [ "$bin_epoch" -gt "$stamp" ]; then
		printf 'note:    %s was built from an older ./marshal; agents would run a stale build\n' "$agent_image_ref"
		printf 'note:    scripts/dev-daemon.sh --dev-image refreshes it\n'
	fi
}

# remote_urls prints the origins another machine can reach the daemon's web
# listener on: every `tailscale serve` entry whose proxy target is the
# listener's own port. The listener binds loopback or a unix socket, so a
# loopback address is the only case with routes of this shape, and the tailnet
# root is usually someone else's service (443 here proxies :8200) — so the port
# comes from the daemon's own report, and only an entry forwarding to it is
# named. jq is not assumed: python3 reads the one field.
remote_urls() {
	local info listen port
	info=$(./marshal daemon status --json 2>/dev/null) || return 0
	listen=$(printf '%s' "$info" | python3 -c '
import json, sys
try:
    doc = json.load(sys.stdin)
except ValueError:
    sys.exit(0)
info = doc.get("daemon") or {}
print(info.get("webListen", ""))
' 2>/dev/null) || return 0
	case $listen in
	127.0.0.1:* | localhost:* | \[::1\]:*) port=${listen##*:} ;;
	*) return 0 ;;
	esac
	[ -n "$port" ] || return 0
	command -v tailscale >/dev/null 2>&1 || return 0
	tailscale serve status 2>/dev/null | awk -v port="$port" '
		$1 ~ /^https?:\/\// { url = $1; next }
		$NF ~ ("^https?://(127\\.0\\.0\\.1|localhost|\\[::1\\]):" port "$") && url != "" {
			print url
		}
	' || true
}

# `daemon status` exits non-zero exactly when the socket is silent, and that
# answer is this script's own, so the notes below it must not swallow it.
daemon_exit=0
./marshal daemon status || daemon_exit=$?

print_agent_image_notes
remotes=$(remote_urls)
if [ -n "$remotes" ]; then
	while IFS= read -r remote; do
		printf 'remote:  %s/\n' "$remote"
	done <<<"$remotes"
fi
exit "$daemon_exit"
