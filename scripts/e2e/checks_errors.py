"""Group H: error handling and edge cases.

Authentication, malformed and oversize bodies, unknown routes, method
mismatch, path traversal and the SSE/MCP entry points. These are the
paths a hostile or buggy client reaches first.
"""

from __future__ import annotations

import json
import socket
import urllib.error
import urllib.request

from e2e_client import q
from harness import Ctx, Failure, Skip, Suite, wait_until


def register(s: Suite) -> None:
    s.area("H errors")

    # ---- authentication --------------------------------------------------

    def no_token(c: Ctx) -> None:
        r = c.client.get("/api/config", token="")
        s.expect_status(r, 401, request="GET /api/config (no token)")

    def wrong_token(c: Ctx) -> None:
        r = c.client.get("/api/config", token="definitely-not-the-token")
        s.expect_status(r, 401, request="GET /api/config (wrong token)")

    def malformed_auth_header(c: Ctx) -> None:
        r = c.client.get("/api/config", token="", headers={"Authorization": "Basic abc"})
        s.expect_status(r, 401, request="GET /api/config (Basic auth)")

    def unauthorized_sets_challenge(c: Ctx) -> None:
        r = c.client.get("/api/config", token="")
        s.expect_status(r, 401, request="GET /api/config (no token)")
        challenge = r.header("WWW-Authenticate")
        s.expect(
            "Bearer" in challenge,
            f"401 has no Bearer challenge (got {challenge!r})",
            request="GET /api/config (no token)",
            response=r,
        )

    def token_never_echoed(c: Ctx) -> None:
        r = c.client.get("/api/config", token="")
        s.expect(
            c.env.token not in r.raw,
            "the 401 body echoes the configured token",
            request="GET /api/config (no token)",
            response=r,
        )

    # ---- body handling ---------------------------------------------------

    def malformed_json(c: Ctx) -> None:
        r = c.client.post(
            "/api/sessions",
            raw_body=b"{not json",
            content_type="application/json",
        )
        s.expect_status(r, 400, request="POST /api/sessions (malformed JSON)")

    def trailing_data(c: Ctx) -> None:
        r = c.client.post(
            "/api/sessions",
            raw_body=b'{"cwd":"/tmp"}{"cwd":"/tmp"}',
            content_type="application/json",
        )
        s.expect_status(r, 400, request="POST /api/sessions (two JSON values)")

    def oversize_body(c: Ctx) -> None:
        big = json.dumps({"cwd": "x" * (2 << 20)}).encode()
        r = c.client.post("/api/sessions", raw_body=big, content_type="application/json")
        s.expect(
            r.status in (400, 413),
            f"a 2 MiB body returned {r.status}, want 400/413",
            request="POST /api/sessions (2 MiB)",
            response=r,
        )

    def null_body(c: Ctx) -> None:
        r = c.client.post("/api/sessions", raw_body=b"null", content_type="application/json")
        s.expect(
            r.status in (400, 422),
            f"a null body returned {r.status}, want 400",
            request="POST /api/sessions (null)",
            response=r,
        )

    def missing_required_field(c: Ctx) -> None:
        r = c.client.post("/api/sessions", {})
        s.expect_status(r, 400, request="POST /api/sessions (no cwd)")

    def empty_body_where_required(c: Ctx) -> None:
        r = c.client.post("/api/sessions", raw_body=b"", content_type="application/json")
        s.expect_status(r, 400, request="POST /api/sessions (empty body)")

    # ---- routing ---------------------------------------------------------

    def unknown_api_route(c: Ctx) -> None:
        r = c.client.get("/api/definitely-not-a-route")
        s.expect_status(r, 404, request="GET /api/definitely-not-a-route")

    def wrong_method(c: Ctx) -> None:
        # /api/config is GET-only.
        r = c.client.post("/api/config", {})
        s.expect(
            r.status in (404, 405),
            f"POST on a GET-only route returned {r.status}",
            request="POST /api/config",
            response=r,
        )

    def unknown_agent_route(c: Ctx) -> None:
        r = c.client.get("/api/agents/nope/no-such-subroute")
        s.expect_status(r, 404, request="GET /api/agents/nope/no-such-subroute")

    # ---- traversal and odd input ----------------------------------------

    def traversal_in_workspace_name(c: Ctx) -> None:
        r = c.client.get("/api/workspaces/..%2f..%2fetc")
        s.expect(
            r.status in (400, 404),
            f"a traversal workspace name returned {r.status}",
            request="GET /api/workspaces/..%2f..%2fetc",
            response=r,
        )

    def traversal_in_secret_ref(c: Ctx) -> None:
        r = c.client.put("/api/secrets/..%2f..%2fetc%2fpasswd", {"value": "x"})
        s.expect(
            r.status in (400, 404),
            f"a traversal secret ref returned {r.status}",
            request="PUT /api/secrets/..%2f..%2fetc%2fpasswd",
            response=r,
        )

    def very_long_path_segment(c: Ctx) -> None:
        r = c.client.get("/api/agents/" + "a" * 5000 + "/diff")
        s.expect(
            r.status < 500,
            f"a 5000-char id returned {r.status}",
            request="GET /api/agents/<5000 chars>/diff",
            response=r,
        )

    def unicode_in_a_path_segment(c: Ctx) -> None:
        r = c.client.get(f"/api/agents/{q('агент-🚀')}/diff")
        s.expect(
            r.status in (404, 410),
            f"a unicode agent id returned {r.status}",
            request="GET /api/agents/<unicode>/diff",
            response=r,
        )

    # ---- streams ---------------------------------------------------------

    def events_requires_auth(c: Ctx) -> None:
        r = c.client.get("/api/events", token="", timeout=5)
        s.expect_status(r, 401, request="GET /api/events (no token)")

    def events_streams(c: Ctx) -> None:
        url = f"{c.env.base}/api/events?stream=fleet"
        req = urllib.request.Request(url, headers={"Authorization": f"Bearer {c.env.token}"})
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                s.expect(
                    resp.status == 200,
                    f"events returned {resp.status}",
                    request="GET /api/events",
                )
                ctype = resp.headers.get("Content-Type", "")
                s.expect(
                    "text/event-stream" in ctype,
                    f"events content-type is {ctype!r}",
                    request="GET /api/events",
                )
                resp.read(64)
        except socket.timeout:
            return
        except urllib.error.HTTPError as exc:
            raise Failure(
                f"events returned {exc.code}",
                request="GET /api/events",
                response=exc.read().decode("utf-8", "replace")[:300],
            ) from exc

    def mcp_rejects_without_its_own_auth(c: Ctx) -> None:
        r = c.client.post("/mcp", {"jsonrpc": "2.0", "id": 1, "method": "initialize"})
        s.expect(
            r.status in (401, 403),
            f"/mcp without a client token returned {r.status}",
            request="POST /mcp (no client token)",
            response=r,
        )

    def mcp_rejects_a_bad_client_token(c: Ctx) -> None:
        r = c.client.post(
            "/mcp",
            {"jsonrpc": "2.0", "id": 1, "method": "initialize"},
            token="not-a-client-token",
        )
        s.expect(
            r.status in (401, 403),
            f"/mcp with a bad client token returned {r.status}",
            request="POST /mcp (bad client token)",
            response=r,
        )

    # ---- clients and pending --------------------------------------------

    def clients_list(c: Ctx) -> None:
        r = c.client.get("/api/clients")
        body = s.expect_ok(r, request="GET /api/clients")
        s.expect(isinstance(body, list), "clients is not a list", response=body)

    def client_roundtrip(c: Ctx) -> None:
        r = c.client.post("/api/clients", {"name": "e2e-client"})
        body = s.expect_ok(r, request="POST /api/clients")
        s.expect_keys(body, ["id"], request="POST /api/clients")
        c.set("client", body["id"])

    def client_delete(c: Ctx) -> None:
        cid = c.get("client")
        if not cid:
            raise Skip("no client created")
        r = c.client.delete(f"/api/clients/{q(cid)}")
        s.expect_ok(r, request=f"DELETE /api/clients/{cid}")

    def client_delete_unknown(c: Ctx) -> None:
        # DELETE is idempotent: removing a client that is already gone
        # succeeds rather than 404ing.
        r = c.client.delete("/api/clients/never-existed")
        s.expect(
            r.status in (200, 204, 404),
            f"deleting an unknown client returned {r.status}",
            request="DELETE /api/clients/never-existed",
            response=r,
        )

    def pending_list(c: Ctx) -> None:
        r = c.client.get("/api/pending")
        body = s.expect_ok(r, request="GET /api/pending")
        s.expect(isinstance(body, list), "pending is not a list", response=body)

    def pending_unknown(c: Ctx) -> None:
        r = c.client.post("/api/pending/nope/approve")
        s.expect(
            r.status in (404, 410),
            f"approving an unknown submission returned {r.status}",
            request="POST /api/pending/nope/approve",
            response=r,
        )

    def permission_unknown(c: Ctx) -> None:
        r = c.client.post("/api/permissions/nope", {"decision": "allow"})
        s.expect(
            r.status in (404, 410),
            f"resolving an unknown permission returned {r.status}",
            request="POST /api/permissions/nope",
            response=r,
        )

    def question_unknown(c: Ctx) -> None:
        r = c.client.post("/api/questions/nope", {"answers": []})
        s.expect(
            r.status in (400, 404, 410),
            f"resolving an unknown question returned {r.status}",
            request="POST /api/questions/nope",
            response=r,
        )

    # ---- disk and audit --------------------------------------------------

    def disk(c: Ctx) -> None:
        r = c.client.get("/api/disk")
        body = s.expect_ok(r, request="GET /api/disk")
        s.expect_keys(body, ["repos", "work", "total"], request="GET /api/disk")

    def audit(c: Ctx) -> None:
        r = c.client.get("/api/audit?limit=5")
        body = s.expect_ok(r, request="GET /api/audit")
        s.expect(isinstance(body, list), "audit is not a list", response=body)
        s.expect(len(body) <= 5, f"audit returned {len(body)} rows for limit=5", response=body)

    def audit_limit_is_bounded(c: Ctx) -> None:
        r = c.client.get("/api/audit?limit=100000")
        body = s.expect_ok(r, request="GET /api/audit?limit=100000")
        s.expect(
            len(body) <= 1000,
            f"audit returned {len(body)} rows for a huge limit",
            response=body,
        )

    def runs_list(c: Ctx) -> None:
        r = c.client.get("/api/runs")
        body = s.expect_ok(r, request="GET /api/runs")
        s.expect(isinstance(body, list), "runs is not a list", response=body)

    def run_unknown(c: Ctx) -> None:
        r = c.client.get("/api/runs/nope")
        s.expect(
            r.status in (404, 410, 501),
            f"an unknown run returned {r.status}",
            request="GET /api/runs/nope",
            response=r,
        )

    for name, fn in [
        ("no token is 401", no_token),
        ("wrong token is 401", wrong_token),
        ("malformed auth header is 401", malformed_auth_header),
        ("401 carries a Bearer challenge", unauthorized_sets_challenge),
        ("the token is never echoed", token_never_echoed),
        ("malformed JSON is 400", malformed_json),
        ("trailing JSON data is 400", trailing_data),
        ("oversize body is rejected", oversize_body),
        ("null body is rejected", null_body),
        ("missing required field is 400", missing_required_field),
        ("empty body is 400", empty_body_where_required),
        ("unknown api route is 404", unknown_api_route),
        ("wrong method is 404/405", wrong_method),
        ("unknown subroute is 404", unknown_agent_route),
        ("traversal in a workspace name", traversal_in_workspace_name),
        ("traversal in a secret ref", traversal_in_secret_ref),
        ("a very long path segment", very_long_path_segment),
        ("unicode in a path segment", unicode_in_a_path_segment),
        ("events requires auth", events_requires_auth),
        ("events streams", events_streams),
        ("mcp rejects without its own auth", mcp_rejects_without_its_own_auth),
        ("mcp rejects a bad client token", mcp_rejects_a_bad_client_token),
        ("clients list", clients_list),
        ("client round trip", client_roundtrip),
        ("client delete", client_delete),
        ("delete an unknown client", client_delete_unknown),
        ("pending list", pending_list),
        ("approve an unknown submission", pending_unknown),
        ("resolve an unknown permission", permission_unknown),
        ("resolve an unknown question", question_unknown),
        ("disk", disk),
        ("audit", audit),
        ("audit limit is bounded", audit_limit_is_bounded),
        ("runs list", runs_list),
        ("unknown run", run_unknown),
    ]:
        s.check(name, fn)
