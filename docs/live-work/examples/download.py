"""Download URL to a new destination, counting bytes actually received."""
import argparse
import time
import urllib.request
from progress import Progress

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
