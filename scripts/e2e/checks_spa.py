"""Group I — the SPA shell and the token contract.

The bridge embeds the built SPA into its own binary (`web/bridge/assets.go`,
`//go:embed static`). That makes the served UI a build-time snapshot: a
change under `web/ui/src` is invisible until `npm run build` reruns AND the
binary is rebuilt. Nothing in the HTTP suite exercised the shell, so a
binary serving a stale bundle — or a bundle whose token handling disagrees
with `bearerAuth` — reported a perfect run.

These checks pin the contract at the boundary actually served to a browser:
the document, the assets it references, and the exact Authorization header
the client would send.
"""

from __future__ import annotations

import re

from harness import Skip, Suite

# Minified, the client's `/^authorization\s*:\s*/i` strip survives verbatim,
# so its presence in a served bundle is evidence the paste normalization is
# in the build the browser will run — not just in the working tree.
NORMALIZE_MARKER = r"authorization\s*:\s*"

# The rejected-token marker. A refused token used to call clearToken(), which
# forgot the refusal and re-armed the prompt, so each request re-asked for the
# value the bridge had just rejected — the loop. The client now records the
# refusal and fails fast; this string is its observable trace.
REJECTED_MARKER = "Token rejected"

_SCRIPT_SRC = re.compile(r'<script[^>]+src="([^"]+)"')
_LINK_HREF = re.compile(r'<link[^>]+href="([^"]+)"')


def _asset_paths(html: str) -> list[str]:
    """Every same-origin asset the document pulls in."""
    out: list[str] = []
    out.extend(m for m in _SCRIPT_SRC.findall(html))
    out.extend(h for h in _LINK_HREF.findall(html) if h.endswith(".css"))
    return [p for p in out if p.startswith("/")]


def register(s: Suite) -> None:
    s.area("I spa and auth")

    def shell_is_served(c) -> None:
        """The root document comes back HTML and references real assets."""
        r = c.client.get("/", token="")
        s.expect_status(r, 200, request="GET /")
        s.expect(
            "<html" in r.raw.lower(),
            "root path is not an HTML document",
            request="GET /",
            response=r,
        )
        assets = _asset_paths(r.raw)
        s.expect(
            any(a.endswith(".js") for a in assets),
            "index.html references no script bundle",
            request="GET /",
            response=assets,
        )
        c.set("spa_assets", assets)
        c.set("spa_index", r.raw)

    def assets_are_served(c) -> None:
        """Every asset the document names must actually resolve."""
        assets = c.get("spa_assets")
        if not assets:
            raise Skip("index.html referenced no assets")
        for path in assets:
            r = c.client.get(path, token="")
            s.expect(
                r.status == 200,
                f"asset {path} referenced by index.html is not served (HTTP {r.status})",
                request=f"GET {path}",
                response=r,
            )
            s.expect(
                len(r.raw) > 0,
                f"asset {path} is empty",
                request=f"GET {path}",
            )

    def index_is_not_cached(c) -> None:
        """The shell must never be cached, or a rebuilt UI cannot reach a browser."""
        r = c.client.get("/", token="")
        s.expect_status(r, 200, request="GET /")
        cc = r.header("Cache-Control")
        s.expect(
            "no-store" in cc or "no-cache" in cc,
            f"index.html is cacheable ({cc!r}); a rebuild would not reach the browser",
            request="GET /",
            response=cc,
        )

    def hashed_assets_are_immutable(c) -> None:
        """A content-hashed asset is safe to cache forever — and only then."""
        assets = [a for a in (c.get("spa_assets") or []) if re.search(r"-[A-Za-z0-9_]{8,}\.", a)]
        if not assets:
            raise Skip("no content-hashed assets in the shell")
        r = c.client.get(assets[0], token="")
        s.expect_status(r, 200, request=f"GET {assets[0]}")
        cc = r.header("Cache-Control")
        s.expect(
            "immutable" in cc or "max-age" in cc,
            f"hashed asset {assets[0]} is not cacheable ({cc!r})",
            request=f"GET {assets[0]}",
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
            "an unknown /api path returned the SPA shell instead of a JSON 404",
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
        """The header the SPA sends must be accepted, verbatim.

        This is the regression guard for the re-prompt loop: the prompt
        stored whatever was pasted, so an entry of "Bearer x" produced
        "Authorization: Bearer Bearer x", bearerAuth compared it
        byte-for-byte, every route 401'd, and the client cleared the token
        and re-prompted forever. The bridge must keep accepting the bare
        form the SPA now normalizes to.
        """
        r = c.client.get("/api/config", token=c.env.token)
        s.expect_status(r, 200, request="GET /api/config with the configured token")
        # A doubled scheme is a different byte string and must be rejected —
        # that is exactly why the client had to normalize.
        doubled = f"Bearer {c.env.token}"
        r2 = c.client.get("/api/config", token=doubled)
        s.expect_status(r2, 401, request=f"GET /api/config with token {doubled!r}")

    def bundle_normalizes_a_pasted_token(c) -> None:
        """The served bundle must contain the paste-normalizing rule.

        The shell is a build-time snapshot embedded in the binary, so a
        binary built before the fix serves a bundle that still stores the
        pasted string verbatim: the browser keeps sending
        `Bearer Bearer <token>`, every route 401s, and the prompt returns
        forever. Reading the served JS is the only way to see which
        behaviour a browser would actually get.
        """
        assets = [a for a in (c.get("spa_assets") or []) if a.endswith(".js")]
        if not assets:
            raise Skip("the shell references no script bundle")
        found = None
        for path in assets:
            r = c.client.get(path, token="")
            if r.status == 200 and NORMALIZE_MARKER in r.raw:
                found = path
                break
        s.expect(
            found is not None,
            "the served bundle carries no token-normalizing rule; a binary "
            "built before the fix serves the paste-loop behaviour",
            request=f"GET {assets[0]}",
        )
        if found:
            r = c.client.get(found, token="")
            s.expect(
                "Token is required" in r.raw,
                "the served bundle is missing the prompt-decline guard",
                request=f"GET {found}",
            )

    def prompt_decline_is_bounded(c) -> None:
        """A declined prompt must be remembered, not re-shown per request.

        The marker is the guard the client sets on a decline; without it the
        SPA opens one dialog per in-flight request.
        """
        assets = [a for a in (c.get("spa_assets") or []) if a.endswith(".js")]
        if not assets:
            raise Skip("the shell references no script bundle")
        r = c.client.get(assets[0], token="")
        s.expect_status(r, 200, request=f"GET {assets[0]}")
        s.expect(
            "Token is required" in r.raw,
            "the served bundle does not carry the decline guard",
            request=f"GET {assets[0]}",
        )

    def bundle_stops_on_a_rejected_token(c) -> None:
        """A refused token must stop the client, not be re-prompted for.

        A 401 means the value itself is wrong — stale, revoked, or issued by
        a different bridge (the reported symptom: a browser pointed at
        another instance whose token differs). Clearing it and prompting
        again re-sends the same refused value, so the refusal must be
        recorded and the client must stop until the user supplies a
        different token. `clearToken` alone cannot do this, so the served
        bundle must carry the distinct refusal state and its fail-fast error.
        """
        assets = [a for a in (c.get("spa_assets") or []) if a.endswith(".js")]
        if not assets:
            raise Skip("the shell references no script bundle")
        found = None
        for path in assets:
            r = c.client.get(path, token="")
            if r.status == 200 and REJECTED_MARKER in r.raw:
                found = path
                break
        s.expect(
            found is not None,
            "the served bundle carries no rejected-token state; a binary built "
            "before the fix serves the re-prompt loop",
            request=f"GET {assets[0]}",
        )
        if not found:
            return
        r = c.client.get(found, token="")
        # The recovery affordance must ship with the refusal, or a wrong
        # token leaves the UI with no way back and no explanation.
        s.expect(
            "Enter a different token" in r.raw,
            "the served bundle offers no way to re-enter a rejected token",
            request=f"GET {found}",
        )

    def mcp_is_guarded_separately(c) -> None:
        """/mcp authenticates per client and must not accept the shared token."""
        r = c.client.post("/mcp", {"jsonrpc": "2.0", "id": 1, "method": "initialize"})
        s.expect_status(r, 401, request="POST /mcp with the shared bearer token")

    def previews_are_not_on_this_origin(c) -> None:
        """Previews are served from their own origin, never the UI's."""
        r = c.client.get("/preview/anything", token="")
        s.expect_status(
            r,
            (404, 401, 403),
            request="GET /preview/anything",
        )

    s.check("the shell is served and names its assets", shell_is_served)
    s.check("every asset the shell names resolves", assets_are_served)
    s.check("index.html is never cached", index_is_not_cached)
    s.check("content-hashed assets are immutable", hashed_assets_are_immutable)
    s.check("client-side routes fall back to the shell", client_routing_falls_back)
    s.check("unknown /api paths 404 as JSON, not the shell", api_paths_do_not_fall_back)
    s.check("every /api route demands a bearer", api_requires_a_bearer)
    s.check("the bearer prefix is not doubled by the client", bearer_prefix_is_not_doubled)
    s.check("the served bundle normalizes a pasted token", bundle_normalizes_a_pasted_token)
    s.check("the served bundle bounds prompt declines", prompt_decline_is_bounded)
    s.check("the served bundle stops on a rejected token", bundle_stops_on_a_rejected_token)
    s.check("/mcp authenticates per client, not with the shared token", mcp_is_guarded_separately)
    s.check("previews are not served from the UI origin", previews_are_not_on_this_origin)
