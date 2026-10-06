"""Group A: session lifecycle.

Covers the routes the SPA's Sessions panel and chat view use:
config, projects, session create/list/load/delete, prompt, cancel,
steer, mode, stack, nodes, roster, last-request and step-diffs.
"""

from __future__ import annotations

from e2e_client import q
from harness import Ctx, Failure, Skip, Suite, wait_until


def register(s: Suite) -> None:
    s.area("A sessions")

    def config(c: Ctx) -> None:
        r = c.client.get("/api/config")
        body = s.expect_ok(r, request="GET /api/config")
        # The bridge serves the project list here. api.ts declares a
        # `cwdRoot` field that no component reads and the bridge never
        # sends; assert on what is actually served.
        s.expect_keys(body, ["projects"], request="GET /api/config")

    def projects(c: Ctx) -> None:
        r = c.client.get("/api/projects")
        body = s.expect_ok(r, request="GET /api/projects")
        s.expect(isinstance(body, list), "projects is not a list", response=body)
        roots = [p.get("root") for p in body]
        s.expect(
            c.env.project in roots,
            f"configured project {c.env.project} missing from {roots}",
            request="GET /api/projects",
            response=body,
        )
        for p in body:
            s.expect_keys(p, ["root", "available"], request="GET /api/projects")

    def create_session(c: Ctx) -> None:
        """POST /api/sessions returns the *agent* id.

        The ACP session id (`sess_…`) is a different namespace and is
        only visible once the session is loaded, so resolve it from the
        stack and keep both.
        """
        r = c.client.post("/api/sessions", {"cwd": c.env.project})
        body = s.expect_ok(r, request="POST /api/sessions")
        s.expect_keys(body, ["sessionId"], request="POST /api/sessions")
        agent_id = body["sessionId"]
        c.set("session_agent", agent_id)
        stack = s.expect_ok(
            c.client.get(f"/api/sessions/{q(agent_id)}/stack"),
            request=f"GET /api/sessions/{agent_id}/stack",
        )
        s.expect_keys(stack, ["sessionId"], request="stack")
        c.set("session", stack["sessionId"])

    def session_is_an_agent(c: Ctx) -> None:
        aid = c.get("session_agent")
        if not aid:
            raise Skip("no session created")
        r = c.client.get("/api/agents")
        agents = s.expect_ok(r, request="GET /api/agents")
        match = [a for a in agents if a.get("id") == aid]
        s.expect(
            bool(match),
            f"session {aid} does not appear in /api/agents",
            request="GET /api/agents",
            response=agents,
        )
        a = match[0]
        s.expect_keys(a, ["id", "project", "status", "updatedAt"], request="GET /api/agents")
        s.expect(
            a.get("origin") == "ui",
            f"session origin is {a.get('origin')!r}, want 'ui'",
            response=a,
        )

    def list_sessions(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.get(f"/api/sessions?cwd={q(c.env.project)}")
        body = s.expect_ok(r, request="GET /api/sessions?cwd=...")
        s.expect_keys(body, ["sessions"], request="GET /api/sessions")
        ids = [x.get("sessionId") for x in body["sessions"]]
        s.expect(
            sid in ids,
            f"created session {sid} missing from the list",
            request="GET /api/sessions",
            response=body,
        )

    def list_sessions_without_an_agent(c: Ctx) -> None:
        """A project the bridge manages but has no live agent for.

        The Sessions panel lists every project from /api/projects and
        calls listSessions(root) when one is picked, so this must work
        for a project that has never been used.
        """
        r = c.client.get(f"/api/sessions?cwd={q(c.env.project2)}")
        if r.status == 502 and "unknown project" in r.error():
            raise Failure(
                "listing sessions for a registered project with no live agent "
                "fails with 'unknown project'; the Sessions panel cannot open it",
                request=f"GET /api/sessions?cwd={c.env.project2}",
                response=r,
            )
        s.expect_ok(r, request=f"GET /api/sessions?cwd={c.env.project2}")

    def prompt(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.post(f"/api/sessions/{q(sid)}/prompt", {"text": "Reply with exactly: PONG"})
        s.expect_status(r, (200, 202), request=f"POST /api/sessions/{sid}/prompt")

    def stack(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.get(f"/api/sessions/{q(sid)}/stack")
        body = s.expect_ok(r, request=f"GET /api/sessions/{sid}/stack")
        s.expect_keys(body, ["sessionId", "rev", "roots", "nodes"], request="stack")
        s.expect(isinstance(body["nodes"], list), "stack nodes is not a list", response=body)
        if body["nodes"]:
            c.set("node", body["nodes"][0]["id"])

    def node_detail(c: Ctx) -> None:
        sid, node = c.get("session"), c.get("node")
        if not sid or not node:
            raise Skip("no stack node to fetch")
        r = c.client.get(f"/api/sessions/{q(sid)}/nodes/{q(node)}")
        s.expect_ok(r, request=f"GET /api/sessions/{sid}/nodes/{node}")

    def roster(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.get(f"/api/sessions/{q(sid)}/roster")
        body = s.expect_ok(r, request=f"GET /api/sessions/{sid}/roster")
        s.expect_keys(body, ["roles"], request="roster")
        s.expect(isinstance(body["roles"], list), "roster roles is not a list", response=body)

    def last_request(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.get(f"/api/sessions/{q(sid)}/last-request")
        s.expect_status(r, (200, 501), request=f"GET /api/sessions/{sid}/last-request")

    def step_diffs(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.get(f"/api/sessions/{q(sid)}/step-diffs")
        s.expect_status(r, (200, 501), request=f"GET /api/sessions/{sid}/step-diffs")

    def set_mode(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.post(f"/api/sessions/{q(sid)}/mode", {"mode": "plan"})
        s.expect_ok(r, request=f"POST /api/sessions/{sid}/mode")
        # The mode must be reflected back on the agent record.
        agents = s.expect_ok(c.client.get("/api/agents"), request="GET /api/agents")
        match = [a for a in agents if a.get("id") == sid]
        if match:
            s.expect(
                match[0].get("mode") == "plan",
                f"mode is {match[0].get('mode')!r} after setting 'plan'",
                response=match[0],
            )
        c.client.post(f"/api/sessions/{q(sid)}/mode", {"mode": "edit"})

    def steer(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.post(f"/api/sessions/{q(sid)}/steer", {"text": "ignore that"})
        # Steering an idle session is a no-op or a conflict, never a 5xx.
        s.expect(
            r.status < 500,
            f"steer returned {r.status}: {r.error()}",
            request=f"POST /api/sessions/{sid}/steer",
            response=r,
        )

    def cancel(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.post(f"/api/sessions/{q(sid)}/cancel")
        s.expect(
            r.status < 500,
            f"cancel returned {r.status}: {r.error()}",
            request=f"POST /api/sessions/{sid}/cancel",
            response=r,
        )

    def load_session(c: Ctx) -> None:
        sid = c.get("session")
        if not sid:
            raise Skip("no session created")
        r = c.client.post(f"/api/sessions/{q(sid)}/load", {"cwd": c.env.project})
        s.expect_ok(r, request=f"POST /api/sessions/{sid}/load")

    def unknown_session(c: Ctx) -> None:
        r = c.client.get("/api/sessions/does-not-exist/stack")
        s.expect(
            r.status in (404, 410),
            f"unknown session returned {r.status}, want 404/410",
            request="GET /api/sessions/does-not-exist/stack",
            response=r,
        )

    def delete_session(c: Ctx) -> None:
        """DELETE removes the session's transcript, not the agent.

        The agent record is retired by discard/exit; deleting a session
        drops its stored transcript so it no longer appears in the
        Sessions panel.
        """
        sid, aid = c.get("session"), c.get("session_agent")
        if not sid or not aid:
            raise Skip("no session created")
        r = c.client.delete(f"/api/sessions/{q(sid)}")
        s.expect_ok(r, request=f"DELETE /api/sessions/{sid}")
        listed = s.expect_ok(
            c.client.get(f"/api/sessions?cwd={q(c.env.project)}"),
            request="GET /api/sessions",
        )
        s.expect(
            not any(x.get("sessionId") == sid for x in listed["sessions"]),
            f"session {sid} is still listed after delete",
            response=listed,
        )

    for name, fn in [
        ("config", config),
        ("projects", projects),
        ("create session", create_session),
        ("session appears as an agent", session_is_an_agent),
        ("list sessions", list_sessions),
        ("list sessions for a project with no agent", list_sessions_without_an_agent),
        ("prompt", prompt),
        ("stack", stack),
        ("node detail", node_detail),
        ("roster", roster),
        ("last request", last_request),
        ("step diffs", step_diffs),
        ("set mode", set_mode),
        ("steer", steer),
        ("cancel", cancel),
        ("load session", load_session),
        ("unknown session is 404", unknown_session),
        ("delete session", delete_session),
    ]:
        s.check(name, fn)
