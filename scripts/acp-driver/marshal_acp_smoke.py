#!/usr/bin/env python3
"""Smoke test for marshal ACP JSON-RPC: initialize, session/new, one prompt, close."""

import json, os, subprocess, threading, time
from pathlib import Path

MARSHAL_BIN = Path("/tmp/kilo/marshal")
PROJECT_ROOT = Path("/Users/alecpullen/projects/marshal").resolve()

proc = subprocess.Popen(
    [str(MARSHAL_BIN), "acp"],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    cwd=str(PROJECT_ROOT), text=True, bufsize=1,
)

def drain(stream, label):
    for line in stream:
        print(f"[{label}] {line.rstrip()}")
threading.Thread(target=drain, args=(proc.stderr, "stderr"), daemon=True).start()

def send(obj):
    raw = json.dumps(obj, separators=(",", ":"), ensure_ascii=False)
    print(f"-> {raw}")
    proc.stdin.write(raw + "\n")
    proc.stdin.flush()

def read_one(timeout=30):
    start = time.time()
    while time.time() - start < timeout:
        line = proc.stdout.readline()
        if line:
            print(f"<- {line.rstrip()}")
            return json.loads(line.strip())
        time.sleep(0.05)
    raise TimeoutError("no frame")

send({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}})
print("init resp:", read_one())

send({"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":str(PROJECT_ROOT),"mcpServers":[]}})
resp = read_one()
print("session/new resp:", resp)
sid = resp.get("result", {}).get("sessionId")
print("session id:", sid)

send({"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":sid,"prompt":[{"type":"text","text":"Say 'ACP smoke test OK' and nothing else."}]}})
print("prompt resp:", read_one())

send({"jsonrpc":"2.0","id":4,"method":"session/close","params":{"sessionId":sid}})
print("close resp:", read_one())

proc.stdin.close()
proc.wait(timeout=10)
print("exit code:", proc.returncode)
