import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart';

import '../support/fake_bridge.dart';
import 'support.dart';

/// Settings' Network section turns the egress proxy's blocked requests into
/// asks (F7 decision 7): one card in the Inbox and in the agent's thread, with
/// the three answers the decision route carries.
void main() {
  final at = DateTime.utc(2026, 11, 17, 4, 12);

  NetPending blocked() => NetPending(
    kind: 'network_block',
    sessionID: 's-1',
    agentID: 'a-1',
    host: 'registry.npmjs.org',
    workspace: 'go-service',
    at: at.millisecondsSinceEpoch,
  );

  /// A store with one agent, a bridge that answers the decision route, and an
  /// inbox holding one blocked request.
  Future<(ModelFixture, InboxModel)> started() async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [agent(id: 'a-1')],
      ),
    ]);
    fixture.bridges['local']!.reply(
      'POST',
      '/api/network/decisions',
      const FakeReply(
        200,
        headers: {'content-type': 'application/json'},
        body: '{"ok":true}',
      ),
    );
    await fixture.refresh();
    final inbox = InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(inbox.dispose);
    inbox.showPendingBlocks('local', [blocked()]);
    return (fixture, inbox);
  }

  test('a pending request is an ask', () async {
    final (_, inbox) = await started();

    final ask = inbox.asks.single;
    expect(ask.kind, AskKind.network);
    expect(ask.title, 'registry.npmjs.org was blocked');
    expect(ask.body, 'go-service · agent a-1');
    expect(ask.network!.agentID, 'a-1');
    expect(ask.network!.host, 'registry.npmjs.org');
    expect(ask.network!.workspace, 'go-service');
    // It belongs to the agent's thread, so the same card is pinned there.
    expect(ask.thread?.id, 'a-1');
    expect(ask.actions.map((action) => action.label), [
      'Allow for this agent',
      'Add to the environment',
      'Block',
    ]);
    expect(ask.actions.first.primary, isTrue);
  });

  test('each network decision posts the right choice', () async {
    const decisions = {
      'Allow for this agent': '"decision":"allow-agent"',
      'Add to the environment': '"decision":"add-to-workspace"',
      'Block': '"decision":"block"',
    };

    for (final MapEntry(key: label, value: decision) in decisions.entries) {
      final (fixture, inbox) = await started();
      final ask = inbox.asks.single;
      final action = ask.actions.firstWhere((action) => action.label == label);

      await inbox.answer(ask, action);

      final posted = fixture
          .requests('local')
          .where((request) => request.uri == '/api/network/decisions')
          .last;
      expect(posted.method, 'POST', reason: label);
      expect(posted.body, contains(decision), reason: label);
      expect(posted.body, contains('"agentId":"a-1"'), reason: label);
      expect(
        posted.body,
        contains('"host":"registry.npmjs.org"'),
        reason: label,
      );
    }
  });
}
