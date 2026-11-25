# Running Marshal on a server

A server is a Linux machine that runs Marshal's daemon as its user's service,
reached over SSH from a laptop and, when you want a page, by a browser through
the proxy you put in front of it.

## What a server is

A server runs the same daemon a laptop runs: there is no server build and no
server mode (one-daemon D5). Install Marshal with `install.sh` or Homebrew,
then install the daemon.

```bash
curl -fsSL https://raw.githubusercontent.com/alecpullen/marshal/main/scripts/install.sh | sh
# or: brew install alecpullen/tap/marshal
marshal daemon install
```

`install.sh` runs `marshal daemon install` itself; after Homebrew, run it once
by hand. It writes the systemd user unit, starts it, and turns on linger for
your user, so the daemon outlives a logout; `--no-linger` leaves linger alone.

## Workspace agents

An agent with a workspace runs in a container (D4), which needs a rootless
runtime: rootless Podman or rootless Docker, on the daemon user's `PATH`. A
rootful runtime is refused rather than used, and the refusal names the fix:
`docker runs as root, and a daemon on the host needs a rootless runtime on
Linux (rootless Docker or Podman)`. Every other agent is a host process and
needs no runtime at all: a plain local agent runs on its project's child, and
the control agent is the fleet's own child — a host process working from
`<state>/control`, holding the sessions that are not tied to a running project
agent.

`marshal daemon status` names what the daemon serves: its `studio:` line is
the directory the Studio is served from, and its `web:` line is where the web
listener bound (`web: off` when it serves none).

## Reaching it from a laptop

A host is an SSH destination (D6–D9). On the laptop:

```bash
marshal fleet add box you@box.example.com
marshal fleet --host box
```

`fleet add` writes the host's table to `~/.config/marshal/fleet.toml` — mode
`0600`, comments kept — and waits for the laptop's daemon to report it. Add
`--marshal PATH` for a remote binary that is not on `PATH`, and `--no-notify`
to leave that host's desktop notifications off. The laptop's daemon keeps one
`ssh -N` per host and reports it in `marshal daemon status`, as `host box
(you@box.example.com): live`, or `down` with ssh's last line beside it.

The SSH has to work without a prompt (A1–A3):

- OpenSSH 6.7 or later at both ends, with `AllowStreamLocalForwarding yes` on
  the server (the default): the tunnel forwards a unix socket.
- A key the service can use unattended — one with no passphrase, or one an
  agent it can reach holds — and the server's key already in `known_hosts`.
  ssh runs with `BatchMode`, so a prompt is a failure whose message
  `marshal daemon status` reports as the host's last error.
- `marshal` on the remote's non-interactive `PATH`, or its path in the host's
  table: `marshal = "/home/you/.local/bin/marshal"`, or `--marshal` above.

Then `marshal fleet --host box` opens that daemon's fleet.

## A browser

`marshal daemon install --studio` sets `web_listen = "unix"` for you, when it is
not already set, and prints the proxy line for the listener it ends with.
`marshal daemon install --studio-url https://marshal.example.com` sets `web_url`
once your proxy has an address, and running it again changes it; it refuses an
address with no listener, so pass `--studio` in the same run or set `web_listen`
first. A plain `marshal daemon install` says nothing about the Studio and leaves
both keys alone, so hand-edited values stay in charge.

The daemon serves the Studio and its API, and authenticates no browser: the
proxy you put in front of it does (D10, D13). In
`~/.config/marshal/config.toml`:

```toml
[daemon]
web_listen = "unix"          # web.sock beside the daemon's socket, mode 0600
web_url = "https://marshal.example.com"
preview_listen = "127.0.0.1:7790"
preview_url = "https://preview.example.com"
```

A project added from the Studio — blank, cloned, or a folder already on the
host — goes in `projects_dir` (default `~/projects`) under the name the Studio
gives it; set it, and reinstall, to put them elsewhere. A clone runs as the
daemon's user, with that user's git config, SSH keys and credential helpers
([new-project](studio-flutter/new-project.md)).

`web_listen` is `unix`, an absolute socket path, or a loopback address; any
other address is refused at start. `web_group` makes a socket `0660` in that
group for a proxy that runs as another user, and needs an absolute path, since
the daemon's directory is private. `web_url` is the address the proxy
publishes, and it is the daemon's public URL too: `marshal studio` opens it,
and the unit passes it as `--public-url`.

Tailscale Serve, which authenticates the tailnet for you:

```bash
# web_listen = "127.0.0.1:7700"; web_url = "https://box.<tailnet>.ts.net"
tailscale serve --bg 7700
```

Caddy, with whatever auth module you pick:

```
marshal.example.com {
	reverse_proxy unix//home/you/.local/share/marshal/daemon/web.sock
}
```

Let `/hooks/`, `/status/` and `/mcp` through without the proxy's login: each
of them carries its own credential.

Set `web_url` to exactly the address the proxy publishes. The web listener
answers only that host and the loopback names, and refuses a state-changing
request another site's page made. A misconfigured proxy is recognisable: a
wrong `Host` is refused with `this daemon does not answer to host <host>; set
[daemon] web_url to the address the proxy publishes (docs/one-daemon/design.md
D13)`, a cross-site write with `cross-site request refused`, and an `/api/`
body that is not JSON with `the API takes application/json`.

Turn response buffering off for `/api/`: the event streams must flush as they
are written. nginx spells it `proxy_buffering off;`, and Caddy
`flush_interval -1` in the `reverse_proxy` block.

Keep the proxy's idle timeout above 30 s. The streams send a heartbeat every
25 s, and a shorter timeout severs a session's stream while it sits quiet.

A proxy that answers an expired session with a redirect to its login page is
read by the Studio as signed out — `signed out: reload to sign in again` — and
a reload signs in again.

### Previews behind a proxy

Previews are published on a second hostname (D11): set `preview_listen` to a
fixed loopback port and publish it, and set `preview_url` to the origin
preview links name.

With wildcard DNS and a wildcard certificate — Caddy on a DNS challenge, or
Cloudflare — make `preview_url` a template,
`"https://p{token}.preview.example.com"`, and each preview gets an origin of
its own:

```
# wildcard DNS, and one wildcard certificate from a DNS challenge
*.preview.example.com {
	tls { dns cloudflare {env.CLOUDFLARE_API_TOKEN} }
	reverse_proxy 127.0.0.1:7790
}
```

With one hostname, as Tailscale Serve gives, set a plain `preview_url` on a
second port instead. Previews then share that origin, which is still not the
Studio's.

Previews carry their own token, so let the preview hostnames past the proxy's
login, as `/hooks/` is.

A forwarded host's previews come through this daemon, which relays them over
the host's tunnel, so the host publishes nothing. Both daemons need this
version.

## MCP clients

An MCP client on the server runs `marshal mcp --credential-file PATH`, which
relays the client's stdin and stdout JSON-RPC to `/mcp` over the daemon's
socket. The file is the `0600` credential Settings › Access shows once. From
another machine, make `ssh box marshal mcp --credential-file PATH` the client's
command.

## The agent image

Unchanged: workspace agents run the image in
[`docs/AGENT-IMAGE.md`](AGENT-IMAGE.md), which covers building it and
declaring a per-project one.

## Moving from the containerized bridge

A machine that ran the retired containerized bridge moves over in four steps:

1. Finish or discard the agents that bridge still holds.
2. Stop the compose stack: `docker compose down`.
3. Install the daemon on the host, as above.
4. The old `marshal-state` volume is not read by the daemon on the host, and
   can be deleted once nothing in it is wanted.
