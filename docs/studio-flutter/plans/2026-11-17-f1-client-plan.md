# F1 plan: the client — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: branch `f0-foundations` at `52221495` (PR #129). Anchors were checked
there.

Approved design: [f1-client.md](../f1-client.md). Programme:
[programme.md](../programme.md). Rules for any UI:
[design-system.md](../design-system.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built and the plan embeds no
implementation code. Contracts holds declarations only. Every step states
its decision in prose. "As `X` does" means `X` exists and is the model to
copy.

## Goal

- Gallery goldens are generated on Linux and compared in CI.
- `web/bridge/contract` generates Dart as well as TypeScript, with a
  currency test.
- `app/lib/src/client/` reaches a local daemon over its unix socket, a
  server over HTTPS with a token, and, in a browser, the bridge that served
  the page.
- The fleet stream survives a dropped connection and reports staleness.
- Several hosts at once.

## Non-goals

- Any screen but a debug connection check.
- Any wire type not already in `web/bridge/contract`.
- Session, factory or settings calls.

## Assumptions

- `tsgen.Generate(dir string) ([]byte, error)` is the generator's one
  exported function; its tests are `TestGeneratorRules` and
  `TestTypeScriptContractIsCurrent`, against `testdata/rules` and
  `testdata/rules.ts`.
- The bridge resumes a stream from the `lastEventId` query parameter or the
  `Last-Event-ID` header (`web/bridge/sse.go`); the fleet stream is
  `GET /api/events` (`serveEvents` in `web/bridge/http.go`).
- `fleetprofile.Load`, `File.Lookup` and `fleetprofile.Token` resolve a
  server profile and its token; `runFleet` and `runFleetLogin` in
  `cmd/marshal/main.go` are where `fleet` subcommands dispatch.
- `app/test/gallery_test.dart` gates pixel comparison on a `goldenHost`
  flag that is true on macOS only.
- `test/contract/daemon-status.json` is the one shared fixture today.

## Decisions this plan makes beyond the spec

- The generator emits one Dart class per Go struct with the Go name
  unchanged, one Dart enum per Go string type with its constants in lower
  camel case plus `unknown`, and JSON keys from the struct tags.
- `marshal fleet token NAME` refuses to print when stdout is a terminal,
  with a message telling the user it is for programs.
- The fake bridge used by Dart tests is a test helper in
  `app/test/support/fake_bridge.dart`; the real `webbridge` is first used
  end to end in F2.
- Backoff: first retry after 500 ms, doubling to a cap of 30 s, with up to
  20% jitter. Stale is declared after 10 s without an event or a heartbeat.

## Execution

- Branch `f1-client` from `f0-foundations` (or from `main` once #129 has
  merged).
- Run each task's Verify and commit it as `f1-client: task N — <title>`.
  Stage explicit paths only.
- Run `gofmt -w` on Go files and `dart format` on Dart files a task touches.
- Commands run on Linux. Go tests need `CGO_ENABLED=1`.

## Contracts

### Generator (`web/bridge/contract/dartgen`, Task 2)

```go
// Generate renders the Dart module for the contract package in dir.
func Generate(dir string) ([]byte, error)
```

Tests: `TestGeneratorRules`, `TestDartContractIsCurrent`. Regenerate with
`go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.
Output: `app/lib/src/client/contract.g.dart`.

### CLI (`cmd/marshal`, Task 4)

```go
// `marshal fleet servers --json` document.
type FleetServers struct {
	SchemaVersion int           `json:"schemaVersion"` // 1
	Servers       []FleetServer `json:"servers"`
}
type FleetServer struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	CAFile string `json:"caFile,omitempty"`
}
```

`marshal fleet token NAME` writes the token and a newline to stdout and
exits 0; exits 1 with a reason on stderr when the profile is unknown, the
token cannot be resolved, or stdout is a terminal.

### Client (`app/lib/src/client/`, Tasks 5–8)

```dart
// transport.dart
abstract interface class Transport {
  Future<Response> send(Request request);
  Stream<String> lines(Request request); // one element per line of the body
}
class Request { String method; String path; Map<String, String> query; Object? body; }
class Response { int status; Map<String, String> headers; String body; }
class BridgeFailure implements Exception { int status; String message; }
class Unauthorized extends BridgeFailure {}

// event_stream.dart
enum StreamState { connecting, live, stale, stopped }
class StreamEvent { int id; String data; }
class EventStream {
  EventStream(Transport transport, String path, {Map<String, String> query});
  Stream<StreamEvent> get events;
  ValueListenable<StreamState> get state;
  DateTime? get lastEventAt;
  Stream<void> get gaps; // the bridge could not replay; refetch a snapshot
  void start(); void stop();
}

// host.dart
enum HostKind { local, server }
enum HostReach { connecting, live, stale, unreachable, unauthorized }
class Host extends ChangeNotifier {
  String name; HostKind kind; BridgeClient client;
  HostReach reach; DateTime? lastContact;
}
class HostRegistry extends ChangeNotifier {
  List<Host> get hosts;
  Future<void> discover(); // local daemon, then each server profile
}

// bridge_client.dart — one method per route; all return generated types
class BridgeClient {
  Future<DaemonInfo> daemon();                       // GET /api/daemon
  Future<List<AgentStatus>> agents();                // GET /api/agents
  Future<List<ProjectStatus>> projects();            // GET /api/projects
  Future<Inbox> inbox();                             // GET /api/inbox
  Future<List<PendingRequest>> pending();            // GET /api/pending
  Future<void> answerPermission(String toolCallId, Decision decision);
                                                     // POST /api/permissions/{toolCallId}
  Future<void> answerQuestion(String questionId, Answers answers);
                                                     // POST /api/questions/{questionId}
  Future<ApproveResult> approveIntake(String id);    // POST /api/pending/{id}/approve
  Future<void> denyIntake(String id);                // POST /api/pending/{id}/deny
  Future<BudgetReport> budgets();                    // GET /api/budgets
  Future<RecentPrompts> recentPrompts();             // GET /api/prompts/recent
  EventStream fleetEvents();                         // GET /api/events
}
```

`client/` files that import `dart:io` end `_io.dart`; those that import a
browser API end `_web.dart`. `host.dart` imports
`package:flutter/foundation.dart` for `ChangeNotifier`, which the boundary
rule allows in `client/`.

## Task 1 — goldens on Linux

**Files:** `app/test/gallery_test.dart`, `app/test/goldens/*`,
`docs/studio-flutter/f0-foundations.md`.

1. Change `goldenHost` to be true on Linux and false elsewhere, and update
   the comment above it and the skip reason to say goldens are generated
   and compared on Linux.
2. Regenerate every golden with `flutter test --update-goldens
   test/gallery_test.dart` on the Linux machine.
3. Record in the F0 spec's implementation record the distribution the
   goldens were generated on.

**Verify:** `cd app && flutter test test/gallery_test.dart` passes with no
skipped golden comparison. After pushing, the `app` job in CI passes on
`ubuntu-latest`; if a comparison differs there, regenerate the goldens from
that job's failure artifacts instead and note it in the record.

## Task 2 — `dartgen`

**Files:** `web/bridge/contract/dartgen/dartgen.go` (new),
`dartgen_test.go` (new), `testdata/` (the `rules` fixture package shared
by path with `tsgen`, and `rules.dart`),
`app/lib/src/client/contract.g.dart` (new), `AGENTS.md`.

1. `Generate` parses the package as `tsgen.Generate` does and renders Dart
   by the rules under Decisions. Each class has a `fromJson` factory and a
   `toJson` method; an unknown enum string decodes to `unknown` and keeps
   its raw value in a `raw` field on the containing class's JSON round
   trip. Optional (`omitempty` or pointer) fields are nullable. Go doc
   comments are carried over.
2. A construct `tsgen` rejects by name is rejected by name here.
3. Tests: `TestGeneratorRules` (fixture output equals `rules.dart`, two
   runs identical, each unsupported construct is a named error);
   `TestDartContractIsCurrent` (checked-in file equals fresh output; with
   `-update` it rewrites it).
4. Add the regeneration command to `AGENTS.md` beside the TypeScript one.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge/contract/... ./web/bridge
-run 'Generator|Current|StdlibOnly' -count=1`, and `cd app && flutter
analyze`.

## Task 3 — shared fixtures

**Files:** `test/contract/*.json` (new, one per contract file:
`agents.json`, `answers.json`, `budgets.json`, `finish.json`, `inbox.json`,
`intake.json`, `projects.json`, `spawn.json`, `stream.json`),
`web/bridge/contract/fixture_test.go` (new),
`app/test/client/contract_fixture_test.dart` (new).

1. Each fixture is one JSON object whose keys are type names and whose
   values are a representative instance, including one enum value the
   client does not know.
2. `TestContractFixturesRoundTrip`: each value decodes into its Go type and
   re-encodes to the same JSON.
3. `contract fixtures round trip`: the same, in Dart, through the generated
   types; the unknown enum value survives.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge/contract -run Fixtures
-count=1`; `cd app && flutter test test/client/contract_fixture_test.dart`.
Mutation: rename a JSON tag in one contract struct; both tests fail.

## Task 4 — `marshal fleet token` and `marshal fleet servers`

**Files:** `cmd/marshal/main.go`, `cmd/marshal/fleet_cli_test.go` (new).

1. In `runFleet`, dispatch `token` and `servers` beside `login`.
2. `servers --json` loads `fleet.toml` as `runFleetLogin` does and writes
   the `FleetServers` document; without `--json` it prints one name per
   line. No token appears in either.
3. `token NAME` resolves the profile and calls `fleetprofile.Token` with the
   same store opener and command runner `runFleet` uses for an attach.
4. Tests: `TestFleetServersJSONListsProfilesWithoutTokens`,
   `TestFleetTokenPrintsResolvedToken`,
   `TestFleetTokenRefusesATerminal`,
   `TestFleetTokenUnknownProfileExitsOne`.

**Verify:** `CGO_ENABLED=1 go test ./cmd/marshal -run 'FleetServers|FleetToken'
-count=1`.

## Task 5 — transport and the fake bridge

**Files:** `app/lib/src/client/transport.dart`, `transport_io.dart`,
`transport_stub.dart`, `app/test/support/fake_bridge.dart`,
`app/test/client/transport_io_test.dart` (all new).

1. `transport.dart` declares the interface and value types, and a factory
   chosen by conditional import.
2. `transport_io.dart` has two constructors: over a unix socket path, using
   `HttpClient.connectionFactory`; and over an HTTPS base URL with a token
   provider and an optional CA file. Any non-2xx response throws
   `BridgeFailure`; 401 throws `Unauthorized`.
3. `fake_bridge.dart` is a `dart:io` server tests start on a unix socket in
   a temporary directory or on loopback with a token, with canned routes
   and a scriptable event stream it can cut mid-flight.
4. Tests: `unix socket request round trips`, `bearer token is sent`,
   `401 is Unauthorized`, `error body becomes BridgeFailure message`,
   `lines yields one element per line as they arrive`.

**Verify:** `cd app && flutter test test/client/transport_io_test.dart
test/boundary_test.dart`.

## Task 6 — the event stream

**Files:** `app/lib/src/client/event_stream.dart`,
`app/test/client/event_stream_test.dart` (new).

1. Parse server-sent events from lines: `id:`, `data:` (joined across
   lines), comment lines as heartbeats, a blank line ends an event.
2. Reconnect with the backoff under Decisions, sending `lastEventId`. An
   `Unauthorized` moves to `stopped` and does not retry. The bridge's
   signal that it cannot replay from that id emits on `gaps` and resets the
   remembered id.
3. Time is injected, so tests do not sleep.
4. Tests: `parses multi-line data`, `resumes from the last id`,
   `backs off and caps`, `goes stale without events`, `returns to live`,
   `stops on unauthorized`, `reports a replay gap`.

**Verify:** `cd app && flutter test test/client/event_stream_test.dart`.

## Task 7 — hosts and typed calls

**Files:** `app/lib/src/client/bridge_client.dart`, `host.dart`,
`host_discovery_io.dart`, `app/test/client/bridge_client_test.dart`,
`host_registry_test.dart` (all new).

1. `BridgeClient` implements the methods in Contracts over a `Transport`.
2. `host_discovery_io.dart` finds the local daemon's socket by running
   `marshal daemon status --json` through the tray's existing
   `lib/src/daemon/cli.dart`, and lists servers by running
   `marshal fleet servers --json`; a server's token provider runs
   `marshal fleet token NAME`.
3. `HostRegistry.discover` builds one `Host` per result. A host's `reach`
   follows its fleet stream's state; an unreachable host stays in the list
   with `lastContact`.
4. Tests: one per `BridgeClient` method against the fake bridge;
   `a stopped daemon becomes stale then unreachable`,
   `a returning daemon becomes live`, `an unreachable host is kept`,
   `a wrong token marks the host unauthorized`.

**Verify:** `cd app && flutter test test/client/`.

## Task 8 — browser transport

**Files:** `app/lib/src/client/transport_web.dart`,
`token_store_web.dart`, `app/test/client/transport_web_test.dart` (new).

1. Same-origin requests with the bearer token from the browser's storage.
   A pasted token is normalised as `api.ts` does: a leading
   `Authorization:` and `Bearer` are stripped.
2. Tests, run in Chrome: `sends the stored token`,
   `normalises a pasted header`, `401 is Unauthorized`.

**Verify:** `cd app && flutter test --platform chrome
test/client/transport_web_test.dart` and `flutter build web -t
lib/gallery_main.dart`.

## Task 9 — connection check, docs and full verification

**Files:** `app/lib/src/ui/debug/connection_check.dart` (new),
`app/lib/gallery_main.dart`, `app/test/support/screens.dart`,
`app/test/ui/connection_check_test.dart` (new), `app/README.md`,
`docs/studio-flutter/f1-client.md`.

1. A debug-only screen, built from the kit, listing each host with its
   reach, last contact and agent count. It is reachable from the gallery
   entry only and is registered in `screens.dart`.
2. Add an implementation record to the spec: commits, deviations, and what
   was not verified.
3. Run the final verification.

**Verify:**
- `cd app && flutter analyze && flutter test && flutter test --platform
  chrome test/client/transport_web_test.dart && dart format --output=none
  --set-exit-if-changed lib test && flutter build linux --debug`
- `CGO_ENABLED=1 go test ./web/... ./cmd/marshal ./internal/fleetprofile/...
  -count=1` and `go vet ./web/... ./cmd/marshal`.

## Done when

- Changing a field in `web/bridge/contract` without regenerating fails both
  currency tests.
- The connection check, run on a Linux machine with a local daemon, lists
  the daemon and its agents with no token entered.
- Stopping and restarting that daemon moves the host through stale,
  unreachable and back to live without restarting the app.
- Goldens are compared, not skipped, in CI.

## Needs a Mac

- Run the connection check on macOS against the installed daemon.

## What later plans rely on

- The Contracts above, unchanged.
- `app/test/support/fake_bridge.dart` for every model and screen test.
- The generator's naming rule, to predict type names as the contract grows.
