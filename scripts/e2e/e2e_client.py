"""HTTP client for the Marshal web-bridge end-to-end suite.

Standard library only, so the suite runs anywhere the bridge does. The
client mirrors the SPA's request helper (web/ui/src/lib/api.ts): bearer
token on every /api route, JSON bodies, and a parsed body that falls
back to raw text when the response is not JSON.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from typing import Any


@dataclass
class Response:
    """One HTTP exchange, successful or not."""

    status: int
    body: Any
    raw: str
    headers: dict[str, str] = field(default_factory=dict)

    @property
    def ok(self) -> bool:
        return 200 <= self.status < 300

    def header(self, name: str) -> str:
        """A response header, looked up case-insensitively.

        Go canonicalises header names (WWW-Authenticate arrives as
        Www-Authenticate) while urllib preserves whatever the server
        sent, so a plain dict lookup is not reliable.
        """
        want = name.lower()
        for k, v in self.headers.items():
            if k.lower() == want:
                return v
        return ""

    def error(self) -> str:
        """The bridge's error string, or the raw body."""
        if isinstance(self.body, dict) and "error" in self.body:
            return str(self.body["error"])
        return self.raw[:300]

    def __repr__(self) -> str:
        return f"<Response {self.status} {self.raw[:160]!r}>"


def _parse(raw: str) -> Any:
    if not raw:
        return None
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return raw


def q(value: str) -> str:
    """Percent-encode a path segment the way the SPA's q() does."""
    return urllib.parse.quote(value, safe="")


class Client:
    """A thin, synchronous HTTP client for the bridge."""

    def __init__(self, base: str, token: str, timeout: float = 30.0):
        self.base = base.rstrip("/")
        self.token = token
        self.timeout = timeout

    def request(
        self,
        method: str,
        path: str,
        body: Any = None,
        *,
        token: str | None = None,
        raw_body: bytes | None = None,
        content_type: str = "application/json",
        timeout: float | None = None,
        headers: dict[str, str] | None = None,
    ) -> Response:
        url = self.base + path
        data: bytes | None = None
        hdrs: dict[str, str] = dict(headers or {})
        if raw_body is not None:
            data = raw_body
            hdrs.setdefault("Content-Type", content_type)
        elif body is not None:
            data = json.dumps(body).encode()
            hdrs.setdefault("Content-Type", content_type)
        tok = self.token if token is None else token
        if tok:
            hdrs["Authorization"] = f"Bearer {tok}"
        req = urllib.request.Request(url, data=data, headers=hdrs, method=method)
        try:
            with urllib.request.urlopen(req, timeout=timeout or self.timeout) as resp:
                raw = resp.read().decode("utf-8", "replace")
                return Response(resp.status, _parse(raw), raw, dict(resp.headers))
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", "replace")
            return Response(exc.code, _parse(raw), raw, dict(exc.headers))
        except urllib.error.URLError as exc:
            return Response(0, None, f"transport error: {exc}", {})

    # Convenience wrappers -------------------------------------------------

    def get(self, path: str, **kw: Any) -> Response:
        return self.request("GET", path, **kw)

    def post(self, path: str, body: Any = None, **kw: Any) -> Response:
        return self.request("POST", path, body, **kw)

    def put(self, path: str, body: Any = None, **kw: Any) -> Response:
        return self.request("PUT", path, body, **kw)

    def delete(self, path: str, body: Any = None, **kw: Any) -> Response:
        return self.request("DELETE", path, body, **kw)
