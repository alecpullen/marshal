"""HTTP client for the Marshal end-to-end suite.

Standard library only, so the suite runs anywhere the bridge does. The
client mirrors the Studio's request helper: JSON bodies, and a parsed body
that falls back to raw text when the response is not JSON.

The daemon's web listener serves the API itself and asks the caller for no
credential, so nothing here sends one. A base is either `unix:<path>` — every
request then goes over that socket, with the request target left as the URL's
path and query — or an `http://host:port` URL on a loopback address.
"""

from __future__ import annotations

import http.client
import json
import socket
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from typing import Any

# The host a request over a unix socket names. A socket request has no real
# one, and the web listener answers only a local Host.
_UNIX_HOST = "localhost"

# A base starting with this names a unix socket instead of a URL.
_UNIX_PREFIX = "unix:"


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


class _UnixHTTPConnection(http.client.HTTPConnection):
    """An HTTPConnection whose connect() dials a unix socket.

    The request target stays the URL's path and query: a socket has no host
    to put in an absolute form, and the listener parses a plain path.
    """

    def __init__(self, path: str, timeout: float = 30.0):
        super().__init__(_UNIX_HOST, timeout=timeout)
        self._socket_path = path

    def connect(self) -> None:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(self.timeout)
        sock.connect(self._socket_path)
        self.sock = sock


def _first_bytes(reader: Any, peek: int) -> str:
    """The first bytes of a stream, tolerating a route with nothing to send.

    An event stream holds its connection open with nothing more to send, so
    a read that reaches the timeout is the route working, not a transport
    failure: the status and headers it already sent still stand.
    """
    try:
        return reader.read(peek).decode("utf-8", "replace")
    except (OSError, http.client.HTTPException):
        return ""


class Client:
    """A thin, synchronous HTTP client for the web listener."""

    def __init__(self, base: str, timeout: float = 30.0):
        base = base.strip()
        self.socket_path: str | None = None
        if base.startswith(_UNIX_PREFIX):
            self.socket_path = base[len(_UNIX_PREFIX) :]
            if not self.socket_path:
                raise ValueError("a unix base needs a socket path: unix:/path/web.sock")
            self.base = base
        else:
            self.base = base.rstrip("/")
        self.timeout = timeout

    def request(
        self,
        method: str,
        path: str,
        body: Any = None,
        *,
        raw_body: bytes | None = None,
        content_type: str = "application/json",
        timeout: float | None = None,
        headers: dict[str, str] | None = None,
    ) -> Response:
        data: bytes | None = None
        hdrs: dict[str, str] = dict(headers or {})
        if raw_body is not None:
            data = raw_body
            hdrs.setdefault("Content-Type", content_type)
        elif body is not None:
            data = json.dumps(body).encode()
            hdrs.setdefault("Content-Type", content_type)
        wait = timeout or self.timeout
        if self.socket_path is not None:
            return self._socket_request(method, path, data, hdrs, wait)
        req = urllib.request.Request(self.base + path, data=data, headers=hdrs, method=method)
        try:
            with urllib.request.urlopen(req, timeout=wait) as resp:
                raw = resp.read().decode("utf-8", "replace")
                return Response(resp.status, _parse(raw), raw, dict(resp.headers))
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", "replace")
            return Response(exc.code, _parse(raw), raw, dict(exc.headers))
        except urllib.error.URLError as exc:
            return Response(0, None, f"transport error: {exc}", {})

    def stream(
        self, path: str, *, timeout: float | None = None, peek: int = 64
    ) -> Response:
        """Open a streaming route: its status, headers and first bytes.

        The body is never read to the end — an event stream stays open — so
        the caller sees the answer the route gave and the event-stream
        content type, which is all a stream check needs.
        """
        wait = timeout or self.timeout
        if self.socket_path is not None:
            conn = _UnixHTTPConnection(self.socket_path, timeout=wait)
            try:
                conn.request("GET", path)
                resp = conn.getresponse()
                return Response(
                    resp.status,
                    None,
                    _first_bytes(resp, peek),
                    dict(resp.getheaders()),
                )
            except (OSError, http.client.HTTPException) as exc:
                return Response(0, None, f"transport error: {exc}", {})
            finally:
                conn.close()
        req = urllib.request.Request(self.base + path, method="GET")
        try:
            resp = urllib.request.urlopen(req, timeout=wait)
        except urllib.error.HTTPError as exc:
            raw = _first_bytes(exc, peek)
            return Response(exc.code, _parse(raw), raw, dict(exc.headers))
        except OSError as exc:  # URLError and a connect timeout alike
            return Response(0, None, f"transport error: {exc}", {})
        try:
            raw = _first_bytes(resp, peek)
            return Response(resp.status, _parse(raw), raw, dict(resp.headers))
        finally:
            resp.close()

    def _socket_request(
        self, method: str, target: str, data: bytes | None, hdrs: dict[str, str], wait: float
    ) -> Response:
        """One exchange over the unix socket at socket_path."""
        conn = _UnixHTTPConnection(self.socket_path or "", timeout=wait)
        try:
            conn.request(method, target, body=data, headers=hdrs)
            resp = conn.getresponse()
            raw = resp.read().decode("utf-8", "replace")
            return Response(resp.status, _parse(raw), raw, dict(resp.getheaders()))
        except (OSError, http.client.HTTPException) as exc:
            return Response(0, None, f"transport error: {exc}", {})
        finally:
            conn.close()

    # Convenience wrappers -------------------------------------------------

    def get(self, path: str, **kw: Any) -> Response:
        return self.request("GET", path, **kw)

    def post(self, path: str, body: Any = None, **kw: Any) -> Response:
        return self.request("POST", path, body, **kw)

    def put(self, path: str, body: Any = None, **kw: Any) -> Response:
        return self.request("PUT", path, body, **kw)

    def delete(self, path: str, body: Any = None, **kw: Any) -> Response:
        return self.request("DELETE", path, body, **kw)
