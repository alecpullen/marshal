#!/usr/bin/env python3
"""Marshal web-bridge end-to-end suite.

Drives the live bridge over HTTP the way the SPA does, across every
route group, and reports defects with the request that produced them.

    python3 scripts/e2e/run_e2e.py \
        --base http://127.0.0.1:7700 --token TOK \
        --project /tmp/w5proj --project2 /tmp/w5proj2 \
        --repo w5forge --workspace w5prev

Groups are selected with --groups (default: all). A group that needs a
precondition the environment lacks skips its checks rather than failing.
"""

from __future__ import annotations

import argparse
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from e2e_client import Client  # noqa: E402
from harness import Ctx, Env, Suite  # noqa: E402

import checks_agents  # noqa: E402
import checks_errors  # noqa: E402
import checks_forge  # noqa: E402
import checks_ops  # noqa: E402
import checks_secrets  # noqa: E402
import checks_sessions  # noqa: E402
import checks_spa  # noqa: E402
import checks_terminal  # noqa: E402
import checks_workspaces  # noqa: E402

GROUPS = {
    "A": ("sessions", checks_sessions),
    "B": ("agents", checks_agents),
    "C": ("terminal", checks_terminal),
    "D": ("ops", checks_ops),
    "E": ("workspaces", checks_workspaces),
    "F": ("secrets", checks_secrets),
    "G": ("forge", checks_forge),
    "H": ("errors", checks_errors),
    "I": ("spa and auth", checks_spa),
}


def parse_args(argv: list[str]) -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--base", default=os.environ.get("E2E_BASE", "http://127.0.0.1:7700"))
    p.add_argument("--token", default=os.environ.get("E2E_TOKEN", ""))
    p.add_argument("--project", default=os.environ.get("E2E_PROJECT", "/tmp/w5proj"))
    p.add_argument("--project2", default=os.environ.get("E2E_PROJECT2", "/tmp/w5proj2"))
    p.add_argument("--repo", default=os.environ.get("E2E_REPO", "w5forge"))
    p.add_argument("--workspace", default=os.environ.get("E2E_WORKSPACE", "w5prev"))
    p.add_argument("--agent-image", default=os.environ.get("E2E_AGENT_IMAGE", ""))
    p.add_argument("--forge-url", default=os.environ.get("E2E_FORGE_URL", ""))
    p.add_argument("--provider", default=os.environ.get("E2E_PROVIDER", ""))
    p.add_argument("--model", default=os.environ.get("E2E_MODEL", ""))
    p.add_argument(
        "--groups",
        default="ABCDEFGHI",
        help="which groups to run, e.g. ABH (default: all)",
    )
    p.add_argument("--json", dest="json_out", default="", help="write a JSON report here")
    p.add_argument("-v", "--verbose", action="store_true", help="print each check as it runs")
    p.add_argument("--timeout", type=float, default=30.0, help="per-request timeout")
    return p.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    if not args.token:
        print("error: --token (or E2E_TOKEN) is required", file=sys.stderr)
        return 2

    env = Env(
        base=args.base,
        token=args.token,
        project=args.project,
        project2=args.project2,
        repo_id=args.repo,
        workspace=args.workspace,
        agent_image=args.agent_image,
        forge_url=args.forge_url,
        provider=args.provider,
        model=args.model,
    )
    client = Client(args.base, args.token, timeout=args.timeout)
    ctx = Ctx(client=client, env=env)
    suite = Suite(ctx, verbose=args.verbose)

    # A reachable bridge is a precondition for everything.
    probe = client.get("/api/config")
    if not probe.ok:
        print(
            f"error: bridge at {args.base} is not answering /api/config "
            f"({probe.status}: {probe.error()})",
            file=sys.stderr,
        )
        return 2

    selected = [g for g in args.groups.upper() if g in GROUPS]
    unknown = [g for g in args.groups.upper() if g not in GROUPS]
    if unknown:
        print(f"warning: ignoring unknown groups {unknown}", file=sys.stderr)

    print(f"Marshal E2E — {args.base} — groups {''.join(selected)}")
    print(f"  project={env.project} project2={env.project2} repo={env.repo_id} ws={env.workspace}")
    print("-" * 72)
    started = time.monotonic()
    for g in selected:
        label, module = GROUPS[g]
        print(f"[{g}] {label}")
        module.register(suite)
    elapsed = time.monotonic() - started

    print()
    print(suite.report())
    print(f"elapsed: {elapsed:.1f}s")
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as fh:
            fh.write(suite.json_report())
        print(f"json report: {args.json_out}")
    return 1 if suite.defects else 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
