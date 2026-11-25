# `marshal daemon install --studio` — design

Status: implemented.

Implements the documented path of one-daemon D10/D13 as one command. It changes
no decision: the daemon still authenticates no browser, and the operator's proxy
still does.

## Context

`docs/DEPLOYMENT.md` ("A browser") is today the only way to learn how to turn
the web Studio on: hand-edit `[daemon] web_listen` and `web_url` in
`~/.config/marshal/config.toml`, then arrange a proxy that authenticates the
browser (D13). Neither key has a default (`internal/app/config/defaults.go`), so
a stock daemon serves no listener, and `marshal studio` answers with an error
that names both keys and the command to run (`cmd/marshal/studio.go`,
`runStudio`). Everything else about the deployment already works: `marshal
daemon install` reads the user config, renders a unit whose `UnitSpec.PublicURL`
becomes webbridge's `--public-url` (`internal/daemon/daemon.go`), writes the
unit, restarts the service, and `marshal daemon status` reports the listener and
the Studio directory.

So the missing piece is not capability, it is the first step: a user must find a
design document and edit TOML before anything can be tried.

The two setups this serves:

- **A local machine.** The native app dials the daemon's socket (D14), so there
  is nothing to configure and no flag to pass. A local user who wants the *web*
  Studio instead sets the loopback pair from `docs/DEPLOYMENT.md` by hand; that
  is the one form that carries D13's caveat, so it stays a deliberate act rather
  than a flag.
- **A headless box reached from other machines** (a homelab host). This is the
  proxy case by construction: a loopback listener is per-machine, so it cannot
  serve other machines at all. The listener plus a proxy that authenticates the
  browser is the only shape that works, and it is exactly what D10/D13 describe.

### Rejected: a default port

A shipped default `web_listen` was considered and rejected. It would make the
one form D13 warns about (`"A web listener on a loopback address is reachable by
every account on the machine"`) the default for every install, publishing
sessions, agents, edits, shell and `/api/hosts/<name>/…` forwarding to any local
account or process. It would also not have helped the multi-machine case, since
loopback does not cross machines. `unix` in a private directory is the form that
needs no such warning, and it is what `--studio` writes.

## Design

### Flags

```
marshal daemon install [--webbridge PATH] [--no-linger] [--studio] [--studio-url URL]
```

- `--studio` — write `daemon.web_listen = "unix"` **when it is unset**. An
  already-configured listener is left alone and reported as unchanged: an
  install restarts the unit, and it must not silently move a listener off the
  address the operator's proxy already points at.
- `--studio-url URL` — write `daemon.web_url`, the address the proxy publishes.
  It requires a listener: `--studio` in the same run, or one already set. A
  `web_url` with no listener is a meaningless pair and is refused rather than
  written. It overwrites an address already set — that is how the address is
  changed — and reports the previous value when it does.
- Neither flag — today's behaviour, unchanged, including its exact output.

`--studio` deliberately has no listener-value argument. The loopback form stays
a documented hand-edit because it is the form that needs D13's caveat printed
alongside it, and a flag invites it to be passed without reading that.

### Order of operations

The order is the load-bearing part; it is what keeps a failure from leaving a
half-configured daemon.

1. Parse the flags.
2. Load the user config exactly as the unit renderer will:
   `config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})`.
3. Compute the intended triple — `web_listen` (existing, else `"unix"`),
   `web_group` (untouched), `web_url` (existing, else the flag).
4. **Validate the triple before writing anything**, through the existing
   `daemonWebListen(listen, group)` and `daemonPublicURL("daemon.web_url", url)`.
   A bad combination fails here, naming its key, and nothing is written:
   - `web_group` set with `"unix"` — `daemonWebListen`'s existing D3 message;
   - a non-http(s) or hostless `--studio-url` — `daemonPublicURL`'s message;
   - `--studio-url` with no listener, and no `--studio` — this command's own.
5. Write through `config.SaveUserConfigSection(config.UserConfigPath(home), cfg,
   baseline)` — the path every other `[daemon]` write uses, with the load-time
   snapshot as the baseline so a `[providers]` or `[models.presets]` entry
   another marshal process saved in between survives the write — and only when a
   value actually changed. A write failure aborts the install before the unit is
   rendered, leaving the unit and the running daemon as they were.
6. Run the existing install unchanged. `daemonUnitSpec` re-reads the config, so
   the unit carries the new `--web-listen` and `--public-url`, and
   `m.Install` writes and restarts as it does today.

A failure *after* the write — a config the existing renderer refuses on a key
this command does not settle, such as a hand-broken `preview_listen`, or a
failed `m.Install` — leaves the written pair on disk with the old unit still
running. Nothing is lost: the pair is itself already validated, and a re-run
once the cause is fixed completes the install.

### Printed output

After the existing `unit:` and `socket:` lines and before the linger line, so a
reader sees what the daemon will bind:

Listener set, no address yet:

```
web_listen: unix (/home/you/.local/share/marshal/daemon/web.sock)
web_url: unset
proxy: point your proxy at /home/you/.local/share/marshal/daemon/web.sock, then run
       marshal daemon install --studio-url https://marshal.example.com
```

Listener and address:

```
web_listen: unix (/home/you/.local/share/marshal/daemon/web.sock)
web_url: https://marshal.example.com
proxy: Caddy, with whatever auth module you pick:

	marshal.example.com {
		reverse_proxy unix//home/you/.local/share/marshal/daemon/web.sock
	}

a proxy that cannot dial a unix socket uses the loopback pair instead:
set web_listen = "127.0.0.1:7700" and web_url = "https://box.<tailnet>.ts.net",
then `tailscale serve --bg 7700` (docs/DEPLOYMENT.md)
/hooks/, /status/ and /mcp pass through without the proxy's login: each carries
its own credential
```

Listener already configured, with an address:

```
web_listen: 127.0.0.1:7700 (unchanged)
web_url: https://box.example.ts.net (was https://old.example.com)
proxy: Caddy:  reverse_proxy 127.0.0.1:7700
       Tailscale Serve:  tailscale serve --bg 7700
       (docs/DEPLOYMENT.md)
/hooks/, /status/ and /mcp pass through without the proxy's login: each carries
its own credential
```

`web_url` prints `(was <old>)` only when the run changed it, and no suffix when
it was already that value. An existing absolute socket path keeps its own path
in the Caddy line and gets no `tailscale serve` line, since no port target can
front a socket.

The whole block prints only when `--studio` or `--studio-url` was passed. A
plain `marshal daemon install` on a daemon someone configured by hand keeps
today's output exactly, so the promise in the flags section holds; the test pins
that by banning `web_listen:`, `web_url:`, `proxy:` and `studio:` from a plain
install's output.

The snippets are quoted from `docs/DEPLOYMENT.md` rather than invented, and the
printed snippet always matches the listener this run ends with: with `unix` the
socket form is primary, and the document's Tailscale example — a *port* target —
appears only as the alternative for a proxy that cannot dial a socket, naming
the one-line change that needs. The `web.sock` path is
`filepath.Join(daemon.PathsFor(home).Dir, "web.sock")`, which is where `unix`
puts it beside the daemon's socket.

### The D12 check

`--studio` is useless on a box with no Studio files, which is exactly a headless
source checkout. After the install, call `bridge.FindStudioDir(spec.Webbridge)`
— the resolver webbridge itself runs — and when it answers `""`, print
`daemonStudioLine`'s existing wording:

```
studio: no files found (reinstall Marshal, or build them: see AGENTS.md)
```

A warning, never a failure: D12 says a daemon without files serves a page that
says where it looked and runs on. When files are found, print
`studio: <dir>`.

### What is not touched

`marshal studio`, `marshal daemon status`, `daemon.Paths`, the web listener's
own behaviour, webbridge's flags, and the retired-key warnings all stay as they
are. The config write is the only new mutation, and it goes through the existing
user-config writer. The stale rationale in `daemonLoopbackAddr`'s comment — the
daemon's retired bearer token — is corrected to D13's proxy authentication and
D2's per-endpoint credentials; no behaviour changes with it.

## Decisions and trade-offs

1. **`web_url` is optional, not required** (the user's choice). Requiring it
   would keep the invariant "`web_listen` set ⇒ `web_url` set" and make
   `marshal studio` work the moment install returns. Making it optional
   unblocks the common homelab order — the listener now, the tailnet name after
   `tailscale serve` — at the cost of a second install run and a window in which
   `marshal studio` errors. That error already names both keys, so the window is
   self-explaining.
2. **Never overwrite a set listener.** An install restarts the unit, so
   replacing a listener that already works would break a working proxy in the
   name of a convenience flag. `--studio` is additive.
3. **Validate before write.** Config is mutated only after the triple is known
   valid, so a failed `--studio-url` cannot leave `web_listen` written and
   `web_url` absent — the state the D12 check and `marshal studio` would then
   both report as broken.
4. **Reuse `SaveUserConfigSection`.** It is the writer every other `[daemon]`
   edit uses and it preserves unrelated sections, so `--studio` cannot clobber a
   user's `[providers]` or `[models.presets]`. Like those edits it normalises the
   file's formatting and drops comments; a comment-preserving writer exists for
   `fleet.toml` (`internal/fleetprofile/edit.go`) but it models a different file
   and section, and duplicating it for `config.toml` would give the config two
   writers that disagree.
5. **The printed snippet follows the listener written.** The document's Tailscale
   example proxies to a *port*, so pairing it with a `unix` listener would
   print an instruction that does not work. Print the socket form as primary and
   the document's loopback pair as the stated alternative for port-only proxies.
6. **No Files is a warning, not a refusal.** D12 already decided that a daemon
   without the Studio runs on and explains itself; `--studio` matches it rather
   than inventing a stricter rule for one command.
7. **No `--studio-listen` flag.** The loopback pair remains a documented manual
   edit, so that accepting D13's caveat stays a deliberate act, and the flag
   surface stays one boolean and one URL.

## Assumptions

- `--studio` runs on the machine that holds the daemon: it writes that machine's
  user config and installs that machine's unit. There is no remote mode here;
  reaching another machine's daemon is `marshal fleet --host` (D6–D9).
- The proxy itself is the operator's, and this command does not install,
  configure or authenticate it.
- `web_group` is left exactly as the user set it, and `daemonWebListen` decides
  whether it is compatible with the listener.
- Linux prints the linger line and macOS does not, as today.
- A `--studio-url` value is validated as an http or https origin by
  `daemonPublicURL`; the command adds no further URL rules.

## Non-goals

- No default port and no default `web_listen`.
- No `--studio-listen`, no loopback convenience form, no port picking.
- No proxy installation, configuration, or TLS.
- No tokens, no browser authentication, no sign-in handover: D10/D13/D14 stand.
- No change to `marshal studio`, `daemon status`, or the Studio's file lookup.
- No remote/SSH form of the command.

## Open questions

None.
