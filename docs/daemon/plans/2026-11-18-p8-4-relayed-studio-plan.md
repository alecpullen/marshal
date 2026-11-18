# P8 plan 4: the Studio through a relay — implementation plan

Date: 2026-11-18. Status: ready for review.

Base: `main` at `bdd20ddc`. Every anchor below was checked there.
**It runs after [plan 2](2026-11-18-p8-2-hosts-plan.md) and
[plan 3](2026-11-18-p8-3-relay-plan.md)** are on `main`. Before starting,
re-check the names they declare:
- plan 2's `AddServer`, `runFleetAdd`, `FleetProfiles`, `HostsModel.signIn`
  and `HostRegistry.retry`;
- plan 3's `RelayHosts` and `RelayHost` contract types, `hostTokenHeader`,
  and the three route-parity rows.

Approved design: [p8-studio-federation.md](../p8-studio-federation.md),
D18–D23, with the Error handling rows for relayed hosts, the Testing
section's plan 4 list and the acceptance run. Rules:
[design-system.md](../../studio-flutter/design-system.md), normative for
everything under `app/lib/src/ui/`.
Previous plans: [F1, the client](../../studio-flutter/plans/2026-11-17-f1-client-plan.md),
whose transports, discovery and token store this plan extends.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **A relayed host is an ordinary host** in both builds. Its client runs over
  a transport that prefixes its routes and carries its own token.
- **A browser on a relay's page lists every host the relay names,** and signs
  in to each with that host's token.
- **A native build names a relayed host in `fleet.toml`** with `relay =
  "HUB"`.
- **The spec's acceptance run passes** by hand.

## Non-goals

- The fleet screen over a relay. It refuses a relayed profile (spec D20).
- A sign-in code through the relay (spec Non-goals).
- A native build listing a relay's hosts on its own.

## Assumptions

Each was checked at the base, apart from the names plans 2 and 3 add.

- **The transport seam** (`app/lib/src/client/transport.dart`):
  - `Request` (`:14`) carries `headers`, set after the transport's own;
  - `Response` (`:40`) carries `status`, `headers` and `body`;
  - `Transport` (`:92`) is `send` and `lines`;
  - `bridgeFailure` (`:77`) turns a 401 into `Unauthorized`.

  `BridgeClient._send` (`bridge_client.dart:1456`) throws for a non-2xx
  answer. `lines` fails the stream itself. `EventStream` stops for good on
  `Unauthorized` (`event_stream.dart:148`).
- **The web transport.** `WebTransport({tokens, base})`
  (`transport_web.dart:16-24`) sets `Authorization` from its `TokenStoreWeb`
  and then the request's headers (`:76-84`).
- **The browser's token.** `TokenStoreWeb` (`token_store_web.dart:38`) reads,
  writes and clears one key, `tokenStorageKey` (`:4`), through
  `normalizeToken`.
- **Discovery.**
  - The browser's `discoverHosts` (`host_discovery_stub.dart:20`) answers the
    page's own bridge.
  - The native one reads `marshal fleet servers --json` in `_serverHosts`
    (`host_discovery_io.dart:53`) and builds each host's `serverTransport`
    with `_fleetToken` (`:72-83`, `:91`).
  - `HostSpec` (`host.dart:21`) is a name, a kind and a client.
  - `HostRegistry.discover` skips a spec whose name and kind are known
    (`:130-135`).
- **What screens see.** `FleetStore.hosts` (`fleet_store.dart:41-49`) builds
  `HostView`s (`:611`) from the registry. The Hosts section's kind line is
  `_kind` (`hosts_section.dart:52-55`).
- **`fleet.toml`.**
  - `rawServer` (`internal/fleetprofile/profile.go:63`) and `Server` (`:36`).
  - `buildServer` (`:152`) checks one server, and `decode` (`:109`) builds
    them all.
  - `FleetServer` (`cmd/marshal/main.go:360`) is one row of `fleet servers
    --json`, written by `runFleetServers` (`:369`).
  - The fleet screen on a server starts at `fleetServerRunner`
    (`main.go:343-347`).
- **Route parity.** `route-parity.md` and `app/test/route_parity_test.dart`
  read client routes from `bridge_client.dart` only.

## Decisions this plan makes beyond the spec

1. **The relayed transport wraps any transport.** It takes the relay's
   transport, the host's name and a token provider that may answer null. It
   imports no `dart:io` and no browser API, so both builds use it.
2. **The prefix is percent-encoded once.** The name is encoded as one path
   segment, then the request's path is appended as it is.
3. **The no-token answer** (spec D18): `send` answers a `Response` with
   status 401 and the body "sign in to <name> first". `lines` fails with
   `Unauthorized` carrying the same words. Neither sends anything.
4. **A relayed `fleet.toml` profile has no `ca_file`.** Its TLS is the hub's.
   A relayed profile naming one is refused.
5. **The browser's host for a relayed name** has kind `server`, and `via` is
   the page's own host's name.
6. **Signing in is a callback.** `HostsModel.signIn` stores a token through
   the `storeToken` callback it was given. A native build passes plan 2's
   `fleet login`. A browser passes the per-host token store's write. Signing
   out is the browser's only, through `clearToken`.

## Execution

- After plans 2 and 3 are on `main`: `git switch -c p8-4-relayed-studio` from
  `main`, or create a worktree on that branch.
- Run each task's Verify and commit it as `p8-4-relayed-studio: task N — <title>`.
- Stage explicit paths only.
- Run `gofmt -w` and `dart format` on the files a task touches before its
  Verify.
- Each test this plan adds is proved by mutation: revert the line it covers
  and watch it fail, then restore with `git checkout`.
- Go tests run with `TMPDIR=/private/tmp/p8-4`.
- The browser tests need Chrome. Where none is installed, record them as not
  run, as F6 plan 1's record does (`docs/studio-flutter/f6-1-work-items.md:125`).

## Contracts

### `fleet.toml` (`internal/fleetprofile/profile.go`, Task 1)

```go
// Server gains:
	Relay string // the server profile this one is reached through, or ""

// rawServer gains:
	Relay string `toml:"relay"`
```

### The fleet command (`cmd/marshal/main.go`, Task 1)

```go
// FleetServer gains:
	Relay string `json:"relay,omitempty"`
```

### Relayed transport (`app/lib/src/client/relayed_transport.dart`, Task 2)

```dart
/// A host's token, or null when none is stored.
typedef HostTokenProvider = Future<String?> Function();

/// [relay] with every route under `/api/hosts/<host>` and the host's own
/// token in `X-Marshal-Host-Token` (spec D18).
Transport relayedTransport(
  Transport relay, {
  required String host,
  required HostTokenProvider token,
});
```

### Hosts (`app/lib/src/client/host.dart`, `app/lib/src/model/fleet_store.dart`, Task 3)

```dart
// HostSpec and Host gain:
  /// The relay's host name this host is reached through, or null.
  final String? via;

// HostView gains:
  final String? via;
```

### Client (`app/lib/src/client/bridge_client.dart`, Task 4)

```dart
/// GET /api/relay/hosts: the hosts this bridge relays (P8 D16).
Future<RelayHosts> relayHosts();
```

### Browser tokens (`app/lib/src/client/token_store_web.dart`, Task 4)

```dart
// TokenStoreWeb gains:
  /// The store for relayed host [name]: the key
  /// `marshal:bridge-token@<name>` (spec D21).
  const TokenStoreWeb.forHost(String name);
```

### Hosts section (`app/lib/src/model/hosts_model.dart`, Task 4)

```dart
// HostsModel's constructor gains:
  Future<void> Function(String host, String token)? storeToken,
  void Function(String host)? clearToken,

// HostsModel gains:
  bool canSignOut(HostView host); // a relayed host, where clearToken is set
  Future<void> signOut(String host);
```

## Task 1 — `fleet.toml` names relayed hosts

**Files:** `internal/fleetprofile/profile.go`, `internal/fleetprofile/edit.go`,
`internal/fleetprofile/profile_test.go`, `internal/fleetprofile/edit_test.go`,
`cmd/marshal/main.go`, `cmd/marshal/fleet_cli_test.go`.

1. **Decoding.** A server has `url` or `relay`, never both and never
   neither.
   - A relayed server's `relay` must name another server with a `url` once
     every server is built, so the check runs in `decode` after
     `buildServer`.
   - A relay naming a relayed server is refused: "servers.NAME.relay: HUB is
     itself relayed".
   - A relayed server with `ca_file` is refused (decision 4).
   - `token_cmd` and `notify` work as for any server.
2. **`AddServer`** writes `relay = "HUB"` in place of `url` when the server
   has one. `fleet add NAME --relay HUB` takes no URL and refuses `--ca-file`.
3. **`fleet servers --json`** writes `relay`, and leaves `url` empty for a
   relayed server.
4. **The fleet screen.** `marshal fleet --server NAME` on a relayed profile
   refuses before `fleetServerRunner`, with spec D20's words.
5. Tests:
   - `profile_test.go`:
     - `TestARelayedServerNamesAURLServer`;
     - `TestARelayMustNotBeRelayed`;
     - `TestAServerNeedsAURLOrARelayNotBoth`;
     - `TestARelayedServerHasNoCAFile`.
   - `edit_test.go`: `TestAddServerWritesARelay`.
   - `fleet_cli_test.go`:
     - `TestFleetAddWithARelay`;
     - `TestFleetServersJSONReportsTheRelay`;
     - `TestTheFleetScreenRefusesARelayedProfile`.

**Verify:** `TMPDIR=/private/tmp/p8-4 go test ./internal/fleetprofile ./cmd/marshal -run 'Relay|TestFleet|TestAServer|TestAddServer' -count=1`.

**Mutation check:** drop the "itself relayed" check.
`TestARelayMustNotBeRelayed` must fail. Restore.

## Task 2 — the relayed transport

**Files:** `app/lib/src/client/relayed_transport.dart` (new),
`app/test/client/relayed_transport_test.dart` (new).

1. **`send` and `lines`** read the token first. With none, they answer as
   decision 3 says. With one, they pass the relay a request whose:
   - path is `/api/hosts/<encoded host>` followed by the original path;
   - query, method and body are the original's;
   - headers are the original's, plus `X-Marshal-Host-Token`.

   The relay's own transport sets its own `Authorization` as it always does.
2. **Nothing else changes.** A failure from the relay passes through
   untouched: a 401 is `Unauthorized`, a 503 is a `BridgeFailure` with the
   relay's words.
3. Tests, against a recording fake `Transport`:
   - `routes go under the host's prefix`;
   - `a name with a slash is one segment`;
   - `the host token rides in its own header`;
   - `the route's own headers are kept`;
   - `with no token send answers 401 and sends nothing`;
   - `with no token a stream fails unauthorized`;
   - `a 503 from the relay is the relay's words`.

**Verify:** `cd app && flutter test test/client/relayed_transport_test.dart test/boundary_test.dart`.

**Mutation check:** skip the no-token check, so the request goes out without
the header. `with no token send answers 401 and sends nothing` must fail.
Restore.

## Task 3 — hosts know their relay, and native discovery

**Files:** `app/lib/src/client/host.dart`,
`app/lib/src/client/host_discovery_io.dart`,
`app/lib/src/model/fleet_store.dart`,
`app/lib/src/ui/settings/hosts_section.dart`,
`app/test/client/host_registry_test.dart`,
`app/test/client/host_discovery_io_test.dart` (new),
`app/test/ui/settings_test.dart`, `app/test/support/screens.dart`.

1. **`via`** travels from `HostSpec` to `Host` to `HostView`, null by
   default.
2. **The kind line** for a host with `via` is "A server profile, through
   <via>" (spec D23).
3. **"Sign in to the relay first."** A relayed host's detail shows that
   sentence while the host named by its `via` is `unauthorized`, and then
   offers no Sign in of its own.
4. **Native discovery** reads `relay` from each row.
   - Rows with a `url` are built as today.
   - A row with `relay` gets a `relayedTransport` over a `serverTransport`
     built as its hub's is: the hub's URL, CA file and `_fleetToken(cli,
     hub)`. The host's token is `_fleetToken(cli, name)`.
   - A row whose hub is missing from the document is skipped.
   - `via` is the hub's name.
5. **Screens.** Register `'settings · hosts · relayed'`: a relayed host whose
   relay is live, and one whose relay is unauthorized.
6. Tests:
   - `host_registry_test.dart`: `a host keeps the relay it was found
     through`.
   - `host_discovery_io_test.dart`, with a fake `marshal` script as plan 2's
     runner test uses:
     - `a relayed profile reaches its host through its hub`;
     - `a relayed profile whose hub is missing is skipped`.
   - `settings_test.dart`: `a relayed host says where it is reached`, and
     `a relayed host behind a refused relay says to sign in to the relay`.

**Verify:** `cd app && flutter test test/client test/ui/settings_test.dart test/states_test.dart test/overflow_test.dart test/tooltips_test.dart`.

**Mutation check:** build the relayed host's transport with the hub's token
in place of its own. `a relayed profile reaches its host through its hub`
must fail, because its script sees the wrong `fleet token` name. Restore.

## Task 4 — the browser lists, and signs in to, relayed hosts

**Files:** `app/lib/src/client/bridge_client.dart`,
`app/lib/src/client/token_store_web.dart`,
`app/lib/src/client/host_discovery_stub.dart`,
`app/lib/src/model/hosts_model.dart`,
`app/lib/src/ui/settings/hosts_section.dart`, `app/lib/main.dart`,
`docs/studio-flutter/route-parity.md`,
`app/test/client/bridge_client_test.dart`,
`app/test/client/transport_web_test.dart`,
`app/test/model/hosts_model_test.dart`, `app/test/ui/settings_test.dart`.

1. **`relayHosts`** reads `/api/relay/hosts`, as `daemon` reads its document
   (`bridge_client.dart:18`). The route-parity row for `GET /api/relay/hosts`
   names `relayHosts` and drops its `dropped:` note.
2. **`TokenStoreWeb.forHost`** keeps its key per spec D21. The default
   constructor's behaviour, the Svelte key fallback included, is unchanged.
   A host store has no fallback.
3. **Browser discovery** lists the page's bridge as today. It then reads its
   `relayHosts`:
   - each name becomes a `HostSpec` with kind `server`, `via` the page's host
     name, and a client over `relayedTransport(WebTransport(), …)`;
   - the token comes from that host's `TokenStoreWeb.forHost(name)`;
   - any failure, 404 included, adds none;
   - a name equal to the page's host name is skipped.
4. **Sign in and sign out** (decision 6).
   - In a browser, `main.dart` builds `HostsModel` with `storeToken` writing
     the host's store and `clearToken` clearing it. `signIn` stores, then
     retries; `signOut` clears, then retries, so the host reads "token
     refused" again.
   - A native build keeps plan 2's `fleet login` for `storeToken` and has no
     `clearToken`.
   - The browser's "Add a host" row stays planned, with spec D22's note.
5. Tests:
   - `bridge_client_test.dart`: `relayHosts reads the relay's hosts`.
   - `transport_web_test.dart`, which is `@TestOn('browser')`:
     - `a host store keeps its own key`;
     - `a host store has no svelte fallback`;
     - `a relayed host's request carries both tokens`.
   - `hosts_model_test.dart`:
     - `sign in stores through the callback, then retries`;
     - `sign out clears, then retries`;
     - `only a relayed host can sign out`.
   - `settings_test.dart`: `a relayed host offers sign out once signed in`.

**Verify:** `cd app && flutter test test/client/bridge_client_test.dart test/model/hosts_model_test.dart test/ui/settings_test.dart test/route_parity_test.dart && flutter test --platform chrome test/client/transport_web_test.dart`.

**Mutation check:** give `forHost` the page token's key. `a host store keeps
its own key` must fail. Restore.

## Task 5 — acceptance, the record and full verification

**Files:** `docs/daemon/p8-studio-federation.md`, `AGENTS.md`.

1. **`AGENTS.md`.** The paragraph on `fleet.toml` gains one sentence: a
   profile with `relay = "HUB"` in place of `url` is reached through HUB,
   and the fleet screen does not use one yet.
2. **The acceptance run,** by hand, as the spec's Testing section lists it:
   1. Build both binaries and the Studio.
   2. Restart the daemon and the browser Studio with `scripts/dev-studio.sh`,
      with `docs/dev-loop.md`'s relay section applied.
   3. Run the five steps in the native app and in a browser.
   4. Record each step with what the Studio showed. A step that fails is
      recorded as failed, with what happened.
3. **The record.** Fill the spec's "Plan 4" record:
   - what landed;
   - any decision the executor had to make;
   - the mutation checks and their results;
   - the acceptance run.
4. **Full verification:**
   - `TMPDIR=/private/tmp/p8-4 go test ./internal/fleetprofile ./cmd/marshal ./web/... -count=1`;
   - `go vet ./...`;
   - `cd app && flutter analyze`;
   - `flutter test`;
   - `flutter test --platform chrome test/client/transport_web_test.dart`;
   - `dart format --output=none --set-exit-if-changed lib test`;
   - on macOS, `flutter test test/gallery_test.dart`.

**Verify:** the full verification above.

## Done when

- A browser on a relay's page lists the relay's hosts, signs in to each with
  its own token, and reads, streams and drives them.
- A native build reaches a `relay = "HUB"` profile through its hub, and the
  fleet screen refuses one with the spec's words.
- A relayed host says where it is reached, and says to sign in to its relay
  first while the relay refuses the page.
- Every test named above passes, each mutation check fails as described, and
  the acceptance run is recorded.
