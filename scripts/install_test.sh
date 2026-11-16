#!/bin/sh
# Offline tests for scripts/install.sh --uninstall.
#
# No network and no real binaries: the "installed" marshal is a stub that
# prints canned `daemon status` output and logs the arguments it was called
# with, so every assertion is about what install.sh actually did.
#
# The bug this guards: the gate matched `^service: loaded` only, so
#     marshal daemon install && marshal daemon stop && install.sh --uninstall
# removed both binaries and left a launchd/systemd unit pointing at deleted
# files. `service: stopped` is exactly that installed-but-not-running case.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
installer="$here/install.sh"

failures=0
sandboxes=""

cleanup() {
	for dir in $sandboxes; do
		rm -rf "$dir"
	done
}
trap cleanup EXIT INT TERM

fail() {
	echo "not ok - $*" >&2
	failures=$((failures + 1))
}

ok() {
	echo "ok - $*"
}

# new_sandbox: create a throwaway prefix holding a stub `marshal` (which logs
# its calls to $sandbox/calls) and a stand-in `webbridge` to remove.
new_sandbox() {
	sandbox=$(mktemp -d) || {
		echo "install_test.sh: mktemp -d failed" >&2
		exit 1
	}
	sandboxes="$sandboxes $sandbox"
	mkdir -p "$sandbox/prefix" "$sandbox/home"
	printf 'stand-in\n' >"$sandbox/prefix/webbridge"
	cat >"$sandbox/prefix/marshal" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>"$STUB_CALLS"
case "$1 $2" in
'daemon status')
	[ -z "$STUB_STATUS" ] || printf '%s\n' "$STUB_STATUS"
	# A real `daemon status` exits non-zero whenever the daemon does not
	# answer, which a stopped service never does: install.sh has to decide
	# from the status line, not from this exit code.
	exit 1
	;;
'daemon uninstall')
	exit 0
	;;
esac
exit 0
STUB
	chmod 0755 "$sandbox/prefix/marshal"
	: >"$sandbox/calls"
}

# uninstall STATUS: run install.sh --uninstall against the current sandbox,
# leaving the exit code in $rc and the output in $sandbox/output.
uninstall() {
	rc=0
	HOME="$sandbox/home" \
		MARSHAL_DATA_DIR="$sandbox/home/data" \
		STUB_CALLS="$sandbox/calls" \
		STUB_STATUS="$1" \
		sh "$installer" --uninstall --prefix "$sandbox/prefix" \
		>"$sandbox/output" 2>&1 || rc=$?
}

expect_success() {
	if [ "$rc" -eq 0 ]; then
		ok "$case_name: exited 0"
	else
		fail "$case_name: exited $rc: $(cat "$sandbox/output")"
	fi
}

expect_refusal() {
	if [ "$rc" -eq 0 ]; then
		fail "$case_name: exited 0; it should have refused to remove anything"
	else
		ok "$case_name: exited $rc"
	fi
}

expect_daemon_uninstall() {
	if grep -qx 'daemon uninstall' "$sandbox/calls"; then
		ok "$case_name: ran daemon uninstall"
	else
		fail "$case_name: did not run daemon uninstall (calls: $(tr '\n' ';' <"$sandbox/calls"))"
	fi
}

expect_no_daemon_uninstall() {
	if grep -q 'daemon uninstall' "$sandbox/calls"; then
		fail "$case_name: ran daemon uninstall (calls: $(tr '\n' ';' <"$sandbox/calls"))"
	else
		ok "$case_name: did not run daemon uninstall"
	fi
}

expect_removed() {
	if [ -e "$sandbox/prefix/marshal" ] || [ -e "$sandbox/prefix/webbridge" ]; then
		fail "$case_name: left files behind in $sandbox/prefix"
	else
		ok "$case_name: removed both binaries"
	fi
}

expect_kept() {
	if [ -x "$sandbox/prefix/marshal" ] && [ -e "$sandbox/prefix/webbridge" ]; then
		ok "$case_name: kept both binaries"
	else
		fail "$case_name: removed a binary it should have kept"
	fi
}

expect_delete_hint() {
	if grep -q 'delete it with: rm -rf' "$sandbox/output"; then
		ok "$case_name: says how to delete the kept data"
	else
		fail "$case_name: does not say how to delete the kept data: $(cat "$sandbox/output")"
	fi
}

# (a) The installed-but-stopped daemon: the case that used to slip through.
case_name="stopped service"
new_sandbox
uninstall 'service: stopped
socket: /run/user/1000/marshal/marshal.sock (not answering)'
expect_success
expect_daemon_uninstall
expect_removed
expect_delete_hint

# (b) A running daemon with a pid.
case_name="loaded and running (pid 1)"
new_sandbox
uninstall 'service: loaded and running (pid 1)
socket: /run/user/1000/marshal/marshal.sock (answering)'
expect_success
expect_daemon_uninstall
expect_removed

# (b') A running daemon whose pid the service manager did not report.
case_name="loaded and running"
new_sandbox
uninstall 'service: loaded and running
socket: /run/user/1000/marshal/marshal.sock (answering)'
expect_success
expect_daemon_uninstall
expect_removed

# (c) Nothing installed: nothing to uninstall.
case_name="not installed"
new_sandbox
uninstall 'service: not installed
socket: /run/user/1000/marshal/marshal.sock (not answering)'
expect_success
expect_no_daemon_uninstall
expect_removed

# (d) State unreadable: an empty status line must refuse, not guess, because
# removing the binaries is what turns a stale unit into a broken one.
case_name="unreadable state"
new_sandbox
uninstall ''
expect_refusal
expect_no_daemon_uninstall
expect_kept

if [ "$failures" -ne 0 ]; then
	echo "install_test.sh: $failures failure(s)" >&2
	exit 1
fi

echo "install_test.sh: all checks passed"
