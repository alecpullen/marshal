#!/usr/bin/env python3
"""Capture a Marshal TUI frame by running the binary in a PTY.

Writes the raw escape stream to a file; render.mjs turns that into an SVG.

Usage:
    capture.py BINARY OUT.bin [options]

Options:
    --width W / --height H   terminal size in cells (default 120x38)
    --seconds S              how long to record (default 10)
    --keys SPEC              scheduled keystrokes, comma separated:
                             "DELAY:text" or just "text".
                             e.g. '3.0:Explain calc.go,0.8:\\r' sends text
                             after 3s, then Enter 0.8s later.
    --workdir DIR            child working directory
    --config-dir DIR         MARSHAL_CONFIG_DIR for the child
    --data-dir DIR           MARSHAL_DATA_DIR for the child

Why a PTY: Marshal is a full-screen Bubble Tea app. It only emits its
interface when it believes it is talking to a terminal, so a pipe yields
nothing useful.

The captured stream is a *stream*, not a screen — it contains cursor
addressing, diff updates and spinners. Do not try to read it as text; feed it
to render.mjs, which replays it through a real VT emulator.
"""

from __future__ import annotations

import argparse
import fcntl
import os
import pty
import select
import signal
import struct
import sys
import termios
import time


def set_winsize(fd: int, rows: int, cols: int) -> None:
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))


def parse_schedule(spec: str) -> list[tuple[float, str]]:
    """Turn "1.0:text,0.5:\\r" into [(1.0, "text"), (1.5, "\\r")]."""
    out: list[tuple[float, str]] = []
    t = 0.0
    for step in spec.split(","):
        step = step.strip()
        if not step:
            continue
        if ":" in step:
            delay, _, keys = step.partition(":")
            t += float(delay)
        else:
            keys = step
        out.append((t, keys.encode().decode("unicode_escape")))
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("binary")
    ap.add_argument("out")
    ap.add_argument("--width", type=int, default=120)
    ap.add_argument("--height", type=int, default=38)
    ap.add_argument("--seconds", type=float, default=10.0)
    ap.add_argument("--keys", default="")
    ap.add_argument("--workdir", default=None)
    ap.add_argument("--config-dir", default=None)
    ap.add_argument("--data-dir", default=None)
    args = ap.parse_args()

    env = dict(os.environ)
    env["TERM"] = "xterm-256color"
    env["COLORTERM"] = "truecolor"
    # Marshal is colour-tier aware; a truecolor TERM keeps the render faithful.
    if args.config_dir:
        env["MARSHAL_CONFIG_DIR"] = args.config_dir
    if args.data_dir:
        env["MARSHAL_DATA_DIR"] = args.data_dir

    pid, fd = pty.fork()
    if pid == 0:
        if args.workdir:
            os.chdir(args.workdir)
        try:
            os.execvpe(args.binary, [args.binary], env)
        except OSError as exc:
            os.write(2, f"exec failed: {exc}\n".encode())
            os._exit(127)

    set_winsize(fd, args.height, args.width)

    schedule = parse_schedule(args.keys)
    buf = bytearray()
    start = time.time()
    deadline = start + args.seconds
    next_step = 0

    while time.time() < deadline:
        r, _, _ = select.select([fd], [], [], 0.1)
        if r:
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                break
            if not chunk:
                break
            buf.extend(chunk)
        while next_step < len(schedule) and time.time() - start >= schedule[next_step][0]:
            keys = schedule[next_step][1]
            if keys:
                os.write(fd, keys.encode())
            next_step += 1

    try:
        os.kill(pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    try:
        os.close(fd)
    except OSError:
        pass
    try:
        os.waitpid(pid, 0)
    except ChildProcessError:
        pass

    data = bytes(buf)
    with open(args.out, "wb") as f:
        f.write(data)

    print(f"{args.out}: {len(data)} bytes", file=sys.stderr)
    if not data:
        print(
            "warning: captured nothing — the binary probably refused to start",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
