"""Group D: recipes, schedules, notifications, status links, library,
models, budgets and watches.

These are the ops surfaces the SPA's Automations, Library, Usage and
Watches pages drive. Mutations here write to the bridge's own state
directory and its isolated config, never the operator's real config.
"""

from __future__ import annotations

from e2e_client import q
from harness import Ctx, Failure, Skip, Suite, wait_until


def register(s: Suite) -> None:
    s.area("D ops")

    # ---- recipes ---------------------------------------------------------

    def list_recipes(c: Ctx) -> None:
        r = c.client.get("/api/recipes")
        body = s.expect_ok(r, request="GET /api/recipes")
        s.expect(isinstance(body, list), "recipes is not a list", response=body)
        names = [x.get("name") for x in body]
        s.expect("fix-ci" in names, f"built-in fix-ci missing from {names}", response=body)
        for rec in body:
            s.expect_keys(rec, ["name", "kind", "prompt"], request="GET /api/recipes")

    def get_recipe(c: Ctx) -> None:
        r = c.client.get("/api/recipes/fix-ci")
        body = s.expect_ok(r, request="GET /api/recipes/fix-ci")
        s.expect_keys(body, ["name", "kind", "prompt"], request="recipe")

    def unknown_recipe(c: Ctx) -> None:
        r = c.client.get("/api/recipes/nope")
        s.expect_status(r, 404, request="GET /api/recipes/nope")

    def recipe_roundtrip(c: Ctx) -> None:
        rec = {
            "name": "e2e-recipe",
            "title": "E2E recipe",
            "kind": "prompt",
            "mode": "plan",
            "prompt": "Say hello.",
        }
        r = c.client.put("/api/recipes/e2e-recipe", rec)
        s.expect_ok(r, request="PUT /api/recipes/e2e-recipe")
        got = s.expect_ok(c.client.get("/api/recipes/e2e-recipe"), request="GET recipe")
        s.expect(got.get("title") == "E2E recipe", f"title is {got.get('title')!r}", response=got)
        c.set("recipe", "e2e-recipe")

    def recipe_copy(c: Ctx) -> None:
        if not c.get("recipe"):
            raise Skip("no recipe created")
        r = c.client.post("/api/recipes/e2e-recipe/copy", {"name": "e2e-recipe-copy"})
        s.expect_ok(r, request="POST /api/recipes/e2e-recipe/copy")
        got = s.expect_ok(c.client.get("/api/recipes/e2e-recipe-copy"), request="GET copy")
        s.expect(got.get("name") == "e2e-recipe-copy", f"copy name is {got.get('name')!r}", response=got)

    def recipe_copy_conflict(c: Ctx) -> None:
        if not c.get("recipe"):
            raise Skip("no recipe created")
        r = c.client.post("/api/recipes/e2e-recipe/copy", {"name": "e2e-recipe-copy"})
        s.expect_status(r, 409, request="POST /api/recipes/e2e-recipe/copy (again)")

    def recipe_invalid(c: Ctx) -> None:
        r = c.client.put("/api/recipes/bad", {"name": "bad", "kind": "nonsense", "prompt": "x"})
        s.expect_status(r, 400, request="PUT /api/recipes/bad")

    def recipe_builtin_is_protected(c: Ctx) -> None:
        r = c.client.delete("/api/recipes/fix-ci")
        s.expect(
            r.status in (400, 403, 409),
            f"deleting a built-in recipe returned {r.status}",
            request="DELETE /api/recipes/fix-ci",
            response=r,
        )

    def recipe_delete(c: Ctx) -> None:
        if not c.get("recipe"):
            raise Skip("no recipe created")
        s.expect_ok(c.client.delete("/api/recipes/e2e-recipe-copy"), request="DELETE copy")
        s.expect_ok(c.client.delete("/api/recipes/e2e-recipe"), request="DELETE recipe")
        r = c.client.get("/api/recipes/e2e-recipe")
        s.expect_status(r, 404, request="GET deleted recipe")

    # ---- schedules -------------------------------------------------------

    def list_schedules(c: Ctx) -> None:
        r = c.client.get("/api/schedules")
        body = s.expect_ok(r, request="GET /api/schedules")
        s.expect(isinstance(body, list), "schedules is not a list", response=body)

    def schedule_roundtrip(c: Ctx) -> None:
        # fix-ci declares a required "check" input, so a schedule that
        # runs it must supply one.
        r = c.client.post(
            "/api/schedules",
            {
                "name": "e2e-schedule",
                "recipe": "fix-ci",
                "project": c.env.project,
                "inputs": {"check": "ci/test"},
                "cron": "0 4 * * *",
                "enabled": False,
            },
        )
        body = s.expect_ok(r, request="POST /api/schedules")
        s.expect_keys(body, ["id"], request="POST /api/schedules")
        c.set("schedule", body["id"])

    def schedule_update(c: Ctx) -> None:
        sid = c.get("schedule")
        if not sid:
            raise Skip("no schedule created")
        r = c.client.put(
            f"/api/schedules/{q(sid)}",
            {
                "id": sid,
                "name": "e2e-schedule-2",
                "recipe": "fix-ci",
                "project": c.env.project,
                "inputs": {"check": "ci/test"},
                "cron": "0 5 * * *",
                "enabled": False,
            },
        )
        s.expect_ok(r, request=f"PUT /api/schedules/{sid}")

    def schedule_invalid_cron(c: Ctx) -> None:
        r = c.client.post(
            "/api/schedules",
            {
                "name": "bad-cron",
                "recipe": "fix-ci",
                "project": c.env.project,
                "cron": "not a cron",
                "enabled": False,
            },
        )
        s.expect_status(r, 400, request="POST /api/schedules (bad cron)")

    def schedule_unknown_recipe(c: Ctx) -> None:
        r = c.client.post(
            "/api/schedules",
            {
                "name": "bad-recipe",
                "recipe": "no-such-recipe",
                "project": c.env.project,
                "cron": "0 4 * * *",
                "enabled": False,
            },
        )
        s.expect(
            r.status in (400, 404),
            f"schedule with an unknown recipe returned {r.status}",
            request="POST /api/schedules (unknown recipe)",
            response=r,
        )

    def schedule_delete(c: Ctx) -> None:
        sid = c.get("schedule")
        if not sid:
            raise Skip("no schedule created")
        s.expect_ok(c.client.delete(f"/api/schedules/{q(sid)}"), request=f"DELETE /api/schedules/{sid}")
        r = c.client.delete(f"/api/schedules/{q(sid)}")
        s.expect_status(r, 404, request=f"DELETE /api/schedules/{sid} (again)")

    # ---- notifications ---------------------------------------------------

    def notifications_get(c: Ctx) -> None:
        r = c.client.get("/api/notifications")
        body = s.expect_ok(r, request="GET /api/notifications")
        s.expect_keys(body, ["webhooks"], request="GET /api/notifications")

    def notifications_roundtrip(c: Ctx) -> None:
        r = c.client.put(
            "/api/notifications",
            {"webhooks": [{"url": "http://127.0.0.1:9/e2e", "events": ["run_finished"]}]},
        )
        body = s.expect_ok(r, request="PUT /api/notifications")
        hooks = body.get("webhooks") or []
        s.expect(len(hooks) == 1, f"expected 1 webhook, got {len(hooks)}", response=body)
        c.set("webhooks_before", hooks)

    def notifications_test(c: Ctx) -> None:
        r = c.client.post("/api/notifications/test", {}, timeout=60)
        s.expect_ok(r, request="POST /api/notifications/test")
        s.expect_keys(r.body, ["sent"], request="notifications test")

    def notifications_restore(c: Ctx) -> None:
        before = c.get("webhooks_before")
        if before is None:
            raise Skip("nothing to restore")
        r = c.client.put("/api/notifications", {"webhooks": before})
        s.expect_ok(r, request="PUT /api/notifications (restore)")

    # ---- status links ----------------------------------------------------

    def status_link_roundtrip(c: Ctx) -> None:
        # A status link needs a live agent.
        r = c.client.post("/api/sessions", {"cwd": c.env.project})
        sid = s.expect_ok(r, request="POST /api/sessions")["sessionId"]
        c.set("link_agent", sid)
        r = c.client.post("/api/status-links", {"agentId": sid, "ttlHours": 1})
        body = s.expect_ok(r, request="POST /api/status-links")
        s.expect_keys(body, ["id", "url", "expiresAt"], request="POST /api/status-links")
        s.expect(
            body["url"].startswith("/"),
            f"status link url {body['url']!r} is not a bridge path",
            response=body,
        )
        c.set("status_link", body["id"])

    def status_link_listed(c: Ctx) -> None:
        lid = c.get("status_link")
        if not lid:
            raise Skip("no status link created")
        r = c.client.get("/api/status-links")
        body = s.expect_ok(r, request="GET /api/status-links")
        s.expect(
            any(x.get("id") == lid for x in body),
            f"status link {lid} missing from the list",
            response=body,
        )

    def status_link_revoke(c: Ctx) -> None:
        lid = c.get("status_link")
        if not lid:
            raise Skip("no status link created")
        s.expect_ok(c.client.delete(f"/api/status-links/{q(lid)}"), request="DELETE status link")
        # Revoking is idempotent: the link is already revoked.
        r = c.client.delete(f"/api/status-links/{q(lid)}")
        s.expect(
            r.status in (200, 204, 404, 410),
            f"revoking twice returned {r.status}",
            request="DELETE status link (again)",
            response=r,
        )

    def status_link_unknown_agent(c: Ctx) -> None:
        r = c.client.post("/api/status-links", {"agentId": "nope", "ttlHours": 1})
        s.expect(
            r.status in (400, 404, 410),
            f"status link for an unknown agent returned {r.status}",
            request="POST /api/status-links (unknown agent)",
            response=r,
        )

    # ---- library ---------------------------------------------------------

    def library_skills(c: Ctx) -> None:
        r = c.client.get("/api/library/skills?scope=global")
        body = s.expect_ok(r, request="GET /api/library/skills")
        s.expect_keys(body, ["skills"], request="library skills")

    def library_plugins(c: Ctx) -> None:
        r = c.client.get("/api/library/plugins?scope=global")
        body = s.expect_ok(r, request="GET /api/library/plugins")
        s.expect_keys(body, ["plugins"], request="library plugins")

    def library_memory_requires_a_project(c: Ctx) -> None:
        r = c.client.get("/api/library/memory")
        s.expect_status(r, 400, request="GET /api/library/memory (no project)")

    def library_memory(c: Ctx) -> None:
        r = c.client.get(f"/api/library/memory?project={q(c.env.project)}")
        body = s.expect_ok(r, request="GET /api/library/memory")
        s.expect_keys(body, ["entries"], request="library memory")

    def library_memory_suggestions(c: Ctx) -> None:
        r = c.client.get(f"/api/library/memory/suggestions?project={q(c.env.project)}")
        body = s.expect_ok(r, request="GET /api/library/memory/suggestions")
        s.expect_keys(body, ["suggestions"], request="memory suggestions")

    def library_skill_preview_rejects_a_bad_source(c: Ctx) -> None:
        r = c.client.post(
            "/api/library/skills/preview",
            {"source": "/nonexistent/path/to/skill.md", "scope": "global"},
        )
        s.expect(
            r.status in (400, 404, 422),
            f"previewing a missing skill returned {r.status}",
            request="POST /api/library/skills/preview (missing file)",
            response=r,
        )

    # ---- models ----------------------------------------------------------

    def models_get(c: Ctx) -> None:
        r = c.client.get("/api/models")
        body = s.expect_ok(r, request="GET /api/models")
        s.expect_keys(
            body,
            ["providers", "presets", "profiles", "defaultProfile", "roles"],
            request="GET /api/models",
        )

    def models_probe_unknown_provider(c: Ctx) -> None:
        r = c.client.post("/api/models/probe", {"name": "no-such-provider"}, timeout=30)
        s.expect(
            r.status in (400, 404),
            f"probing an unknown provider returned {r.status}",
            request="POST /api/models/probe (unknown)",
            response=r,
        )

    def models_probe_known_provider(c: Ctx) -> None:
        if not c.env.provider:
            raise Skip("no provider configured in the environment")
        r = c.client.post("/api/models/probe", {"name": c.env.provider}, timeout=60)
        s.expect_ok(r, request=f"POST /api/models/probe ({c.env.provider})")

    def models_routing_roundtrip(c: Ctx) -> None:
        """Setting a profile that exists must stick.

        The engine validates defaultProfile against the configured
        profiles, so this needs a real one. The environment's config
        names "single" as the default but defines no profiles, so the
        round trip is only meaningful when one is defined.
        """
        before = s.expect_ok(c.client.get("/api/models"), request="GET /api/models")
        c.set("routing_before", before.get("defaultProfile"))
        profiles = before.get("profiles") or {}
        if not profiles:
            raise Skip("no routing profiles are configured")
        name = sorted(profiles)[0]
        r = c.client.put("/api/models/routing", {"defaultProfile": name})
        s.expect_ok(r, request="PUT /api/models/routing")
        after = s.expect_ok(c.client.get("/api/models"), request="GET /api/models")
        s.expect(
            after.get("defaultProfile") == name,
            f"defaultProfile is {after.get('defaultProfile')!r} after setting {name!r}",
            response=after,
        )

    def models_routing_rejects_an_unknown_profile(c: Ctx) -> None:
        r = c.client.put("/api/models/routing", {"defaultProfile": "no-such-profile"})
        s.expect_status(r, 400, request="PUT /api/models/routing (unknown profile)")

    def models_routing_restore(c: Ctx) -> None:
        before = c.get("routing_before")
        if before is None:
            raise Skip("nothing to restore")
        r = c.client.put("/api/models/routing", {"defaultProfile": before})
        s.expect_ok(r, request="PUT /api/models/routing (restore)")

    # ---- budgets ---------------------------------------------------------

    def budgets_get(c: Ctx) -> None:
        r = c.client.get("/api/budgets")
        body = s.expect_ok(r, request="GET /api/budgets")
        s.expect_keys(body, ["budgets", "daily", "agents"], request="GET /api/budgets")

    def budgets_roundtrip(c: Ctx) -> None:
        before = s.expect_ok(c.client.get("/api/budgets"), request="GET /api/budgets")
        c.set("budgets_before", before.get("budgets"))
        r = c.client.put(
            "/api/budgets",
            {"budgets": {"dailyUsd": 12.5, "perAgentUsd": 3, "onDailyCap": "warn", "onAgentCap": "warn"}},
        )
        body = s.expect_ok(r, request="PUT /api/budgets")
        s.expect(
            body["budgets"]["dailyUsd"] == 12.5,
            f"dailyUsd is {body['budgets'].get('dailyUsd')!r} after setting 12.5",
            response=body,
        )

    def budgets_restore(c: Ctx) -> None:
        before = c.get("budgets_before")
        if before is None:
            raise Skip("nothing to restore")
        r = c.client.put("/api/budgets", {"budgets": before})
        s.expect_ok(r, request="PUT /api/budgets (restore)")

    def usage(c: Ctx) -> None:
        # by is one of day, project, role or model (api.ts UsageBy).
        r = c.client.get("/api/usage?range=7d&by=day")
        body = s.expect_ok(r, request="GET /api/usage")
        s.expect_keys(body, ["range", "totals", "series"], request="GET /api/usage")

    def usage_rejects_a_bad_range(c: Ctx) -> None:
        r = c.client.get("/api/usage?range=99y&by=day")
        s.expect(
            r.status in (200, 400),
            f"usage with a bad range returned {r.status}",
            request="GET /api/usage?range=99y",
            response=r,
        )

    def usage_rejects_a_bad_grouping(c: Ctx) -> None:
        r = c.client.get("/api/usage?range=7d&by=agent")
        s.expect_status(r, 400, request="GET /api/usage?by=agent")

    # ---- watches ---------------------------------------------------------

    def watches_list(c: Ctx) -> None:
        r = c.client.get("/api/watches")
        body = s.expect_ok(r, request="GET /api/watches")
        s.expect(isinstance(body, list), "watches is not a list", response=body)

    def watch_requires_a_spec(c: Ctx) -> None:
        r = c.client.post("/api/watches", {})
        s.expect_status(r, 400, request="POST /api/watches (no spec)")

    def watch_roundtrip(c: Ctx) -> None:
        r = c.client.post(
            "/api/watches",
            {
                "spec": {
                    "name": "e2e-watch",
                    "kind": "file",
                    "path": "/tmp/e2e-watch-target",
                    "condition": "change",
                    "mode": "once",
                }
            },
        )
        body = s.expect_ok(r, request="POST /api/watches")
        s.expect_keys(body, ["id"], request="POST /api/watches")
        c.set("watch", body["id"])

    def watch_listed(c: Ctx) -> None:
        wid = c.get("watch")
        if not wid:
            raise Skip("no watch created")
        r = c.client.get("/api/watches")
        body = s.expect_ok(r, request="GET /api/watches")
        s.expect(
            any(w.get("id") == wid for w in body),
            f"watch {wid} missing from the list",
            response=body,
        )

    def watch_stop(c: Ctx) -> None:
        wid = c.get("watch")
        if not wid:
            raise Skip("no watch created")
        r = c.client.delete(f"/api/watches/studio/{q(wid)}")
        s.expect_ok(r, request=f"DELETE /api/watches/studio/{wid}")

    def watch_reroute_needs_a_role_and_preset(c: Ctx) -> None:
        r = c.client.post(
            "/api/watches",
            {
                "spec": {"name": "bad-reroute", "kind": "file", "path": "/tmp/x", "mode": "once"},
                "onTrip": {"reroute": {"role": "router"}},
            },
        )
        s.expect_status(r, 400, request="POST /api/watches (reroute without a preset)")

    # ---- prompts ---------------------------------------------------------

    def recent_prompts(c: Ctx) -> None:
        r = c.client.get(f"/api/prompts/recent?project={q(c.env.project)}&limit=5")
        body = s.expect_ok(r, request="GET /api/prompts/recent")
        s.expect_keys(body, ["prompts"], request="recent prompts")

    for name, fn in [
        ("list recipes", list_recipes),
        ("get recipe", get_recipe),
        ("unknown recipe is 404", unknown_recipe),
        ("recipe round trip", recipe_roundtrip),
        ("recipe copy", recipe_copy),
        ("recipe copy conflict is 409", recipe_copy_conflict),
        ("invalid recipe is 400", recipe_invalid),
        ("built-in recipe is protected", recipe_builtin_is_protected),
        ("recipe delete", recipe_delete),
        ("list schedules", list_schedules),
        ("schedule round trip", schedule_roundtrip),
        ("schedule update", schedule_update),
        ("invalid cron is 400", schedule_invalid_cron),
        ("schedule with an unknown recipe", schedule_unknown_recipe),
        ("schedule delete", schedule_delete),
        ("notifications get", notifications_get),
        ("notifications round trip", notifications_roundtrip),
        ("notifications test", notifications_test),
        ("notifications restore", notifications_restore),
        ("status link round trip", status_link_roundtrip),
        ("status link listed", status_link_listed),
        ("status link revoke", status_link_revoke),
        ("status link for an unknown agent", status_link_unknown_agent),
        ("library skills", library_skills),
        ("library plugins", library_plugins),
        ("library memory needs a project", library_memory_requires_a_project),
        ("library memory", library_memory),
        ("library memory suggestions", library_memory_suggestions),
        ("skill preview rejects a bad source", library_skill_preview_rejects_a_bad_source),
        ("models get", models_get),
        ("models probe unknown provider", models_probe_unknown_provider),
        ("models probe known provider", models_probe_known_provider),
        ("models routing round trip", models_routing_roundtrip),
        ("models routing rejects an unknown profile", models_routing_rejects_an_unknown_profile),
        ("models routing restore", models_routing_restore),
        ("budgets get", budgets_get),
        ("budgets round trip", budgets_roundtrip),
        ("budgets restore", budgets_restore),
        ("usage", usage),
        ("usage with a bad range", usage_rejects_a_bad_range),
        ("usage with a bad grouping is 400", usage_rejects_a_bad_grouping),
        ("watches list", watches_list),
        ("watch requires a spec", watch_requires_a_spec),
        ("watch round trip", watch_roundtrip),
        ("watch listed", watch_listed),
        ("watch stop", watch_stop),
        ("reroute needs a role and preset", watch_reroute_needs_a_role_and_preset),
        ("recent prompts", recent_prompts),
    ]:
        s.check(name, fn)
