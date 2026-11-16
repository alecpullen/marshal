#!/usr/bin/env python3
"""Generate assets/tray.png: a 22x22 black-on-transparent template image.

macOS tints a template image itself, so the icon must carry no colour of its
own; a coloured icon looks wrong in dark mode. Run this from `app/` after
changing the mark, and commit the result:

    python3 tool/generate_tray_icon.py

Kept in the tree because a hand-made binary PNG cannot be reviewed or
regenerated otherwise.
"""

import math
import struct
import zlib
from pathlib import Path

SIZE = 22
STROKE = 3.0
# The mark: a bold "M", as polylines in the 22x22 grid.
STROKES = [
    ((4.0, 17.0), (4.0, 5.0)),
    ((4.0, 5.0), (11.0, 13.0)),
    ((11.0, 13.0), (18.0, 5.0)),
    ((18.0, 5.0), (18.0, 17.0)),
]


def distance_to_segment(px, py, a, b):
    ax, ay = a
    bx, by = b
    dx, dy = bx - ax, by - ay
    length_sq = dx * dx + dy * dy
    if length_sq == 0:
        return math.hypot(px - ax, py - ay)
    t = ((px - ax) * dx + (py - ay) * dy) / length_sq
    t = max(0.0, min(1.0, t))
    return math.hypot(px - (ax + t * dx), py - (ay + t * dy))


def coverage(px, py):
    """Antialiased coverage: 1 inside the stroke, fading over one pixel."""
    nearest = min(distance_to_segment(px, py, a, b) for a, b in STROKES)
    return max(0.0, min(1.0, STROKE / 2.0 + 0.5 - nearest))


def pixels():
    for y in range(SIZE):
        row = bytearray()
        for x in range(SIZE):
            # Sample at the pixel centre: the mark is black with an alpha
            # that carries the antialiasing.
            alpha = round(255 * coverage(x + 0.5, y + 0.5))
            row += bytes((0, 0, 0, alpha))
        yield bytes(row)


def chunk(kind, data):
    return (
        struct.pack(">I", len(data))
        + kind
        + data
        + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)
    )


def main():
    # Filter byte 0 (None) in front of every scanline.
    raw = b"".join(b"\x00" + row for row in pixels())
    png = b"\x89PNG\r\n\x1a\n"
    png += chunk(b"IHDR", struct.pack(">IIBBBBB", SIZE, SIZE, 8, 6, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(raw, 9))
    png += chunk(b"IEND", b"")
    out = Path(__file__).resolve().parent.parent / "assets" / "tray.png"
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_bytes(png)
    print(f"wrote {out} ({len(png)} bytes)")


if __name__ == "__main__":
    main()
