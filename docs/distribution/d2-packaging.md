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

## Implementation record

Implemented 2026-11-16 on branch `d2-packaging`, based on `main` at `4e748e6c`.

### The pinned image

`.github/workflows/release.yaml` uses `goreleaser/goreleaser-cross:v1.27.3`,
the newest tag on `goreleaser/goreleaser-cross` named exactly `vX.Y.Z`
(published 2026-10-20). Later rebuilds of the same version exist
(`v1.27.3-1` … `v1.27.3-3`, the last on 2026-11-12); they carry the same
GoReleaser, and the plan asked for a `vX.Y.Z` tag. `goreleaser-cross`
numbers its tags by the Go toolchain it pins (v1.27.3 ships Go 1.27.3, the
release notes say `feat(go): bump go to v1.27.3`); the image bundles
**GoReleaser 2.18.3**, and that is the version every schema claim below was
checked against. The checks ran with `podman` — `docker` is not installed in
the environment this was implemented in — against the pulled image, with the
config placed in a throwaway git repository because `goreleaser check`
refuses to run outside one.

### The keys the pinned version accepts

`goreleaser check` parses `.goreleaser.yaml` with no unknown-field error, so
both new blocks use the pinned version's spelling:

- `notarize.macos[]`: `enabled`, `sign.certificate`, `sign.password`,
  `notarize.issuer_id`, `notarize.key_id`, `notarize.key` — exactly as
  written in the plan, including the `isEnvSet "MACOS_SIGN_P12"` gate. No
  fallback to a post-build `codesign`/`notarytool` step is needed: the
  assumption that the pinned image bundles `notarize.macos` holds.
- `brews[]`: `name`, `repository.{owner,name,token}`, `directory`,
  `homepage`, `description`, `license`, `skip_upload`, `install`, `caveats`
  — all accepted, and no `service:` key is declared (the unit stays
  `marshal daemon install`'s).

`brews` sits after the `checksum:` and `notarize:` blocks rather than
immediately after `archives:`, with the same effect: the plan's step 1 and
step 2 are two separate additions to the same region of the file.

### What this plan got short

1. **`goreleaser check` exits 2, not 0, once `brews` is in the config.**
   GoReleaser 2.18.3 prints `DEPRECATED: brews should not be used anymore`
   and `configuration is valid, but uses deprecated properties`. There is no
   replacement spelling to adopt in that version — `homebrew_formulae`,
   `homebrew_formulas`, `homebrew_formula` and `formulae` each fail with
   `field … not found in type config.Project` — and the formula still
   publishes, so `brews` is kept. GoReleaser 2.20.0 behaves identically, so
   the config will not rot when CI's `~> v2` moves.
2. **CI's `check` step therefore tolerates exit 2.** The plan's step would
   fail every PR over a deprecation it cannot avoid. `.github/workflows/
   go-checks.yml` installs GoReleaser with `goreleaser-action@v6`
   (`install-only: true`) and runs `check` itself, failing on any code other
   than 0 and 2. Exit 1 — a config that would break at tag time — still
   fails; that distinction was verified against both image versions.
3. **The `dockers`/`docker_manifests` phase-out notices are informational.**
   The pre-change config exits 0 with them, so D2 adds no new failure.
4. **`scripts/install.sh` was verified on Linux only.** `sh -n` is clean,
   `shellcheck` (the `koalaman/shellcheck:stable` image, since it is not
   installed here) reports nothing, `--help` exits 0, `--bogus` exits 2
   naming the option, and `--uninstall --prefix <temp dir>` exits 0, removes
   nothing and prints the kept-data line. The macOS path, the download and
   checksum paths, and `daemon install` were not exercised: they need a real
   release and a macOS host.
5. **Nothing was published.** No tag, no release, no tap push, and no
   notarization run: a real release needs the tag, `HOMEBREW_TAP_TOKEN` and
   the five Apple secrets. The signing gate is therefore verified as
   *configuring* off when `MACOS_SIGN_P12` is unset, not as producing a
   signed binary.
6. **The manual checks in the plan are still manual.** `brew install
   alecpullen/tap/marshal`, `marshal daemon status` after it, `brew
   uninstall` leaving the daemon running, and a browser download on macOS
   running without `xattr` all wait for the first tagged release from this
   branch.

### Review fixups: the installer

Review found `--uninstall` gated on `service: loaded` alone, so
`daemon install`, then `daemon stop`, then `install.sh --uninstall` removed
both binaries and left the launchd/systemd unit behind, pointing at files
that no longer existed. `service: stopped` is one of the four states
`daemon status` prints (`cmd/marshal/daemon.go`, `daemonServiceLine`) and it
is exactly the installed-but-not-running case the gate existed for. The gate
now matches `loaded` or `stopped`, and an unreadable service state is a
refusal rather than a silent skip: with no `service:` line there is no way to
tell "nothing installed" from "could not read the state", and a unit pointing
at deleted binaries is worse than an uninstall that asks for help.

Reading caught that bug, so the fix came with a test that could have. There
is no release to test against, so `scripts/install_test.sh` drives
`--uninstall` against a stub `marshal` that prints canned `daemon status`
output and records its calls: stopped, loaded-and-running (with and without a
pid), not-installed, and unreadable are all asserted offline, and
`.github/workflows/go-checks.yml` runs it beside `shellcheck`.

Three smaller corrections came with it. An unsupported platform exits 2, as
the Platform section says, instead of 1. A machine with neither `sha256sum`
nor `shasum` is told it has no hash tool rather than that a checksum
mismatched. `--uninstall` prints the `rm -rf` for the data it keeps, and a
404 from `/releases/latest` is reported as "no published release yet" — the
shape of a repository whose tags are all drafts — instead of as a network
failure.

### The smoke job runs after the release is published

`release.draft: true` (`.goreleaser.yaml`) is policy, not an oversight, and a
draft's assets are not publicly downloadable: `install.sh` fetches them with
a plain unauthenticated `curl`, so the `install-smoke` job could never pass
while it ran inside the tag workflow. It now triggers on
`release: [published]` — the moment a human makes the draft public — and on
`workflow_dispatch` with a tag, and the `release` job is gated to a tag push
so neither of those runs tries to cut the tag again. The job still pins
`--version "$TAG"` and then runs `--version` on **both** binaries, so it
keeps exercising the same download, checksum and extract path a user gets
rather than the `/releases/latest` redirect.

The tradeoff is real and is stated here rather than papered over: a broken
installer is now caught immediately **after** a human publishes, not before.
That is the only honest option while releases are drafts, and the alternative
— letting `install.sh` carry a token to reach draft assets — is refused,
because a `curl | sh` installer that accepts a token is a foot-gun.
