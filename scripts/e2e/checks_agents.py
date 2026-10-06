"""Group B: agent lifecycle.

Spawns a real isolated agent and exercises the ship path the SPA uses:
spawn, list, diff, files, file, commit-draft, gate, verify, patch,
merge, discard, exit.
"""

from __future__ import annotations

from e2e_client import q
from harness import Ctx, Failure, Skip, Suite, wait_until


def register(s: Suite) -> None:
    s.area("B agents")

    def spawn(c: Ctx) -> None:
        r = c.client.post(
            "/api/agents",
            {
                "project": c.env.project,
                "name": "e2e lifecycle",
                "mode": "edit",
                "isolated": True,
            },
        )
        body = s.expect_ok(r, request="POST /api/agents")
        s.expect_keys(body, ["agentId"], request="POST /api/agents")
        c.set("agent", body["agentId"])

    def appears_isolated(c: Ctx) -> None:
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        agents = s.expect_ok(c.client.get("/api/agents"), request="GET /api/agents")
        match = [a for a in agents if a.get("id") == aid]
        s.expect(bool(match), f"agent {aid} missing from the list", response=agents)
        a = match[0]
        s.expect(
            a.get("isolated") is True,
            f"agent is not isolated: {a.get('isolated')!r}",
            response=a,
        )
        s.expect(
            bool(a.get("branch")),
            f"isolated agent has no branch: {a.get('branch')!r}",
            response=a,
        )
        s.expect(
            a.get("mode") == "edit",
            f"mode is {a.get('mode')!r}, want 'edit'",
            response=a,
        )

    def diff(c: Ctx) -> None:
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.get(f"/api/agents/{q(aid)}/diff")
        s.expect_ok(r, request=f"GET /api/agents/{aid}/diff")

    def files(c: Ctx) -> None:
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.get(f"/api/agents/{q(aid)}/files?path=")
        s.expect_ok(r, request=f"GET /api/agents/{aid}/files")

    def file_read(c: Ctx) -> None:
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.get(f"/api/agents/{q(aid)}/file?path=README.md")
        s.expect_ok(r, request=f"GET /api/agents/{aid}/file?path=README.md")

    def file_traversal(c: Ctx) -> None:
        """A path outside the agent's root must be refused, not served."""
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.get(f"/api/agents/{q(aid)}/file?path=../../../../etc/passwd")
        if r.ok and isinstance(r.body, dict):
            content = str(r.body.get("content", ""))
            if "root:" in content:
                raise Failure(
                    "path traversal escaped the agent root and read /etc/passwd",
                    request=f"GET /api/agents/{aid}/file?path=../../../../etc/passwd",
                    response=r,
                )
        s.expect(
            r.status < 500,
            f"traversal returned {r.status}: {r.error()}",
            request=f"GET /api/agents/{aid}/file?path=../../../../etc/passwd",
            response=r,
        )

    def commit_draft(c: Ctx) -> None:
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.get(f"/api/agents/{q(aid)}/commit-draft")
        s.expect_status(r, (200, 501), request=f"GET /api/agents/{aid}/commit-draft")

    def gate_before_verify(c: Ctx) -> None:
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.get(f"/api/agents/{q(aid)}/gate")
        s.expect_status(r, (200, 204), request=f"GET /api/agents/{aid}/gate")

    def verify(c: Ctx) -> None:
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.post(f"/api/agents/{q(aid)}/verify", {}, timeout=180)
        s.expect_ok(r, request=f"POST /api/agents/{aid}/verify")
        s.expect_keys(r.body, ["result", "at"], request="verify")

    def gate_after_verify(c: Ctx) -> None:
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.get(f"/api/agents/{q(aid)}/gate")
        s.expect_status(r, 200, request=f"GET /api/agents/{aid}/gate")
        s.expect_keys(r.body, ["result", "at"], request="gate")

    def patch(c: Ctx) -> None:
        """Patch export applies to git-sourced agents only.

        This agent is local, so its work is the project itself and there
        is no checkout to diff against. That is a conflict, not a gateway
        fault: a 502 would tell the SPA the bridge is broken.
        """
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.get(f"/api/agents/{q(aid)}/patch")
        s.expect_status(r, 409, request=f"GET /api/agents/{aid}/patch")

    def merge(c: Ctx) -> None:
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.post(
            f"/api/agents/{q(aid)}/merge", {"commitMessage": "e2e: merge"}, timeout=120
        )
        # 200 merged, or 409 with a reason — never a 5xx.
        s.expect(
            r.status in (200, 409),
            f"merge returned {r.status}: {r.error()}",
            request=f"POST /api/agents/{aid}/merge",
            response=r,
        )

    def discard(c: Ctx) -> None:
        """Discard throws the work away.

        A git-sourced agent is retired outright: its whole workspace is
        the throwaway checkout. A local isolated agent keeps its record
        but loses the worktree, returning to the project root with no
        changes. Either way the work must be gone.
        """
        aid = c.get("agent")
        if not aid:
            raise Skip("no agent spawned")
        r = c.client.post(f"/api/agents/{q(aid)}/discard", {}, timeout=120)
        s.expect_ok(r, request=f"POST /api/agents/{aid}/discard")
        agents = s.expect_ok(c.client.get("/api/agents"), request="GET /api/agents")
        match = [a for a in agents if a.get("id") == aid]
        if not match:
            return  # retired outright
        a = match[0]
        s.expect(
            not a.get("isolated") and not a.get("branch"),
            f"agent {aid} is still isolated after discard: {a}",
            response=a,
        )

    def unknown_agent(c: Ctx) -> None:
        r = c.client.get("/api/agents/nope/diff")
        s.expect(
            r.status in (404, 410),
            f"unknown agent returned {r.status}, want 404/410",
            request="GET /api/agents/nope/diff",
            response=r,
        )

    def exit_requires_a_message(c: Ctx) -> None:
        r = c.client.post("/api/agents/nope/exit", {})
        s.expect(
            r.status in (400, 404, 410),
            f"exit on an unknown agent returned {r.status}",
            request="POST /api/agents/nope/exit",
            response=r,
        )

    for name, fn in [
        ("spawn isolated agent", spawn),
        ("agent reports isolation and branch", appears_isolated),
        ("diff", diff),
        ("files", files),
        ("file read", file_read),
        ("file traversal is refused", file_traversal),
        ("commit draft", commit_draft),
        ("gate before verify is empty", gate_before_verify),
        ("verify runs the gate", verify),
        ("gate after verify", gate_after_verify),
        ("patch", patch),
        ("merge", merge),
        ("discard", discard),
        ("unknown agent is 404", unknown_agent),
        ("exit on unknown agent", exit_requires_a_message),
    ]:
        s.check(name, fn)
