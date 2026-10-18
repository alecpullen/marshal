"""Small events-v1 producer; run under shell.run with source events-v1."""
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
