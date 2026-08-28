# Builder. CGO is required: tree-sitter backs Go symbol extraction, so a
# pure-Go build will not link.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=1 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/marshal ./cmd/marshal

# Runtime. Debian-slim rather than Alpine: CGO against musl is a known
# source of subtle breakage, and tree-sitter is exactly the dependency
# that finds it.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/marshal /usr/local/bin/marshal

# The agent needs git for its own read-side tools (status, log). Git is
# bridge-mediated for credentials and pushes; mediation governs
# authority, not observation.
#
# ca-certificates is not optional: the agent calls the model provider
# over HTTPS from in here, and a missing trust store fails in a way that
# reads as a network fault.

WORKDIR /work
ENTRYPOINT ["marshal"]
