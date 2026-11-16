# F1 — the client

Status: **draft for approval**, 2026-11-17. Authored against `main` at
`f2ea2717`, ahead of F0. Part of [the Studio programme](programme.md).

Read first: [programme.md](programme.md), [f0-foundations.md](f0-foundations.md),
`web/bridge/contract/`, `web/bridge/contract/tsgen/`, `web/ui/src/lib/sse.ts`,
`web/ui/src/lib/api.ts` (the token and 401 handling at its head),
`internal/daemon/client.go`, `internal/fleetprofile/` (`profile.go`,
`token.go`), and the `fleet` subcommands in `cmd/marshal/main.go`.

**Before starting.** F0 has landed. Re-read the files above at the current
`main`; this document names functions and files, not line numbers.

## Context

The bridge speaks HTTP and server-sent events. The local profile serves its
routes on a unix socket of the user's own, peer-checked and without a
bearer token; the server profile serves them on a network listener behind
a bearer token, over TLS or on loopback (`cmd/webbridge/main.go`,
`web/bridge/auth.go`). The fleet stream is `GET /api/events`, resumed with a
`lastEventId` query parameter (`web/ui/src/lib/sse.ts`).

`web/bridge/contract` holds the fleet loop's wire types in ten Go files:
agents, answers, budgets, daemon, finish, inbox, intake, projects, spawn and
stream. `tsgen` generates `web/ui/src/lib/contract.ts` from them, with
`TestGeneratorRules` checking each mapping rule against `testdata/rules` and
`TestTypeScriptContractIsCurrent` failing when the checked-in file is stale.

The Go client for the same daemon is `internal/daemon/client.go`:
`NewClient(socket)` dials the unix socket whatever host the URL names, and
`Remote(base, tlsConfig, token)` reaches a server profile. Server profiles
live in `fleet.toml`, owned by `internal/fleetprofile`; a server's token
comes from its `token_cmd` or from the keychain entry `marshal fleet login`
writes.

## Goal

- Dart types for everything in `web/bridge/contract`, generated and kept
  current by a test.
- A client under `app/lib/src/client/` that reaches a local daemon, a
  server, and (in the browser) the bridge that served the page.
- A fleet stream that survives a dropped connection and says when its data
  is stale.
- More than one host at a time.
- No screen. One debug connection check, reachable only in debug builds.

## Non-goals

- Any type not already in `web/bridge/contract`. Later phases move theirs.
- Sessions, transcripts, factory or settings calls.
- Storing a server token in a new place.
- Mobile transports.

## Decisions

1. **Generator.** `web/bridge/contract/dartgen`, a sibling of `tsgen` with
   the same shape and the same standard-library-only constraint. It writes
   `app/lib/src/client/contract.g.dart`. Its tests are
   `TestGeneratorRules`, against the same `testdata/rules` fixture package
   `tsgen` uses with an expected `rules.dart` beside `rules.ts`, and
   `TestDartContractIsCurrent`. The regeneration command is
   `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.
   A construct the generator does not support is a named error, as in
   `tsgen`, never a silent skip.
2. **What a generated type is.** An immutable class with a constructor from
   decoded JSON and a method back to it; string enums as enums with an
   explicit unknown value, so a state a newer bridge sends is preserved and
   shown, not dropped; optional fields nullable; field comments carried
   over. No code-generation package on the Dart side.
3. **Transport.** One interface in `transport.dart` with two operations: a
   request returning status, headers and body, and a streaming request
   returning lines. Three implementations:
   - `transport_io.dart` — a unix socket, through
     `HttpClient.connectionFactory`, for the local daemon;
   - the same file — HTTPS with a bearer token and an optional CA file, for
     a server;
   - `transport_web.dart` — same-origin browser requests with a bearer
     token.
   A conditional import picks the file; nothing outside `client/` knows
   which is in use.
4. **Finding the local socket.** The native app asks
   `marshal daemon status --json`, which the tray already runs, and reads
   the socket path from the daemon document. It does not compute the path
   and does not read the state directory.
5. **A server's token.** The native app runs
   `marshal fleet token NAME`, a new subcommand that prints the token
   `internal/fleetprofile` resolves for that profile and exits non-zero
   with a reason when it cannot. The app reads `fleet.toml` nowhere and
   stores no token. Server profiles are listed with a second new
   subcommand, `marshal fleet servers --json`. In the browser the token is
   typed once and kept in the browser's storage, normalised as `api.ts`
   normalises a pasted header.
6. **Unauthorised is terminal.** A 401 stops requests and the stream for
   that host until the user supplies a different token. It is never retried
   on a timer.
7. **The stream.** `event_stream.dart` is pure Dart: it parses server-sent
   events from lines, remembers the last event id per stream, reconnects
   with capped exponential backoff and jitter, and resumes with
   `lastEventId`. It reports four states — connecting, live, stale (with
   the time of the last event) and stopped. A gap the bridge can no longer
   replay triggers a fresh snapshot fetch.
8. **Hosts.** `host.dart` describes one daemon: a name, its kind (local or
   server), its transport, and its connection state. A registry holds
   several. A host that cannot be reached is kept and marked unreachable
   with the time of last contact; it is never removed for being down.
9. **Typed calls.** `bridge_client.dart` wraps the transport with one method
   per route F2 needs: the daemon document, agents, projects, inbox,
   pending requests and their answers, budgets, recent prompts, disk, and
   the fleet stream. Every method returns generated types. An error
   response becomes a typed failure carrying the status and the bridge's
   message.

## Work

Suggested pull requests, in order.

1. **dartgen.** The generator, its fixture output, its two tests, and the
   first `contract.g.dart`. Add the regeneration command to `AGENTS.md`
   beside the TypeScript one.
2. **Shared fixtures.** For each contract file, a JSON fixture under
   `test/contract/` that a Go test decodes into the contract type and
   re-encodes, and a Dart test decodes into the generated type and
   re-encodes, each comparing with the fixture. The existing
   `daemon-status.json` is the model.
3. **Transport and stream.** The interface, the two native transports, the
   stream, and their tests against an in-process fake bridge: a small Dart
   HTTP server the tests start, listening on a unix socket for one group
   and on loopback TLS with a token for another.
4. **Go subcommands.** `marshal fleet token NAME` and
   `marshal fleet servers --json`, with tests in `cmd/marshal` and
   `internal/fleetprofile`. `--json` output carries a schema version and
   never includes a token.
5. **Hosts and typed calls.** The registry and `bridge_client.dart`, tested
   against the fake bridge.
6. **Browser transport.** `transport_web.dart`, with its test run under
   `flutter test --platform chrome`.
7. **Connection check.** A debug-only screen built from the kit that lists
   hosts, their state, and a count of agents per host.

## Tests

- `web/bridge/contract/dartgen`: `TestGeneratorRules`,
  `TestDartContractIsCurrent`.
- `app/test/client/`: `contract_fixture_test.dart`,
  `event_stream_test.dart` (parse, resume, backoff, stale, gap, 401),
  `transport_io_test.dart`, `transport_web_test.dart`,
  `host_registry_test.dart`, `bridge_client_test.dart`.
- Go: fixture round-trip tests beside the contract; the two subcommands.
- None of these reads the developer's config, keychain or running daemon.
  A test that needs a token injects one.

## Done when

- Changing a field in `web/bridge/contract` without regenerating fails both
  currency tests.
- The debug connection check, on a Mac with the daemon installed, shows the
  local daemon and its agents with no token entered anywhere.
- With a server profile configured, it shows that server's agents too.
- Stopping the daemon marks the host stale then unreachable with a last-seen
  time; starting it again returns the host to live with no restart of the
  app and no missed event.
- A wrong token stops that host's requests and says so.

## Open questions

- Whether the fake bridge should be replaced by the real `webbridge` binary
  in one end-to-end test here, or left to F2.
- Whether `marshal fleet token` should refuse to run when its output is a
  terminal, to keep tokens out of scrollback.

## Implementation record

Implemented on branch `studio/f1-client`, one commit per task, on Linux
(Fedora 44, Flutter 3.50.0 / Dart 3.14.0), against `main` at `c2f93381`.

- `f1-client: task 1 — goldens on Linux` — `app/test/goldens/*` regenerated
  on Linux and `goldenHost` flipped to `Platform.isLinux`; the distribution is
  recorded in `f0-foundations.md`.
- `f1-client: task 2 — dartgen` — `web/bridge/contract/dartgen`, its
  `testdata/rules.dart` (the fixture package shared by path with `tsgen`), and
  `app/lib/src/client/contract.g.dart`.
- `f1-client: task 3 — shared fixtures` — `test/contract/*.json` and both
  round-trip tests. Renaming one JSON tag fails both, as the plan requires.
- `f1-client: task 4 — marshal fleet token and servers` — `FleetServers`,
  `marshal fleet servers [--json]` and `marshal fleet token NAME`.
- `f1-client: task 5 — transport and the fake bridge` — `transport.dart`,
  `transport_io.dart`, `transport_stub.dart`, `test/support/fake_bridge.dart`.
- `f1-client: task 6 — the event stream` — `event_stream.dart`.
- `f1-client: task 7 — hosts and typed calls` — `bridge_client.dart`,
  `host.dart`, `host_discovery_io.dart`.
- `f1-client: task 8 — browser transport` — `transport_web.dart`,
  `token_store_web.dart`.
- `f1-client: task 9 — connection check, docs and full verification` —
  `lib/src/ui/debug/connection_check.dart`, its registration in
  `test/support/screens.dart`, the debug entry in `lib/gallery_main.dart`, and
  this record.

### Decisions the plan left open

- **`pending()` returns `PendingSubmission`.** The plan's Contracts sketch
  named `PendingRequest`, but GET /api/pending lists the submissions an
  operator decides on (`web/bridge/intake.go`), which `approveIntake` and
  `denyIntake` act on. Decoding a submission as a parked agent's request would
  fail on every field.
- **Enum constants are lower camel case of the Go constant name**, not of the
  wire value (`AgentAwaitingApproval` → `agentAwaitingApproval`): the fixture's
  `Shade` and `Repeat` deliberately give two constants one value, which would
  collide by value.
- **A class that holds an enum field also holds `rawEnums`**, the wire text of
  a value this build does not know, keyed by JSON name. The plan calls it "a
  `raw` field"; the fixture `rules.Sample` already has a wire field named
  `raw`, so the map is named `rawEnums` instead.
- **A named string type with no constants is a `typedef ... = String`**, not an
  enum with only `unknown`, which could not carry a value.
- **An enum inside a list or a map is a named error**, as are the constructs
  `tsgen` rejects: there is nowhere on the containing class to keep the raw
  text of an element this build does not know.
- **The generated Dart module carries `// dart format off`.** Its layout is
  emitted to be read, and without the marker every formatter release would
  rewrite the file without changing what it says (the currency test compares
  bytes).
- **`DaemonInfo` gained `socket`.** The plan reads the daemon's socket from
  `marshal daemon status --json`, but the tray's model dropped that field; it
  is now parsed, defaulted, and unused by the tray.
- **`app/pubspec.yaml` names `web`.** The browser transport uses
  `package:web`; it was available only transitively before.
- **`host_discovery_stub.dart`** is the web side of `gallery_main.dart`'s
  conditional import: a browser has no `marshal` binary to run, so it
  discovers no host until F2 wires the same-origin bridge.
- **The connection check is reached with
  `--dart-define=connection-check=true`** on the gallery entry, so the gallery
  page itself is unchanged and the shipped `main.dart` never reaches it.
- **Answers to the open questions.** The fake bridge stays an in-process Dart
  server for F1, and the real `webbridge` is used end to end from F2.
  `marshal fleet token` refuses a terminal, as decision 3 says.

### Limitation of the fixture-coverage test

`web/bridge/contract/fixture_test.go` holds `fixtureTypes` and the
`test/contract/*.json` files to each other in both directions: a fixture value
whose type is missing from the map fails, and an entry in the map with no
fixture value fails. Nothing ties the map to the contract package's own
declarations, so a type added to `web/bridge/contract` and listed in neither
place is covered by no fixture and fails no test. Closing that needs a check
that reads the contract source (the reverse direction this plan's file list
does not include); until then, adding a contract type is a matter of adding it
to `fixtureTypes` and its fixture by hand.

### Not verified on this machine

- `flutter test --platform chrome test/client/transport_web_test.dart` (Task
  8 and Task 9). This machine has no browser installed — no Chrome or
  Chromium on `PATH` and no `CHROME_EXECUTABLE` — so the Chrome platform
  cannot start. The file is analyzed and type-checks; a Mac or CI machine
  with Chrome should run it. `flutter build web -t lib/gallery_main.dart`
  (also Task 8) passes here.
- The connection check against a running daemon (the "Done when" run): it
  needs a graphical session and the installed `marshal`. It builds
  (`flutter build linux --debug`) but was not launched.
- The macOS run, which the plan already lists under "Needs a Mac".
- CI's `app` job on `ubuntu-latest` compared against the Linux goldens: the
  goldens were generated and compared on Linux here, but the runner's own
  pixels are only proven by that job.
