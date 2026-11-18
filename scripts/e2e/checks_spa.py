"""Group I — the Studio's shell and the token contract.

The bridge embeds the Studio's web build into its own binary
(`web/bridge/assets.go`, `//go:embed static`). Since F8's cutover that build is
the Flutter Studio's; the Svelte one, and the checks that grepped its bundle,
went with it.

What is still worth pinning at the boundary a browser actually gets:

- the document at `/` is the Studio's index, never cached, and a client-side
  route falls back to it rather than 404ing;
- a real build's bootstrap is served by this bridge, and the rendering engine
  is not fetched from a CDN — the one defect that left the Studio blank on a
  private network that cannot reach gstatic;
- `/api` belongs to the mux: an unknown path is a JSON 404 and never the shell,
  every route demands a bearer, and `/mcp` authenticates per client.

A Go-only checkout embeds a placeholder index instead of a build, so the checks
that need the real bootstrap skip there rather than passing vacuously. The
table in `docs/studio-flutter/f8-cutover.md` accounts for each retired Svelte
check.
"""

from __future__ import annotations

import re

from harness import Skip, Suite

# A real Studio document names its bootstrap; the placeholder names none.
_BOOTSTRAP = re.compile(r'<script[^>]+src="([^"]*flutter_bootstrap\.js)"')

# The engine is fetched at run time. With `--no-web-resources-cdn` the build
# carries it itself; without that flag it points at gstatic, and a network that
# cannot reach the CDN never starts the Studio.
_CDN = re.compile(r"gstatic\.com|fonts\.gstatic|cdn\.jsdelivr")


def _bootstrap_path(html: str) -> str | None:
    """The bootstrap the document names, as a same-origin path."""
    m = _BOOTSTRAP.search(html)
    if not m:
        return None
    src = m.group(1)
    return src if src.startswith("/") else "/" + src


def register(s: Suite) -> None:
    s.area("I studio shell and auth")

    def studio_shell_is_served(c) -> None:
        """The root document is the Studio's index, not the retired one."""
        r = c.client.get("/", token="")
        s.expect_status(r, 200, request="GET /")
        s.expect(
            "<html" in r.raw.lower(),
            "root path is not an HTML document",
            request="GET /",
            response=r,
        )
        s.expect(
            "Marshal Studio" in r.raw,
            "the root document is not the Studio's index",
            request="GET /",
            response=r.raw[:200],
        )
        s.expect(
            "Marshal Web UI" not in r.raw,
            "the Svelte Studio is still served at the root; F8 removed it",
            request="GET /",
            response=r.raw[:200],
        )
        c.set("studio_index", r.raw)

    def bootstrap_is_served(c) -> None:
        """A real build's bootstrap must resolve from this bridge."""
        path = _bootstrap_path(c.get("studio_index") or "")
        if path is None:
            raise Skip("the embedded tree is the placeholder, not a Studio build")
        r = c.client.get(path, token="")
        s.expect_status(r, 200, request=f"GET {path}")
        s.expect(
            len(r.raw) > 0,
            f"the bootstrap {path} is empty",
            request=f"GET {path}",
        )

    def engine_is_not_fetched_from_a_cdn(c) -> None:
        """The engine must come from this bridge, or a private network fails.

        A build made without `--no-web-resources-cdn` fetches CanvasKit from
        gstatic at run time. On a network that cannot reach it the Studio never
        starts at all, which is invisible on a development host with a route to
        the internet.
        """
        path = _bootstrap_path(c.get("studio_index") or "")
        if path is None:
            raise Skip("the embedded tree is the placeholder, not a Studio build")
        r = c.client.get(path, token="")
        if r.status != 200:
            raise Skip(f"the bootstrap {path} is not served (HTTP {r.status})")
        found = _CDN.search(r.raw)
        s.expect(
            found is None,
            f"the served Studio fetches {found.group(0) if found else ''} from a "
            "CDN; build it with --no-web-resources-cdn",
            request=f"GET {path}",
            response=found.group(0) if found else "",
        )

    def index_is_not_cached(c) -> None:
        """The shell must never be cached, or a rebuilt Studio cannot reach a browser."""
        r = c.client.get("/", token="")
        s.expect_status(r, 200, request="GET /")
        cc = r.header("Cache-Control")
        s.expect(
            "no-store" in cc or "no-cache" in cc,
            f"index.html is cacheable ({cc!r}); a rebuild would not reach the browser",
            request="GET /",
            response=cc,
        )

    def client_routing_falls_back(c) -> None:
        """An unknown path is a client route: the shell, not a 404."""
        r = c.client.get("/workspaces/some-name/edit", token="")
        s.expect_status(r, 200, request="GET /workspaces/some-name/edit")
        s.expect(
            "<html" in r.raw.lower(),
            "a client-side route did not fall back to the shell",
            request="GET /workspaces/some-name/edit",
            response=r.raw[:200],
        )

    def api_paths_do_not_fall_back(c) -> None:
        """An unknown /api path must 404 as JSON, never return the shell."""
        r = c.client.get("/api/definitely-not-a-route")
        s.expect_status(r, 404, request="GET /api/definitely-not-a-route")
        s.expect(
            "<html" not in r.raw.lower(),
            "an unknown /api path returned the Studio shell instead of a JSON 404",
            request="GET /api/definitely-not-a-route",
            response=r.raw[:200],
        )

    def api_requires_a_bearer(c) -> None:
        """Every /api route is guarded; the shell is not."""
        for path in ("/api/config", "/api/agents", "/api/sessions", "/api/models"):
            r = c.client.get(path, token="")
            s.expect_status(r, 401, request=f"GET {path} (no token)")
            s.expect(
                "Bearer" in r.header("WWW-Authenticate"),
                f"{path} rejects without the RFC 6750 challenge header",
                request=f"GET {path}",
                response=r.headers,
            )

    def bearer_prefix_is_not_doubled(c) -> None:
        """The header the Studio sends must be accepted, verbatim.

        This is the regression guard for the re-prompt loop: the prompt
        stored whatever was pasted, so an entry of "Bearer x" produced
        "Authorization: Bearer Bearer x", bearerAuth compared it
        byte-for-byte, every route 401'd, and the client cleared the token
        and re-prompted forever. The bridge must keep accepting the bare
        form the client normalizes to.
        """
        r = c.client.get("/api/config", token=c.env.token)
        s.expect_status(r, 200, request="GET /api/config with the configured token")
        # A doubled scheme is a different byte string and must be rejected —
        # that is exactly why the client had to normalize.
        doubled = f"Bearer {c.env.token}"
        r2 = c.client.get("/api/config", token=doubled)
        s.expect_status(r2, 401, request=f"GET /api/config with token {doubled!r}")

    def mcp_is_guarded_separately(c) -> None:
        """/mcp authenticates per client and must not accept the shared token."""
        r = c.client.post("/mcp", {"jsonrpc": "2.0", "id": 1, "method": "initialize"})
        s.expect_status(r, 401, request="POST /mcp with the shared bearer token")

    def previews_are_not_on_this_origin(c) -> None:
        """Previews are served from their own origin, never the Studio's."""
        r = c.client.get("/preview/anything", token="")
        s.expect_status(
            r,
            (404, 401, 403),
            request="GET /preview/anything",
        )

    s.check("the root document is the Studio's index", studio_shell_is_served)
    s.check("the Studio's bootstrap resolves", bootstrap_is_served)
    s.check("the Studio does not fetch its engine from a CDN", engine_is_not_fetched_from_a_cdn)
    s.check("index.html is never cached", index_is_not_cached)
    s.check("client-side routes fall back to the shell", client_routing_falls_back)
    s.check("unknown /api paths 404 as JSON, not the shell", api_paths_do_not_fall_back)
    s.check("every /api route demands a bearer", api_requires_a_bearer)
    s.check("the bearer prefix is not doubled by the client", bearer_prefix_is_not_doubled)
    s.check("/mcp authenticates per client, not with the shared token", mcp_is_guarded_separately)
    s.check("previews are not served from the Studio origin", previews_are_not_on_this_origin)
