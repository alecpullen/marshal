#!/usr/bin/env python3
"""Ping the ollama-cloud provider via ACP with a tiny prompt to measure latency."""
import sys, time
sys.path.insert(0, "/tmp/kilo")
from marshal_acp_driver import ACPClient, initialize_and_create_session, run_single_turn, close_session
from pathlib import Path

Path("/tmp/kilo/marshal_acp_ping.log").write_text("", encoding="utf-8")
client = ACPClient()
client.start()
try:
    sid = initialize_and_create_session(client)
    t0 = time.time()
    reply, resp = run_single_turn(client, sid, "Reply with exactly the word pong.", timeout=120.0)
    print(f"elapsed={time.time()-t0:.1f}s stop={resp} reply={reply!r}")
    close_session(client, sid)
finally:
    client.close()
