# One daemon plan 7: adding and removing hosts from a browser — implementation plan

Date: 2026-11-20. Status: approved 2026-11-20.

Base: `main` at `bbdc4486`, with plans [1](2026-11-20-od-1-one-daemon-plan.md),
[2](2026-11-20-od-2-hosts-over-ssh-plan.md) and
[3](2026-11-20-od-3-web-listener-plan.md) applied first, plan 3 with its
request guard (Task 14). It does not depend on plans 4–6. Most of what it
changes is code that plans 2 and 3 create, so steps name that code by the
functions and files those plans declare.

Design: [design.md](../design.md) D15 and open question 4. Open question 4
recommended that a browser stay read-only. On 2026-11-20 the user chose
otherwise: a browser adds and removes hosts. Planning settled these, and
design.md records them:
- **The daemon edits `fleet.toml` by running `marshal fleet`.** `web/` is
  standard library only and cannot import `internal/fleetprofile`. So `POST
  /api/hosts` and `DELETE /api/hosts/<name>` run `<marshal-bin> fleet add` and
  `fleet remove` as the daemon's user, on the daemon's machine. The rules,
  the file's privacy, and the wait for the tunnel report are the CLI's own,
  from plan 2.
- **Both builds add hosts the same way.** The Studio adds and removes hosts
  through its home daemon's API: the local daemon in a native build, the
  page's daemon in a browser. The native app no longer runs `marshal fleet`
  itself, so `fleet_profiles_io.dart` and `fleet_profiles_stub.dart` go.
- **A host is always added to the home daemon.** Never to a forwarded host:
  the forward refuses `api/hosts/` paths, so hosts do not chain.
- **The gates.**
  - The routes are on the web listener, so plan 3's guard and the proxy's
    login are what stand in front of them. The socket serves them too, to the
    owner.
  - `ssh` is hardened so a host added this way cannot take more than a
    tunnel: no agent forwarding, no X11 forwarding, no local command, and no
    remote command from the user's config.

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- A browser's Settings › Hosts adds a host by name and SSH destination, and
  removes one, as the native app's does.
- Both builds do it through the daemon's API. The daemon runs `marshal fleet
  add` or `remove`, and answers the hosts as they stand after the change.
- The tunnel's `ssh` takes no more than a tunnel from any host, whatever the
  user's `~/.ssh/config` says.

## Non-goals

- Editing a host's destination or `marshal` path in place. Remove it and add
  it again, as the CLI does.
- Adding a host to a forwarded host's daemon from this Studio.
- A setting that turns the routes off. If that is wanted, the proxy can refuse
  `POST /api/hosts`.
- Notifications (later).

## Assumptions

- **Plan 2's CLI and file.**
  - `marshal fleet add NAME DEST [--marshal PATH] [--no-notify]` checks the
    host with `fleetprofile.AddHost`, writes `fleet.toml` atomically at
    `0600`, and waits up to `fleetHostWait` (5 s) for the daemon's report to
    list the host.
  - `marshal fleet remove NAME` does the reverse.
  - Both write their refusal to stderr as `marshal fleet <sub>: <reason>` and
    exit 1.
- **Plan 2's tunnel commands.** `internal/tunnels`' `statusArgv` and
  `forwardArgv` share the options `-T -o BatchMode=yes -o ConnectTimeout=15
  -o ControlMaster=no -o ControlPath=none`.
- **Plan 3's routes.**
  - `GET /api/hosts` (`listHosts`) and the `/api/hosts/` forward prefix
    (`hostForward`) are registered in `routes()`.
  - `splitForward` refuses a remainder under `api/hosts/`.
  - A method-qualified pattern for one segment, `DELETE /api/hosts/{name}`,
    is more specific than the `/api/hosts/` prefix in Go's mux. It does not
    catch a forward's longer path.
- **The bridge knows its marshal binary:** `Fleet.marshalBin`, set from
  `--marshal-bin`. The daemon runs as the user, with the user's `HOME`, so
  `marshal fleet add` edits the same `fleet.toml` the tunnel helper reads.
- **The Studio.**
  - After plan 2, `FleetProfiles` declares `supported`, `add(name,
    destination)` and `remove(name)`, chosen by conditional import between
    `fleet_profiles_io.dart`, which runs `marshal fleet`, and
    `fleet_profiles_stub.dart`, which refuses.
  - `_Settings` is built per host (`main.dart:488–495`) and passes
    `profiles: fleetProfiles()` (`:755`).
  - The first host discovery finds is the local daemon in a native build
    (plan 2) and the page's daemon in a browser (plan 3).
  - After plan 3, a browser's Hosts section shows a planned "Add a host" row
    with D15's note, and no Remove.

## Contracts

```go
// web/bridge/contract/daemon.go

// HostAdd is POST /api/hosts's body: a host to add to fleet.toml on this
// daemon's machine (one-daemon D15, plan 7).
type HostAdd struct {
	Name    string `json:"name"`
	SSH     string `json:"ssh"`
	Marshal string `json:"marshal,omitempty"`
}
```

```go
// web/bridge/hostedit.go (new)

// fleetCommand runs `<marshal-bin> fleet <args…>`, and answers nil, or the
// command's last line of stderr as the error. Tests replace it.
type fleetCommand func(ctx context.Context, args ...string) error

// addHost answers POST /api/hosts: it runs `fleet add` and answers 201 with
// the HostList after it.
func (s *Server) addHost(w http.ResponseWriter, r *http.Request)

// removeHost answers DELETE /api/hosts/{name}: it runs `fleet remove` and
// answers 200 with the HostList after it.
func (s *Server) removeHost(w http.ResponseWriter, r *http.Request)

// New Server fields (http.go):
fleetCmd   fleetCommand // nil: run the fleet's marshal binary
hostsEdits sync.Mutex   // one fleet.toml edit at a time
```

```dart
// app/lib/src/client/bridge_client.dart
/// POST /api/hosts: adds a host on this daemon's machine.
Future<HostList> addHost(HostAdd host);
/// DELETE /api/hosts/{name}: removes it.
Future<HostList> removeHost(String name);

// app/lib/src/client/fleet_profiles.dart — one implementation, both builds:
/// The hosts commands, run by the daemon [client] reaches.
FleetProfiles fleetProfiles(BridgeClient client);
```

## Task 1: The tunnel's `ssh` takes nothing but a tunnel

**Goal:** both tunnel commands refuse agent forwarding, X11 forwarding, local
commands and a configured remote command, whatever `~/.ssh/config` sets.

**Files:** `internal/tunnels/ssh.go`, `internal/tunnels/ssh_test.go`.

**Steps:**
1. Append to the shared options, after `-o ControlPath=none`: `-o
   ForwardAgent=no`, `-o ForwardX11=no`, `-o PermitLocalCommand=no` and `-o
   RemoteCommand=none`.
2. Add a comment above them: a host can now be added from a browser session,
   so the daemon's `ssh` hands no credential and runs nothing but the status
   call and the forward. `-o` on the command line wins over the config file.

**Tests:** update `TestStatusArgvAsksForTheJSONStatus` and
`TestForwardArgvForwardsTheSocket` (and plan 6's preview variants, if plan 6
has landed) for the four options.

**Verify:** `go test ./internal/tunnels -count=1` passes.

## Task 2: The contract has a host to add

**Goal:** `contract.HostAdd` exists, and the Dart module is regenerated.

**Files:** `web/bridge/contract/daemon.go`, `app/lib/src/client/contract.g.dart`
(generated).

**Steps:**
1. Add `HostAdd`, as Contracts declares it, after `HostList`.
2. Regenerate with `go test ./web/bridge/contract/dartgen -run
   TestDartContractIsCurrent -update`.

**Verify:** `go test ./web/bridge/contract/... -count=1` passes without
`-update`.

## Task 3: The daemon adds and removes hosts

**Goal:** `POST /api/hosts` and `DELETE /api/hosts/{name}` run `marshal fleet
add` and `remove`, one at a time, and answer the hosts after the change.

**Files:** `web/bridge/hostedit.go` (new), `web/bridge/hostedit_test.go`
(new), `web/bridge/http.go`.

**Steps:**
1. Add `fleetCommand`, `addHost` and `removeHost`, and the two `Server`
   fields, as Contracts declares them. Register `POST /api/hosts` and `DELETE
   /api/hosts/{name}` in `routes()`, beside plan 3's `GET /api/hosts`.
2. The default `fleetCommand`:
   - runs `exec.CommandContext(ctx, s.fleet.marshalBin, append([]string{"fleet"},
     args...)...)` with the daemon's environment, no standard input, and
     stdout discarded;
   - on failure, answers the last non-empty line of stderr, trimmed, with a
     leading `marshal fleet add: ` or `marshal fleet remove: ` removed;
   - when stderr is empty, answers the exit status.

   A server with no fleet answers 501 from both routes.
3. `addHost`:
   1. Decode a `contract.HostAdd` with `decodeJSON`.
   2. Refuse with 400, naming the field, when `name` or `ssh` is empty, or
      when either starts with `-`. The CLI refuses these too, but nothing that
      looks like a flag is ever passed to it.
   3. Take `hostsEdits`, and run `fleet add <name> <ssh>`, plus `--marshal
      <marshal>` when set, with a 30 s context. The CLI waits for the report
      itself.
   4. A command error answers 400 with `{"error": <its line>}`, and anything
      else 201 with `contract.HostList{Hosts: s.reportedHosts()}`.
4. `removeHost` does the same with `fleet remove <name>`, answering 200. The
   name comes from the path, and a name starting with `-` is refused with 400.
5. Tests (new). `fleetCmd` is replaced with a recorder that can be told to
   fail, and a hosts report is seeded as plan 2's tests seed it.
   - `TestAddingAHostRunsFleetAdd`: the arguments, then 201 with the report.
   - `TestAddingAHostPassesTheMarshalPath`.
   - `TestAnAddThatFailsAnswersTheReason`: 400, the reason with its prefix
     removed.
   - `TestAnAddRefusesAFlagLikeValue`: the command is not run.
   - `TestRemovingAHostRunsFleetRemove`.
   - `TestHostEditsRunOneAtATime`: two concurrent adds never overlap in the
     recorder.
   - `TestTheHostRoutesAreGuarded`: a cross-site `POST /api/hosts` through
     `ServeHTTP` answers plan 3's 403, and the command is not run.
   - `TestTheForwardStillOwnsLongerPaths`: `DELETE /api/hosts/box/api/agents/x`
     reaches `hostForward`, not `removeHost`.
   - `TestTheDefaultFleetCommandCarriesTheLastLine`: a stand-in
     `marshal-bin` script that writes two lines to stderr and exits 1, so the
     error is the second line.

**Verify:** `go test ./web/bridge -run 'Host|Guard|Forward' -count=1` passes.

## Task 4: The Studio adds hosts through its daemon, in both builds

**Goal:** `BridgeClient` adds and removes hosts. There is one `FleetProfiles`,
built over the home daemon's client, and the native app runs no `marshal
fleet`.

**Files:** `app/lib/src/client/bridge_client.dart`,
`app/lib/src/client/fleet_profiles.dart`,
`app/lib/src/client/fleet_profiles_io.dart` and `fleet_profiles_stub.dart`
(deleted), `app/lib/main.dart`, `app/test/client/bridge_client_test.dart`,
`app/test/client/fleet_profiles_io_test.dart` (deleted),
`app/test/client/fleet_profiles_test.dart` (new).

**Steps:**
1. In `bridge_client.dart`, add `addHost` and `removeHost`, as Contracts
   declares them. Each decodes the answer as a `HostList`, as `hosts()` does.
2. Rewrite `fleet_profiles.dart` without the conditional import.
   - `fleetProfiles(BridgeClient client)` answers a private implementation.
     - `supported` is true.
     - `add` calls `client.addHost(HostAdd(name: name, ssh: destination))`.
     - `remove` calls `client.removeHost(name)`.
     - A `BridgeFailure` from either is rethrown as `FleetProfileFailure`
       with the bridge's message.
   - `FleetProfileFailure` stays.
   - Rewrite the file's comments: the daemon runs `marshal fleet` on its own
     machine (plan 7). The app runs no process for it.
3. Delete `fleet_profiles_io.dart`, `fleet_profiles_stub.dart` and
   `fleet_profiles_io_test.dart`.
4. In `main.dart`'s `_Settings` (`:755`), `profiles:` becomes
   `fleetProfiles(registry.hosts.first.client)`, the home daemon's. A comment
   says why: hosts are added to the daemon that keeps the tunnels, never
   through a forward.
5. Tests:
   - `bridge_client_test.dart`: `addHost posts the host and reads the list`,
     and `removeHost deletes by name`;
   - `fleet_profiles_test.dart` (new), over a scripted transport: `add posts
     the name and destination`, `remove deletes the name`, and `a refusal
     carries the daemon's words`.

**Verify:**
- `cd app && flutter analyze` reports nothing new.
- `cd app && flutter test test/client/ test/boundary_test.dart` passes.
- `grep -rn 'Process.run\|fleet_profiles_io\|fleet_profiles_stub' app/lib`
  finds no `fleet` command.

## Task 5: A browser's Hosts section adds and removes

**Goal:** in a browser, "Add a host" is a real row and a host with a
destination has Remove, as in the native app. D15's read-only note is gone.

**Files:** `app/lib/src/ui/settings/hosts_section.dart`,
`app/lib/src/model/hosts_model.dart`, `app/test/ui/settings_test.dart`,
`app/test/model/hosts_model_test.dart`, `app/test/support/screens.dart`.

**Steps:**
1. In `hosts_section.dart`:
   - delete the planned add row and its note (plan 3's Task 17). The add row
     is shown whenever `model.canAdd`, which is now true in both builds;
   - its description reads "Another machine's daemon, reached over SSH from
     the machine this daemon runs on";
   - rewrite the class comment.
2. In `hosts_model.dart`, the class comment says the hosts are added to the
   home daemon in both builds. `canAdd` stays `profiles?.supported ?? false`,
   so a fixture with no profiles still draws the planned row.
3. In `HostDetail`, Remove already follows `canAdd` and a destination (plan
   2). The removal sheet's text becomes "The Studio stops showing it, and
   this daemon deletes its table from fleet.toml. Its own daemon keeps
   running."
4. Tests and fixtures:
   - `settings_test.dart`: `the add row is planned in a browser` becomes `the
     add row is planned without a daemon to add through` (a model with no
     profiles), and plan 3's `a browser says where hosts are added` is
     deleted;
   - new `a browser adds a host through its daemon`: a model whose profiles
     record the call;
   - `hosts_model_test.dart`: `a browser cannot add` becomes `a model without
     profiles cannot add`;
   - `screens.dart`: `_settings`'s browser case, if it has one, uses
     `_FixtureFleetProfiles`.

**Verify:**
- `cd app && flutter analyze` reports nothing new.
- `cd app && flutter test` passes in full, the design rules, tooltips, states
  and overflow tests included.

## Task 6: The docs, and verification

**Goal:** the docs say a browser adds hosts, and the whole works by hand.

**Files:** `AGENTS.md`, and `docs/DEPLOYMENT.md` if plan 5 has rewritten it.

**Steps:**
1. In `AGENTS.md`'s hosts paragraph (plan 2's Task 17), add one sentence:
   `POST /api/hosts` and `DELETE /api/hosts/<name>` run `marshal fleet add`
   and `remove` on the daemon's machine, so the Studio adds hosts in either
   build.
2. In `docs/DEPLOYMENT.md`'s browser section, if it exists, add that the
   proxy's login also gates adding hosts. A proxy can refuse `POST
   /api/hosts` and `DELETE /api/hosts/*` to keep a browser read-only.
3. Run:
   - `gofmt -l .` prints nothing, and `go vet ./...` passes;
   - `scripts/test.sh` passes;
   - `cd app && flutter analyze && flutter test` passes.
4. **By hand.**
   1. With the web listener on loopback, in a browser, add `self` as
      `localhost`.
   2. `fleet.toml` gains the table at `0600`, and within a few seconds the
      host is live in the section.
   3. Remove it, and the table and `hosts/self.sock` are gone.
   4. An invalid destination (`-oProxyCommand=x`) is refused with the
      CLI's words, and nothing is written.
   5. In the native app, the same add works with no `marshal` process
      started by the app (`ps` during the add shows the daemon's child, not
      the app's).
   6. With `ForwardAgent yes` under `Host *` in `~/.ssh/config`, the tunnel's
      `ssh -v` log shows no agent forwarding requested.

**Verify:** `grep -n 'POST /api/hosts' AGENTS.md` prints the new sentence, and
the step 4 checks pass.

## Integration notes

- **The native app needs its daemon to add a host.** That is already true of
  everything else it does. A daemon too old to serve `POST /api/hosts`
  answers 405, which the Hosts section shows as the refusal.
- **Adding a host is the owner's power, through the proxy.** Anyone the
  proxy lets in can make the daemon open an SSH connection with the user's
  keys. Task 1 makes that connection carry nothing but a tunnel, and the
  proxy can refuse the two routes for a read-only browser.
