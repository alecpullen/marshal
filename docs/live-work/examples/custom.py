"""Hash actual files and write a new manifest, reporting two measured phases."""
import argparse
import hashlib
import json
import os
from progress import Progress

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
