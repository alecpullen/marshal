#!/usr/bin/env python3
"""Example script showing how to use the ACPClient class."""

from acp_client import ACPClient, TextBlock


def main() -> int:
    project_root = "/Users/alecpullen/projects/marshal"

    with ACPClient(cwd=project_root) as client:
        info = client.initialize()
        print("capabilities:", info.get("agentCapabilities"))

        session = client.session_new(cwd=project_root)
        print("session id:", session.session_id)

        reply, result = client.prompt(
            session.session_id,
            "Run 'git status --short' and report the output in one sentence.",
            timeout=120.0,
        )
        print("stop_reason:", result.stop_reason)
        print("reply:", reply)

        client.session_close(session.session_id)
        print("session closed")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
