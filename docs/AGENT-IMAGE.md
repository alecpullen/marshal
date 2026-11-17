# Agent Image

Marshal ships a locally-buildable Docker image for running the agent in a
container. This document covers how to build it, what the default image
deliberately does *not* include, and how to declare a per-project image.

## Building locally

From the repository root:

```bash
docker build -f build/Dockerfile -t marshal/agent:dev .
```

The build uses a multi-stage Dockerfile:

- **Builder stage** (`golang:1.26-bookworm`): compiles the `marshal` binary
  with `CGO_ENABLED=1`. CGO is required because tree-sitter backs Go symbol
  extraction and a pure-Go build will not link.
- **Runtime stage** (`debian:bookworm-slim`): installs `git` and
  `ca-certificates`, then copies in the compiled binary.

Debian-slim is used rather than Alpine because CGO against musl is a known
source of subtle breakage, and tree-sitter is exactly the dependency that
finds it.

Verify the image works:

```bash
docker run --rm marshal/agent:dev --version
docker run --rm marshal/agent:dev acp --help 2>&1 | head -3
```

The default `ENTRYPOINT` is `marshal`, and the working directory is `/work`.

## What the default image deliberately cannot do

The default image carries only the `marshal` binary, `git`, and
`ca-certificates`. It does **not** include a Go, Node, Python, or Rust
toolchain.

This is intentional. When a project's verify gate needs a language toolchain
that is not present, the gate reports `Skipped` and blocks pending an
override. That is the correct behaviour: the agent must not silently run
verification it cannot actually perform, and it must not guess at a toolchain
that was never declared.

If your project needs a toolchain, declare a per-project image (below) that
includes it.

## Declaring a per-project image

To give a project its own image with the toolchains it needs, declare it in
`.devcontainer/devcontainer.json` using an `"image"` field:

```json
{
  "image": "my-org/marshal-project:latest"
}
```

The image should be built on top of the marshal agent image so it inherits
the `marshal` binary, `git`, and `ca-certificates`, then adds whatever
toolchains the project requires. For example:

```dockerfile
FROM marshal/agent:dev
RUN apt-get update \
 && apt-get install -y --no-install-recommends golang nodejs python3 rustc \
 && rm -rf /var/lib/apt/lists/*
```

When a project declares an image, the agent runs inside that image and the
verify gate can use the toolchains it provides.

## The fast dev image

`build/Dockerfile` is the right thing to publish and the wrong thing to run on
every dev commit: a rebuild pulls a `golang` builder and recompiles the whole
module. `build/Dockerfile.dev` trades that portability for speed — it `COPY`s a
host-built binary into an image that adds only `git` and `ca-certificates`, so
refreshing it is a copy:

```bash
scripts/dev-studio.sh --dev-image
```

Two things about it are load-bearing:

- **The base must match the host's libc.** marshal links tree-sitter through
  cgo, so the copied binary is dynamically linked against the machine that
  built it. `debian:bookworm-slim` (glibc 2.36) cannot run a binary linked
  against a newer host; the failure reads as `GLIBC_2.43 not found` at agent
  start. The script derives the base from `/etc/os-release` (fedora, debian,
  ubuntu) and accepts `--build-arg BASE_IMAGE=` when that guess is wrong.
- **It is dev-only.** Do not publish it, and do not expect it to run on a
  machine other than one like the machine that built it. Releases keep using
  `build/Dockerfile`.

Both images tag `ghcr.io/marshal/agent:latest`, because a source build has no
published tag to pin to (`defaultAgentImageFor`); that is what makes the
studio's default project settings resolve to the image built here. The dev
image also carries a `marshal.dev.bin-mtime` label so `--status` can report
when it was built from an older `./marshal`.

The agent's `marshal` comes from the image, not from the bridge's
`--marshal-bin`, which reaches only host-process agents. This is why the image
is a third artifact to refresh on the dev loop, alongside the SPA and the two
binaries.
