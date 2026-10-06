"""Group G: forge.

Repos, issues, the review bot's drafts and the CI fixer's history. The
read paths run against the live forge; mutations create and remove a
throwaway repo so the real one is untouched.
"""

from __future__ import annotations

from e2e_client import q
from harness import Ctx, Failure, Skip, Suite, wait_until


def register(s: Suite) -> None:
    s.area("G forge")

    # ---- repos -----------------------------------------------------------

    def list_repos(c: Ctx) -> None:
        r = c.client.get("/api/repos")
        body = s.expect_ok(r, request="GET /api/repos")
        s.expect(isinstance(body, list), "repos is not a list", response=body)
        ids = [x.get("id") for x in body]
        s.expect(
            c.env.repo_id in ids,
            f"configured repo {c.env.repo_id} missing from {ids}",
            response=body,
        )

    def register_repo(c: Ctx) -> None:
        r = c.client.post(
            "/api/repos",
            {
                "id": "e2e-repo",
                "url": "https://example.invalid/e2e/repo.git",
                "branch": "main",
            },
        )
        body = s.expect_ok(r, request="POST /api/repos")
        s.expect(body.get("id") == "e2e-repo", f"id is {body.get('id')!r}", response=body)
        c.set("repo", "e2e-repo")

    def register_repo_bad_id(c: Ctx) -> None:
        r = c.client.post("/api/repos", {"id": "bad id!", "url": "https://example.invalid/x.git"})
        s.expect_status(r, 400, request="POST /api/repos (bad id)")

    def register_repo_missing_url(c: Ctx) -> None:
        r = c.client.post("/api/repos", {"id": "e2e-nourl"})
        s.expect_status(r, 400, request="POST /api/repos (no url)")

    def register_repo_watch_without_forge(c: Ctx) -> None:
        r = c.client.post(
            "/api/repos",
            {"id": "e2e-watch", "url": "https://example.invalid/x.git", "watch": True},
        )
        s.expect_status(r, 400, request="POST /api/repos (watch without forge)")

    def register_repo_unknown_forge(c: Ctx) -> None:
        r = c.client.post(
            "/api/repos",
            {"id": "e2e-forge", "url": "https://example.invalid/x.git", "forge": "bitbucket"},
        )
        s.expect_status(r, 400, request="POST /api/repos (unknown forge)")

    def register_repo_unknown_cred(c: Ctx) -> None:
        r = c.client.post(
            "/api/repos",
            {"id": "e2e-cred", "url": "https://example.invalid/x.git", "credRef": "nope"},
        )
        s.expect_status(r, 400, request="POST /api/repos (unknown credential)")

    def remove_repo(c: Ctx) -> None:
        rid = c.get("repo")
        if not rid:
            raise Skip("no repo registered")
        r = c.client.delete(f"/api/repos/{q(rid)}")
        s.expect_ok(r, request=f"DELETE /api/repos/{rid}")

    def remove_unknown_repo(c: Ctx) -> None:
        r = c.client.delete("/api/repos/never-existed")
        s.expect_status(r, 404, request="DELETE /api/repos/never-existed")

    def remove_in_use_repo(c: Ctx) -> None:
        # The forge agents were spawned from w5forge.
        r = c.client.delete(f"/api/repos/{q(c.env.repo_id)}")
        s.expect_status(r, 409, request=f"DELETE /api/repos/{c.env.repo_id} (in use)")

    # ---- issues ----------------------------------------------------------

    def list_issues(c: Ctx) -> None:
        r = c.client.get(f"/api/repos/{q(c.env.repo_id)}/issues", timeout=60)
        body = s.expect_ok(r, request=f"GET /api/repos/{c.env.repo_id}/issues")
        s.expect(isinstance(body, list), "issues is not a list", response=body)

    def list_issues_unknown_repo(c: Ctx) -> None:
        r = c.client.get("/api/repos/nope/issues", timeout=30)
        s.expect_status(r, 404, request="GET /api/repos/nope/issues")

    def spawn_from_issue_bad_number(c: Ctx) -> None:
        r = c.client.post(f"/api/repos/{q(c.env.repo_id)}/issues/notanumber/spawn")
        s.expect_status(r, 400, request="POST /api/repos/.../issues/notanumber/spawn")

    def spawn_from_issue_unknown_repo(c: Ctx) -> None:
        r = c.client.post("/api/repos/nope/issues/1/spawn")
        s.expect_status(r, 404, request="POST /api/repos/nope/issues/1/spawn")

    # ---- review bot ------------------------------------------------------

    def list_drafts(c: Ctx) -> None:
        r = c.client.get("/api/automations/review/drafts")
        body = s.expect_ok(r, request="GET /api/automations/review/drafts")
        s.expect_keys(body, ["drafts"], request="review drafts")
        drafts = body["drafts"] or []
        if drafts:
            c.set("draft", drafts[0]["id"])

    def get_draft(c: Ctx) -> None:
        did = c.get("draft")
        if not did:
            raise Skip("no review draft exists")
        r = c.client.get(f"/api/automations/review/drafts/{q(did)}")
        body = s.expect_ok(r, request=f"GET /api/automations/review/drafts/{did}")
        s.expect_keys(body, ["id", "repoId", "number", "findings"], request="review draft")

    def get_unknown_draft(c: Ctx) -> None:
        r = c.client.get("/api/automations/review/drafts/nope")
        s.expect_status(r, 404, request="GET /api/automations/review/drafts/nope")

    def edit_unknown_draft(c: Ctx) -> None:
        r = c.client.put(
            "/api/automations/review/drafts/nope", {"findings": [], "summary": "x"}
        )
        s.expect_status(r, 404, request="PUT /api/automations/review/drafts/nope")

    def discard_unknown_draft(c: Ctx) -> None:
        r = c.client.post("/api/automations/review/drafts/nope/discard")
        s.expect_status(r, 404, request="POST /api/automations/review/drafts/nope/discard")

    def post_unknown_draft(c: Ctx) -> None:
        r = c.client.post("/api/automations/review/drafts/nope/post")
        s.expect_status(r, 404, request="POST /api/automations/review/drafts/nope/post")

    def send_to_author_unknown_draft(c: Ctx) -> None:
        r = c.client.post(
            "/api/automations/review/drafts/nope/send-to-author", {"findingIds": ["f1"]}
        )
        s.expect_status(r, 404, request="POST /api/automations/review/drafts/nope/send-to-author")

    def run_review_bot_unknown_repo(c: Ctx) -> None:
        r = c.client.post("/api/automations/review/run", {"repoId": "nope", "number": 1})
        s.expect_status(r, 404, request="POST /api/automations/review/run (unknown repo)")

    def run_review_bot_bad_number(c: Ctx) -> None:
        r = c.client.post(
            "/api/automations/review/run", {"repoId": c.env.repo_id, "number": 0}
        )
        s.expect(
            r.status in (400, 404, 409),
            f"review bot with number 0 returned {r.status}",
            request="POST /api/automations/review/run (number 0)",
            response=r,
        )

    # ---- CI fixer --------------------------------------------------------

    def ci_history(c: Ctx) -> None:
        r = c.client.get("/api/automations/ci/history")
        body = s.expect_ok(r, request="GET /api/automations/ci/history")
        s.expect_keys(body, ["history"], request="ci history")
        hist = body["history"] or []
        if hist:
            c.set("ci_run", hist[0]["id"])

    def ci_history_detail(c: Ctx) -> None:
        rid = c.get("ci_run")
        if not rid:
            raise Skip("no CI run recorded")
        r = c.client.get(f"/api/automations/ci/history/{q(rid)}")
        body = s.expect_ok(r, request=f"GET /api/automations/ci/history/{rid}")
        s.expect_keys(body, ["id", "repoId", "status"], request="ci history detail")

    def ci_history_unknown(c: Ctx) -> None:
        r = c.client.get("/api/automations/ci/history/nope")
        s.expect_status(r, 404, request="GET /api/automations/ci/history/nope")

    # ---- webhook secret --------------------------------------------------

    def webhook_secret_unknown_repo(c: Ctx) -> None:
        r = c.client.post("/api/repos/nope/webhook-secret")
        s.expect_status(r, 404, request="POST /api/repos/nope/webhook-secret")

    def webhook_secret(c: Ctx) -> None:
        r = c.client.post(f"/api/repos/{q(c.env.repo_id)}/webhook-secret")
        if r.status == 409:
            raise Skip(f"webhook secret already in use: {r.error()}")
        body = s.expect_ok(r, request=f"POST /api/repos/{c.env.repo_id}/webhook-secret")
        s.expect_keys(body, ["secret"], request="webhook secret")
        s.expect(
            len(body["secret"]) >= 16,
            f"webhook secret is only {len(body['secret'])} chars",
            response=body,
        )

    for name, fn in [
        ("list repos", list_repos),
        ("register repo", register_repo),
        ("repo with a bad id is 400", register_repo_bad_id),
        ("repo without a url is 400", register_repo_missing_url),
        ("watch without a forge is 400", register_repo_watch_without_forge),
        ("repo with an unknown forge is 400", register_repo_unknown_forge),
        ("repo with an unknown credential is 400", register_repo_unknown_cred),
        ("remove repo", remove_repo),
        ("remove an unknown repo is 404", remove_unknown_repo),
        ("remove an in-use repo is 409", remove_in_use_repo),
        ("list issues", list_issues),
        ("list issues for an unknown repo is 404", list_issues_unknown_repo),
        ("spawn from an issue with a bad number", spawn_from_issue_bad_number),
        ("spawn from an issue on an unknown repo", spawn_from_issue_unknown_repo),
        ("list review drafts", list_drafts),
        ("get review draft", get_draft),
        ("unknown review draft is 404", get_unknown_draft),
        ("edit an unknown draft is 404", edit_unknown_draft),
        ("discard an unknown draft is 404", discard_unknown_draft),
        ("post an unknown draft is 404", post_unknown_draft),
        ("send an unknown draft to the author is 404", send_to_author_unknown_draft),
        ("review bot on an unknown repo is 404", run_review_bot_unknown_repo),
        ("review bot with a bad number", run_review_bot_bad_number),
        ("CI history", ci_history),
        ("CI history detail", ci_history_detail),
        ("unknown CI history is 404", ci_history_unknown),
        ("webhook secret on an unknown repo is 404", webhook_secret_unknown_repo),
        ("issue a webhook secret", webhook_secret),
    ]:
        s.check(name, fn)
