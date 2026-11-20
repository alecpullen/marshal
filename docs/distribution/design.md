# Marshal distribution — design

Status: approved 2026-11-16.

The local daemon is finished and well built; getting it onto a machine a
user does not control is not. `marshal daemon install` writes a launchd
agent or a systemd user unit, bakes in the resolved binaries and
environment, and reports whether the daemon answered
(`cmd/marshal/daemon.go`). Everything before that first command is
missing: there is no package, no installer, no Homebrew formula, no tray
icon, and no answer for a Mac whose download is quarantined.

This programme takes Marshal from a developer tool to something a user
can install, and ends at the first screen of the native app.

**Numbering.** These phases are **D1–D3** (distribution). The daemon's own
phases are P0–P6 in `docs/daemon/`; a bare "P1" in a conversation is
ambiguous, so nothing here uses it.

## Ship order

Each phase is useful on its own, and each one is a prerequisite for the
next only where stated.

| Phase | Spec | Goal | Why here |
|---|---|---|---|
| D1 | [d1-install-hardening.md](d1-install-hardening.md) | A Linux install survives logout | Smallest change that fixes a wrong answer the docs already promise (§4.3 of `docs/daemon/design.md`: a pinned project keeps running "through the linger") |
| D2 | [d2-packaging.md](d2-packaging.md) | One command to install, on both platforms, signed | Removes "download a draft archive and hope you got the arch right" |
| D3 | [d3-tray-app.md](d3-tray-app.md) | A menu-bar/tray icon showing daemon state, from a Flutter app that grows into the native apps | Needs both: a signed core to bundle, and an installer to be the GUI for |

## Sequencing against the daemon's P6 (PR #101)

PR #101 is docs-only — the P6 spec (`docs/daemon/p6-daemon-lifecycle.md`)
and plans 1, 2, 3 and 6 — so nothing conflicts in the tree today. Three
overlaps matter once P6's plans are executed, and every D-plan below is
written to survive them:

1. **`cmd/marshal/daemon.go`.** P6 plan 1, task 2 changes `daemonVersion`
   (its `dev` fallback becomes `buildinfo.Version`). D1 adds a helper and
   inserts lines in `runDaemonStatus`; D3 adds `--json` to the same
   function. Different code, same file: whichever lands second re-reads
   the function before editing. D1 and D3 anchor on **function names, not
   line numbers**, for this reason.
2. **`marshal daemon status` grows in two directions.** P6's decision 6
   and B4 add per-child pending-restart and version lines after the child
   lines; D1 adds one `linger:` line immediately after the service line.
   They compose in a fixed order — service, linger, socket, daemon,
   refresh, children, pins — and neither plan reorders the other's lines.
3. **`internal/daemon/manager.go`.** No P6 plan touches it: the daemon
   restarts by re-executing itself in the bridge (P6 B5), not through
   `Manager`. D1's `Linger`/`EnableLinger` additions and the manager's
   existing `Install`/`Start`/`Stop`/`Uninstall` are therefore independent
   of P6.
4. **Version strings.** P6 makes a source build report `dev+<commit>` and
   `dev+<commit>+dirty`. D3's JSON fixture therefore uses a stamped
   release version (`0.9.1`), never a build stamp, so the golden does not
   change when P6 lands.
5. **`marshal daemon restart`.** P6 plan 5 (not yet written) adds the
   subcommand next to the ones D1 edits. D1 touches only the `uninstall`
   branch of `runDaemonService` and adds `--no-linger` to `install`, so the
   two do not collide; D1 explicitly does not enable linger from `start`
   or from any restart path.

## Repo scope — decided

**The native apps stay in this repository, behind an enforced boundary.**

Marshal already carries a separate product line — Marshal Web Studio
(`docs/web-studio/`) — without taking the agent with it, and the three
mechanisms that make that work are the ones a native app needs:

- `web/` is standard library only, enforced by `TestWebIsStdlibOnly`.
- It reaches the agent side only through the wire contract in
  `web/bridge/contract`.
- Its TypeScript module is generated from those Go types by
  `web/bridge/contract/tsgen`, with a regeneration command and a
  currency test, so drift is impossible.

The Flutter app gets the same treatment: its own top-level directory, a
written boundary rule, and — where it needs types at all — models pinned
against a fixture shared with a Go test.

Carving now would cost more than it saves. The contract has no version
number, so splitting today would mean inventing API stability for a
daemon whose decisions are still changing; and the one CLI surface the
app depends on (`marshal daemon status --json`, D3) does not exist yet.
Keeping one repository is what prevents contract drift, and a directory
with no inbound Go imports moves to its own repository with
`git filter-repo` in an afternoon once the product lines genuinely
diverge.

The three products named in the plan map to what already exists:

| Product | Today |
|---|---|
| daemon, bundled with the TUI | Already true: the unit runs `webbridge`, found beside `marshal` (`daemon.FindWebbridge`) |
| Studio, pointed at any daemon | `webbridge` behind a proxy (`web_listen`), and the native Studio over the daemon's tunnels (one-daemon D9, D10) |
| native apps, optionally installing the daemon | New: D3 |

## Cross-phase decisions

1. **The native app bundles the daemon binaries.** Chosen over
   downloading on demand or requiring a package manager, because it is
   the only shape that works offline and always matches versions. Its
   consequence binds D2: a notarized macOS app may contain only
   Developer-ID-signed Mach-Os, so **macOS signing and notarization are
   mandatory in D2**, not a later nicety.
2. **The app reaches Marshal through the CLI, not the socket.** In D3
   the tray runs `marshal daemon status --json`. Dart cannot use its
   `HttpClient` on a unix socket, and shelling out means the app needs no
   socket client, no contract code generation, and no knowledge of the
   daemon's internal state files. It also makes the front-end swappable.
3. **`marshal daemon status --json` is the app's only new dependency on
   this repository**, and it is schema-versioned in the document itself.
4. **Nothing in this programme invokes `sudo`.** Not the installer, not
   the app. A prefix the user cannot write is reported, not escalated
   into.
5. **The service manager stays the only manager.** `marshal daemon
   install` owns the unit; the Homebrew formula declares no `service`
   block and the app offers the command, so `brew services` and the
   daemon cannot fight over one service.

## Assumptions

- The release repository is `github.com/alecpullen/marshal`, and releases
  are drafted by `.github/workflows/release.yaml` from `v*` tags. The
  archive and checksum names in D2 follow `.goreleaser.yaml` as it stands
  (`marshal_<version>_<os>_<arch>.tar.gz`,
  `marshal_<version>_checksums.txt`).
- An Apple Developer ID Application certificate and a notarytool
  credential are available to the maintainer. Where they are not, D2's
  signing is skipped loudly rather than silently, and releases still cut.
- A `HOMEBREW_TAP_TOKEN` with write access to `alecpullen/homebrew-tap`
  exists, or the release skips the formula (D2).
- The tray's Linux target is a desktop with a StatusNotifier host
  (KDE, XFCE, GNOME plus the AppIndicator extension). A bare GNOME
  session shows no icon, and no toolkit changes that.

## Programme non-goals

- Windows as a target.
- deb/rpm/AppImage packaging, and any store distribution (App Store,
  Flathub, Snap).
- Auto-update for any component.
- Rewriting the webbridge, the Studio, or the TUI.
- Splitting the repository.

## Open questions

None. The choices above were put to the maintainer and answered:
Flutter for the native app, tray as its first screen, bundled daemon
binaries, one repository.
