"""Send one file to SSH stdin; only the SSH exit confirms remote completion."""
import argparse
import os
import shlex
import subprocess
import time
from progress import Progress

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
