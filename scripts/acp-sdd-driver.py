#!/usr/bin/env python3
"""ACP-driven Subagent Development (SDD) dispatcher.

Drives Marshal's own ACP protocol to execute an implementation plan using
Marshal as the subagent engine. Replaces the in-process Task tool with a
Python driver that sends `agent.run` calls to a running `marshal acp` process.

Usage:
    python3 scripts/acp-sdd-driver.py \
        --plan docs/superpowers/plans/YYYY-MM-DD-feature-name.md \
        --worktree /path/to/worktree \
        [--tasks 1,2,3] \
        [--binary /tmp/kilo/marshal] \
        [--ledger .superpowers/sdd/progress.md]

Each task is dispatched as an `agent.run` with the `sdd-implementer` custom
agent. Per-task review uses `sdd-reviewer`; final whole-branch review uses
`sdd-branch-reviewer`. The custom agents are configured in the worktree's
`.marshal/config.toml` and map to the same pinned models as the native SDD
workflow:
    sdd-implementer    -> deepseek-v4-flash
    sdd-reviewer       -> deepseek-v4-pro
    sdd-branch-reviewer -> minimax-m3
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

# Add the ACP client directory to the path.
ACP_CLIENT_DIR = Path(__file__).resolve().parent / "acp-driver"
sys.path.insert(0, str(ACP_CLIENT_DIR))

from acp_client import (  # type: ignore
    ACPClient,
    ACPError,
    PermissionPolicy,
    PromptResult,
    TextBlock,
)


# ---------------------------------------------------------------------------
# Approval policy: automated driver, approve everything.
# ---------------------------------------------------------------------------

class ApproveAllPolicy(PermissionPolicy):
    """Approve every outbound permission request from Marshal.

    The worktree config already runs in approval_mode='auto' with
    auto_approve=true, so most confirms are resolved locally. This policy
    exists so forced approvals (e.g. global config changes in tests) do not
    block the automated driver.
    """

    def decide(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return {"approved": True}


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

class DriverError(Exception):
    pass


def run_task_brief(plan: Path, n: int, worktree: Path) -> Path:
    """Extract task n from the plan into a brief file."""
    out = worktree / ".superpowers" / "sdd" / f"task-{n}-brief.md"
    out.parent.mkdir(parents=True, exist_ok=True)
    awk = r'''
    /^```/ { infence = !infence }
    !infence && /^#+[ \t]+Task[ \t]+[0-9]+/ {
        intask = ($0 ~ ("^#+[ \t]+Task[ \t]+" n "([^0-9]|$)"))
    }
    intask { print }
    '''
    proc = subprocess.run(
        ["awk", "-v", f"n={n}", awk],
        input=plan.read_text(),
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        raise DriverError(f"task-brief awk failed: {proc.stderr}")
    text = proc.stdout
    if not text.strip():
        raise DriverError(f"task {n} not found in {plan}")
    out.write_text(text)
    return out


def review_package(worktree: Path, base: str, head: str) -> Path:
    """Generate a focused diff package for the commits in base..head.

    Includes full diffs only for files changed in those commits, plus a stat
    summary of the whole branch so the reviewer still has context without
    re-reading every prior task's diff.
    """
    out = worktree / ".superpowers" / "sdd" / f"review-{base[:12]}-{head[:12]}.md"
    log = subprocess.run(
        ["git", "log", "--oneline", f"{base}..{head}"],
        cwd=worktree,
        capture_output=True,
        text=True,
        check=True,
    )
    stat = subprocess.run(
        ["git", "diff", "--stat", f"{base}..{head}"],
        cwd=worktree,
        capture_output=True,
        text=True,
        check=True,
    )
    # Focus the expensive diff on files touched in this task range only.
    files = subprocess.run(
        ["git", "diff", "--name-only", f"{base}..{head}"],
        cwd=worktree,
        capture_output=True,
        text=True,
        check=True,
    )
    focused_paths = [p for p in files.stdout.strip().splitlines() if p.strip()]
    if focused_paths:
        diff = subprocess.run(
            ["git", "diff", "-U10", f"{base}..{head}", "--", *focused_paths],
            cwd=worktree,
            capture_output=True,
            text=True,
            check=True,
        )
        diff_text = diff.stdout
    else:
        diff_text = ""
    out.write_text(
        f"# Review package\n\n"
        f"## Commits ({base[:12]}..{head[:12]})\n\n```\n{log.stdout}\n```\n\n"
        f"## Stat\n\n```\n{stat.stdout}\n```\n\n"
        f"## Diff (files changed in this range only)\n\n```diff\n{diff_text}\n```\n"
    )
    return out


def current_git_ref(worktree: Path) -> str:
    return subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=worktree,
        capture_output=True,
        text=True,
        check=True,
    ).stdout.strip()


def parse_status(report_text: str) -> Tuple[str, str]:
    """Return (status, one_line_summary) from the report text."""
    for line in report_text.splitlines():
        m = re.match(r"(?i)^\s*\*?\s*Status:\s*(DONE|DONE_WITH_CONCERNS|BLOCKED|NEEDS_CONTEXT)", line)
        if m:
            return m.group(1).upper(), line.strip()
    # Fallback: if report exists and no explicit BLOCKED, assume DONE.
    return "DONE", "(no explicit status line; report present)"


def load_ledger(ledger: Path) -> List[str]:
    if not ledger.exists():
        return []
    return [line.strip() for line in ledger.read_text().splitlines() if line.strip()]


def save_ledger(ledger: Path, lines: List[str]) -> None:
    ledger.parent.mkdir(parents=True, exist_ok=True)
    ledger.write_text("\n".join(lines) + "\n")


# ---------------------------------------------------------------------------
# Dispatch prompts
# ---------------------------------------------------------------------------

IMPLEMENTER_PROMPT = """You are the sdd-implementer subagent for Task {task_num}: {task_title}.

## Source of truth

Read your task brief first: {brief_path}
The brief contains the exact requirements, exact file paths, and often exact
code blocks. Treat it as authoritative. When the brief provides code, use it
verbatim unless there is a clear reason not to. Avoid open-ended exploration;
only read/write the files named in the brief.

## Context

You are implementing a piece of the agent-driven configuration tools feature
for Marshal. Work in the directory {worktree}. Follow the file structure in the
brief. Use existing patterns in the codebase.

## Before You Begin

If you have questions about requirements, approach, dependencies, or anything
unclear in the brief, ask them now in your response. Otherwise proceed.

## Your Job

1. Implement exactly what the brief specifies.
2. Write tests (TDD if required by the brief).
3. Verify implementation works.
4. Commit your work.
5. Self-review.
6. Report back.

## Working notes

- Follow the numbered steps in the brief in order.
- Run focused tests during iteration; run the relevant package tests before committing.
- Use `gofmt -w .` before committing.
- Do not modify files outside the task scope.
- Do not get stuck reading the same files repeatedly; if the brief already gives
  you the code, use it.

## Report Format

Write your full report to {report_path} with:
- What you implemented
- Test commands and results
- TDD evidence (RED/GREEN) if required
- Files changed
- Self-review findings
- Any issues or concerns

Then return a short final message (under 15 lines) with:
- **Status:** DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
- Commits (short SHA + subject)
- One-line test summary
- Concerns, if any
- The report file path
"""

REVIEWER_PROMPT = """You are the sdd-reviewer subagent for Task {task_num}: {task_title}.

Read the task brief first: {brief_path}
Then read the implementer report: {report_path}
Then read the review package (diff + commits): {package_path}

## Your Job

Judge spec compliance and code quality for this task only. Do not implement.

Report format:
- **Spec compliance:** PASS | FAIL (list any missing requirements)
- **Quality:** APPROVED | NEEDS_FIX (list concrete findings with severity: Critical / Important / Minor)
- **Overall:** APPROVED | NEEDS_FIX

Be specific. If you find issues, the implementer will fix them and you will
re-review. Return only your verdict.
"""

FIXER_PROMPT = """You are the sdd-implementer subagent fixing review findings for Task {task_num}: {task_title}.

Read the task brief: {brief_path}
Read the previous report: {report_path}
Read the reviewer findings below and address every Critical and Important
item. Minor items may be noted but do not need to block unless trivial.

## Reviewer findings

{findings}

## Your Job

Implement the fixes, run the tests covering the changed code, update the
report at {report_path} with the fix details and new test results, then report
back with **Status:** DONE or DONE_WITH_CONCERNS, commits, and a one-line test
summary.
"""

BRANCH_REVIEWER_PROMPT = """You are the sdd-branch-reviewer subagent.

Read the implementation plan: {plan_path}
Read the final review package (full branch diff): {package_path}

## Your Job

Judge whole-branch integration, cross-task consistency, and merge readiness.
Do not implement.

Report format:
- **Plan coverage:** PASS | FAIL
- **Integration:** PASS | FAIL
- **Quality:** APPROVED | NEEDS_FIX
- **Overall:** READY_TO_MERGE | NEEDS_FIX

List concrete findings with severity. This is the final gate before merge.
"""


# ---------------------------------------------------------------------------
# Driver
# ---------------------------------------------------------------------------

class SDDDriver:
    def __init__(self, *, plan: Path, worktree: Path, binary: Path, ledger: Path, tasks: Optional[List[int]] = None):
        self.plan = plan
        self.worktree = worktree
        self.binary = binary
        self.ledger = ledger
        self.tasks = tasks
        self.client: Optional[ACPClient] = None
        self.session_id: Optional[str] = None

    def start(self) -> None:
        self.client = ACPClient(
            marshal_bin=str(self.binary),
            cwd=str(self.worktree),
            permission_policy=ApproveAllPolicy(),
        )
        self.client.start()
        info = self.client.initialize()
        print(f"[driver] initialized: {info.get('agentInfo', {}).get('title')}")
        session = self.client.session_new(cwd=str(self.worktree), session_id="")
        self.session_id = session.session_id
        print(f"[driver] session: {self.session_id}")

    def close(self) -> None:
        if self.client:
            self.client.close()
            self.client = None

    def _prompt(self, content: str, timeout: float = 600.0) -> Tuple[str, PromptResult]:
        assert self.client is not None and self.session_id is not None
        print(f"[driver] prompting (timeout={timeout}s)...")
        reply, result = self.client.prompt(self.session_id, [TextBlock(text=content)], timeout=timeout)
        return reply, result

    def _dispatch_agent(self, agent: str, prompt: str, timeout: float = 600.0) -> Tuple[str, PromptResult]:
        """Dispatch agent.run with the given agent name and prompt."""
        payload = json.dumps({"agent": agent, "prompt": prompt, "description": f"{agent} dispatch"})
        content = f"Run agent.run with this exact payload:\n\n```json\n{payload}\n```\n\nReturn the subagent's final status line."
        return self._prompt(content, timeout=timeout)

    def run_task(self, n: int) -> Tuple[str, str, str, Path]:
        """Run implementer for task n. Returns (status, summary, head_after, report_path)."""
        brief = run_task_brief(self.plan, n, self.worktree)
        report = self.worktree / ".superpowers" / "sdd" / f"task-{n}-report.md"
        base = current_git_ref(self.worktree)

        # Extract task title from brief heading.
        title = ""
        for line in brief.read_text().splitlines()[:3]:
            m = re.match(r"^#+\s+Task\s+\d+:\s*(.+)", line)
            if m:
                title = m.group(1).strip()
                break

        prompt = IMPLEMENTER_PROMPT.format(
            task_num=n,
            task_title=title,
            brief_path=str(brief),
            worktree=str(self.worktree),
            report_path=str(report),
        )

        print(f"\n[driver] === Task {n}: {title} ===")
        reply, result = self._dispatch_agent("sdd-implementer", prompt, timeout=1800.0)
        print(f"[driver] implementer stop_reason={result.stop_reason}")
        print(reply[-2000:])  # tail of reply for visibility

        if not report.exists():
            raise DriverError(f"implementer did not write report: {report}")

        report_text = report.read_text()
        status, summary = parse_status(report_text)
        head = current_git_ref(self.worktree)
        return status, summary, head, report

    def review_task(self, n: int, report: Path, base: str, head: str) -> Tuple[str, str]:
        """Run reviewer for task n. Returns (verdict, findings_text)."""
        brief = self.worktree / ".superpowers" / "sdd" / f"task-{n}-brief.md"
        package = review_package(self.worktree, base, head)

        # Extract task title.
        title = ""
        for line in brief.read_text().splitlines()[:3]:
            m = re.match(r"^#+\s+Task\s+\d+:\s*(.+)", line)
            if m:
                title = m.group(1).strip()
                break

        prompt = REVIEWER_PROMPT.format(
            task_num=n,
            task_title=title,
            brief_path=str(brief),
            report_path=str(report),
            package_path=str(package),
        )
        print(f"[driver] reviewing task {n}...")
        reply, result = self._dispatch_agent("sdd-reviewer", prompt, timeout=1800.0)
        print(f"[driver] reviewer stop_reason={result.stop_reason}")
        print(reply[-1500:])
        return reply, package.read_text()

    def fix_task(self, n: int, report: Path, findings: str) -> Tuple[str, str]:
        """Dispatch fixer for task n. Returns (status, head_after)."""
        brief = self.worktree / ".superpowers" / "sdd" / f"task-{n}-brief.md"
        prompt = FIXER_PROMPT.format(
            task_num=n,
            task_title="",
            brief_path=str(brief),
            report_path=str(report),
            findings=findings,
        )
        print(f"[driver] dispatching fixer for task {n}...")
        reply, result = self._dispatch_agent("sdd-implementer", prompt, timeout=1800.0)
        print(f"[driver] fixer stop_reason={result.stop_reason}")
        print(reply[-1500:])
        status, _ = parse_status(report.read_text())
        return status, current_git_ref(self.worktree)

    def execute(self) -> None:
        ledger_lines = load_ledger(self.ledger)
        completed = {self._task_from_ledger(line) for line in ledger_lines}

        # Determine task numbers if not provided.
        task_nums = self.tasks
        if task_nums is None:
            task_nums = self._discover_task_numbers()

        print(f"[driver] plan: {self.plan}")
        print(f"[driver] worktree: {self.worktree}")
        print(f"[driver] tasks: {task_nums}")
        print(f"[driver] already complete: {sorted(completed)}")

        self.start()
        try:
            for n in task_nums:
                if n in completed:
                    print(f"\n[driver] Task {n} already complete, skipping.")
                    continue
                self._run_one_task(n, ledger_lines)
        finally:
            self.close()

    def _run_one_task(self, n: int, ledger_lines: List[str]) -> None:
        # Allow up to three review/fix loops per task.
        for attempt in range(3):
            status, summary, head, report = self.run_task(n)
            if status in ("BLOCKED", "NEEDS_CONTEXT"):
                print(f"[driver] Task {n} status={status}: {summary}")
                raise DriverError(f"Task {n} blocked/needs context: {summary}")

            verdict, findings = self.review_task(n, report, current_git_ref(self.worktree)[:0] or "HEAD", head)
            # Simple verdict parsing.
            upper = verdict.upper()
            needs_fix = "NEEDS_FIX" in upper or "FAIL" in upper
            if not needs_fix:
                print(f"[driver] Task {n} review approved.")
                break
            if attempt == 2:
                raise DriverError(f"Task {n} still needs fix after 3 attempts.\nFindings:\n{findings}")
            print(f"[driver] Task {n} needs fix, dispatching fixer (attempt {attempt + 2}/3)...")
            status, head = self.fix_task(n, report, findings)
            if status in ("BLOCKED", "NEEDS_CONTEXT"):
                raise DriverError(f"Task {n} fixer blocked/needs context")
        else:
            # This path only taken if the loop completes without break; shouldn't happen.
            pass

        ledger_lines.append(f"Task {n}: complete (commits ..{head[:7]}, review clean)")
        save_ledger(self.ledger, ledger_lines)
        print(f"[driver] Task {n} marked complete in ledger.")

    def _discover_task_numbers(self) -> List[int]:
        nums = set()
        for line in self.plan.read_text().splitlines():
            m = re.match(r"^#+\s+Task\s+(\d+):", line)
            if m:
                nums.add(int(m.group(1)))
        return sorted(nums)

    @staticmethod
    def _task_from_ledger(line: str) -> Optional[int]:
        m = re.match(r"^Task\s+(\d+):\s+complete", line)
        return int(m.group(1)) if m else None

    def final_review(self) -> str:
        """Run whole-branch review and return the verdict text."""
        merge_base = subprocess.run(
            ["git", "merge-base", "main", "HEAD"],
            cwd=self.worktree,
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()
        package = review_package(self.worktree, merge_base, "HEAD")
        prompt = BRANCH_REVIEWER_PROMPT.format(
            plan_path=str(self.plan),
            package_path=str(package),
        )
        self.start()
        try:
            print("\n[driver] === Final whole-branch review ===")
            reply, result = self._dispatch_agent("sdd-branch-reviewer", prompt, timeout=1800.0)
            print(f"[driver] branch reviewer stop_reason={result.stop_reason}")
            print(reply[-2000:])
            return reply
        finally:
            self.close()


def main(argv: List[str]) -> int:
    parser = argparse.ArgumentParser(description="ACP-driven SDD dispatcher using Marshal as the subagent engine.")
    parser.add_argument("--plan", required=True, type=Path, help="Path to the implementation plan.")
    parser.add_argument("--worktree", required=True, type=Path, help="Path to the worktree root.")
    parser.add_argument("--tasks", type=str, help="Comma-separated task numbers to run (default: all).")
    parser.add_argument("--binary", type=Path, default=Path("/tmp/kilo/marshal"), help="Path to the marshal binary.")
    parser.add_argument("--ledger", type=Path, default=None, help="Path to the progress ledger.")
    parser.add_argument("--final-review", action="store_true", help="Run final whole-branch review after tasks.")
    args = parser.parse_args(argv)

    ledger = args.ledger or args.worktree / ".superpowers" / "sdd" / "progress.md"
    tasks = [int(x.strip()) for x in args.tasks.split(",")] if args.tasks else None

    driver = SDDDriver(
        plan=args.plan,
        worktree=args.worktree,
        binary=args.binary,
        ledger=ledger,
        tasks=tasks,
    )
    driver.execute()
    if args.final_review:
        driver.final_review()
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
