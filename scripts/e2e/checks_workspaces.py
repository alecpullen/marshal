"""Group E: workspaces.

Templates, drafts, patches, publish, diff, pool, builds, CA rotation and
the workspace test shell. A workspace is created from a starter, edited,
published and deleted; the build path is exercised only when the runtime
is available.
"""

from __future__ import annotations

import base64

from e2e_client import q
from harness import Ctx, Failure, Skip, Suite, wait_until


def register(s: Suite) -> None:
    s.area("E workspaces")

    def list_workspaces(c: Ctx) -> None:
        r = c.client.get("/api/workspaces")
        body = s.expect_ok(r, request="GET /api/workspaces")
        s.expect(isinstance(body, list), "workspaces is not a list", response=body)
        for w in body:
            s.expect_keys(w, ["name", "source"], request="GET /api/workspaces")

    def create(c: Ctx) -> None:
        r = c.client.post("/api/workspaces", {"name": "e2e-ws", "from": "starter:minimal"})
        body = s.expect_ok(r, request="POST /api/workspaces")
        s.expect_keys(body, ["name"], request="POST /api/workspaces")
        c.set("workspace", "e2e-ws")

    def create_duplicate(c: Ctx) -> None:
        if not c.get("workspace"):
            raise Skip("no workspace created")
        r = c.client.post("/api/workspaces", {"name": "e2e-ws", "from": "starter:minimal"})
        s.expect_status(r, 409, request="POST /api/workspaces (duplicate)")

    def create_bad_from(c: Ctx) -> None:
        r = c.client.post("/api/workspaces", {"name": "e2e-bad", "from": "nonsense"})
        s.expect_status(r, 400, request="POST /api/workspaces (bad from)")

    def create_bad_name(c: Ctx) -> None:
        r = c.client.post("/api/workspaces", {"name": "Bad Name!", "from": "starter:minimal"})
        s.expect_status(r, 400, request="POST /api/workspaces (bad name)")

    def get(c: Ctx) -> None:
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        r = c.client.get(f"/api/workspaces/{q(name)}")
        body = s.expect_ok(r, request=f"GET /api/workspaces/{name}")
        s.expect_keys(body, ["source", "version", "doc", "sections"], request="workspace get")

    def unknown(c: Ctx) -> None:
        r = c.client.get("/api/workspaces/no-such-workspace")
        s.expect_status(r, 404, request="GET /api/workspaces/no-such-workspace")

    def save_draft(c: Ctx) -> None:
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        src = (
            '[workspace]\nname = "e2e-ws"\nbase = "debian:bookworm-slim"\n\n'
            '[packages]\napt = ["git"]\n\n[network]\nmode = "open"\n'
        )
        c.set("workspace_source", src)
        r = c.client.put(f"/api/workspaces/{q(name)}/draft", {"source": src})
        body = s.expect_ok(r, request=f"PUT /api/workspaces/{name}/draft")
        s.expect_keys(body, ["source", "doc"], request="workspace draft")

    def save_invalid_draft(c: Ctx) -> None:
        """A draft that does not parse is stored with diagnostics.

        Saving is deliberately lenient so the editor can hold a
        half-typed document; publish is what refuses it. The valid
        draft is restored afterwards so later checks have something to
        patch and publish.
        """
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        r = c.client.put(f"/api/workspaces/{q(name)}/draft", {"source": "this is not toml = = ="})
        body = s.expect_ok(r, request=f"PUT /api/workspaces/{name}/draft (invalid)")
        s.expect(
            bool(body.get("diagnostics")),
            "an unparseable draft was stored with no diagnostics",
            response=body,
        )
        src = c.get("workspace_source")
        if src:
            s.expect_ok(
                c.client.put(f"/api/workspaces/{q(name)}/draft", {"source": src}),
                request=f"PUT /api/workspaces/{name}/draft (restore)",
            )

    def publish_rejects_an_invalid_draft(c: Ctx) -> None:
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        s.expect_ok(
            c.client.put(f"/api/workspaces/{q(name)}/draft", {"source": "not toml = = ="}),
            request=f"PUT /api/workspaces/{name}/draft (invalid)",
        )
        r = c.client.post(f"/api/workspaces/{q(name)}/publish", {}, timeout=60)
        s.expect(
            r.status in (400, 422),
            f"publishing an unparseable draft returned {r.status}",
            request=f"POST /api/workspaces/{name}/publish (invalid)",
            response=r,
        )
        src = c.get("workspace_source")
        if src:
            s.expect_ok(
                c.client.put(f"/api/workspaces/{q(name)}/draft", {"source": src}),
                request=f"PUT /api/workspaces/{name}/draft (restore)",
            )

    def patch(c: Ctx) -> None:
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        r = c.client.post(
            f"/api/workspaces/{q(name)}/patch",
            {"layer": 3, "value": {"apt": ["git", "curl"]}},
        )
        s.expect_ok(r, request=f"POST /api/workspaces/{name}/patch")

    def publish(c: Ctx) -> None:
        """Publish records a version; the build is a separate call.

        The Designer publishes and then starts the build, so the version
        is usable only once the build reports ok.
        """
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        r = c.client.post(f"/api/workspaces/{q(name)}/publish", {}, timeout=300)
        if r.status == 409 and "workspace_not_built" in r.raw:
            raise Skip("the runtime cannot build images here")
        body = s.expect_ok(r, request=f"POST /api/workspaces/{name}/publish")
        s.expect_keys(body, ["n"], request="workspace publish")
        c.set("workspace_version", body["n"])

    def build(c: Ctx) -> None:
        name, n = c.get("workspace"), c.get("workspace_version")
        if not name or not n:
            raise Skip("no workspace version published")
        r = c.client.post(f"/api/workspaces/{q(name)}/builds", {"version": n}, timeout=60)
        if r.status == 409:
            raise Skip(f"the runtime cannot build images here: {r.error()}")
        s.expect_status(r, 202, request=f"POST /api/workspaces/{name}/builds")

    def build_completes(c: Ctx) -> None:
        name, n = c.get("workspace"), c.get("workspace_version")
        if not name or not n:
            raise Skip("no workspace version published")

        def done() -> bool:
            r = c.client.get(f"/api/workspaces/{q(name)}/builds")
            if not r.ok:
                return False
            for v in r.body.get("versions") or []:
                if v.get("n") == n:
                    return v.get("buildStatus") not in ("pending", "building")
            return False

        if not wait_until(done, timeout=600, interval=5, what="the build"):
            raise Failure(
                "the workspace build never left pending/building",
                request=f"GET /api/workspaces/{name}/builds",
            )
        r = c.client.get(f"/api/workspaces/{q(name)}/builds")
        for v in r.body.get("versions") or []:
            if v.get("n") == n:
                s.expect(
                    v.get("buildStatus") == "ok",
                    f"build status is {v.get('buildStatus')!r}, want ok",
                    request=f"GET /api/workspaces/{name}/builds",
                    response=v,
                )
                return
        raise Failure("the built version vanished from the list", response=r.body)

    def diff(c: Ctx) -> None:
        name, _ = c.get("workspace"), c.get("workspace_version")
        if not name:
            raise Skip("no workspace created")
        r = c.client.get(f"/api/workspaces/{q(name)}/diff?a=0&b=1")
        s.expect(
            r.status in (200, 404),
            f"workspace diff returned {r.status}: {r.error()}",
            request=f"GET /api/workspaces/{name}/diff",
            response=r,
        )

    def pool(c: Ctx) -> None:
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        r = c.client.put(f"/api/workspaces/{q(name)}/pool", {"size": 0})
        s.expect_ok(r, request=f"PUT /api/workspaces/{name}/pool")

    def builds(c: Ctx) -> None:
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        r = c.client.get(f"/api/workspaces/{q(name)}/builds")
        s.expect_ok(r, request=f"GET /api/workspaces/{name}/builds")

    def ca_rotate(c: Ctx) -> None:
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        r = c.client.post(f"/api/workspaces/{q(name)}/ca/rotate", {}, timeout=60)
        s.expect(
            r.status in (200, 204, 409),
            f"CA rotate returned {r.status}: {r.error()}",
            request=f"POST /api/workspaces/{name}/ca/rotate",
            response=r,
        )

    def shell_open(c: Ctx) -> None:
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        r = c.client.post(f"/api/workspaces/{q(name)}/shell", {"cols": 80, "rows": 24}, timeout=120)
        if r.status == 409:
            raise Skip(f"workspace shell unavailable: {r.error()}")
        body = s.expect_ok(r, request=f"POST /api/workspaces/{name}/shell")
        s.expect_keys(body, ["terminalId"], request="workspace shell open")
        c.set("ws_shell", body["terminalId"])

    def shell_input(c: Ctx) -> None:
        name, tid = c.get("workspace"), c.get("ws_shell")
        if not name or not tid:
            raise Skip("no workspace shell open")
        data = base64.b64encode(b"echo E2E_SHELL_OK\n").decode()
        r = c.client.post(f"/api/workspaces/{q(name)}/shell/{q(tid)}/input", {"data": data})
        s.expect_status(r, 204, request=f"POST /api/workspaces/{name}/shell/{tid}/input")

    def shell_resize(c: Ctx) -> None:
        name, tid = c.get("workspace"), c.get("ws_shell")
        if not name or not tid:
            raise Skip("no workspace shell open")
        r = c.client.post(
            f"/api/workspaces/{q(name)}/shell/{q(tid)}/resize", {"cols": 100, "rows": 30}
        )
        s.expect_status(r, 204, request=f"POST /api/workspaces/{name}/shell/{tid}/resize")

    def shell_close(c: Ctx) -> None:
        name, tid = c.get("workspace"), c.get("ws_shell")
        if not name or not tid:
            raise Skip("no workspace shell open")
        r = c.client.delete(f"/api/workspaces/{q(name)}/shell/{q(tid)}")
        s.expect_status(r, 204, request=f"DELETE /api/workspaces/{name}/shell/{tid}")

    def delete(c: Ctx) -> None:
        name = c.get("workspace")
        if not name:
            raise Skip("no workspace created")
        r = c.client.delete(f"/api/workspaces/{q(name)}", timeout=120)
        s.expect_ok(r, request=f"DELETE /api/workspaces/{name}")
        r = c.client.get(f"/api/workspaces/{q(name)}")
        s.expect_status(r, 404, request=f"GET deleted workspace")

    def delete_in_use(c: Ctx) -> None:
        # gofix is used by the forge agents; deleting it must be refused.
        r = c.client.delete("/api/workspaces/gofix")
        s.expect(
            r.status in (409, 404),
            f"deleting an in-use workspace returned {r.status}",
            request="DELETE /api/workspaces/gofix",
            response=r,
        )

    for name, fn in [
        ("list workspaces", list_workspaces),
        ("create workspace", create),
        ("duplicate workspace is 409", create_duplicate),
        ("bad from is 400", create_bad_from),
        ("bad name is 400", create_bad_name),
        ("get workspace", get),
        ("unknown workspace is 404", unknown),
        ("save draft", save_draft),
        ("an invalid draft is stored with diagnostics", save_invalid_draft),
        ("publish rejects an invalid draft", publish_rejects_an_invalid_draft),
        ("patch a layer", patch),
        ("publish", publish),
        ("start a build", build),
        ("the build completes", build_completes),
        ("diff versions", diff),
        ("set pool", pool),
        ("list builds", builds),
        ("rotate CA", ca_rotate),
        ("open workspace shell", shell_open),
        ("workspace shell input", shell_input),
        ("workspace shell resize", shell_resize),
        ("close workspace shell", shell_close),
        ("delete workspace", delete),
        ("delete an in-use workspace", delete_in_use),
    ]:
        s.check(name, fn)
