#!/bin/sh
# Marshal's installer: both binaries, then the daemon.
#
#   curl -fsSL https://raw.githubusercontent.com/alecpullen/marshal/main/scripts/install.sh | sh
#
# It never runs sudo: a prefix this user cannot write is reported, not
# escalated into.
set -eu

REPO="alecpullen/marshal"
PREFIX="$HOME/.local/bin"
VERSION=""
INSTALL_DAEMON=1
UNINSTALL=0

usage() {
	cat <<'EOF'
Usage: install.sh [options]

  --version V     install tag V (v0.9.1 or 0.9.1); default: the latest release
  --prefix DIR    where to put marshal and webbridge (default: ~/.local/bin)
  --no-daemon     install the binaries only; do not install the daemon
  --uninstall     stop and remove the daemon, then remove the binaries
  -h, --help      this text
EOF
}

die() {
	echo "install.sh: $*" >&2
	exit 1
}

# die_usage exits 2, which the spec reserves for "this machine or this flag is
# not supported, nothing was done" (docs/distribution/d2-packaging.md, Platform).
die_usage() {
	echo "install.sh: $*" >&2
	exit 2
}

say() {
	echo "$*"
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || die "--version needs a value"
		VERSION="$2"
		shift 2
		;;
	--prefix)
		[ $# -ge 2 ] || die "--prefix needs a value"
		PREFIX="$2"
		shift 2
		;;
	--no-daemon)
		INSTALL_DAEMON=0
		shift
		;;
	--uninstall)
		UNINSTALL=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "install.sh: unknown option $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

command -v curl >/dev/null 2>&1 || die "curl is required"

case "$(uname -s)" in
Darwin) os="darwin" ;;
Linux) os="linux" ;;
*) die_usage "unsupported operating system: $(uname -s); Marshal runs on macOS and Linux" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch="amd64" ;;
arm64 | aarch64) arch="arm64" ;;
*) die_usage "unsupported architecture: $(uname -m); Marshal ships amd64 and arm64" ;;
esac

if [ "$UNINSTALL" -eq 1 ]; then
	if [ -x "$PREFIX/marshal" ]; then
		# A loaded service is removed, and so is a stopped one: an installed
		# unit that outlives its binaries points at files that no longer
		# exist. The status line, not the exit code, decides — `daemon
		# status` exits non-zero whenever the daemon does not answer, which
		# a stopped service never does.
		state="$("$PREFIX/marshal" daemon status 2>/dev/null |
			grep '^service: ' || true)"
		case "$state" in
		"service: loaded"* | "service: stopped"*)
			"$PREFIX/marshal" daemon uninstall ||
				die "the daemon could not be uninstalled; nothing was removed"
			;;
		"service: not installed"*) ;;
		*)
			# Removing the binaries would leave a unit pointing at deleted
			# files, which is worse than refusing: stop here and say so.
			die "could not read the marshal daemon's service state; nothing was removed. Run $PREFIX/marshal daemon status to see why, uninstall the service by hand, then rerun --uninstall"
			;;
		esac
	fi
	rm -f "$PREFIX/marshal" "$PREFIX/webbridge"
	say "removed marshal and webbridge from $PREFIX"
	data_dir="${MARSHAL_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/marshal}"
	say "kept your data in $data_dir"
	say "delete it with: rm -rf \"$data_dir\""
	exit 0
fi

if command -v sha256sum >/dev/null 2>&1; then
	hash_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then
	hash_tool=shasum
else
	die "no sha256 tool found (need sha256sum or shasum)"
fi

if [ -z "$VERSION" ]; then
	# `-f` is left off so the HTTP status survives: a 404 here means every tag
	# cut so far is still a draft, which is not a network failure.
	probe="$(curl -sSIL -o /dev/null -w '%{url_effective} %{http_code}' \
		"https://github.com/$REPO/releases/latest" 2>/dev/null)" ||
		die "could not reach github.com to find the latest release"
	code="${probe##* }"
	latest="${probe% *}"
	case "$code" in
	200) ;;
	404)
		die "no published release found yet: tags are cut as drafts. Rerun with --version V once a release is published"
		;;
	*)
		die "could not find the latest release (github.com answered HTTP $code)"
		;;
	esac
	VERSION="${latest##*/}"
fi
VERSION="${VERSION#v}"
[ -n "$VERSION" ] || die "could not work out which version to install"

base="https://github.com/$REPO/releases/download/v$VERSION"
archive="marshal_${VERSION}_${os}_${arch}.tar.gz"
checksums="marshal_${VERSION}_checksums.txt"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading Marshal $VERSION for $os/$arch"
curl -fsSL -o "$tmp/$archive" "$base/$archive" || die "no $archive at $base"
curl -fsSL -o "$tmp/$checksums" "$base/$checksums" || die "no $checksums at $base"

grep "  $archive\$" "$tmp/$checksums" >"$tmp/$archive.sha256" ||
	die "$checksums does not list $archive"
if [ "$hash_tool" = sha256sum ]; then
	(cd "$tmp" && sha256sum -c "$archive.sha256") >/dev/null ||
		die "checksum mismatch for $archive"
else
	(cd "$tmp" && shasum -a 256 -c "$archive.sha256") >/dev/null ||
		die "checksum mismatch for $archive"
fi

mkdir -p "$tmp/out"
tar -xzf "$tmp/$archive" -C "$tmp/out" || die "could not unpack $archive"
for binary in marshal webbridge; do
	[ -f "$tmp/out/$binary" ] || die "$archive does not contain $binary"
done

mkdir -p "$PREFIX" 2>/dev/null || die "cannot create $PREFIX"
[ -w "$PREFIX" ] || die "cannot write to $PREFIX; rerun with --prefix DIR that you own"
install -m 0755 "$tmp/out/marshal" "$PREFIX/marshal" ||
	die "could not install $PREFIX/marshal"
install -m 0755 "$tmp/out/webbridge" "$PREFIX/webbridge" ||
	die "could not install $PREFIX/webbridge"

say "installed $("$PREFIX/marshal" --version) in $PREFIX"
case ":$PATH:" in
*":$PREFIX:"*) ;;
*) say "add $PREFIX to your PATH: export PATH=\"$PREFIX:\$PATH\"" ;;
esac

if [ "$INSTALL_DAEMON" -eq 1 ]; then
	if ! "$PREFIX/marshal" daemon install; then
		die "the binaries are installed in $PREFIX, but the daemon did not install; run $PREFIX/marshal daemon install to retry"
	fi
else
	say "skipped the daemon (--no-daemon); run $PREFIX/marshal daemon install when you want it"
fi
say "run marshal to start the TUI"
