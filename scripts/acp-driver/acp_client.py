#!/usr/bin/env python3
"""Reusable ACP v1 JSON-RPC client for `marshal acp`.

This module wraps the headless ACP stdio transport in a testable class that
exposes every ACP method Marshal currently supports:

- initialize
- session/new, session/load, session/resume, session/list, session/delete, session/close
- session/prompt, session/cancel
- session/update notifications
- session/request_permission outbound requests (with a pluggable policy)

It is designed for two use cases:

1. **Interactive scripting** — write Python scripts that drive Marshal through
   its ACP protocol to implement plans, run reviews, or query state.
2. **Testing Marshal itself** — write pytest-style tests that start Marshal,
   send ACP frames, and assert on responses, notifications, and side effects.

Basic usage:

    from acp_client import ACPClient, TextBlock

    client = ACPClient()
    client.start()
    try:
        info = client.initialize()
        session = client.session_new(cwd="/path/to/project")
        reply, result = client.prompt(session.session_id, [TextBlock("hello")])
        print(reply)
    finally:
        client.close()
"""

from __future__ import annotations

import json
import os
import subprocess
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Dict, List, Optional, Tuple, Union


# ---------------------------------------------------------------------------
# Public data types
# ---------------------------------------------------------------------------

@dataclass
class ACPError(Exception):
    """Raised when an ACP request returns a JSON-RPC error."""
    code: int
    message: str
    data: Any = None


@dataclass
class SessionResponse:
    session_id: str


@dataclass
class PromptResult:
    stop_reason: str


@dataclass
class SessionListPage:
    sessions: List[Dict[str, Any]]
    next_cursor: Optional[str] = None


@dataclass
class TextBlock:
    text: str

    def to_acp(self) -> Dict[str, str]:
        return {"type": "text", "text": self.text}


@dataclass
class ResourceLinkBlock:
    name: str
    uri: str

    def to_acp(self) -> Dict[str, str]:
        return {"type": "resource_link", "name": self.name, "uri": self.uri}


ContentBlock = Union["TextBlock", "ResourceLinkBlock"]


# ---------------------------------------------------------------------------
# Permission policy
# ---------------------------------------------------------------------------

class PermissionPolicy:
    """Decides how to answer outbound `session/request_permission` frames.

    Subclass this to implement custom approval logic. The default
    `AutoApprovePolicy` approves every request and sets `always=True` for
    read-only and safe shell commands.
    """

    def decide(self, request: Dict[str, Any]) -> Dict[str, Any]:
        raise NotImplementedError


class AutoApprovePolicy(PermissionPolicy):
    """Auto-approve all permission requests.

    Sets `always=True` for known safe tool patterns so Marshal does not
    re-prompt on every similar call.
    """

    SAFE_TOOLS = {"file.read", "search.grep", "repo.map", "symbols.find"}
    SAFE_SHELL_PREFIXES = (
        "go test", "go build", "gofmt", "go vet",
        "git status", "git diff", "git log", "git add", "git commit",
    )

    def decide(self, request: Dict[str, Any]) -> Dict[str, Any]:
        tool = request.get("toolName", "")
        command = request.get("command", "")
        decision: Dict[str, Any] = {"approved": True}
        if tool in self.SAFE_TOOLS:
            decision["always"] = True
        elif tool == "shell.run" and any(
            command.strip().startswith(p) for p in self.SAFE_SHELL_PREFIXES
        ):
            decision["always"] = True
        return decision


class DenyAllPolicy(PermissionPolicy):
    """Deny every permission request. Useful for tests that assert Marshal
    handles denials correctly."""

    def decide(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return {"approved": False}


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------

class _Waiter:
    __slots__ = ("event", "result")

    def __init__(self) -> None:
        self.event = threading.Event()
        self.result: Optional[Dict[str, Any]] = None


# ---------------------------------------------------------------------------
# ACP client
# ---------------------------------------------------------------------------

class ACPClient:
    """Headless ACP v1 client for Marshal.

    The client spawns `marshal acp` as a subprocess, speaks JSON-RPC 2.0 over
    stdio, and correlates responses by id. Notifications and outbound
    permission requests are dispatched to registered handlers.

    All public request methods block until a matching response is received or
    the timeout expires.

    Args:
        marshal_bin: Path to the `marshal` executable. Defaults to the one
            built at `/tmp/kilo/marshal` so scripts can run without installing.
        cwd: Working directory passed to the Marshal runtime. Defaults to the
            current working directory.
        env: Extra environment variables merged into the subprocess env.
        permission_policy: Policy used to answer outbound permission requests.
        log_callback: Optional callable that receives every log line. If None,
            log lines are printed to stdout.
    """

    DEFAULT_BIN = Path("/tmp/kilo/marshal")

    def __init__(
        self,
        *,
        marshal_bin: Path | str = DEFAULT_BIN,
        cwd: Path | str | None = None,
        env: Optional[Dict[str, str]] = None,
        permission_policy: PermissionPolicy | None = None,
        log_callback: Callable[[str], None] | None = None,
    ) -> None:
        self.marshal_bin = Path(marshal_bin)
        self.cwd = Path(cwd) if cwd else Path.cwd()
        self.extra_env = env or {}
        self.permission_policy = permission_policy or AutoApprovePolicy()
        self.log_callback = log_callback

        self._proc: Optional[subprocess.Popen] = None
        self._reader_thread: Optional[threading.Thread] = None
        self._stderr_thread: Optional[threading.Thread] = None
        self._closed = False

        self._next_id = 1
        self._lock = threading.Lock()
        self._pending: Dict[str, _Waiter] = {}
        self._orphan_responses: Dict[str, Dict[str, Any]] = {}

        self._notif_handlers: List[Callable[[Dict[str, Any]], None]] = []
        self._permission_handlers: List[Callable[[Dict[str, Any], Dict[str, Any]], None]] = []

    # -----------------------------------------------------------------------
    # Lifecycle
    # -----------------------------------------------------------------------

    def start(self) -> None:
        """Start the `marshal acp` subprocess and the transport reader."""
        if self._proc is not None:
            raise RuntimeError("ACPClient already started")

        env = os.environ.copy()
        env.update(self.extra_env)

        self._log(f"starting marshal acp: {self.marshal_bin} (cwd={self.cwd})")
        self._proc = subprocess.Popen(
            [str(self.marshal_bin), "acp"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=str(self.cwd),
            env=env,
            text=True,
            bufsize=1,
        )

        self._reader_thread = threading.Thread(target=self._read_loop, daemon=True)
        self._reader_thread.start()

        self._stderr_thread = threading.Thread(target=self._drain_stderr, daemon=True)
        self._stderr_thread.start()

    def close(self, timeout: float = 15.0) -> None:
        """Close stdin, wait for the subprocess to exit, and clean up."""
        if self._closed:
            return
        self._closed = True

        if self._proc is None:
            return

        try:
            if self._proc.stdin:
                self._proc.stdin.close()
        except Exception as exc:
            self._log(f"error closing stdin: {exc}")

        try:
            self._proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            self._log("marshal did not exit in time, killing")
            self._proc.kill()
            self._proc.wait(timeout=5)

        self._proc = None

    def __enter__(self) -> ACPClient:
        self.start()
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    # -----------------------------------------------------------------------
    # Public API: ACP methods
    # -----------------------------------------------------------------------

    def initialize(self, protocol_version: int = 1, timeout: float = 30.0) -> Dict[str, Any]:
        """Send `initialize` and return the server result."""
        return self._result(
            self.request("initialize", {"protocolVersion": protocol_version}, timeout=timeout)
        )

    def session_new(
        self,
        cwd: Path | str,
        session_id: str = "",
        additional_directories: Optional[List[Path | str]] = None,
        timeout: float = 60.0,
    ) -> SessionResponse:
        """Send `session/new` and return the created session id."""
        params: Dict[str, Any] = {
            "cwd": str(Path(cwd).resolve()),
            "sessionId": session_id,
            "mcpServers": [],
        }
        if additional_directories:
            params["additionalDirectories"] = [str(Path(d).resolve()) for d in additional_directories]
        result = self._result(self.request("session/new", params, timeout=timeout))
        return SessionResponse(session_id=result["sessionId"])

    def session_load(
        self,
        cwd: Path | str,
        session_id: str,
        additional_directories: Optional[List[Path | str]] = None,
        timeout: float = 60.0,
    ) -> None:
        """Send `session/load`. The replayed messages arrive as `session/update`
        notifications before the method returns."""
        params: Dict[str, Any] = {
            "cwd": str(Path(cwd).resolve()),
            "sessionId": session_id,
            "mcpServers": [],
        }
        if additional_directories:
            params["additionalDirectories"] = [str(Path(d).resolve()) for d in additional_directories]
        self._result(self.request("session/load", params, timeout=timeout))

    def session_resume(
        self,
        cwd: Path | str,
        session_id: str,
        additional_directories: Optional[List[Path | str]] = None,
        timeout: float = 60.0,
    ) -> None:
        """Send `session/resume`. Does not replay history."""
        params: Dict[str, Any] = {
            "cwd": str(Path(cwd).resolve()),
            "sessionId": session_id,
            "mcpServers": [],
        }
        if additional_directories:
            params["additionalDirectories"] = [str(Path(d).resolve()) for d in additional_directories]
        self._result(self.request("session/resume", params, timeout=timeout))

    def session_list(
        self,
        cwd: Path | str,
        cursor: Optional[str] = None,
        timeout: float = 30.0,
    ) -> SessionListPage:
        """Send `session/list` and return a page of sessions."""
        params: Dict[str, Any] = {"cwd": str(Path(cwd).resolve())}
        if cursor is not None:
            params["cursor"] = cursor
        result = self._result(self.request("session/list", params, timeout=timeout))
        return SessionListPage(
            sessions=result.get("sessions", []),
            next_cursor=result.get("nextCursor"),
        )

    def session_delete(
        self,
        cwd: Path | str,
        session_id: str,
        timeout: float = 30.0,
    ) -> None:
        """Send `session/delete`."""
        params = {
            "cwd": str(Path(cwd).resolve()),
            "sessionId": session_id,
            "mcpServers": [],
        }
        self._result(self.request("session/delete", params, timeout=timeout))

    def session_close(self, session_id: str, timeout: float = 30.0) -> None:
        """Send `session/close`."""
        self._result(self.request("session/close", {"sessionId": session_id}, timeout=timeout))

    def prompt(
        self,
        session_id: str,
        content: str | List[ContentBlock],
        *,
        timeout: float = 600.0,
        collect_updates: bool = True,
        update_callback: Callable[[str, Optional[Dict[str, Any]]], None] | None = None,
    ) -> Tuple[str, PromptResult]:
        """Send `session/prompt` and wait for the turn to finish.

        Args:
            session_id: Target ACP session id.
            content: Either a plain string (sent as a single text block) or a
                list of `TextBlock` / `ResourceLinkBlock` objects.
            timeout: Maximum seconds to wait for the prompt response.
            collect_updates: If True, accumulate `agent_message_chunk` and
                `agent_thought_chunk` text into the returned reply string.
            update_callback: Optional callback invoked for every update. Receives
                the accumulated text so far and the raw update envelope.

        Returns:
            (assistant_reply, PromptResult)
        """
        blocks = self._normalize_content(content)

        chunks: List[str] = []
        lock = threading.Lock()
        active = threading.Event()
        active.set()

        def on_frame(frame: Dict[str, Any]) -> None:
            if not active.is_set():
                return
            method = frame.get("method")
            if method == "session/update":
                upd = frame.get("params", {}).get("update", {})
                kind = upd.get("kind")
                if kind in ("agent_message_chunk", "agent_thought_chunk"):
                    text = upd.get("content", {}).get("text", "")
                    with lock:
                        chunks.append(text)
                        current = "".join(chunks)
                    if update_callback:
                        update_callback(current, upd)

        self.on_notification(on_frame)
        try:
            resp = self.request(
                "session/prompt",
                {"sessionId": session_id, "prompt": [b.to_acp() for b in blocks]},
                timeout=timeout,
            )
        finally:
            active.clear()
            self._notif_handlers.remove(on_frame)

        result = self._result(resp)
        reply = "".join(chunks) if collect_updates else ""
        return reply, PromptResult(stop_reason=result["stopReason"])

    def cancel(self, session_id: str) -> None:
        """Send `session/cancel` notification for an active turn."""
        self.notify("session/cancel", {"sessionId": session_id})

    # -----------------------------------------------------------------------
    # Public API: low-level transport
    # -----------------------------------------------------------------------

    def request(self, method: str, params: Dict[str, Any], timeout: float = 120.0) -> Dict[str, Any]:
        """Send a JSON-RPC request and return the raw response frame."""
        rid = self._next_json_id()
        sid = str(rid)
        waiter = _Waiter()
        with self._lock:
            self._pending[sid] = waiter
        self._write({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        if not waiter.event.wait(timeout):
            with self._lock:
                self._pending.pop(sid, None)
            raise TimeoutError(f"request {method} id={rid} timed out after {timeout}s")
        return waiter.result

    def notify(self, method: str, params: Dict[str, Any]) -> None:
        """Send a JSON-RPC notification (no response expected)."""
        self._write({"jsonrpc": "2.0", "method": method, "params": params})

    def on_notification(self, handler: Callable[[Dict[str, Any]], None]) -> None:
        """Register a callback for every inbound notification or outbound request."""
        self._notif_handlers.append(handler)

    def on_permission_decision(self, handler: Callable[[Dict[str, Any], Dict[str, Any]], None]) -> None:
        """Register a callback invoked after a permission decision is sent.

        The callback receives (request_frame, decision_frame).
        """
        self._permission_handlers.append(handler)

    # -----------------------------------------------------------------------
    # Internal machinery
    # -----------------------------------------------------------------------

    def _next_json_id(self) -> int:
        with self._lock:
            i = self._next_id
            self._next_id += 1
            return i

    def _write(self, obj: Dict[str, Any]) -> None:
        raw = json.dumps(obj, separators=(",", ":"), ensure_ascii=False)
        self._log(f"[-> marshal] {raw}")
        if self._proc is None or self._proc.stdin is None:
            raise RuntimeError("ACPClient is not started")
        self._proc.stdin.write(raw + "\n")
        self._proc.stdin.flush()

    def _read_loop(self) -> None:
        if self._proc is None or self._proc.stdout is None:
            return
        for line in self._proc.stdout:
            line = line.strip()
            if not line:
                continue
            try:
                frame = json.loads(line)
            except json.JSONDecodeError as exc:
                self._log(f"[driver] malformed JSON: {exc} | {line[:200]}")
                continue
            self._dispatch(frame)
        self._log("[driver] stdout closed")

    def _dispatch(self, frame: Dict[str, Any]) -> None:
        method = frame.get("method")
        fid = frame.get("id")

        if fid is not None and method is None:
            # Inbound response to one of our requests.
            sid = str(fid)
            with self._lock:
                waiter = self._pending.pop(sid, None)
            if waiter is not None:
                waiter.result = frame
                waiter.event.set()
            else:
                with self._lock:
                    self._orphan_responses[sid] = frame
            return

        if method is not None:
            # Inbound notification or outbound request.
            if method == "session/request_permission":
                self._handle_permission(frame)
            for handler in list(self._notif_handlers):
                try:
                    handler(frame)
                except Exception as exc:
                    self._log(f"[driver] notification handler error: {exc}")
            return

    def _handle_permission(self, frame: Dict[str, Any]) -> None:
        decision = self.permission_policy.decide(frame.get("params", {}))
        response = {"jsonrpc": "2.0", "id": frame["id"], "result": decision}
        self._write(response)
        for handler in list(self._permission_handlers):
            try:
                handler(frame, response)
            except Exception as exc:
                self._log(f"[driver] permission handler error: {exc}")

    def _drain_stderr(self) -> None:
        if self._proc is None or self._proc.stderr is None:
            return
        for line in self._proc.stderr:
            self._log(f"[marshal stderr] {line.rstrip()}")

    def _result(self, response: Dict[str, Any]) -> Dict[str, Any]:
        if "error" in response:
            err = response["error"]
            raise ACPError(code=err.get("code", 0), message=err.get("message", "unknown"), data=err.get("data"))
        return response.get("result", {})

    def _normalize_content(self, content: str | List[ContentBlock]) -> List[ContentBlock]:
        if isinstance(content, str):
            return [TextBlock(text=content)]
        return list(content)

    def _log(self, msg: str) -> None:
        line = f"{time.strftime('%Y-%m-%d %H:%M:%S')} {msg}"
        if self.log_callback:
            self.log_callback(line)
        else:
            print(line)
