#!/usr/bin/env python3
"""ACP v1 headless driver for marshal: runs plan tasks one at a time, then
runs a fresh-context review turn before proceeding to the next task."""

import json
import os
import re
import subprocess
import sys
import threading
import time
from pathlib import Path

MARSHAL_BIN = Path("/tmp/kilo/marshal")
WORK_DIR = Path("/tmp/kilo/marshal_work").resolve()
PROJECT_ROOT = Path("/Users/alecpullen/projects/marshal").resolve()
LOG_PATH = Path("/tmp/kilo/marshal_acp_driver.log")

PREEXISTING_FAILING_USABILITY_TEST = "TestUsability"  # generic substring match


def log(msg: str):
    with LOG_PATH.open("a", encoding="utf-8") as f:
        f.write(f"{time.strftime('%Y-%m-%d %H:%M:%S')} {msg}\n")
    print(msg)


class ACPClient:
    def __init__(self):
        self._next_id = 1
        self._lock = threading.Lock()
        self._pending = {}  # id -> threading.Event + result
        self._responses = {}  # id -> frame
        self._notif_handlers = []
        self._reader_thread = None
        self._proc = None

    def _next_json_id(self):
        with self._lock:
            i = self._next_id
            self._next_id += 1
            return i

    def start(self):
        env = os.environ.copy()
        # Make sure marshal uses the real project as working dir for config discovery.
        env["MARSHAL_ACP_CWD"] = str(PROJECT_ROOT)
        self._proc = subprocess.Popen(
            [str(MARSHAL_BIN), "acp"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=str(PROJECT_ROOT),
            env=env,
            text=True,
            bufsize=1,
        )
        self._reader_thread = threading.Thread(target=self._read_loop, daemon=True)
        self._reader_thread.start()
        # Start stderr drain to keep subprocess from blocking.
        threading.Thread(target=self._drain_stderr, daemon=True).start()

    def _drain_stderr(self):
        for line in self._proc.stderr:
            log(f"[marshal stderr] {line.rstrip()}")

    def _read_loop(self):
        for line in self._proc.stdout:
            line = line.strip()
            if not line:
                continue
            try:
                frame = json.loads(line)
            except json.JSONDecodeError as e:
                log(f"[driver] malformed JSON from marshal: {e} | {line[:200]}")
                continue
            fid = frame.get("id")
            method = frame.get("method")
            if fid is not None and method is None:
                # Response
                sid = str(fid)
                with self._lock:
                    if sid in self._pending:
                        self._pending[sid]["result"] = frame
                        self._pending[sid]["event"].set()
                    else:
                        self._responses[sid] = frame
            elif method is not None:
                # Notification or outbound request
                for h in self._notif_handlers:
                    h(frame)

    def _write(self, obj: dict):
        raw = json.dumps(obj, separators=(",", ":"), ensure_ascii=False)
        log(f"[driver -> marshal] {raw}")
        self._proc.stdin.write(raw + "\n")
        self._proc.stdin.flush()

    def request(self, method: str, params: dict, timeout: float = 120.0) -> dict:
        rid = self._next_json_id()
        sid = str(rid)
        evt = threading.Event()
        with self._lock:
            self._pending[sid] = {"event": evt, "result": None}
        self._write({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        if not evt.wait(timeout):
            raise TimeoutError(f"request {method} id={rid} timed out")
        with self._lock:
            return self._pending.pop(sid)["result"]

    def notify(self, method: str, params: dict):
        self._write({"jsonrpc": "2.0", "method": method, "params": params})

    def on_frame(self, handler):
        self._notif_handlers.append(handler)

    def close(self):
        try:
            self._proc.stdin.close()
        except Exception:
            pass
        try:
            self._proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self._proc.kill()
            self._proc.wait(timeout=5)


def extract_stop_reason(result: dict) -> str:
    if "error" in result:
        return f"error: {result['error']}"
    return result.get("result", {}).get("stopReason", "unknown")


def gather_assistant_text(client: ACPClient, timeout_per_chunk: float = 120.0) -> str:
    """Listen to session/update notifications and accumulate agent_message_chunk text.
    Returns when the prompt response arrives."""
    chunks = []
    response_event = threading.Event()
    response = [None]

    def handler(frame):
        method = frame.get("method")
        if method == "session/update":
            upd = frame.get("params", {}).get("update", {})
            if upd.get("kind") in ("agent_message_chunk", "agent_thought_chunk"):
                content = upd.get("content", {}).get("text", "")
                chunks.append(content)
        elif frame.get("id") is not None and method is None:
            response[0] = frame
            response_event.set()

    client.on_frame(handler)
    if not response_event.wait(timeout_per_chunk):
        raise TimeoutError("waiting for prompt response")
    return "".join(chunks), response[0]


def approve_permission(frame: dict) -> dict:
    """Auto-approve permission requests; prefer 'always' for safe commands."""
    params = frame.get("params", {})
    tool = params.get("toolName", "")
    cmd = params.get("command", "")
    decision = {"approved": True}
    if tool in ("file.read", "search.grep", "repo.map", "symbols.find"):
        decision["always"] = True
    elif tool == "shell.run":
        # Approve go test / gofmt / git / read-only commands.
        safe_prefixes = (
            "go test", "go build", "gofmt", "git diff", "git status",
            "git log", "git add", "git commit", "go vet",
        )
        if any(cmd.strip().startswith(p) for p in safe_prefixes):
            decision["always"] = True
    return {"jsonrpc": "2.0", "id": frame["id"], "result": decision}


def run_single_turn(client: ACPClient, session_id: str, text: str, timeout: float = 600.0) -> str:
    """Send session/prompt and accumulate the assistant reply. Auto-approves permissions."""
    reply_chunks = []
    lock = threading.Lock()
    active = threading.Event()
    active.set()

    def handler(frame):
        method = frame.get("method")
        if method == "session/request_permission":
            client._write(approve_permission(frame))
            return
        if not active.is_set():
            return
        if method == "session/update":
            upd = frame.get("params", {}).get("update", {})
            if upd.get("kind") in ("agent_message_chunk", "agent_thought_chunk"):
                content = upd.get("content", {}).get("text", "")
                with lock:
                    reply_chunks.append(content)

    client.on_frame(handler)
    resp = client.request(
        "session/prompt",
        {"sessionId": session_id, "prompt": [{"type": "text", "text": text}]},
        timeout=timeout,
    )
    active.clear()
    with lock:
        reply = "".join(reply_chunks)
    return reply, resp


def initialize_and_create_session(client: ACPClient) -> str:
    log("Sending initialize...")
    resp = client.request("initialize", {"protocolVersion": 1}, timeout=30.0)
    log(f"initialize response: {resp}")
    log("Creating session...")
    resp = client.request("session/new", {
        "cwd": str(PROJECT_ROOT),
        "sessionId": "",
        "mcpServers": [],
    }, timeout=60.0)
    log(f"session/new response: {resp}")
    sid = resp.get("result", {}).get("sessionId")
    if not sid:
        raise RuntimeError(f"no session id in response: {resp}")
    return sid


def close_session(client: ACPClient, sid: str):
    log(f"Closing session {sid}...")
    resp = client.request("session/close", {"sessionId": sid}, timeout=30.0)
    log(f"session/close response: {resp}")


def load_plan() -> list:
    plan_path = PROJECT_ROOT / "docs/superpowers/plans/2026-07-24-token-metric-tracking.md"
    text = plan_path.read_text(encoding="utf-8")
    # Split by task headers
    tasks = []
    pattern = re.compile(r"### Task (\d+):\s*(.+?)\n")
    matches = list(pattern.finditer(text))
    for i, m in enumerate(matches):
        start = m.end()
        end = matches[i + 1].start() if i + 1 < len(matches) else len(text)
        body = text[start:end]
        tasks.append({
            "num": int(m.group(1)),
            "title": m.group(2).strip(),
            "body": body,
        })
    return tasks


TASK_PROMPT_TEMPLATE = """You are implementing the Token Metric Tracking plan for Marshal. Work ONLY on Task {num}: {title}.

Follow this exact workflow:
1. Write or update the failing test first (TDD).
2. Run the narrow test to confirm failure.
3. Implement the minimal production code to make the test pass.
4. Run the narrow test again to confirm it passes.
5. Run `gofmt -w` on all files you touched.
6. Stage and commit with a conventional commit message.

There is ONE pre-existing failing usability test whose name contains "{ignore}". Ignore it when running tests. Do NOT modify that test.

Do everything via tool calls. Do not ask the user questions. Use git add / git commit. Here is the detailed plan for this task:

{body}

Proceed step by step. Report your final status at the end: TASK {num} COMPLETE if all tests passed and the commit was made, or TASK {num} FAILED with the reason.
"""

REVIEW_PROMPT_TEMPLATE = """You are performing an independent review of Task {num}: {title} in the Token Metric Tracking plan.

Do NOT look at the previous session context; verify the current repository state directly by reading the relevant files and running the narrow tests for this task. Ignore any pre-existing failing test whose name contains "{ignore}".

Check these requirements:
{requirements}

After checking, respond with exactly one of these verdicts on its own line at the very end:
VERDICT: APPROVED
or
VERDICT: REJECTED - <concise reason>
"""


def build_review_requirements(num: int, task: dict) -> str:
    # Generate a generic requirements checklist from the task body.
    lines = task["body"].splitlines()
    reqs = []
    for line in lines:
        s = line.strip()
        if s.startswith("- Modify:") or s.startswith("- Create:") or s.startswith("- Produces:") or s.startswith("- Consumes:"):
            reqs.append("- " + s)
        if s.startswith("- [ ] **Step") and "test" in s.lower():
            reqs.append("- Tests were written and pass for this task.")
    return "\n".join(reqs[:12]) or "- All files listed in the plan are created/modified as specified.\n- Tests pass."


def main():
    LOG_PATH.write_text("", encoding="utf-8")
    log("ACP driver starting")
    client = ACPClient()
    client.start()
    try:
        sid = initialize_and_create_session(client)
        tasks = load_plan()
        log(f"Loaded {len(tasks)} tasks from plan")
        for task in tasks:
            num = task["num"]
            title = task["title"]
            log(f"\n===== Task {num}: {title} =====")
            # Send the implementation prompt on the existing session.
            prompt = TASK_PROMPT_TEMPLATE.format(
                num=num, title=title, body=task["body"], ignore=PREEXISTING_FAILING_USABILITY_TEST
            )
            reply, resp = run_single_turn(client, sid, prompt, timeout=900.0)
            log(f"Task {num} raw response stopReason={extract_stop_reason(resp)}")
            log(f"Task {num} reply length={len(reply)}\n{reply[-2000:]}")
            # Fresh-context review in a new session.
            log(f"\n----- Review for Task {num} -----")
            close_session(client, sid)
            sid = initialize_and_create_session(client)
            review_reqs = build_review_requirements(num, task)
            review_prompt = REVIEW_PROMPT_TEMPLATE.format(
                num=num, title=title, requirements=review_reqs, ignore=PREEXISTING_FAILING_USABILITY_TEST
            )
            review_reply, review_resp = run_single_turn(client, sid, review_prompt, timeout=600.0)
            log(f"Review {num} raw response stopReason={extract_stop_reason(review_resp)}")
            log(f"Review {num} reply length={len(review_reply)}\n{review_reply[-2000:]}")
            if "VERDICT: APPROVED" in review_reply:
                log(f"Task {num} APPROVED — proceeding")
            else:
                log(f"Task {num} NOT APPROVED — stopping")
                close_session(client, sid)
                return 1
        # Final full suite.
        log("\n===== Final full test suite =====")
        final_prompt = (
            "Run the final verification for the token metrics tracking work:\n"
            "1. Run `gofmt -w .`\n"
            "2. Run `go test ./...` and ignore the pre-existing failing usability test "
            f"whose name contains '{PREEXISTING_FAILING_USABILITY_TEST}'.\n"
            "3. Run `go build ./cmd/marshal`.\n"
            "Report the results. If everything passes except the known pre-existing test, "
            "end with: FINAL STATUS: READY. Otherwise end with FINAL STATUS: FAILED - <reason>."
        )
        final_reply, final_resp = run_single_turn(client, sid, final_prompt, timeout=900.0)
        log(f"Final raw response stopReason={extract_stop_reason(final_resp)}")
        log(f"Final reply length={len(final_reply)}\n{final_reply[-3000:]}")
        close_session(client, sid)
        if "FINAL STATUS: READY" in final_reply:
            return 0
        return 1
    finally:
        client.close()


if __name__ == "__main__":
    sys.exit(main())
