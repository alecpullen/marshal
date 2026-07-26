#!/usr/bin/env python3
"""Minimal pytest-style tests demonstrating how to test Marshal over ACP.

Run with:
    python3 -m pytest scripts/acp-driver/test_acp_client.py -v

Requires `go build -o /tmp/kilo/marshal ./cmd/marshal` first.
"""

import pytest
from pathlib import Path

from acp_client import ACPClient, ACPError, AutoApprovePolicy, DenyAllPolicy, TextBlock


PROJECT_ROOT = Path(__file__).resolve().parents[2]


@pytest.fixture
def client(tmp_path):
    c = ACPClient(cwd=PROJECT_ROOT)
    c.start()
    try:
        yield c
    finally:
        c.close()


def test_initialize(client):
    info = client.initialize()
    assert info.get("protocolVersion") == 1
    assert "agentCapabilities" in info


def test_session_lifecycle(client, tmp_path):
    session = client.session_new(cwd=PROJECT_ROOT)
    assert session.session_id

    page = client.session_list(cwd=PROJECT_ROOT)
    ids = {s["sessionId"] for s in page.sessions}
    assert session.session_id in ids

    client.session_close(session.session_id)


def test_prompt_receives_reply(client):
    session = client.session_new(cwd=PROJECT_ROOT)
    reply, result = client.prompt(
        session.session_id,
        "Reply with exactly the word 'ack'.",
        timeout=60.0,
    )
    assert result.stop_reason == "end_turn"
    assert "ack" in reply.lower()
    client.session_close(session.session_id)


def test_content_blocks(client):
    session = client.session_new(cwd=PROJECT_ROOT)
    reply, result = client.prompt(
        session.session_id,
        [TextBlock("Say 'blocks ok'.")],
        timeout=60.0,
    )
    assert result.stop_reason == "end_turn"
    client.session_close(session.session_id)


def test_duplicate_prompt_rejected(client):
    session = client.session_new(cwd=PROJECT_ROOT)

    # Start a blocking prompt in the background.
    import concurrent.futures
    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
        future = pool.submit(
            client.prompt,
            session.session_id,
            "Wait for my next message. Do nothing else.",
            timeout=30.0,
        )

        # Give the first prompt time to reserve the active-turn slot.
        import time
        time.sleep(1.0)

        with pytest.raises(ACPError) as exc_info:
            client.prompt(session.session_id, "second", timeout=10.0)

        assert exc_info.value.code == -32000
        assert "active turn" in exc_info.value.message.lower()

        # Cancel the first prompt so the background thread unblocks.
        client.cancel(session.session_id)
        future.result(timeout=10.0)

    client.session_close(session.session_id)


def test_permission_policy_can_deny():
    policy = DenyAllPolicy()
    assert policy.decide({"toolName": "shell.run", "command": "ls"})["approved"] is False


from acp_client import DeclineQuestionsPolicy, StaticAnswersPolicy


def test_question_policy_can_decline():
    assert DeclineQuestionsPolicy().decide({"questions": []}) == {"declined": True}


def test_static_answers_policy_returns_answers():
    answers = [{"question": "pick", "answer": "a"}]
    assert StaticAnswersPolicy(answers).decide({"questions": []}) == {"answers": answers}
