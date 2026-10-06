"""Group F: secrets, credentials, network and egress.

The secrets store, credential records, the network view and the
allow/block decision route. Secrets are written under a test prefix and
removed again; the value never has a route out, so the checks assert on
refs and status only.
"""

from __future__ import annotations

from e2e_client import q
from harness import Ctx, Failure, Skip, Suite, wait_until


def register(s: Suite) -> None:
    s.area("F secrets")

    # ---- secrets ---------------------------------------------------------

    def status(c: Ctx) -> None:
        r = c.client.get("/api/secrets/status")
        body = s.expect_ok(r, request="GET /api/secrets/status")
        s.expect_keys(body, ["backend", "healthy"], request="secrets status")

    def list_secrets(c: Ctx) -> None:
        r = c.client.get("/api/secrets")
        body = s.expect_ok(r, request="GET /api/secrets")
        s.expect_keys(body, ["refs"], request="list secrets")
        s.expect(isinstance(body["refs"], list), "refs is not a list", response=body)

    def roundtrip(c: Ctx) -> None:
        ref = "e2e/test-secret"
        r = c.client.put(f"/api/secrets/{ref}", {"value": "s3cr3t-value"})
        s.expect_status(r, 204, request=f"PUT /api/secrets/{ref}")
        c.set("secret_ref", ref)
        listed = s.expect_ok(c.client.get("/api/secrets?prefix=e2e"), request="GET /api/secrets?prefix=e2e")
        s.expect(
            f"vault:{ref}" in listed["refs"],
            f"secret vault:{ref} missing from {listed['refs']}",
            response=listed,
        )

    def value_has_no_route_out(c: Ctx) -> None:
        ref = c.get("secret_ref")
        if not ref:
            raise Skip("no secret written")
        r = c.client.get(f"/api/secrets/{ref}")
        s.expect(
            r.status in (404, 405),
            f"GET on a secret ref returned {r.status}; values must not be readable",
            request=f"GET /api/secrets/{ref}",
            response=r,
        )

    def empty_value_is_rejected(c: Ctx) -> None:
        r = c.client.put("/api/secrets/e2e/empty", {"value": ""})
        s.expect_status(r, 400, request="PUT /api/secrets/e2e/empty")

    def reserved_path_is_rejected(c: Ctx) -> None:
        # The bridge keeps workspace CA keys under "ca/"; the secrets API
        # must never read, write or list them.
        r = c.client.put("/api/secrets/ca/e2e-attempt", {"value": "x"})
        s.expect_status(r, 400, request="PUT /api/secrets/ca/e2e-attempt")

    def delete(c: Ctx) -> None:
        ref = c.get("secret_ref")
        if not ref:
            raise Skip("no secret written")
        r = c.client.delete(f"/api/secrets/{ref}")
        s.expect_status(r, 204, request=f"DELETE /api/secrets/{ref}")
        listed = s.expect_ok(c.client.get("/api/secrets?prefix=e2e"), request="GET /api/secrets")
        s.expect(
            f"vault:{ref}" not in listed["refs"],
            f"secret vault:{ref} still listed after delete",
            response=listed,
        )

    def delete_unknown(c: Ctx) -> None:
        r = c.client.delete("/api/secrets/e2e/never-existed")
        s.expect(
            r.status in (204, 404),
            f"deleting an unknown secret returned {r.status}",
            request="DELETE /api/secrets/e2e/never-existed",
            response=r,
        )

    # ---- credentials -----------------------------------------------------

    def credentials_list(c: Ctx) -> None:
        r = c.client.get("/api/credentials")
        body = s.expect_ok(r, request="GET /api/credentials")
        s.expect(isinstance(body, list), "credentials is not a list", response=body)
        for row in body:
            s.expect_keys(row, ["id", "kind", "set"], request="GET /api/credentials")

    def credential_roundtrip(c: Ctx) -> None:
        r = c.client.post(
            "/api/credentials",
            {"id": "e2e-pat", "kind": "pat", "envVar": "E2E_TOKEN", "user": "e2e"},
        )
        body = s.expect_ok(r, request="POST /api/credentials")
        s.expect(body.get("id") == "e2e-pat", f"id is {body.get('id')!r}", response=body)
        c.set("credential", "e2e-pat")

    def credential_invalid_kind(c: Ctx) -> None:
        r = c.client.post("/api/credentials", {"id": "e2e-bad", "kind": "nonsense"})
        s.expect_status(r, 400, request="POST /api/credentials (bad kind)")

    def credential_pat_needs_env(c: Ctx) -> None:
        r = c.client.post("/api/credentials", {"id": "e2e-bad2", "kind": "pat"})
        s.expect_status(r, 400, request="POST /api/credentials (pat without envVar)")

    def credential_ssh_rejects_metacharacters(c: Ctx) -> None:
        r = c.client.post(
            "/api/credentials",
            {"id": "e2e-bad3", "kind": "ssh", "keyPath": "/tmp/key; rm -rf /"},
        )
        s.expect_status(r, 400, request="POST /api/credentials (ssh with metacharacters)")

    def credential_bad_id(c: Ctx) -> None:
        r = c.client.post("/api/credentials", {"id": "bad id!", "kind": "none"})
        s.expect_status(r, 400, request="POST /api/credentials (bad id)")

    def credential_delete(c: Ctx) -> None:
        cid = c.get("credential")
        if not cid:
            raise Skip("no credential created")
        r = c.client.delete(f"/api/credentials/{q(cid)}")
        s.expect_status(r, 204, request=f"DELETE /api/credentials/{cid}")

    def credential_delete_unknown(c: Ctx) -> None:
        r = c.client.delete("/api/credentials/never-existed")
        s.expect_status(r, 404, request="DELETE /api/credentials/never-existed")

    def credential_in_use_is_refused(c: Ctx) -> None:
        # gitea is referenced by the w5forge repo.
        r = c.client.delete("/api/credentials/gitea")
        s.expect_status(r, 409, request="DELETE /api/credentials/gitea (in use)")

    # ---- network ---------------------------------------------------------

    def network_hosts(c: Ctx) -> None:
        r = c.client.get("/api/network")
        body = s.expect_ok(r, request="GET /api/network")
        s.expect_keys(body, ["processMode", "rows"], request="GET /api/network")
        s.expect(isinstance(body["rows"], list), "rows is not a list", response=body)

    def network_requests(c: Ctx) -> None:
        r = c.client.get("/api/network?view=requests")
        body = s.expect_ok(r, request="GET /api/network?view=requests")
        s.expect(isinstance(body, list), "requests is not a list", response=body)

    def network_agents(c: Ctx) -> None:
        r = c.client.get("/api/network?view=agents")
        body = s.expect_ok(r, request="GET /api/network?view=agents")
        s.expect(isinstance(body, list), "agents is not a list", response=body)

    def network_bad_view(c: Ctx) -> None:
        r = c.client.get("/api/network?view=nonsense")
        s.expect_status(r, 400, request="GET /api/network?view=nonsense")

    def network_pending(c: Ctx) -> None:
        r = c.client.get("/api/network/pending")
        body = s.expect_ok(r, request="GET /api/network/pending")
        s.expect_keys(body, ["pending"], request="GET /api/network/pending")

    def network_decision_unknown_agent(c: Ctx) -> None:
        r = c.client.post(
            "/api/network/decisions",
            {"agentId": "nope", "host": "example.com", "decision": "block"},
        )
        s.expect_status(r, 404, request="POST /api/network/decisions (unknown agent)")

    def network_decision_bad_host(c: Ctx) -> None:
        r = c.client.post(
            "/api/network/decisions",
            {"agentId": "nope", "host": "http://example.com/path", "decision": "block"},
        )
        s.expect_status(r, 400, request="POST /api/network/decisions (bad host)")

    def network_decision_wildcard_host(c: Ctx) -> None:
        r = c.client.post(
            "/api/network/decisions",
            {"agentId": "nope", "host": "*.example.com", "decision": "block"},
        )
        s.expect_status(r, 400, request="POST /api/network/decisions (wildcard host)")

    for name, fn in [
        ("secrets status", status),
        ("list secrets", list_secrets),
        ("secret round trip", roundtrip),
        ("a secret value has no route out", value_has_no_route_out),
        ("empty secret value is 400", empty_value_is_rejected),
        ("reserved secret path is 400", reserved_path_is_rejected),
        ("delete secret", delete),
        ("delete an unknown secret", delete_unknown),
        ("list credentials", credentials_list),
        ("credential round trip", credential_roundtrip),
        ("credential with a bad kind is 400", credential_invalid_kind),
        ("pat credential needs envVar", credential_pat_needs_env),
        ("ssh credential rejects metacharacters", credential_ssh_rejects_metacharacters),
        ("credential with a bad id is 400", credential_bad_id),
        ("delete credential", credential_delete),
        ("delete an unknown credential is 404", credential_delete_unknown),
        ("delete an in-use credential is 409", credential_in_use_is_refused),
        ("network hosts", network_hosts),
        ("network requests", network_requests),
        ("network agents", network_agents),
        ("network with a bad view is 400", network_bad_view),
        ("network pending", network_pending),
        ("network decision on an unknown agent", network_decision_unknown_agent),
        ("network decision with a bad host", network_decision_bad_host),
        ("network decision with a wildcard host", network_decision_wildcard_host),
    ]:
        s.check(name, fn)
