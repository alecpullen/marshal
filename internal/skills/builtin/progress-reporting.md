---
name: progress-reporting
description: Show a live progress bar for a long download, transfer, test run or phased job, using shell.run's progress option.
risk: workspace_write
---

# Progress Reporting

A measured card shows what a long command has really done, not just that it is
still running. Turn it on with `shell.run`'s `progress` option.

## When to use it

- Use it when the command runs long enough to watch (roughly more than half a
  minute) and has something real to count: bytes, tests, files, or phases of a
  multi-step job.
- Otherwise run without `progress`: the generic card shows elapsed time and
  the last output line.
- Never write a producer for a command that finishes in seconds.

## The `progress` option

- Fields: `profile` (`generic`, `tests`, `download`, `transfer`, `custom`),
  `source` (`none`, `go-test-json`, `events-v1`), `title` (at most 80
  characters).
- `go-test-json` needs profile `tests`; `events-v1` needs one of the four
  profiles other than `generic`; source `none` gives the plain card whatever
  the profile. An invalid combination is rejected before launch.
- Run long work with `"background": true`, then `agent.await`.
- The option never changes the command; Marshal adds no flags.

```json
{"command":"python3 .marshal/progress/download.py URL DEST","background":true,"progress":{"profile":"download","source":"events-v1","title":"Download"}}
```

## Go tests

- Run `go test -json` plus the packages, with profile `tests` and source
  `go-test-json`; no producer is needed.
- The card counts passed, failed and skipped tests, and never shows a total:
  the JSON declares none.
- Other test runners can print records with profile `tests` and a `tests`
  metric.

## The record format

- The prefix is exactly `@@marshal-progress/v1 ` (with the trailing space),
  then one JSON object and a newline, on stdout. Stderr and other stdout lines
  are ordinary output.
- Each record is a complete snapshot, never a patch: an omitted field clears.
- `version` is `1`. `sequence` starts at 1 and strictly increases, up to
  2^53−1; an older or repeated sequence is ignored.
- **Metrics:** at most 4, each with a unique `id` (up to 64 characters),
  `label` (up to 80) and `unit`.
  - The unit is `bytes`, `files`, `tests`, `packages` or `items` (integers),
    or any other unit of up to 16 characters.
  - `current` is 0 or more. `total` is optional and above 0. **Omit `total`
    when you do not know it, and never invent a total.**
  - `rate` is optional, in units per second; without it the card derives one
    from successive records.
  - `primaryMetricId` names one of the metrics; without it the first is
    primary.
- **Phase:** optional `{id, label, index, total}`, with `1 ≤ index ≤ total`. A
  phase count is not a percentage of the whole job.
- **Rows:** at most 8, each `{id, label, state}`, state `pending`, `running`,
  `done` or `error`.
- **Limits:** `detail` at most 1,024 bytes, a whole record line at most
  16 KiB.
- **When to emit:** at most about five records a second, plus one at the start,
  each phase change and the end.
- **What the card does with them:**
  - The row shows the primary metric: with a `total`, the phase as
    `1/2 <label>` when present, then a bar, percentage, count, ETA and rate;
    without one, the count, the rate and, for bytes, "size unknown". Bytes use
    IEC units.
  - Expanding shows `detail` when set, else the first four rows as a
    checklist; leave `detail` empty to see the checklist.
  - A record more than 30 seconds old is stale: the bar holds, rate and ETA
    hide, and the row warns "no update for …". Emit whenever the work moves,
    so a stale warning means the work really stalled.
  - An invalid record is dropped with one warning; the last valid one stays.
  - Only the process's exit finishes the card; a record at 100% shows the work
    as reported done while the process still runs. The completion event then
    shows the last record's measurement, and under it its `detail`, else the
    last output line.

@@marshal-progress/v1 {"version":1,"sequence":7,"phase":{"id":"hash","label":"Hash","index":1,"total":2},"metrics":[{"id":"hashed","label":"Hashed","unit":"bytes","current":1048576,"total":4194304}],"rows":[{"id":"hash","label":"Hash","state":"running"},{"id":"manifest","label":"Manifest","state":"pending"}],"detail":"a.bin"}

## Producers

- Write them with `file.write` under `.marshal/progress/`; if one exists, read
  and reuse it.
- Write `marshal_progress.py` first, then the producer you need.
- Run each as `python3 .marshal/progress/<file> …` from the workspace root, so
  its own directory is on the import path.
- For a job none of these fit, write a producer that imports
  `marshal_progress` and reports only what it really measures.

### `marshal_progress.py`

```python
# .marshal/progress/marshal_progress.py
"""Shared events-v1 progress helper."""
import json
import sys
import time

class Progress:
    def __init__(self):
        self.sequence = 0
        self.last = None

    def emit(self, metrics, *, phase=None, rows=None, detail="", force=False):
        now = time.monotonic()
        if not force and self.last is not None and now - self.last < 0.2:
            return
        self.sequence += 1
        if self.sequence > 2**53 - 1:
            raise ValueError("sequence exhausted")
        detail = detail.encode("utf-8")[:1024].decode("utf-8", errors="ignore")
        frame = {"version": 1, "sequence": self.sequence, "metrics": metrics,
                 "detail": detail}
        if metrics:
            frame["primaryMetricId"] = metrics[0]["id"]
        if phase is not None:
            frame["phase"] = phase
        if rows is not None:
            frame["rows"] = rows
        payload = json.dumps(frame, ensure_ascii=False, allow_nan=False)
        line = "@@marshal-progress/v1 " + payload
        if len(line.encode("utf-8")) > 16384:
            raise ValueError("progress record too large")
        print(line, file=sys.stdout, flush=True)
        self.last = now
```

### `download.py`

Profile `download`, source `events-v1`. It leaves a partial file on failure
and refuses to overwrite.

```python
# .marshal/progress/download.py
"""Download URL to a new file, counting bytes received."""
import argparse
import time
import urllib.request
from marshal_progress import Progress

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("url")
    parser.add_argument("destination")
    args = parser.parse_args()
    meter = Progress()
    received = 0
    started = time.monotonic()
    with urllib.request.urlopen(args.url) as response, open(args.destination, "xb") as output:
        header = response.headers.get("Content-Length", "")
        total = int(header) if header.isdigit() and 0 < int(header) <= 2**53-1 else None
        def report(force=False):
            elapsed = time.monotonic() - started
            metric = {"id": "received", "label": "Bytes received", "unit": "bytes",
                      "current": received, "rate": received / elapsed if elapsed > 0 else 0}
            if total is not None:
                metric["total"] = total
            meter.emit([metric], detail=args.destination, force=force)
        report(True)
        while True:
            chunk = response.read(256 * 1024)
            if not chunk:
                break
            output.write(chunk)
            received += len(chunk)
            report()
        output.flush()
        report(True)
    print(f"Downloaded {received} bytes to {args.destination}")

if __name__ == "__main__":
    main()
```

### `transfer.py`

Profile `transfer`, source `events-v1`. It counts bytes supplied to SSH, not
bytes verified on the remote, and may overwrite the remote file.

```python
# .marshal/progress/transfer.py
"""Send one file to SSH stdin, counting bytes supplied."""
import argparse
import os
import shlex
import subprocess
import time
from marshal_progress import Progress

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source")
    parser.add_argument("target", help="Configured SSH host or user@host")
    parser.add_argument("destination", help="Remote file path; may overwrite that file")
    args = parser.parse_args()
    if not args.target or args.target.startswith("-"):
        parser.error("target must be a host, not an option")
    if not args.destination or "\x00" in args.destination:
        parser.error("invalid remote file path")
    meter = Progress()
    sent = 0
    started = time.monotonic()
    with open(args.source, "rb") as source:
        total = os.fstat(source.fileno()).st_size
        remote = subprocess.Popen(
            ["ssh", "-T", args.target, "cat > " + shlex.quote(args.destination)],
            stdin=subprocess.PIPE)
        def report(force=False):
            elapsed = time.monotonic() - started
            metric = {"id": "supplied", "label": "Bytes supplied to SSH", "unit": "bytes",
                      "current": sent, "rate": sent / elapsed if elapsed > 0 else 0}
            if total > 0:
                metric["total"] = total
            meter.emit([metric], detail=args.destination, force=force)
        try:
            report(True)
            while True:
                chunk = source.read(256 * 1024)
                if not chunk:
                    break
                remote.stdin.write(chunk)
                sent += len(chunk)
                report()
            remote.stdin.flush()
            report(True)
        finally:
            try:
                remote.stdin.close()
            finally:
                code = remote.wait()
        if code:
            raise SystemExit(code)
    print(f"SSH completed successfully after accepting {sent} bytes")

if __name__ == "__main__":
    main()
```

### `custom.py`

Profile `custom`, source `events-v1`. Its percentage belongs to its phase.

```python
# .marshal/progress/custom.py
"""Hash files and write a new manifest, in two measured phases."""
import argparse
import hashlib
import json
import os
from marshal_progress import Progress

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest")
    parser.add_argument("files", nargs="+")
    args = parser.parse_args()
    meter = Progress()
    rows = [{"id": "hash", "label": "Hash files", "state": "running"},
            {"id": "manifest", "label": "Write manifest", "state": "pending"}]
    size = sum(os.path.getsize(path) for path in args.files)
    done = 0
    entries = []
    def report(detail="", force=False):
        metric = {"id": "hashed", "label": "Bytes hashed", "unit": "bytes", "current": done}
        if size > 0:
            metric["total"] = size
        meter.emit([metric], phase={"id": "hash", "label": "Hash files", "index": 1, "total": 2},
                   rows=rows, detail=detail, force=force)
    report(force=True)
    for path in args.files:
        digest = hashlib.sha256()
        with open(path, "rb") as source:
            while True:
                chunk = source.read(256 * 1024)
                if not chunk:
                    break
                digest.update(chunk)
                done += len(chunk)
                report(path)
        entries.append({"path": path, "sha256": digest.hexdigest()})
    rows[0]["state"] = "done"
    rows[1]["state"] = "running"
    meter.emit([], phase={"id": "manifest", "label": "Write manifest", "index": 2, "total": 2},
               rows=rows, detail=args.manifest, force=True)
    with open(args.manifest, "x", encoding="utf-8") as output:
        json.dump(entries, output, ensure_ascii=False, indent=2)
        output.write("\n")
    rows[1]["state"] = "done"
    meter.emit([{"id": "files", "label": "Files recorded", "unit": "files",
                 "current": len(entries), "total": len(entries)}],
               phase={"id": "manifest", "label": "Write manifest", "index": 2, "total": 2},
               rows=rows, detail=args.manifest, force=True)
    print(f"Wrote {len(entries)} manifest entries")

if __name__ == "__main__":
    main()
```

## Housekeeping

- Tell the user where the scripts are.
- If `git status` lists `.marshal/progress/`, the project's `.marshal/.gitignore`
  predates the entry: say so, and do not edit it unasked.
- The producers need `python3`; if `python3 --version` fails, use the generic
  card.
