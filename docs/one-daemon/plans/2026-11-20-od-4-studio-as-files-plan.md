# One daemon plan 4: the Studio as files — implementation plan

Date: 2026-11-20. Status: approved 2026-11-20. Amended 2026-11-20: Task 5
also runs the browser tests and a served-Studio smoke test.

Base: `main` at `bbdc4486`, with plans [1](2026-11-20-od-1-one-daemon-plan.md),
[2](2026-11-20-od-2-hosts-over-ssh-plan.md) and
[3](2026-11-20-od-3-web-listener-plan.md) applied first. Every anchor below was
checked at `bbdc4486`. A step names the function or the step as well as the
line, because plans 1–3 move lines in some of these files. The baseline, with
`TMPDIR=/private/tmp/od4`: `go test -count=1 ./cmd/webbridge/ ./cmd/marshal/`,
the asset tests in `./web/bridge`, `sh scripts/install_test.sh`, and `bash -n
scripts/dev-daemon.sh`.

Approved design: [design.md](../design.md) D12. It was split out of plan 3 on
2026-11-20. Planning settled these, and design.md records them:
- **Where the files live.** The Studio is `share/marshal/studio/`, beside the
  binaries in the release archive and under the prefix everywhere else. The
  daemon finds it from its own executable. It looks first for
  `<bin dir>/share/marshal/studio`, which is the archive's layout and a
  checkout's. Then it looks for `<bin dir>/../share/marshal/studio`, which is
  Homebrew's, `install.sh`'s and both app bundles'.
- **An override.** `webbridge --studio-dir DIR` overrides that search. The
  unit never writes it.
- **No files found.** A daemon with no Studio files serves a page that says
  where it looked and how to build them. It answers 503, and the daemon runs
  on.
- **The development box.** It runs one daemon. `scripts/dev-daemon.sh
  --studio` builds the Studio into the checkout's `share/marshal/studio/`,
  which the daemon serves at once, with no restart. `--dev-image` and
  `--agent-image` move into `dev-daemon.sh`, and `dev-studio.sh` is deleted.

Inventory: [inventory.md](../inventory.md) §Plan 4.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **`webbridge` builds with Go alone.** `//go:embed static` and
  `web/bridge/static/` are gone.
- **The daemon serves the Studio from a directory.** It reads the directory on
  each request, so a rebuilt Studio needs no restart. `GET /api/daemon` and
  `marshal daemon status` name the directory.
- **Every install carries the directory:** the release archive, Homebrew,
  `install.sh`, and the macOS and Linux app bundles.
- **The web build has a CI step of its own,** with the check that it fetches
  nothing from a CDN.
- **The development box has one daemon.** `dev-studio.sh` and its unit are
  gone.

## Non-goals

- Changing the Studio itself, or its build flags.
- Retiring `compose.yaml` and `Dockerfile.bridge`: plan 5.
  `build/Dockerfile.bridge` copies a `webbridge` that no longer embeds a
  Studio. A containerized bridge built from it serves the no-files page until
  plan 5 deletes it.
- A `[daemon] studio_dir` setting. `--studio-dir` covers a hand-written unit.

## Assumptions

Each was checked at the base unless it says otherwise.

- **The embed.**
  - `web/bridge/assets.go` embeds `static` (`:28`), takes `studioRoot` as
    `fs.Sub` of it (`:33`), and serves it with `studioHandler` (`:63`),
    `serveFrom` (`:77`) and `serveFile` (`:97`).
  - `serveFile` reads a whole file into memory, because an embedded file is not
    an `io.ReadSeeker` (`:119–126`).
  - `route` calls `studioHandler()` for every path that is not the API
    (`http.go:274`).
  - `web/bridge/static/` holds one tracked file, a placeholder `index.html`.
  - `assets_test.go` has eight tests (`:13–146`), which call `studioHandler()`
    and read `studioRoot`.
- **The webbridge binary** finds its own path with `ownExecutable`
  (`cmd/webbridge/main.go:637`), which is `os.Executable`. Plan 1's restart
  path uses it too.
- **CI.**
  - `go-checks.yml`'s `app` job builds the Studio into `../web/bridge/static`
    (`:85–90`), then links `webbridge` to prove it embeds (`:91–93`).
  - `scripts/e2e/checks_spa.py` checks a served build for a CDN URL
    (`_CDN`, `:36`).
- **Release.**
  - `release.yaml`'s `studio-web` job (`:72–92`) builds into
    `web/bridge/static` and uploads it as the `studio-web` artifact.
  - The `release` job downloads that artifact into `web/bridge/static`
    (`:43–46`) before GoReleaser runs.
  - `app-darwin` extracts `marshal webbridge` from the darwin archive (`:135`)
    and copies them to `Contents/Resources/bin` (`:149–156`). `app-linux`
    extracts at `:260` and copies to `bundle/bin` (`:271–277`).
  - `install-smoke` runs `install.sh --no-daemon` and checks both binaries'
    `--version` (`:321–326`).
- **GoReleaser.** `.goreleaser.yaml`'s archive `files` are `README.md` and
  `LICENSE` (`:90–92`). A plain glob there keeps each file's path from the
  repository root. The formula installs with `bin.install "marshal",
  "webbridge"` (`:126–127`). Not checked here: that `share/marshal/studio/**/*`
  keeps nested paths. Task 6 checks it with `goreleaser check` and an archive
  listing.
- **`install.sh`** unpacks the archive into `$tmp/out`, and installs the two
  binaries into `$PREFIX` (default `~/.local/bin`). `--uninstall` removes
  them. `install_test.sh` covers the uninstall branch with a stub `marshal`.
- **The development scripts.**
  - `dev-daemon.sh` builds `./marshal` and `./webbridge` at the repository root
    (`:189`). Its flags are parsed at `:66–110`, and it ends by printing
    `./marshal daemon status` (`:248`).
  - `dev-studio.sh` holds `remote_urls` (`:318`, the `tailscale serve`
    origins), the agent image notes in `print_status` (`:355–375`),
    `host_base_image` (`:422`), `build_dev_image` (`:439`), and the
    `--agent-image` build (`:477–495`). The image tag is
    `ghcr.io/marshal/agent:latest` (`:116`).
- **Docs that name the embed:**
  - `AGENTS.md:58–70`;
  - `README.md:313`;
  - `docs/ARCHITECTURE.md:163–175`;
  - `docs/dev-loop.md` (the studio throughout, and the artifacts table at
    `:63–75`);
  - `.gitattributes:4–6`.

## Contracts

```go
// web/bridge/contract/daemon.go — new DaemonInfo field:
// StudioDir is the directory the Studio is served from, or "" when the
// daemon found none (one-daemon D12).
StudioDir string `json:"studioDir,omitempty"`
```

```go
// web/bridge/assets.go

// studioSubdir is where the Studio's files sit under a prefix or beside the
// binaries.
const studioSubdir = "share/marshal/studio"

// FindStudioDir answers the Studio directory for the webbridge at exe:
// <dir>/share/marshal/studio, then <dir>/../share/marshal/studio, the first
// holding an index.html. exe's symlinks are resolved first. looked lists every
// candidate, in order, for the no-files page and the log.
func FindStudioDir(exe string) (dir string, looked []string)

// SetStudioDir makes the server serve the Studio from dir. With "", it serves
// the no-files page naming looked.
func (s *Server) SetStudioDir(dir string, looked []string)

// studioHandler serves the Studio from s's directory.
func (s *Server) studioHandler() http.Handler

// New Server fields (http.go):
studioDir    string
studioLooked []string
```

```go
// cmd/marshal/daemon.go

// daemonStudioLine renders the Studio's directory for marshal daemon status.
func daemonStudioLine(info daemon.Info) string
```

## Task 1: The bridge serves the Studio from a directory

**Goal:** `assets.go` embeds nothing. `Server.SetStudioDir` names the
directory, and with none set the Studio's routes answer the no-files page.

**Files:** `web/bridge/assets.go`, `web/bridge/assets_test.go`,
`web/bridge/http.go`, `web/bridge/static/index.html` (deleted),
`web/bridge/contract/daemon.go`, `app/lib/src/client/contract.g.dart`
(generated), and any `web/bridge` test that expects the old placeholder at
`/`.

**Steps:**
1. Delete `web/bridge/static/` and the `embed` import, `studioFS` and
   `studioRoot`.
2. Rewrite the comment above them. The Studio is the directory
   `FindStudioDir` finds, and the build is `flutter build web --base-href /
   --no-web-resources-cdn` into it. Keep the paragraph on why
   `--no-web-resources-cdn` is not optional.
3. Add `studioSubdir`, `FindStudioDir` and `SetStudioDir`, as Contracts
   declares them, and the two `Server` fields in `http.go` beside `daemonInfo`.
   - `FindStudioDir` resolves `exe` with `filepath.EvalSymlinks`, keeping `exe`
     when that fails. It cleans each candidate, and counts it when `index.html`
     inside it is a regular file.
4. `studioHandler` becomes a method.
   - With `s.studioDir` set, it serves through `serveFrom` with
     `os.DirFS(s.studioDir)`. The `/api/` guard stays.
   - With none, it answers 503 with `Content-Type: text/html; charset=utf-8`
     and `Cache-Control: no-store`. The small page says:
     - "Marshal's Studio files are not installed";
     - the directories in `s.studioLooked`, each HTML-escaped;
     - "build them with `cd app && flutter build web --base-href /
       --no-web-resources-cdn --output ../share/marshal/studio`, or reinstall
       Marshal".
   - `route` (`http.go:274`) calls `s.studioHandler()`.
5. `serveFile`: when the opened file is an `io.ReadSeeker` (an `*os.File` is),
   pass it to `http.ServeContent` directly. Keep the read-all path for any
   other `fs.File`. The cache rules are unchanged.
6. In `contract/daemon.go`, add `DaemonInfo.StudioDir`, as Contracts declares
   it. `Server.daemon` (`daemoninfo.go:43`) sets `out.StudioDir =
   s.studioDir`. Regenerate the Dart module with `go test
   ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.
7. Rewrite `assets_test.go`.
   - A helper writes a temporary Studio: an `index.html`, a
     `main.dart.js`, and one hashed asset under `assets/`. It calls
     `SetStudioDir` on a server built with `NewServer(testFleet(t))`.
   - The eight tests keep their names and assertions against that directory.
     `hashedAssetName` reads it with `os.DirFS`.
   - New tests:
     - `TestFindStudioDirPrefersBesideTheBinary`;
     - `TestFindStudioDirFallsBackToTheSharePrefix`;
     - `TestFindStudioDirFollowsASymlinkedBinary`: a link in one directory to
       a binary in a prefix's `bin/`;
     - `TestFindStudioDirSkipsADirectoryWithoutAnIndex`;
     - `TestTheStudioWithoutFilesNamesWhereItLooked`: 503, and the page holds
       each looked-in path;
     - `TestTheStudioIsReadOnEachRequest`: rewriting `index.html` changes the
       next answer;
     - `TestTheStudioServesNothingOutsideItsDirectory`: `/../secret` and
       `/%2e%2e/secret` answer the index, never a file beside the directory.
8. Any other `web/bridge` test that asserted the placeholder page at `/` calls
   the helper first, or asserts the 503 page. Find them by running `go test
   ./web/bridge`.

**Verify:**
- `go test ./web/bridge/... -count=1` passes.
- `grep -rn 'go:embed' web/` prints nothing.
- `go build ./cmd/webbridge` succeeds on a tree with no `web/bridge/static`.

## Task 2: `webbridge` finds its Studio

**Goal:** `webbridge` serves the Studio from `--studio-dir`, or from the
directory `FindStudioDir` finds beside its own binary, and reports it.

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/main_test.go`,
`cmd/webbridge/local_test.go`.

**Steps:**
1. Add `studioDir` to `config`, and the flag `--studio-dir`
   (`WEBBRIDGE_STUDIO_DIR`): "the Studio's files (default:
   share/marshal/studio beside or above this binary)". A given directory must
   hold an `index.html`, or `parseConfig` refuses it, naming the path.
2. In `run`, right after `NewServer`:
   - With `--studio-dir`, call `api.SetStudioDir(cfg.studioDir,
     []string{cfg.studioDir})`.
   - Otherwise, call `bridge.FindStudioDir` on `ownExecutable()` and pass
     both answers to `SetStudioDir`. When `ownExecutable` fails, pass `""` and
     no candidates.
   - Log `webbridge: serving the Studio from <dir>`, or warn `webbridge: no
     Studio files found; looked in <dirs>`.
3. Tests:
   - `TestParseConfigReadsTheStudioDir` (`main_test.go`): the flag, the
     variable, and the refusal of a directory with no index.
   - `TestTheDaemonReportsItsStudioDir` (`local_test.go`): with
     `--studio-dir` pointed at a temporary Studio, `GET /api/daemon` reports
     it, and `GET /` over the socket answers its index.
   - `TestTheDaemonRunsWithoutStudioFiles`: with `ownExecutable` replaced by
     a path in an empty directory, the daemon starts, `studioDir` is empty,
     and `GET /` answers 503.

**Verify:** `go test ./cmd/webbridge -count=1` passes.

## Task 3: `marshal daemon status` names the Studio's directory

**Goal:** status prints one line for the Studio's files.

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

**Steps:**
1. Add `daemonStudioLine`, as Contracts declares it:
   - `studio: <dir>`;
   - or `studio: no files found (reinstall Marshal, or build them: see
     AGENTS.md)` when `StudioDir` is empty.
2. In `runDaemonStatus`'s text mode, print it right after plan 3's web line.

**Tests:** `TestDaemonStatusNamesTheStudioDir`, with both forms.

**Verify:** `go test ./cmd/marshal -run 'DaemonStatus|StatusDocument' -count=1`
passes.

## Task 4: The checkout builds the Studio where the daemon looks

**Goal:**
- A checkout's Studio build goes to `share/marshal/studio/`, which git
  ignores.
- `dev-daemon.sh` builds it, and refreshes the agent image.
- `dev-studio.sh` is deleted.

**Files:** `.gitignore`, `.gitattributes`, `scripts/dev-daemon.sh`,
`scripts/dev-studio.sh` (deleted).

**Steps:**
1. `.gitignore`: add `/share/` under "Local build outputs", with the comment
   "the Studio's web build (flutter build web --output ../share/marshal/studio)".
2. `.gitattributes`: delete the three `web/bridge/static/assets/*` lines.
3. `dev-daemon.sh` gains three flags, documented in its header beside the
   others:
   - **`--studio`.** It builds the Studio with `(cd app && flutter build web
     --base-href / --no-web-resources-cdn --output ../share/marshal/studio)`,
     before the Go build.
     - The step needs `flutter` on `PATH`. Move `flutter_ready` from
       `dev-studio.sh` (`:385`) unchanged.
     - The header says a Studio-only change needs `--studio --no-build
       --no-restart`: the daemon reads the files on each request.
   - **`--dev-image`.** It runs `build_dev_image` after the Go build. Move
     `build_dev_image` and `host_base_image` from `dev-studio.sh` (`:417–470`),
     with `agent_image_ref` (`:112–116`). Only their messages change: they
     name `dev-daemon.sh`.
   - **`--agent-image`.** It runs the slow build, moved from `dev-studio.sh`
     (`:477–495`) the same way.
4. `dev-daemon.sh`'s status, after `./marshal daemon status`:
   - Move the agent image notes from `dev-studio.sh`'s `print_status`
     (`:355–375`). Print them only when a container runtime is on `PATH`.
   - Move `remote_urls` (`:318`). Its port comes from `./marshal daemon
     status --json` (`daemon.webListen`) when that is a loopback address, and
     nothing prints otherwise.
   - Use `python3 -c` with `json.load` to read the field if `jq` may be
     missing. `jq` is not assumed.
5. In the header, delete the paragraph that points at `dev-studio.sh`
   (`:27–28`). Say that the browser Studio is this daemon's web listener, set
   with `[daemon] web_listen`.
6. Delete `scripts/dev-studio.sh`.

**Verify:**
- `bash -n scripts/dev-daemon.sh` passes.
- `scripts/dev-daemon.sh --help` lists `--studio`, `--dev-image` and
  `--agent-image`.
- `git status --short` after `scripts/dev-daemon.sh --studio --no-restart`
  shows no file under `share/`.
- `grep -rn 'dev-studio' scripts .github` prints nothing.

## Task 5: CI builds the Studio on its own, and tests it in a browser

**Goal:**
- The `app` job builds the Studio into `share/marshal/studio`, and fails on a
  CDN URL.
- It runs the browser-only tests in Chrome.
- It serves the build from a real daemon, through plan 3's guard.
- No step links `webbridge` to embed the Studio.

**Files:** `.github/workflows/go-checks.yml`, `scripts/studio-smoke.sh` (new).

**Steps:**
1. In the `app` job:
   - the build step (`:85–90`) outputs to `../share/marshal/studio`;
   - its comment says the build is what every install ships beside the
     binaries, and keeps the `--no-web-resources-cdn` sentence.
2. Replace "The Studio's build embeds" (`:91–93`) with a step named "The
   Studio fetches nothing from a CDN". Its working directory is the
   repository, and it runs `grep -rlE
   'gstatic\.com|fonts\.gstatic|cdn\.jsdelivr' share/marshal/studio`. The
   step fails when that finds any file, and prints them. The pattern is
   `checks_spa.py`'s `_CDN` (`:36`), and a comment there says so.
3. Upload the build as an artifact named `studio-web`. Task 6 does not
   consume this one: the release builds its own.
4. Add a step named "Browser tests", which runs `flutter test --platform
   chrome` over the browser-only tests: `test/client/transport_web_test.dart`,
   and any test marked `@TestOn('browser')`. Chrome is on `ubuntu-latest`.
   Today `flutter test` on the VM skips these, so the browser transport has no
   test in CI.
5. Add `scripts/studio-smoke.sh`. It needs a built `webbridge` and a Studio
   build.
   - It makes a short temporary directory and starts `webbridge --socket
     <tmp>/s.sock --state-dir <tmp>/state --workspace <tmp>/fleet.json
     --web-listen 127.0.0.1:0 --studio-dir share/marshal/studio`, with
     `MARSHAL_DATA_DIR` pointed into the temporary directory.
   - It waits up to 20 s for `curl --unix-socket <tmp>/s.sock
     http://d/api/daemon` to answer, and reads `webListen` from it with
     `python3 -c`.
   - It checks, failing with a message on the first that does not hold:
     1. `GET /` on the web listener answers 200, and its body names
        `flutter_bootstrap.js`;
     2. `GET /api/agents` answers 200 with a JSON array;
     3. `POST /api/agents` with `Sec-Fetch-Site: cross-site` answers 403
        (plan 3's guard);
     4. `GET /api/agents` with `Host: evil.example` answers 403.
   - It stops the daemon on exit, whatever happened.
6. Add a step named "The Studio is served", which builds `webbridge` (`go
   build -o webbridge ./cmd/webbridge`; the job needs Go set up, as the `go`
   job does) and runs `scripts/studio-smoke.sh`.

**Verify:**
- `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/go-checks.yml'))"`
  succeeds, or `actionlint` passes if it is installed.
- `bash -n scripts/studio-smoke.sh` passes, and so does running it locally
  after `scripts/dev-daemon.sh --studio --no-restart`.
- The CI run on the pull request is the real check.

## Task 6: Releases carry the Studio beside the binaries

**Goal:**
- The release archive holds `share/marshal/studio/`.
- The Homebrew formula installs it into `pkgshare`.
- Both app bundles carry it under `share/marshal/studio`.

**Files:** `.github/workflows/release.yaml`, `.goreleaser.yaml`.

**Steps:**
1. `studio-web` job (`:72–92`):
   - build with `--output ../share/marshal/studio`;
   - upload `share/marshal/studio`;
   - its comment says GoReleaser archives the build beside the binaries.
2. `release` job:
   - `needs` becomes `[checks, studio-web]`;
   - the download step (`:43–46`) writes to `share/marshal/studio`, and its
     comment says the archives carry it.
3. `.goreleaser.yaml`:
   - `archives.files` gains `share/marshal/studio/**/*`;
   - the formula's `install` gains `pkgshare.install "share/marshal/studio"`
     after `bin.install`.
4. `app-darwin`:
   - its extract step takes `share` as well as `marshal webbridge`;
   - "Bundle the daemon binaries" also runs `mkdir -p
     "$bundle/Contents/Resources/share/marshal"` and `cp -R
     app-bins/share/marshal/studio "$bundle/Contents/Resources/share/marshal/"`.
     It runs before signing, so the bundle's seal covers the files.
5. `app-linux`: the same, into `$bundle/share/marshal/studio`.
6. `install-smoke`: after the `--version` checks, check that
   `"$RUNNER_TEMP/share/marshal/studio/index.html"` exists. The prefix is
   `$RUNNER_TEMP/marshal-bin`, so `install.sh` puts the Studio in
   `$RUNNER_TEMP/share/marshal/studio` (Task 7).

**Verify:**
- `goreleaser check` passes, if `goreleaser` is installed.
- With a stand-in Studio (`mkdir -p share/marshal/studio && echo x >
  share/marshal/studio/index.html`) and `goreleaser release --snapshot
  --clean --skip=docker,homebrew,sign,notarize,publish --single-target`,
  `tar -tzf dist/*.tar.gz` lists `share/marshal/studio/index.html`. If
  `--single-target` is refused for `release`, record that, and leave this
  check to the next tag's draft.
- Remove the stand-in afterwards.

## Task 7: `install.sh` installs and removes the Studio

**Goal:** an install puts `share/marshal/studio` at `<prefix>/../share/marshal/studio`,
and `--uninstall` removes it.

**Files:** `scripts/install.sh`, `scripts/install_test.sh`.

**Steps:**
1. Add `STUDIO_DIR`, `$(dirname "$PREFIX")/share/marshal/studio`, computed
   once `PREFIX` is final. The usage text says `--prefix DIR` also places the
   Studio at `DIR/../share/marshal/studio`.
2. After the binaries are installed:
   - refuse an archive without `share/marshal/studio/index.html`, naming it;
   - otherwise copy the directory to `$STUDIO_DIR.new` with `cp -R`, remove
     `$STUDIO_DIR`, and move `.new` into place;
   - a parent that cannot be created or written stops with the same refusal
     style as the prefix, never with `sudo`;
   - say `installed the Studio in $STUDIO_DIR`.
3. In `--uninstall`, after the binaries, remove `$STUDIO_DIR` and say so. Keep
   the data-directory lines as they are. Remove `share/marshal` and `share`
   only if they are then empty (`rmdir`, ignoring failure).
4. `install_test.sh`:
   - `new_sandbox` makes `$sandbox/share/marshal/studio/index.html`;
   - new `expect_studio_removed` asserts it is gone after `--uninstall`, and
     each uninstall case calls it;
   - a case where the daemon uninstall fails asserts the Studio is kept.

**Verify:** `sh scripts/install_test.sh` passes, and `sh -n scripts/install.sh`
succeeds.

## Task 8: The e2e suite checks the Studio it is served

**Goal:** group I describes the Studio served from a directory, and checks
that `GET /api/daemon` names one.

**Files:** `scripts/e2e/checks_spa.py`, `scripts/e2e/README.md`.

**Steps:**
1. In `checks_spa.py`:
   - rewrite the module docstring and the comments that say the build is
     embedded (`:14`, `:33–36`, `:91–92`): the daemon serves the build from
     its Studio directory;
   - keep `_CDN` and its check.
2. Add one check: `GET /api/daemon` has a non-empty `studioDir`. Report it as a
   skip, not a failure, when the field is absent from an older daemon.
3. `README.md`: one sentence, that the daemon under test needs its Studio
   files (`scripts/dev-daemon.sh --studio` in a checkout).

**Verify:** `python3 -m py_compile scripts/e2e/checks_spa.py` succeeds.

## Task 9: The docs follow

**Goal:** no live doc says the Studio is embedded, or describes a second
studio daemon.

**Files:** `AGENTS.md`, `README.md`, `docs/ARCHITECTURE.md`, `docs/dev-loop.md`,
`cmd/webbridge/README.md`, `docs/studio-flutter/f8-cutover.md`.

**Steps:**
1. **`AGENTS.md`.**
   - Under "Web control plane" (`:55–70`), the commands become `cd app &&
     flutter build web --base-href / --no-web-resources-cdn --output
     ../share/marshal/studio` and `go build ./cmd/webbridge`. Drop "# embeds
     the Studio".
   - Replace the paragraph that begins "`web/bridge/static/` is the build"
     with one that says:
     - `webbridge` serves the Studio from `share/marshal/studio`, found beside
       or above its binary, or `--studio-dir`;
     - a checkout builds it there with the command above;
     - releases, Homebrew, `install.sh` and the app bundles ship it;
     - a daemon without it serves a page that says where it looked
       (one-daemon D12).
   - The `--no-web-resources-cdn` paragraph stays, with "the e2e suite"
     changed to "CI and the e2e suite".
   - In the Commands block, delete the `scripts/dev-studio.sh` lines. Add
     `scripts/dev-daemon.sh --studio` ("also rebuild the browser Studio") and
     `--dev-image`.
2. **`README.md:313`** and **`docs/ARCHITECTURE.md:163–175`**: the same
   facts, in each file's existing style.
3. **`docs/dev-loop.md`** describes one daemon.
   - The service table loses the `marshal-studio` row.
   - "The loop" and "Common invocations" use `dev-daemon.sh` with `--studio`,
     `--dev-image` and `--agent-image`.
   - The artifacts table's Studio row reads: `share/marshal/studio/` | built
     by `dev-daemon.sh --studio` | read on each request, so no restart.
   - "The agent image" section names `dev-daemon.sh`.
   - "Reaching studio from another machine" points `tailscale serve` at the
     daemon's `web_listen` port.
   - "Recreating this setup from a bare checkout" sets `[daemon] web_listen =
     "127.0.0.1:7700"`, then runs `scripts/dev-daemon.sh --install --studio`.
   - A new short section, "Retiring the old studio daemon", says to finish or
     discard its agents, then run `systemctl --user disable --now
     marshal-studio.service` and remove the unit file and `<data>/studio/`.
4. **`cmd/webbridge/README.md`**: add `--studio-dir`, with its default search.
5. **`docs/studio-flutter/f8-cutover.md`**: one dated line under its header:
   "Superseded in part by one-daemon D12 (2026-11-20): the Studio is served
   from share/marshal/studio, not embedded."

**Verify:**
- `grep -rn 'web/bridge/static\|go:embed\|dev-studio' AGENTS.md README.md docs/ARCHITECTURE.md docs/dev-loop.md cmd/webbridge/README.md`
  prints nothing.

## Task 10: Full verification

**Steps:**
1. `gofmt -l .` prints nothing, and `go vet ./...` passes.
2. With no `share/` directory, `CGO_ENABLED=1 go build ./cmd/marshal
   ./cmd/webbridge` succeeds.
3. `scripts/test.sh` (the packages the diff affects) passes. Compare it with
   the baseline at the top of this plan.
4. `go test ./web/bridge -run TestWebIsStdlibOnly -count=1` and `go test
   ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -count=1`
   pass.
5. `cd app && flutter analyze && flutter test` passes, and `sh
   scripts/install_test.sh` passes.
6. **By hand on macOS.**
   1. Run `scripts/dev-daemon.sh --install --studio` with `[daemon]
      web_listen = "127.0.0.1:7700"`.
   2. `marshal daemon status` shows `studio: <repo>/share/marshal/studio`, and
      the page loads at `http://127.0.0.1:7700/`.
   3. Edit a string in `app/lib`, then run `scripts/dev-daemon.sh --studio
      --no-build --no-restart`. A reload shows the change, and the daemon's
      `started` time is unchanged.
   4. Move `share/` aside and restart: the page is the 503 that names both
      candidates, and the daemon's socket still answers. Move it back.
7. **On the development box.** Follow "Retiring the old studio daemon" in
   `docs/dev-loop.md`. Then `tailscale serve` reaches the one daemon's web
   listener, and `scripts/dev-daemon.sh --dev-image` refreshes the agent
   image.
8. **On the next tag's draft.** The darwin and linux archives list
   `share/marshal/studio/index.html`. `Marshal.app/Contents/Resources/share/marshal/studio/index.html`
   exists and the app still notarizes. A Homebrew install's `marshal daemon
   status` names `.../share/marshal/studio` under its Cellar.

## Integration notes

- **The development box's second daemon goes.** Finish or discard the studio
  daemon's agents before deleting `dev-studio.sh`'s unit. Its fleet and state
  are its own, and do not move to the main daemon.
- **A checkout without a Studio build serves the 503 page.** That includes CI
  jobs that start a daemon. `scripts/dev-daemon.sh --studio`, or the build
  command in `AGENTS.md`, fixes it.
- **`Dockerfile.bridge` images serve no Studio** from here until plan 5
  deletes them.
- **A hand-installed `webbridge`** copied somewhere without its `share/`
  needs `--studio-dir`, or the directory copied beside it.
