"""Group C: terminal and preview.

Opens a real terminal on a containerized agent, drives it, and checks
the preview port policy. The agent is spawned in a workspace that
declares a preview port, because previews are refused otherwise.
"""

from __future__ import annotations

import base64
import json
import socket
import urllib.error
import urllib.parse
import urllib.request

from e2e_client import q
from harness import Ctx, Failure, Skip, Suite, wait_until


def _b64(text: str) -> str:
    return base64.b64encode(text.encode()).decode()


def register(s: Suite) -> None:
    s.area("C terminal")

    def spawn(c: Ctx) -> None:
        r = c.client.post(
            "/api/agents",
            {
                "project": c.env.project,
                "name": "e2e terminal",
                "mode": "edit",
                "workspace": c.env.workspace,
            },
        )
        body = s.expect_ok(r, request="POST /api/agents (workspace)")
        s.expect_keys(body, ["agentId"], request="POST /api/agents")
        c.set("term_agent", body["agentId"])

    def open_terminal(c: Ctx) -> None:
        aid = c.get("term_agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.post(f"/api/agents/{q(aid)}/terminal", {"cols": 80, "rows": 24})
        body = s.expect_ok(r, request=f"POST /api/agents/{aid}/terminal")
        s.expect_keys(body, ["terminalId"], request="terminal open")
        c.set("terminal", body["terminalId"])

    def input(c: Ctx) -> None:
        aid, tid = c.get("term_agent"), c.get("terminal")
        if not aid or not tid:
            raise Skip("no terminal open")
        r = c.client.post(
            f"/api/agents/{q(aid)}/terminal/{q(tid)}/input",
            {"data": _b64("echo E2E_TERM_OK\n")},
        )
        s.expect_status(r, 204, request=f"POST /api/agents/{aid}/terminal/{tid}/input")

    def resize(c: Ctx) -> None:
        aid, tid = c.get("term_agent"), c.get("terminal")
        if not aid or not tid:
            raise Skip("no terminal open")
        r = c.client.post(
            f"/api/agents/{q(aid)}/terminal/{q(tid)}/resize", {"cols": 100, "rows": 30}
        )
        s.expect_status(r, 204, request=f"POST /api/agents/{aid}/terminal/{tid}/resize")

    def input_rejects_bad_base64(c: Ctx) -> None:
        aid, tid = c.get("term_agent"), c.get("terminal")
        if not aid or not tid:
            raise Skip("no terminal open")
        r = c.client.post(
            f"/api/agents/{q(aid)}/terminal/{q(tid)}/input", {"data": "not base64!!"}
        )
        s.expect_status(
            r, 400, request=f"POST /api/agents/{aid}/terminal/{tid}/input (bad base64)"
        )

    def input_rejects_oversize(c: Ctx) -> None:
        aid, tid = c.get("term_agent"), c.get("terminal")
        if not aid or not tid:
            raise Skip("no terminal open")
        # 1 MiB of base64 is well past the 64 KiB terminal input cap.
        r = c.client.post(
            f"/api/agents/{q(aid)}/terminal/{q(tid)}/input",
            {"data": _b64("x" * (1 << 20))},
        )
        s.expect(
            r.status in (400, 413),
            f"oversize terminal input returned {r.status}, want 400/413",
            request=f"POST /api/agents/{aid}/terminal/{tid}/input (1MiB)",
            response=r,
        )

    def events_route_exists(c: Ctx) -> None:
        aid, tid = c.get("term_agent"), c.get("terminal")
        if not aid or not tid:
            raise Skip("no terminal open")
        # A bad terminal id must be a clean 404, not a hang or a 5xx.
        r = c.client.get(f"/api/agents/{q(aid)}/terminal/nope/events", timeout=5)
        s.expect_status(
            r, 404, request=f"GET /api/agents/{aid}/terminal/nope/events"
        )

    def events_stream(c: Ctx) -> None:
        aid, tid = c.get("term_agent"), c.get("terminal")
        if not aid or not tid:
            raise Skip("no terminal open")
        url = f"{c.env.base}/api/agents/{q(aid)}/terminal/{q(tid)}/events"
        req = urllib.request.Request(url, headers={"Authorization": f"Bearer {c.env.token}"})
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                s.expect(
                    resp.status == 200,
                    f"terminal events returned {resp.status}",
                    request=f"GET {url}",
                )
                ctype = resp.headers.get("Content-Type", "")
                s.expect(
                    "text/event-stream" in ctype,
                    f"terminal events content-type is {ctype!r}",
                    request=f"GET {url}",
                )
                resp.read(64)
        except socket.timeout:
            # A quiet terminal legitimately sends nothing; the stream is open.
            return
        except urllib.error.HTTPError as exc:
            raise Failure(
                f"terminal events returned {exc.code}",
                request=f"GET {url}",
                response=exc.read().decode("utf-8", "replace")[:300],
            ) from exc

    def release(c: Ctx) -> None:
        aid, tid = c.get("term_agent"), c.get("terminal")
        if not aid or not tid:
            raise Skip("no terminal open")
        r = c.client.post(f"/api/agents/{q(aid)}/terminal/{q(tid)}/release", {})
        s.expect_status(r, 204, request=f"POST /api/agents/{aid}/terminal/{tid}/release")

    def close(c: Ctx) -> None:
        aid, tid = c.get("term_agent"), c.get("terminal")
        if not aid or not tid:
            raise Skip("no terminal open")
        r = c.client.delete(f"/api/agents/{q(aid)}/terminal/{q(tid)}")
        s.expect_status(r, 204, request=f"DELETE /api/agents/{aid}/terminal/{tid}")

    def unknown_terminal(c: Ctx) -> None:
        aid = c.get("term_agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.delete(f"/api/agents/{q(aid)}/terminal/nope")
        s.expect_status(r, 404, request=f"DELETE /api/agents/{aid}/terminal/nope")

    def preview_declared_port(c: Ctx) -> None:
        aid = c.get("term_agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.post(f"/api/agents/{q(aid)}/preview/3000", {})
        body = s.expect_ok(r, request=f"POST /api/agents/{aid}/preview/3000")
        s.expect_keys(body, ["url"], request="preview")
        url = body["url"]
        s.expect(
            "/preview/" in url,
            f"preview url {url!r} is not on the preview origin",
            response=body,
        )
        c.set("preview_url", url)

    def preview_undeclared_port(c: Ctx) -> None:
        aid = c.get("term_agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.post(f"/api/agents/{q(aid)}/preview/9999", {})
        s.expect_status(r, 403, request=f"POST /api/agents/{aid}/preview/9999")

    def preview_invalid_port(c: Ctx) -> None:
        aid = c.get("term_agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.post(f"/api/agents/{q(aid)}/preview/0", {})
        s.expect_status(r, 400, request=f"POST /api/agents/{aid}/preview/0")

    def preview_unknown_agent(c: Ctx) -> None:
        r = c.client.post("/api/agents/nope/preview/3000", {})
        s.expect(
            r.status in (403, 404, 410),
            f"preview on an unknown agent returned {r.status}",
            request="POST /api/agents/nope/preview/3000",
            response=r,
        )

    def preview_origin_serves(c: Ctx) -> None:
        """The issued URL must be reachable on the preview listener.

        The first request carries ?t= and is answered with a 302 that
        sets the preview cookie; the token is then dropped from the URL.
        urllib follows the redirect but does not carry the cookie, so the
        follow-up 404s. Drive the exchange by hand: take the redirect,
        keep the cookie, and re-request with it.

        Nothing is listening on the agent's port, so the proxy answers
        502 — but it must answer, not 404 or refuse the connection.
        """
        url = c.get("preview_url")
        if not url:
            raise Skip("no preview issued")

        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, req, fp, code, msg, headers, newurl):
                return None

        opener = urllib.request.build_opener(NoRedirect)
        try:
            opener.open(url, timeout=8)
            raise Failure(
                "the preview URL with ?t= did not redirect",
                request=f"GET {url}",
            )
        except urllib.error.HTTPError as exc:
            if exc.code not in (301, 302, 303, 307, 308):
                raise Failure(
                    f"the preview URL with ?t= returned {exc.code}, want a redirect",
                    request=f"GET {url}",
                    response=exc.read().decode("utf-8", "replace")[:200],
                ) from exc
            location = exc.headers.get("Location", "")
            cookie = exc.headers.get("Set-Cookie", "")
            s.expect(
                bool(cookie),
                "the preview redirect set no cookie",
                request=f"GET {url}",
                response=dict(exc.headers),
            )
            s.expect(
                "t=" not in location,
                f"the redirect kept the token in the URL: {location!r}",
                request=f"GET {url}",
            )

        # Re-request the redirect target with the cookie.
        target = urllib.parse.urljoin(url, location)
        req = urllib.request.Request(target, headers={"Cookie": cookie.split(";")[0]})
        try:
            with urllib.request.urlopen(req, timeout=8) as resp:
                s.expect(
                    resp.status < 500,
                    f"preview origin returned {resp.status}",
                    request=f"GET {target}",
                )
        except urllib.error.HTTPError as exc:
            s.expect(
                exc.code in (502, 503, 504),
                f"preview origin returned {exc.code}, want a proxy error",
                request=f"GET {target}",
                response=exc.read().decode("utf-8", "replace")[:200],
            )
        except urllib.error.URLError as exc:
            raise Failure(
                f"preview origin is unreachable: {exc}",
                request=f"GET {target}",
            ) from exc

    def cleanup(c: Ctx) -> None:
        aid = c.get("term_agent")
        if not aid:
            raise Skip("no agent spawned")
        c.client.post(f"/api/agents/{q(aid)}/discard", {}, timeout=120)

    for name, fn in [
        ("spawn agent in a preview workspace", spawn),
        ("open terminal", open_terminal),
        ("terminal input", input),
        ("terminal resize", resize),
        ("terminal input rejects bad base64", input_rejects_bad_base64),
        ("terminal input rejects oversize", input_rejects_oversize),
        ("terminal events rejects an unknown id", events_route_exists),
        ("terminal events streams", events_stream),
        ("terminal release", release),
        ("terminal close", close),
        ("unknown terminal is 404", unknown_terminal),
        ("preview on a declared port", preview_declared_port),
        ("preview on an undeclared port is 403", preview_undeclared_port),
        ("preview on an invalid port is 400", preview_invalid_port),
        ("preview on an unknown agent", preview_unknown_agent),
        ("preview origin serves", preview_origin_serves),
        ("discard the terminal agent", cleanup),
    ]:
        s.check(name, fn)
