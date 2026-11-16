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
#   scripts/dev-daemon.sh --no-build      # restart the binaries already on disk
#   scripts/dev-daemon.sh --no-restart    # build only; the daemon keeps its old binary
#   scripts/dev-daemon.sh --install       # rewrite the unit, then restart
#   scripts/dev-daemon.sh --status        # print status; build and restart nothing
#
#   --timeout SECS   how long to wait for the daemon to answer (default 20)
#   --force          install even though a location variable points outside $HOME
#   -h, --help       this text
#
# Restart is the verb, not `daemon start`: on Linux `start` is `systemctl --user
# start`, which is a no-op while the unit is active, and the running process
# keeps the binary it exec'd. There is no `marshal daemon restart`; when idle
# restart lands, this switch becomes it.
#
# The browser studio is a different service with a different profile; see
# scripts/dev-studio.sh. This script only ever touches marshal-daemon.
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
restart=1
install=0
force=0
timeout=20

# usage prints the header comment: every leading `#` line after the shebang,
# stopping at the first line that is not one.
usage() {
	awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
}

while [ $# -gt 0 ]; do
	case $1 in
	--no-build) build=0 ;;
	--no-restart) restart=0 ;;
	--install) install=1 ;;
	--status)
		build=0
		restart=0
		;;
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

if [ "$build" -eq 1 ]; then
	echo "building marshal and webbridge"
	CGO_ENABLED=1 go build -o . ./cmd/marshal ./cmd/webbridge
	version=$(./marshal --version)
	version=${version#marshal }
	echo "built marshal ${version%% *}"
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

./marshal daemon status
