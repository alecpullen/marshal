# D2 — packaging: design

Status: approved 2026-11-16. Part of [the distribution programme](design.md).

## Context

`.goreleaser.yaml` builds `marshal` and `webbridge` for darwin and linux
on amd64 and arm64 with `CGO_ENABLED=1` and osxcross cross-compilers,
packs them as `marshal_<version>_<os>_<arch>.tar.gz`, writes
`marshal_<version>_checksums.txt`, and drafts a release whose footer tells
the reader to download an archive and unpack it by hand. There is no
installer, no formula, and nothing is signed.

Three consequences, in order of how much they cost a user:

1. **There is no install command.** The first step is choosing an archive
   by OS and architecture and putting two binaries somewhere on `PATH`.
2. **Nothing is notarized.** A binary fetched by a browser carries
   `com.apple.quarantine` and is refused; the release notes do not say so.
3. **`webbridge` is easy to lose.** `marshal daemon install` resolves it
   beside `marshal` or on `PATH` (`daemon.FindWebbridge`), so an installer
   that copies only `marshal` produces a daemon that cannot start. Any
   packaging must move both or neither.

The programme also decided the native app will bundle both binaries, which
makes signing a hard prerequisite rather than a polish item: a notarized
macOS app may contain only Developer-ID-signed Mach-Os.

## Design

### 1. `scripts/install.sh`

POSIX `sh`, `set -eu`, a `trap` removing the temp directory. It never runs
`sudo`, and it writes only inside `$PREFIX` and its temp directory.

```
scripts/install.sh [--version V] [--prefix DIR] [--no-daemon] [--uninstall] [--help]
```

- **Platform.** `uname -s` must be `Darwin` or `Linux`; `uname -m` must map
  `x86_64`→`amd64`, `arm64`/`aarch64`→`arm64`. Anything else exits 2 and
  lists what is supported.
- **Version.** `--version` accepts `v0.9.1` or `0.9.1` and is normalized by
  stripping one leading `v`, because the tag carries one and
  `{{ .Version }}` does not. With no `--version`, the latest tag comes from
  the redirect target of
  `https://github.com/alecpullen/marshal/releases/latest`
  (`curl -fsSIL -o /dev/null -w '%{url_effective}'`), which is not rate
  limited the way `api.github.com` is.
- **Download.** From
  `https://github.com/alecpullen/marshal/releases/download/v$VERSION/`:
  `marshal_${VERSION}_${os}_${arch}.tar.gz` and
  `marshal_${VERSION}_checksums.txt`. `curl -fL`.
- **Verify.** `sha256sum` if present, else `shasum -a 256`. A mismatch is
  fatal, names the file, and removes the temp directory on the way out.
- **Install.** Extract; require both `marshal` and `webbridge` in the
  archive (a missing `webbridge` is a hard error naming the archive, since
  the daemon would be broken later rather than now); `install -m 0755` both
  into `$PREFIX`.
- **Prefix.** Default `$HOME/.local/bin`, created with `mkdir -p`. If
  `$PREFIX` is not on `PATH`, print the `export PATH=...` line and say so
  once. If it cannot be created or written, exit with the reason — never
  escalate.
- **Daemon.** Unless `--no-daemon`, run `"$PREFIX/marshal" daemon install`.
  A failure exits non-zero with a sentence saying the binaries are
  installed and how to retry; the files stay, since they are usable.
- **`--uninstall`.** If `$PREFIX/marshal` exists and reports the service
  installed, run `daemon uninstall`; remove both binaries; print the data
  directory that was kept and how to delete it.
- **Success output.** The installed version (`marshal --version`), where the
  binaries went, `linger`/service state as `daemon install` printed it, and
  the next command to run.

macOS quarantine needs no special handling here: `curl` does not set
`com.apple.quarantine`, so a script install runs whether or not the
artifacts are notarized. Notarization is what protects the paths that do
set it — a browser download, and the D3 app bundle.

### 2. `.goreleaser.yaml`

- **Pin the release image.** `.github/workflows/release.yaml` uses
  `goreleaser/goreleaser-cross:latest`. A floating tag means the
  notarization and Homebrew schemas can change under a release. Pin an
  exact tag and record it; the plan verifies the schema it documents
  against that tag before writing the blocks below.
- **Notarization.** A `notarize.macos` block for the darwin builds, gated
  so it self-disables when the secrets are absent:

  | Secret | Purpose |
  |---|---|
  | `MACOS_SIGN_P12` | base64 Developer ID Application certificate |
  | `MACOS_SIGN_P12_PASSWORD` | its password |
  | `MACOS_NOTARY_ISSUER_ID` | notarytool issuer |
  | `MACOS_NOTARY_KEY_ID` | notarytool key id |
  | `MACOS_NOTARY_KEY` | notarytool API key |

  Gate on `MACOS_SIGN_P12` being set. Absent, the release still cuts, the
  binaries are unsigned, and the release footer gains a line saying a
  quarantined download may need `xattr -d com.apple.quarantine`. That line
  is generated, not remembered: the footer is a template, so it must be
  unconditional and honest — see decision 3.
- **Homebrew.** A `brews` entry publishing to `alecpullen/homebrew-tap`
  (`directory: Formula`, token from `HOMEBREW_TAP_TOKEN`), installing both
  binaries, with `skip_upload: auto` so a prerelease tag does not move the
  formula, and **no `service` block** (decision 5 of the programme). The
  caveats print `marshal daemon install`.
- **Footer.** The hand-written `release.footer` install section becomes the
  one-liner plus the Homebrew command.

### 3. `.github/workflows/release.yaml`

- Keep the `goreleaser-cross` container; use the pinned tag.
- Pass `HOMEBREW_TAP_TOKEN` to goreleaser.
- One step decides the skip list, so a fork with no tap still releases:

  ```
  skip=$([ -z "$TAP" ] && echo --skip=homebrew || echo '')
  ```

  written to `$GITHUB_OUTPUT` and interpolated into the goreleaser command
  beside the existing `--skip=docker`.

### 4. Documentation

`README.md` gains an **Install** section: the Homebrew command, the
`curl` one-liner, and a line saying both install the daemon and offer
`--no-daemon`.

### 5. Verification in CI

- `shellcheck scripts/install.sh` and `goreleaser check` as steps in
  `.github/workflows/go-checks.yml`. `goreleaser check` catches a config
  that would fail only at tag time.
- A smoke job on both `ubuntu-latest` and `macos-latest`: install the
  *previous* release into a temp prefix with `--no-daemon`, then assert
  `marshal --version` and `webbridge --version` both answer. No service
  manager, no daemon, no network beyond the release download.
- Manual, once per release: `brew install` from the tap, then
  `marshal daemon status`.

## Decisions and trade-offs

1. **An installer script and a formula, not deb/rpm.** Two artifacts cover
   macOS and Linux desktops with one implementation each, and the Flutter
   app bundles the same binaries rather than needing a package. Distribution
   packages are a later phase if a distro asks.
2. **Both binaries always.** The installer fails if `webbridge` is absent
   from the archive rather than producing a `marshal` that cannot install a
   daemon.
3. **Signing is gated, and its absence is stated.** Requiring secrets to
   cut any release would block a contributor's tag; shipping silently
   unsigned artifacts would produce an unexplainable Gatekeeper refusal. The
   gate plus an explicit footer line is the honest middle, and the D3 app
   build refuses to produce a distributable bundle without a signature.
4. **The formula declares no `service`.** Two managers for one unit is a
   support burden with no upside: `marshal daemon install` already writes
   the right unit with the right environment baked in.
5. **No `sudo`, in the script or the app.** A user can rerun with a prefix
   they own or use their package manager; an installer that escalates is a
   worse default than one that explains.
6. **Redirect, not the GitHub API, for the latest tag.** The API is rate
   limited per IP and an installer is exactly the kind of thing run from
   shared addresses.

## Assumptions

- Releases are public, so both `curl` and `brew` work unauthenticated.
- The tap repository `alecpullen/homebrew-tap` exists (or is created by the
  plan's first release), and `HOMEBREW_TAP_TOKEN` is a fine-grained token
  with `contents: write` on it.
- `goreleaser-cross` bundles `gon`'s successor support as `notarize.macos`.
  If the pinned tag does not, the plan falls back to a post-build
  `codesign`/`notarytool` step in the workflow and says so in the spec's
  implementation record.
- The archives keep both binaries at the archive root; `files:` in
  `.goreleaser.yaml` adds only `README.md` and `LICENSE`.

## Non-goals

- deb, rpm, AppImage, Snap, Flathub, the Mac App Store.
- Auto-update of any kind; `brew upgrade` and rerunning the script are the
  update paths.
- A `get.marshal.dev` short URL or a hosted install page. The `raw`
  GitHub URL is the documented source.
- Windows.
- Signing the Linux artifacts (no equivalent requirement).

## Open questions

None.
