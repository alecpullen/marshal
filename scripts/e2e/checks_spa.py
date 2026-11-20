"""Group I — the Studio's shell and the API's boundaries.

The daemon serves the Studio's web build from its Studio directory
(`share/marshal/studio`, found beside or above its binary), naming it in
`GET /api/daemon`'s `studioDir`. Since F8's cutover that build is the Flutter
Studio's; the Svelte one, and the checks that grepped its bundle, went with it.

What is still worth pinning at the boundary a browser actually gets:

- the daemon names the directory its Studio comes from;
- the document at `/` is the Studio's index, never cached, and a client-side
  route falls back to it rather than 404ing;
- a real build's bootstrap is served by this daemon, and the rendering engine
  is not fetched from a CDN — the one defect that left the Studio blank on a
  private network that cannot reach gstatic;
- `/api` belongs to the mux: an unknown path is a JSON 404 and never the shell,
  and `/mcp` keeps its own per-client authentication.

The web listener serves /api with no bearer, so nothing here sends one.
A daemon with no Studio files answers 503 with a page naming where it looked,
so the checks that need the real bootstrap skip there rather than passing
vacuously. The table in `docs/studio-flutter/f8-cutover.md` accounts for each
retired Svelte check.
"""

from __future__ import annotations

import re

from harness import Skip, Suite

# A real Studio document names its bootstrap; the no-files page names none.
_BOOTSTRAP = re.compile(r'<script[^>]+src="([^"]*flutter_bootstrap\.js)"')

# The engine is fetched at run time. With `--no-web-resources-cdn` the build
# ships it itself — canvaskit/canvaskit.wasm beside the shell, and a bootstrap
# whose build config sets this flag; without that flag the bootstrap leaves the
# flag false and the engine comes from gstatic, so a network that cannot reach
# the CDN never starts the Studio.
#
# A hostname search cannot ask this question: a *correct* build still spells
# gstatic out in the bootstrap's dead branch
# (`useLocalCanvasKit?E("https://www.gstatic.com/flutter-canvaskit",…)`), in
# flutter.js.map's copied source, and as the engine's default font base. The
# flag is what separates the two builds, and only the flag: Flutter 3.50 copies
# canvaskit/ into a default build as well, so the wasm's presence alone says
# nothing about where the engine is fetched from. Both are still checked here —
# the flag for the CDN, the bytes for a serve that silently fell back to
# index.html.
_LOCAL_CANVASKIT = '"useLocalCanvasKit":true'

# The engine's entry file, as the bootstrap names it for a build that carries
# its own: a path relative to the document, so a same-origin path here.
_ENGINE = "/canvaskit/canvaskit.wasm"

# A wasm module starts with this: `\0asm` and the format version. Checking it
# keeps the check from passing vacuously — a static handler that cannot find a
# file falls back to index.html with a 200, which is HTML, not the engine.
_WASM_MAGIC = "\0asm"


def _bootstrap_path(html: str) -> str | None:
    """The bootstrap the document names, as a same-origin path."""
    m = _BOOTSTRAP.search(html)
    if not m:
        return None
    src = m.group(1)
    return src if src.startswith("/") else "/" + src


def register(s: Suite) -> None:
    s.area("I studio shell")

    def studio_dir_is_named(c) -> None:
        """The daemon says which directory it serves the Studio from."""
        r = c.client.get("/api/daemon")
        body = s.expect_ok(r, request="GET /api/daemon")
        if not isinstance(body, dict):
            s.expect(False, "GET /api/daemon did not answer a JSON object", response=body)
        if "studioDir" not in body:
            raise Skip("this daemon does not report studioDir; it predates serving the Studio from a directory")
        s.expect(
            bool(str(body.get("studioDir") or "").strip()),
            "the daemon serves no Studio files: studioDir is empty, so GET / "
            "answers the no-files page",
            request="GET /api/daemon",
            response=body,
        )

    def studio_shell_is_served(c) -> None:
        """The root document is the Studio's index, not the retired one."""
        r = c.client.get("/")
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
        """A real build's bootstrap must resolve from this daemon."""
        path = _bootstrap_path(c.get("studio_index") or "")
        if path is None:
            raise Skip("the daemon serves no Studio build, so there is no bootstrap")
        r = c.client.get(path)
        s.expect_status(r, 200, request=f"GET {path}")
        s.expect(
            len(r.raw) > 0,
            f"the bootstrap {path} is empty",
            request=f"GET {path}",
        )

    def engine_is_not_fetched_from_a_cdn(c) -> None:
        """The engine must come from this bridge, or a private network fails.

        A build made without `--no-web-resources-cdn` leaves the local-engine
        flag false and fetches CanvasKit from gstatic at run time. On a network
        that cannot reach it the Studio never starts at all, which is invisible
        on a development host with a route to the internet. Two facts tell the
        two builds apart, and both must hold: the served bootstrap says the
        engine is local, and the engine itself is a file this daemon serves.
        """
        path = _bootstrap_path(c.get("studio_index") or "")
        if path is None:
            raise Skip("the daemon serves no Studio build, so there is no bootstrap")
        r = c.client.get(path)
        if r.status != 200:
            raise Skip(f"the bootstrap {path} is not served (HTTP {r.status})")
        s.expect(
            _LOCAL_CANVASKIT in r.raw,
            f"the served Studio does not name its own CanvasKit engine: {path} "
            f"carries no {_LOCAL_CANVASKIT}, so the engine is fetched from a "
            "CDN; build it with --no-web-resources-cdn",
            request=f"GET {path}",
            response=next(
                (line for line in r.raw.splitlines() if "CanvasKit" in line),
                r.raw[:200],
            ),
        )
        # The engine's own first bytes, not just its status: the static handler
        # falls back to index.html with a 200 for a path it cannot find, so a
        # status alone would pass vacuously on a build that ships no engine.
        engine = c.client.get(_ENGINE, headers={"Range": "bytes=0-7"})
        s.expect(
            engine.status in (200, 206) and engine.raw.startswith(_WASM_MAGIC),
            f"the served Studio has no local CanvasKit engine: GET {_ENGINE} "
            f"answered HTTP {engine.status} with {engine.raw[:8]!r}, so the "
            "engine is fetched from a CDN; build it with --no-web-resources-cdn",
            request=f"GET {_ENGINE}",
            response=engine.raw[:200],
        )

    def index_is_not_cached(c) -> None:
        """The shell must never be cached, or a rebuilt Studio cannot reach a browser."""
        r = c.client.get("/")
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
        r = c.client.get("/workspaces/some-name/edit")
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

    def mcp_is_guarded_separately(c) -> None:
        """/mcp authenticates per client, on its own credentials."""
        r = c.client.post("/mcp", {"jsonrpc": "2.0", "id": 1, "method": "initialize"})
        s.expect_status(r, 401, request="POST /mcp without a client credential")

    def previews_are_not_on_this_origin(c) -> None:
        """Previews are served from their own origin, never the Studio's."""
        r = c.client.get("/preview/anything")
        s.expect_status(
            r,
            (404, 401, 403),
            request="GET /preview/anything",
        )

    s.check("the daemon names its Studio directory", studio_dir_is_named)
    s.check("the root document is the Studio's index", studio_shell_is_served)
    s.check("the Studio's bootstrap resolves", bootstrap_is_served)
    s.check("the Studio does not fetch its engine from a CDN", engine_is_not_fetched_from_a_cdn)
    s.check("index.html is never cached", index_is_not_cached)
    s.check("client-side routes fall back to the shell", client_routing_falls_back)
    s.check("unknown /api paths 404 as JSON, not the shell", api_paths_do_not_fall_back)
    s.check("/mcp authenticates per client, on its own credentials", mcp_is_guarded_separately)
    s.check("previews are not served from the Studio origin", previews_are_not_on_this_origin)
