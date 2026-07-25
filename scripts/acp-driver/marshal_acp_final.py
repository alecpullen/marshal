#!/usr/bin/env python3
"""Final full verification via ACP."""
import sys
sys.path.insert(0, "/tmp/kilo")
from marshal_acp_driver import ACPClient, initialize_and_create_session, run_single_turn, close_session, log
from pathlib import Path

LOG_PATH = Path("/tmp/kilo/marshal_acp_final.log")
LOG_PATH.write_text("", encoding="utf-8")

client = ACPClient()
client.start()
try:
    sid = initialize_and_create_session(client)
    prompt = """Run final verification for the token metrics tracking work.

Steps:
1. Run `gofmt -w .`
2. Run `go vet ./...` and report any issues.
3. Run `go test ./...` and report the results. All tests must pass.
4. Run `go build ./cmd/marshal`.

End with exactly one of:
FINAL STATUS: READY
or
FINAL STATUS: FAILED - reason
"""
    reply, resp = run_single_turn(client, sid, prompt, timeout=900.0)
    print("STOP:", resp)
    print("REPLY:\n", reply)
    close_session(client, sid)
finally:
    client.close()
