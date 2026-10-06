"""Harness for the Marshal web-bridge end-to-end suite.

A check is a small function that exercises one bridge behaviour and
records a verdict. The harness runs checks in order, keeps going after a
failure, and prints a report grouped by area. Every failure is captured
with the request that produced it so it can be triaged as a defect.

Usage:
    python3 scripts/e2e/run_e2e.py --base http://127.0.0.1:7700 --token TOK
"""

from __future__ import annotations

import json
import sys
import time
import traceback
from dataclasses import dataclass, field
from typing import Any, Callable

from e2e_client import Client, Response


@dataclass
class Defect:
    """A check that failed, with enough context to triage it."""

    area: str
    name: str
    detail: str
    request: str = ""
    response: str = ""
    traceback: str = ""

    def render(self) -> str:
        out = [f"[{self.area}] {self.name}", f"    {self.detail}"]
        if self.request:
            out.append(f"    request : {self.request}")
        if self.response:
            out.append(f"    response: {self.response}")
        if self.traceback:
            out.append("    traceback:")
            out.extend("      " + line for line in self.traceback.splitlines())
        return "\n".join(out)


@dataclass
class Result:
    area: str
    name: str
    ok: bool
    detail: str = ""
    skipped: bool = False
    ms: float = 0.0


class Failure(Exception):
    """Raised by a check to record a defect with context."""

    def __init__(self, detail: str, *, request: str = "", response: Any = ""):
        super().__init__(detail)
        self.detail = detail
        self.request = request
        self.response = response if isinstance(response, str) else repr(response)


class Skip(Exception):
    """Raised by a check when a precondition is absent."""


@dataclass
class Ctx:
    """Everything a check needs: the client, the environment, and state."""

    client: Client
    env: "Env"
    state: dict[str, Any] = field(default_factory=dict)

    def get(self, key: str, default: Any = None) -> Any:
        return self.state.get(key, default)

    def set(self, key: str, value: Any) -> Any:
        self.state[key] = value
        return value


@dataclass
class Env:
    """The live environment the suite is pointed at."""

    base: str
    token: str
    project: str
    project2: str
    repo_id: str
    workspace: str
    agent_image: str
    forge_url: str
    preview_base: str = ""
    model: str = ""
    provider: str = ""

    def as_dict(self) -> dict[str, Any]:
        return {
            "base": self.base,
            "project": self.project,
            "project2": self.project2,
            "repoId": self.repo_id,
            "workspace": self.workspace,
            "agentImage": self.agent_image,
            "forgeUrl": self.forge_url,
            "previewBase": self.preview_base,
            "model": self.model,
            "provider": self.provider,
        }


class Suite:
    """Registers and runs checks, collecting results and defects."""

    def __init__(self, ctx: Ctx, *, verbose: bool = False):
        self.ctx = ctx
        self.verbose = verbose
        self.results: list[Result] = []
        self.defects: list[Defect] = []
        self._area = "general"

    def area(self, name: str) -> None:
        self._area = name

    def check(self, name: str, fn: Callable[[Ctx], None]) -> None:
        """Run one check, recording pass, skip or defect."""
        started = time.monotonic()
        try:
            fn(self.ctx)
        except Skip as exc:
            self.results.append(Result(self._area, name, True, str(exc), skipped=True))
            self._emit(f"  SKIP {name}: {exc}")
            return
        except Failure as exc:
            ms = (time.monotonic() - started) * 1000
            self.results.append(Result(self._area, name, False, exc.detail, ms=ms))
            self.defects.append(
                Defect(self._area, name, exc.detail, exc.request, exc.response)
            )
            self._emit(f"  FAIL {name}: {exc.detail}")
            return
        except Exception as exc:  # noqa: BLE001 - a crash is a defect too
            ms = (time.monotonic() - started) * 1000
            tb = traceback.format_exc()
            self.results.append(Result(self._area, name, False, f"crashed: {exc}", ms=ms))
            self.defects.append(Defect(self._area, name, f"crashed: {exc}", traceback=tb))
            self._emit(f"  FAIL {name}: crashed: {exc}")
            return
        ms = (time.monotonic() - started) * 1000
        self.results.append(Result(self._area, name, True, ms=ms))
        self._emit(f"  ok   {name} ({ms:.0f}ms)")

    def _emit(self, line: str) -> None:
        if self.verbose:
            print(line, flush=True)

    # Assertion helpers ----------------------------------------------------

    def expect(self, cond: bool, detail: str, *, request: str = "", response: Any = "") -> None:
        if not cond:
            raise Failure(detail, request=request, response=response)

    def expect_status(
        self, resp: Response, want: int | tuple[int, ...], *, request: str = ""
    ) -> None:
        wants = (want,) if isinstance(want, int) else want
        if resp.status not in wants:
            raise Failure(
                f"expected HTTP {want}, got {resp.status}",
                request=request,
                response=resp,
            )

    def expect_ok(self, resp: Response, *, request: str = "") -> Any:
        if not resp.ok:
            raise Failure(
                f"expected 2xx, got {resp.status}: {resp.error()}",
                request=request,
                response=resp,
            )
        return resp.body

    def expect_keys(self, body: Any, keys: list[str], *, request: str = "") -> None:
        if not isinstance(body, dict):
            raise Failure(f"expected a JSON object, got {type(body).__name__}", request=request, response=body)
        missing = [k for k in keys if k not in body]
        if missing:
            raise Failure(
                f"response is missing {missing}", request=request, response=body
            )

    # Reporting ------------------------------------------------------------

    def report(self) -> str:
        lines: list[str] = []
        passed = sum(1 for r in self.results if r.ok and not r.skipped)
        skipped = sum(1 for r in self.results if r.skipped)
        failed = sum(1 for r in self.results if not r.ok)
        lines.append("=" * 72)
        lines.append(
            f"E2E RESULT: {passed} passed, {failed} failed, {skipped} skipped "
            f"({len(self.results)} checks)"
        )
        lines.append("=" * 72)
        by_area: dict[str, list[Result]] = {}
        for r in self.results:
            by_area.setdefault(r.area, []).append(r)
        for area, rows in by_area.items():
            bad = sum(1 for r in rows if not r.ok)
            mark = "FAIL" if bad else "ok  "
            lines.append(f"{mark} {area}: {len(rows) - bad}/{len(rows)}")
        if self.defects:
            lines.append("")
            lines.append(f"DEFECTS ({len(self.defects)})")
            lines.append("-" * 72)
            for i, d in enumerate(self.defects, 1):
                lines.append(f"{i}. " + d.render())
                lines.append("")
        return "\n".join(lines)

    def json_report(self) -> str:
        return json.dumps(
            {
                "passed": sum(1 for r in self.results if r.ok and not r.skipped),
                "failed": sum(1 for r in self.results if not r.ok),
                "skipped": sum(1 for r in self.results if r.skipped),
                "checks": [
                    {
                        "area": r.area,
                        "name": r.name,
                        "ok": r.ok,
                        "skipped": r.skipped,
                        "detail": r.detail,
                        "ms": round(r.ms, 1),
                    }
                    for r in self.results
                ],
                "defects": [
                    {
                        "area": d.area,
                        "name": d.name,
                        "detail": d.detail,
                        "request": d.request,
                        "response": d.response,
                    }
                    for d in self.defects
                ],
            },
            indent=2,
        )


def wait_until(
    fn: Callable[[], bool],
    *,
    timeout: float = 60.0,
    interval: float = 1.0,
    what: str = "condition",
) -> bool:
    """Poll fn until it is true or the timeout expires."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if fn():
                return True
        except Exception:  # noqa: BLE001 - polling tolerates transient errors
            pass
        time.sleep(interval)
    return False


def eprint(*args: Any) -> None:
    print(*args, file=sys.stderr, flush=True)
