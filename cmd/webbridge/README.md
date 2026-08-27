# webbridge

The fleet control plane. Serves the REST API, the SSE streams, the MCP
endpoint, and the embedded SPA.

```bash
webbridge --project /path/to/repo --addr 127.0.0.1:7700
```

The listen address defaults to `127.0.0.1:7700`, so the bridge is not
reachable off-host unless you say otherwise.

## Serving over HTTPS

`webbridge` will serve TLS directly when given a certificate and key:

```bash
webbridge --tls-cert /etc/ssl/marshal.crt --tls-key /etc/ssl/marshal.key
```

Both flags are also readable from `WEBBRIDGE_TLS_CERT` and
`WEBBRIDGE_TLS_KEY`. Supplying only one is a startup error rather than a
silent fall back to plaintext.

**Certificate lifecycle is deliberately out of scope.** `webbridge` does
not implement ACME, and does not renew anything. Every deployment already
solves this, and differently:

- **A terminating reverse proxy** (Caddy, Traefik, nginx) in front of a
  plaintext `webbridge` on loopback. The common homelab shape, especially
  with a wildcard certificate.
- **Your own certificates**, passed with the flags above — an internal CA,
  a wildcard for your domain, or `tailscale cert`, which issues a
  publicly-trusted certificate for a `*.ts.net` name and renews it.

When something else terminates TLS, set `--public-url` to the externally
reachable base (`https://marshal.example.dev`). URLs handed to MCP clients
are built from it, so without it they will point at the internal listen
address.

> **Note on `.dev` domains:** the entire `.dev` TLD is on the HSTS preload
> list, so browsers refuse plaintext HTTP to a `.dev` hostname outright.
> Behind a terminating proxy this never comes up; hitting the bridge
> directly over HTTP on such a name will simply fail to load.
