# Deploying the webbridge as a container

The bridge runs as a Linux container with all state on a named volume.
This is the only supported deployment for containerized agents: a Unix
socket created inside a container is not dialable from a macOS host, so
the bridge must be a Linux container sharing the state volume with its
agents.

## Quick start

```bash
docker compose up -d
```

The compose file mounts the Docker socket into the bridge container so
it can spawn agent containers. Inside the container the bridge binds to
`0.0.0.0:7700` (required for Docker's port forwarding to reach it); the
compose file publishes the port as `127.0.0.1:7700` on the host, so the
API is reachable only from localhost. The bridge rejects unauthenticated
API calls — generate a token with `webbridge --token` or let it
auto-generate one (printed to stderr on first boot).

## The Docker socket

**Mounting `/var/run/docker.sock` makes the bridge container
root-equivalent on its host.** This is not a new privilege — a
host-process bridge that could invoke `docker` already had it — but in
a compose file it reads as a choice, so it is a deliberate one. The
bridge needs the daemon to spawn agent containers; there is no way to
grant that without root-equivalent access to the Docker API.

## State volume

One named volume, `marshal-state`, mounted at `/state` in the bridge:

```
/state/work/<agentID>/      agent workspace
/state/repos/<hash>/        shared bare mirrors
/state/sockets/<agentID>/   per-agent ACP socket
/state/audit/               audit log
```

Agents mount only their own subpaths:

```
--mount type=volume,source=marshal-state,target=/work,volume-subpath=work/<id>
--mount type=volume,source=marshal-state,target=/run/marshal,volume-subpath=sockets/<id>
```

The option name differs by runtime: Docker spells it `volume-subpath=`,
Podman spells it `subpath=` (per `podman-run(1)`). The bridge detects
the runtime and builds the mount argument accordingly.

`--state-volume` must name the volume mounted at `--state-dir` (default
`marshal-state`). A mismatch — a volume name that does not match the
volume actually mounted at the state directory — surfaces as a
`cannot access path …` error from the runtime naming the configured
volume rather than the mounted one, so the two flags must be kept in
step.

## Local project mounts

A containerized bridge sees a host checkout at its own mount point, but
Docker resolves a bind-mount source against the daemon's view, not the
bridge's. Declare each shared root at startup:

```bash
docker compose run bridge \
  --project-mount /Users/you/code:/host-projects
```

The bridge reads the checkout at `/host-projects/marshal` and passes
`/Users/you/code/marshal` as the mount source for the agent. An
undeclared path fails loudly with `mounts denied` — the good failure
mode, surfacing at spawn rather than as an empty workspace.

## Image

The bridge image is larger than the agent image because it carries git
and the Docker CLI. This is inherent: the bridge runs every git
operation itself (clone, mirror, push) and spawns agent containers
through the mounted Docker socket.

Build locally:

```bash
docker build -f build/Dockerfile.bridge -t marshal/webbridge:dev .
```
