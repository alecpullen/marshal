#!/bin/sh
# Offline tests for scripts/install.sh: the install branch and --uninstall.
#
# No network and no real binaries. For --uninstall the "installed" marshal is a
# stub that prints canned `daemon status` output and logs the arguments it was
# called with; for the install branch, a stub `curl` serves a fixture archive
# and checksums file from a temporary directory, so every assertion is about
# what install.sh actually did.
#
# The uninstall cases guard this bug: the gate matched `^service: loaded` only,
# so
#     marshal daemon install && marshal daemon stop && install.sh --uninstall
# removed both binaries and left a launchd/systemd unit pointing at deleted
# files. `service: stopped` is exactly that installed-but-not-running case.
#
# The install cases guard the Studio's atomic replace: the copy goes to
# $STUDIO_DIR.new, the old directory is removed, and .new is moved into place.
# Collapsing that into one `cp -R` onto an existing $STUDIO_DIR still passes
# every uninstall case, and nests studio/ inside studio/ on the second
# install — so the second install here walks the tree and counts.
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

# write_stub_marshal DEST: the marshal the tests put in place — the one already
# in the prefix, for the uninstall cases, and the one inside the fixture
# archive, for the install cases. It answers exactly the subcommands install.sh
# runs, logs each call to $STUB_CALLS, and reports the daemon's state from
# $STUB_STATUS. Every script that runs it in these tests sets those.
write_stub_marshal() {
	cat >"$1" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>"${STUB_CALLS:-/dev/null}"
case "$1" in
--version)
	printf 'marshal 9.9.9\n'
	exit 0
	;;
daemon)
	case "$2" in
	status)
		[ -z "${STUB_STATUS:-}" ] || printf '%s\n' "$STUB_STATUS"
		# A real `daemon status` exits non-zero whenever the daemon
		# does not answer, which a stopped service never does: install.sh
		# has to decide from the status line, not from this exit code.
		exit 1
		;;
	uninstall)
		exit "${STUB_UNINSTALL_RC:-0}"
		;;
	esac
	;;
esac
exit 0
STUB
	chmod 0755 "$1"
}

# new_sandbox: create a throwaway prefix holding a stub `marshal` (which logs
# its calls to $sandbox/calls) and a stand-in `webbridge` to remove, plus the
# Studio that install.sh puts beside the prefix at share/marshal/studio.
new_sandbox() {
	sandbox=$(mktemp -d) || {
		echo "install_test.sh: mktemp -d failed" >&2
		exit 1
	}
	sandboxes="$sandboxes $sandbox"
	mkdir -p "$sandbox/prefix" "$sandbox/home" "$sandbox/share/marshal/studio"
	printf 'stand-in\n' >"$sandbox/prefix/webbridge"
	printf '<!doctype html>\n' >"$sandbox/share/marshal/studio/index.html"
	write_stub_marshal "$sandbox/prefix/marshal"
	: >"$sandbox/calls"
}

# uninstall STATUS [RC [PREFIX]]: run install.sh --uninstall against the
# current sandbox, leaving the exit code in $rc and the output in
# $sandbox/output. RC is what the stub's `daemon uninstall` exits with, so a
# failing service removal can be exercised. PREFIX is the sandbox's own unless
# a case installs somewhere else, as the data-directory case below does.
uninstall() {
	rc=0
	HOME="$sandbox/home" \
		MARSHAL_DATA_DIR="$sandbox/home/data" \
		STUB_CALLS="$sandbox/calls" \
		STUB_STATUS="$1" \
		STUB_UNINSTALL_RC="${2:-0}" \
		sh "$installer" --uninstall --prefix "${3:-$sandbox/prefix}" \
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

expect_studio_removed() {
	if [ -e "$sandbox/share/marshal/studio" ]; then
		fail "$case_name: left the Studio behind in $sandbox/share/marshal/studio"
	else
		ok "$case_name: removed the Studio"
	fi
}

expect_studio_kept() {
	if [ -f "$sandbox/share/marshal/studio/index.html" ]; then
		ok "$case_name: kept the Studio"
	else
		fail "$case_name: removed the Studio it should have kept"
	fi
}

expect_delete_hint() {
	if grep -q 'delete it with: rm -rf' "$sandbox/output"; then
		ok "$case_name: says how to delete the kept data"
	else
		fail "$case_name: does not say how to delete the kept data: $(cat "$sandbox/output")"
	fi
}

# sha256 FILE: the hex digest, with whichever tool install.sh itself will pick.
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# new_install_sandbox: new_sandbox, plus a bin directory holding a stub `curl`
# and a fixture directory for it to serve. The stub answers install.sh's two
# downloads — the archive and its checksums — from $CURL_FIXTURES, so the
# install branch runs with no network at all.
new_install_sandbox() {
	new_sandbox
	mkdir -p "$sandbox/bin" "$sandbox/fixtures"
	cat >"$sandbox/bin/curl" <<'STUB'
#!/bin/sh
# A curl that serves a local fixture directory: install.sh fetches
# $base/$archive and $base/$checksums by URL, and this copies the file of that
# name out of $CURL_FIXTURES instead of reaching the network.
dest=""
src=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o)
		dest="$2"
		shift 2
		;;
	http://* | https://*)
		src="${1##*/}"
		shift
		;;
	*) shift ;;
	esac
done
[ -n "$dest" ] && [ -n "$src" ] || exit 22
[ -f "${CURL_FIXTURES:?}/$src" ] || exit 22
cp "${CURL_FIXTURES:?}/$src" "$dest"
STUB
	chmod 0755 "$sandbox/bin/curl"
}

# make_archive [--no-index]: pack the release archive install.sh downloads —
# marshal, webbridge and a Studio — into the sandbox's fixture directory, with
# the checksums file it verifies beside it. --no-index leaves the Studio's
# index.html out, which is the archive install.sh must refuse.
make_archive() {
	case "$(uname -s)" in
	Darwin) release_os=darwin ;;
	Linux) release_os=linux ;;
	*)
		echo "install_test.sh: the install cases need macOS or Linux" >&2
		exit 1
		;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) release_arch=amd64 ;;
	arm64 | aarch64) release_arch=arm64 ;;
	*)
		echo "install_test.sh: the install cases need amd64 or arm64" >&2
		exit 1
		;;
	esac
	asset="marshal_9.9.9_${release_os}_${release_arch}.tar.gz"
	stage="$sandbox/stage"
	rm -rf "$stage"
	mkdir -p "$stage/share/marshal/studio"
	write_stub_marshal "$stage/marshal"
	printf 'stand-in\n' >"$stage/webbridge"
	if [ "${1:-}" != "--no-index" ]; then
		printf '<!doctype html>\n' >"$stage/share/marshal/studio/index.html"
	fi
	tar -czf "$sandbox/fixtures/$asset" -C "$stage" . ||
		{
			echo "install_test.sh: could not build $asset" >&2
			exit 1
		}
	printf '%s  %s\n' "$(sha256 "$sandbox/fixtures/$asset")" "$asset" \
		>"$sandbox/fixtures/marshal_9.9.9_checksums.txt"
}

# run_install [PREFIX]: run install.sh's install branch against the fixtures,
# with the stub curl ahead of any real one on PATH. rc and $sandbox/output are
# the result, as for uninstall() above.
run_install() {
	rc=0
	HOME="$sandbox/home" \
		XDG_DATA_HOME= \
		MARSHAL_DATA_DIR="$sandbox/home/data" \
		PATH="$sandbox/bin:$PATH" \
		CURL_FIXTURES="$sandbox/fixtures" \
		STUB_CALLS="$sandbox/calls" \
		sh "$installer" --version 9.9.9 --prefix "${1:-$sandbox/prefix}" \
		>"$sandbox/output" 2>&1 || rc=$?
}

expect_binaries_installed() {
	if [ -x "$sandbox/prefix/marshal" ] && [ -x "$sandbox/prefix/webbridge" ]; then
		ok "$case_name: installed both binaries"
	else
		fail "$case_name: did not install both binaries (output: $(cat "$sandbox/output"))"
	fi
}

expect_daemon_installed() {
	if grep -qx 'daemon install' "$sandbox/calls"; then
		ok "$case_name: ran daemon install"
	else
		fail "$case_name: did not run daemon install (calls: $(tr '\n' ';' <"$sandbox/calls"))"
	fi
}

expect_studio_installed() {
	if [ -f "$sandbox/share/marshal/studio/index.html" ]; then
		ok "$case_name: installed the Studio with its index.html"
	else
		fail "$case_name: no index.html under $sandbox/share/marshal/studio (output: $(cat "$sandbox/output"))"
	fi
}

expect_no_studio_new() {
	if [ -e "$sandbox/share/marshal/studio.new" ]; then
		fail "$case_name: left $sandbox/share/marshal/studio.new behind"
	else
		ok "$case_name: left no studio.new behind"
	fi
}

expect_one_studio_index() {
	count=$(find "$sandbox/share/marshal/studio" -name index.html -type f | wc -l | tr -d ' ')
	if [ "$count" -eq 1 ]; then
		ok "$case_name: the Studio holds exactly one index.html"
	else
		fail "$case_name: $count index.html files under $sandbox/share/marshal/studio; a plain copy nests studio/ inside itself"
	fi
}

expect_names_missing_index() {
	if grep -q 'share/marshal/studio/index.html' "$sandbox/output"; then
		ok "$case_name: names the share/marshal/studio/index.html it refuses"
	else
		fail "$case_name: does not name share/marshal/studio/index.html: $(cat "$sandbox/output")"
	fi
}

expect_data_tidying_skipped() {
	if [ -d "$sandbox/home/data/share" ]; then
		ok "$case_name: pruned nothing inside the data directory"
	else
		fail "$case_name: removed $sandbox/home/data/share, which is inside the data directory"
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
expect_studio_removed
expect_delete_hint

# (b) A running daemon with a pid.
case_name="loaded and running (pid 1)"
new_sandbox
uninstall 'service: loaded and running (pid 1)
socket: /run/user/1000/marshal/marshal.sock (answering)'
expect_success
expect_daemon_uninstall
expect_removed
expect_studio_removed

# (b') A running daemon whose pid the service manager did not report.
case_name="loaded and running"
new_sandbox
uninstall 'service: loaded and running
socket: /run/user/1000/marshal/marshal.sock (answering)'
expect_success
expect_daemon_uninstall
expect_removed
expect_studio_removed

# (c) Nothing installed: nothing to uninstall.
case_name="not installed"
new_sandbox
uninstall 'service: not installed
socket: /run/user/1000/marshal/marshal.sock (not answering)'
expect_success
expect_no_daemon_uninstall
expect_removed
expect_studio_removed

# (d) State unreadable: an empty status line must refuse, not guess, because
# removing the binaries is what turns a stale unit into a broken one.
case_name="unreadable state"
new_sandbox
uninstall ''
expect_refusal
expect_no_daemon_uninstall
expect_kept
expect_studio_kept

# (e) The daemon's own uninstall fails: the binaries stay, and so does the
# Studio that goes with them.
case_name="daemon uninstall fails"
new_sandbox
uninstall 'service: loaded and running (pid 1)
socket: /run/user/1000/marshal/marshal.sock (answering)' 1
expect_refusal
expect_daemon_uninstall
expect_kept
expect_studio_kept

# (f) A plain install: both binaries, the daemon, and the Studio beside the
# prefix with its front page — and no .new directory left by the swap.
case_name="install puts the Studio in place"
new_install_sandbox
make_archive
run_install
expect_success
expect_binaries_installed
expect_daemon_installed
expect_studio_installed
expect_no_studio_new

# (g) A second install replaces the Studio instead of nesting it. The swap
# goes through $STUDIO_DIR.new, so collapsing it into one `cp -R` onto the
# directory that is already there shows up as studio/studio/index.html.
case_name="a second install does not nest the Studio"
new_install_sandbox
make_archive
run_install
run_install
expect_success
expect_studio_installed
expect_no_studio_new
expect_one_studio_index

# (h) An archive without the Studio's index.html: refused, naming the member
# it lacks, and the Studio already installed is left as it was.
case_name="archive without the Studio"
new_install_sandbox
make_archive --no-index
run_install
expect_refusal
expect_names_missing_index
expect_studio_kept

# (i) A prefix inside the data directory puts the Studio where the retired
# studio daemon kept its state: the install warns, naming --prefix, and the
# uninstall prunes nothing inside a directory it did not create.
case_name="prefix inside the data directory"
new_install_sandbox
make_archive
run_install "$sandbox/home/data/bin"
expect_success
if grep -q "inside Marshal's data directory" "$sandbox/output" &&
	grep -q -- '--prefix DIR' "$sandbox/output"; then
	ok "$case_name: warned about the data directory, naming --prefix"
else
	fail "$case_name: no warning about the data directory, naming --prefix: $(cat "$sandbox/output")"
fi
uninstall 'service: not installed' 0 "$sandbox/home/data/bin"
expect_success
expect_data_tidying_skipped

if [ "$failures" -ne 0 ]; then
	echo "install_test.sh: $failures failure(s)" >&2
	exit 1
fi

echo "install_test.sh: all checks passed"
