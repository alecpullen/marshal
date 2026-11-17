# D2 plan: packaging — implementation plan

Date: 2026-11-16. Status: ready for review.

Authored against `main` at `87c41ad9`. Archive names, the checksum name, the
`release.footer` block and the `goreleaser-cross:latest` image line were
read there. This plan touches no Go source, so it does not overlap PR #101.

Approved design: [d2-packaging.md](../d2-packaging.md).
Programme: [design.md](../design.md).

## Goal

- One command installs `marshal` and `webbridge` on macOS and Linux and
  installs the daemon.
- The darwin binaries are Developer-ID signed and notarized when the
  secrets exist, and the release says so plainly when they are not.
- A Homebrew formula publishes to `alecpullen/homebrew-tap` and declares no
  `service` block.
- CI catches a broken installer or a broken release config before a tag.

## Non-goals

- deb, rpm, AppImage, Snap, Flathub, the Mac App Store.
- Auto-update.
- A short install URL or a hosted page.
- Signing Linux artifacts.
- The Flutter app's bundle (D3).

## Assumptions

- `LICENSE`'s first line is `MIT License`, so the formula's `license` is
  `MIT`.
- Releases are public and are cut from `v*` tags by
  `.github/workflows/release.yaml`.
- `alecpullen/homebrew-tap` exists with a `Formula/` directory, and the
  `HOMEBREW_TAP_TOKEN` secret has `contents: write` on it. When it does
  not, the release runs with `--skip=homebrew` and still cuts.
- The five signing secrets exist in the repository settings, or they do
  not; either way the release completes.
- `install` exists on both target platforms (it does on macOS and on
  coreutils Linux).

## Contracts

Nothing in Go. The script's interface is the contract:

| Invocation | Result |
|---|---|
| `install.sh` | installs the latest release into `~/.local/bin`, then runs `marshal daemon install` |
| `install.sh --version V` | the same for tag `V`, with or without a leading `v` |
| `install.sh --prefix DIR` | the same into `DIR` |
| `install.sh --no-daemon` | binaries only |
| `install.sh --uninstall` | `daemon uninstall` when installed, then removes both binaries, keeping the data directory |

---

## Task 1 — the installer

**Goal.** A POSIX shell script installs both binaries from a release,
verifying its checksum, and installs the daemon unless told not to.

**Files.** `scripts/install.sh` (new).

**Steps.**

1. Create `scripts/install.sh` with exactly this content, then
   `chmod +x scripts/install.sh`:

```sh
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
*) die "unsupported operating system: $(uname -s); Marshal runs on macOS and Linux" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch="amd64" ;;
arm64 | aarch64) arch="arm64" ;;
*) die "unsupported architecture: $(uname -m); Marshal ships amd64 and arm64" ;;
esac

if [ "$UNINSTALL" -eq 1 ]; then
	if [ -x "$PREFIX/marshal" ]; then
		# A loaded service is removed; a silent daemon still has a unit,
		# so the status line, not the exit code, decides.
		if "$PREFIX/marshal" daemon status 2>/dev/null | grep -q '^service: loaded'; then
			"$PREFIX/marshal" daemon uninstall ||
				die "the daemon could not be uninstalled; nothing was removed"
		fi
	fi
	rm -f "$PREFIX/marshal" "$PREFIX/webbridge"
	say "removed marshal and webbridge from $PREFIX"
	say "kept your data in ${MARSHAL_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/marshal}"
	exit 0
fi

if [ -z "$VERSION" ]; then
	latest="$(curl -fsSIL -o /dev/null -w '%{url_effective}' \
		"https://github.com/$REPO/releases/latest")" ||
		die "could not reach github.com to find the latest release"
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
if command -v sha256sum >/dev/null 2>&1; then
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
```

**Verify.**

- `sh -n scripts/install.sh` — no syntax error.
- `shellcheck scripts/install.sh` — no findings (CI runs this too; if
  shellcheck is not installed locally, this check is CI's).
- `./scripts/install.sh --help` — prints the usage and exits 0.
- `./scripts/install.sh --bogus` — exits 2 with a sentence naming the
  option.
- `./scripts/install.sh --uninstall --prefix "$(mktemp -d)"` — exits 0,
  removes nothing, prints the kept-data line.

---

## Task 2 — the README's Install section

**Goal.** A user reading the README sees one command per platform.

**Files.** `README.md`.

**Steps.**

1. Replace this block (`## Quickstart` and its shell block):

```
## Quickstart

```bash
# Grab a prebuilt binary (Linux, macOS)
tar -xzf marshal_*_<os>_<arch>.tar.gz
sudo mv marshal /usr/local/bin/

# Or build from source (Go 1.26+ and a C toolchain)
CGO_ENABLED=1 go build ./cmd/marshal
```
```

   with:

```
## Install

```bash
# macOS
brew install alecpullen/tap/marshal

# Linux, or macOS without Homebrew
curl -fsSL https://raw.githubusercontent.com/alecpullen/marshal/main/scripts/install.sh | sh
```

Both install `marshal` and `webbridge` and run `marshal daemon install`,
which starts the background daemon. Add `--no-daemon` to install only the
binaries, and `--uninstall` to remove them (your data is kept).

## Quickstart

```bash
# Build from source (Go 1.26+ and a C toolchain)
CGO_ENABLED=1 go build ./cmd/marshal
```
```

2. Leave the paragraph after it ("Run `marshal` and the first launch walks
   you through connecting a provider…") untouched. It still follows the
   build-from-source block and still reads correctly.

**Verify.**

- `grep -n '^## Install' README.md` — prints a line before
  `## Quickstart`.
- `grep -c 'sudo mv' README.md` — prints 0.

---

## Task 3 — the release workflow: a pinned image, the tap, and a smoke install

**Goal.** The release uses a pinned image, publishes the formula when a
token exists, and proves the installer works on both platforms.

**Files.** `.github/workflows/release.yaml`.

**Steps.**

1. Pin the image. Replace `image: goreleaser/goreleaser-cross:latest` with
   an exact tag: run `curl -fsSL
   https://api.github.com/repos/goreleaser/goreleaser-cross/tags | head`
   and pick the newest `vX.Y.Z` tag, then write
   `image: goreleaser/goreleaser-cross:vX.Y.Z`. Record the chosen tag in
   the D2 spec's implementation record in Task 6.

2. Add a step before `Run GoReleaser` that decides the optional publishers:

```yaml
      - name: Decide optional publishers
        id: optional
        env:
          TAP: ${{ secrets.HOMEBREW_TAP_TOKEN }}
        run: |
          if [ -z "$TAP" ]; then
            echo "skip=--skip=homebrew" >> "$GITHUB_OUTPUT"
          else
            echo "skip=" >> "$GITHUB_OUTPUT"
          fi
```

3. Change the goreleaser step to use it and to pass the token:

```yaml
      - name: Run GoReleaser
        run: goreleaser release --clean --skip=docker ${{ steps.optional.outputs.skip }}
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
```

4. Add a job after `release`:

```yaml
  install-smoke:
    name: Installer smoke (${{ matrix.os }})
    needs: release
    permissions:
      contents: read
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
      - name: Install the release this workflow just cut
        run: |
          prefix="$RUNNER_TEMP/marshal-bin"
          ./scripts/install.sh --no-daemon --version "$GITHUB_REF_NAME" --prefix "$prefix"
          "$prefix/marshal" --version
          "$prefix/webbridge" --version
```

   `--no-daemon` is deliberate: the smoke job must not install a service
   on a shared runner.

**Verify.**

- `python3 -c 'import yaml,sys; yaml.safe_load(open(".github/workflows/release.yaml"))'`
  — parses (or `yq . .github/workflows/release.yaml` if `yq` is present).
- `grep -n 'goreleaser-cross:' .github/workflows/release.yaml` — the tag is
  not `latest`.
- `grep -n 'HOMEBREW_TAP_TOKEN' .github/workflows/release.yaml` — two
  lines: the gate and the goreleaser env.

---

## Task 4 — signing, the formula, and the footer

**Goal.** The darwin binaries are signed and notarized when the secrets
exist, the formula publishes both binaries without a `service` block, and
the release notes carry the one-liner.

**Files.** `.goreleaser.yaml`.

**Steps.**

1. Immediately after the `checksum:` block, add:

```yaml
# The darwin binaries are signed with a Developer ID Application certificate
# and notarized, so a download runs without a quarantine removal. Absent the
# secrets this block is skipped, the binaries are unsigned, and the release
# footer says what to do about it.
notarize:
  macos:
    - enabled: '{{ isEnvSet "MACOS_SIGN_P12" }}'
      sign:
        certificate: '{{ .Env.MACOS_SIGN_P12 }}'
        password: '{{ .Env.MACOS_SIGN_P12_PASSWORD }}'
      notarize:
        issuer_id: '{{ .Env.MACOS_NOTARY_ISSUER_ID }}'
        key_id: '{{ .Env.MACOS_NOTARY_KEY_ID }}'
        key: '{{ .Env.MACOS_NOTARY_KEY }}'
```

2. After the `archives:` block, add:

```yaml
brews:
  - name: marshal
    repository:
      owner: alecpullen
      name: homebrew-tap
      token: '{{ .Env.HOMEBREW_TAP_TOKEN }}'
    directory: Formula
    homepage: https://github.com/alecpullen/marshal
    description: A local-friendly TUI coding agent, and the local daemon it drives
    license: MIT
    # A prerelease tag must not move the formula.
    skip_upload: auto
    install: |
      bin.install "marshal", "webbridge"
    caveats: |
      Run `marshal daemon install` to install and start the background daemon.
      Do not use `brew services`: the daemon writes its own launchd agent or
      systemd user unit.
```

3. Replace the `release.footer`'s `### Install` section with:

```yaml
    ### Install

    ```bash
    brew install alecpullen/tap/marshal
    # or
    curl -fsSL https://raw.githubusercontent.com/alecpullen/marshal/main/scripts/install.sh | sh
    ```

    Both install `marshal` and `webbridge` and run `marshal daemon install`.
    The archives below are the same two binaries.

    macOS builds are signed and notarized. A release cut without the signing
    secrets is not: if macOS refuses a quarantined download, remove the
    attribute with `xattr -d com.apple.quarantine marshal`.
```

4. **Verify the schema against the pinned image before trusting the two
   blocks above.** GoReleaser's `notarize.macos` and `brews` fields are
   version-specific; if the pinned tag spells any of them differently, use
   that version's spelling and note it in the implementation record.

**Verify.**

- `docker run --rm -v "$PWD":/w -w /w goreleaser/goreleaser-cross:<PINNED> goreleaser check`
  — prints "config is valid" and exits 0. This is the authoritative check
  for both new blocks.
- `grep -n 'service' .goreleaser.yaml` — no `service:` key inside the
  `brews` entry.

---

## Task 5 — CI checks the installer and the release config

**Goal.** A PR that breaks the script or the goreleaser config fails before
a tag.

**Files.** `.github/workflows/go-checks.yml`.

**Steps.**

1. Add a job:

```yaml
  release-config:
    name: Installer and release config
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
      - name: Shellcheck the installer
        run: shellcheck scripts/install.sh
      - name: GoReleaser config
        uses: goreleaser/goreleaser-action@v6
        with:
          version: '~> v2'
          args: check
```

   `goreleaser check` parses the config only. It does not build, so it is
   cheap and needs no C toolchain.

**Verify.**

- `python3 -c 'import yaml; yaml.safe_load(open(".github/workflows/go-checks.yml"))'`
  — parses.
- `grep -n 'shellcheck scripts/install.sh' .github/workflows/go-checks.yml`
  — one line.

---

## Task 6 — record the outcome and verify the whole tree

**Goal.** The spec records the pinned image tag, the verified schema
spellings and the manual checks; the tree is clean.

**Files.** `docs/distribution/d2-packaging.md`, `docs/DEPLOYMENT.md` if it
documents installation (it does not at present; skip it then).

**Steps.**

1. Append an `## Implementation record` section to the D2 spec: the pinned
   `goreleaser-cross` tag, the exact `notarize`/`brews` keys the pinned
   version accepts, and anything this plan got short.
2. Run the repository's gates.

**Verify.**

- `sh -n scripts/install.sh` — clean.
- `gofmt -l .` — prints nothing (this plan adds no Go, but the gate stands).
- `scripts/test.sh full` — passes; the suite is untouched by this plan.
- Manual, once per release:
  `brew install alecpullen/tap/marshal`, `marshal daemon status` prints an
  answering daemon, and `brew uninstall marshal` does not stop it.
- Manual, once: download an archive with a browser on macOS and confirm it
  runs without `xattr` (the signed path), or that the footer's `xattr` line
  is what the release actually says (the unsigned path).

## Done when

- `curl … | sh` installs both binaries and a running daemon on macOS and
  Linux.
- `brew install alecpullen/tap/marshal` does the same, and
  `brew services list` never shows the daemon.
- A release with the signing secrets produces notarized darwin binaries; a
  release without them still cuts and says so.
- CI fails on a shellcheck finding or an invalid goreleaser config.
