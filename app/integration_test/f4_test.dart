import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/changes_model.dart';

import '../test/support/fake_bridge.dart';

/// F4's end-to-end flow, driven on a device where one exists
/// (f4-inspector.md, Work 9): an agent makes a change; the app runs the gate,
/// comments on a line, sees the agent answer, and ships to the fixture forge.
///
/// The fixture is the same in-process fake bridge the unit tests use, so the
/// flow needs no daemon, no keychain and no network. The UI half of the same
/// flow is `test/ui/inspector/changes_test.dart` and `ship_test.dart`, which
/// run everywhere; this is the same calls on a real device.
void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('a change is gated, commented on and shipped', (tester) async {
    HttpOverrides.global = null;
    final bridge = await FakeBridge.onLoopback();
    addTearDown(bridge.close);
    FakeReply json(Object body, [int status = 200]) => FakeReply(
      status,
      headers: const {'content-type': 'application/json'},
      body: jsonEncode(body),
    );

    const diff =
        'diff --git a/internal/acp/stack.go b/internal/acp/stack.go\n'
        '--- a/internal/acp/stack.go\n'
        '+++ b/internal/acp/stack.go\n'
        '@@ -1,2 +1,2 @@\n'
        ' one\n'
        '-two\n'
        '+three\n';

    bridge
      ..reply(
        'GET',
        '/api/agents/a-1/diff',
        json(
          const DiffResult(
            files: [
              DiffFile(path: 'internal/acp/stack.go', added: 1, removed: 1),
            ],
            diff: diff,
          ).toJson(),
        ),
      )
      ..reply(
        'GET',
        '/api/agents/a-1/gate',
        json(
          const GateRecord(
            at: '2026-11-17T04:08:00Z',
            result: GateResult(ok: true, skipped: false),
          ).toJson(),
        ),
      )
      ..reply(
        'GET',
        '/api/agents/a-1/commit-draft',
        json(const CommitDraft(message: 'acp: trim the fold fixture').toJson()),
      )
      ..reply(
        'GET',
        '/api/agents/a-1/review/comments',
        json({'comments': <Object>[]}),
      )
      ..reply(
        'POST',
        '/api/agents/a-1/review/comments',
        json(
          const ReviewComment(
            id: 'rc-1',
            agentID: 'a-1',
            path: 'internal/acp/stack.go',
            line: 2,
            side: 'old',
            quote: '-two',
            body: 'Keep this line: the fold test reads it.',
            createdAt: '2026-11-17T04:09:00Z',
            sentAt: '2026-11-17T04:09:01Z',
            ownerID: 'u-1',
          ).toJson(),
        ),
      )
      ..reply(
        'POST',
        '/api/agents/a-1/exit',
        json(
          const ExitResult(
            destination: 'push',
            branch: 'f4-inspector',
            prUrl: 'https://example.com/marshal/pull/7',
          ).toJson(),
        ),
      );

    final client = BridgeClient(
      serverTransport(bridge.base, token: () async => 'unused'),
    );
    final changes = ChangesModel(client, agentId: 'a-1');

    // The change arrives, and the gate says it passes.
    await changes.refresh();
    expect(changes.files.single.path, 'internal/acp/stack.go');
    expect(changes.gateChecks.single.state, GateState.passed);
    expect(changes.diffOf('internal/acp/stack.go'), contains('+three'));

    // A comment on the line is the steering the agent hears, and its thread
    // is in the model.
    await changes.comment(
      'internal/acp/stack.go',
      2,
      'Keep this line: the fold test reads it.',
      side: 'old',
      quote: '-two',
    );
    expect(changes.commentThreads.single.id, 'rc-1');
    expect(changes.commentThreads.single.sent, isTrue);

    // Shipping says where it went, and the pull request's link comes back.
    final result = await changes.ship(
      message: changes.draft?.message ?? 'acp: trim the fold fixture',
      openPr: true,
    );
    expect(result.outcome, ShipOutcome.pullRequest);
    expect(result.url, 'https://example.com/marshal/pull/7');

    // The bridge saw the three writes a person made: the comment, and the
    // exit that shipped it.
    final writes = bridge.requests.where((r) => r.method == 'POST').toList();
    expect(
      writes.map((r) => r.uri),
      containsAll(['/api/agents/a-1/review/comments', '/api/agents/a-1/exit']),
    );
  });
}
