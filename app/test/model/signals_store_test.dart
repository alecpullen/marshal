import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind;
import 'package:marshal_app/src/model/inbox_model.dart';
import 'package:marshal_app/src/model/signals_store.dart';

import '../support/fake_bridge.dart';
import 'support.dart';

/// Every host's signals (spec D4): the watches that tripped, the requests the
/// egress proxy held and the reviews the bot drafted, read per host and handed
/// to the one inbox.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  Map<String, Object?> watchJson({
    required String agentID,
    required String id,
    String name = 'the gate',
    String state = 'fired',
    int? lastFiredAt,
  }) => {
    'agentId': agentID,
    'id': id,
    'name': name,
    'kind': 'command',
    'state': state,
    'condition': 'exit != 0',
    'mode': 'interval',
    'intervalMs': 5000,
    'owner': agentID,
    'fireCount': 1,
    'lastSample': 'exit 1',
    'createdAt': 1763352000000,
    'lastFiredAt': ?lastFiredAt,
    'samples': <Object>[],
  };

  Map<String, Object?> blockJson({
    required String agentID,
    String host = 'api.example.com',
    int at = 1763352000000,
  }) => {
    'kind': 'network_block',
    'sessionId': 'session-$agentID',
    'agentId': agentID,
    'host': host,
    'workspace': 'default',
    'at': at,
  };

  Map<String, Object?> draftJson({
    required String id,
    String agentID = 'a-2',
  }) => {
    'id': id,
    'repoId': 'acme/widgets',
    'number': 42,
    'title': 'Make the scan report a file it cannot read',
    'headSha': '9f1ad475',
    'agentId': agentID,
    'findings': <Object>[],
    'summary': 'one finding',
    'status': 'draft',
    'createdAt': '2026-11-17T09:00:00Z',
  };

  /// Two hosts over in-process bridges, the one inbox, and the store.
  Future<({ModelFixture fixture, InboxModel inbox, SignalsStore signals})>
  start({Duration interval = const Duration(seconds: 15)}) async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [agent(id: 'a-1', project: '/home/u/alpha')],
      ),
      FixtureHost(
        name: 'studio',
        kind: HostKind.server,
        projects: [project('/home/u/beta')],
        agents: [agent(id: 'a-2', project: '/home/u/beta')],
      ),
    ], now: now);
    await fixture.refresh();
    final inbox = InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(inbox.dispose);
    final signals = SignalsStore(
      fixture.registry,
      inbox: inbox,
      now: fixture.clock.now,
      interval: interval,
    );
    addTearDown(signals.dispose);
    return (fixture: fixture, inbox: inbox, signals: signals);
  }

  /// Every route the store reads, answered with nothing for [host].
  void serveEmpty(ModelFixture fixture, String host) {
    fixture.serve(host, '/api/watches', <Object>[]);
    fixture.serve(host, '/api/network/pending', {'pending': <Object>[]});
    fixture.serve(host, '/api/automations/review/drafts', <Object>[]);
  }

  /// How many times [host]'s bridge was asked for [path].
  int reads(ModelFixture fixture, String host, String path) => fixture
      .requests(host)
      .where((request) => request.method == 'GET' && request.uri == path)
      .length;

  test('every host’s tripped watches are listed, newest first', () async {
    final (:fixture, :inbox, :signals) = await start();
    serveEmpty(fixture, 'local');
    serveEmpty(fixture, 'studio');
    fixture.serve('local', '/api/watches', [
      watchJson(
        agentID: 'a-1',
        id: 'w-1',
        name: 'the gate',
        lastFiredAt: now
            .subtract(const Duration(minutes: 10))
            .millisecondsSinceEpoch,
      ),
    ]);
    fixture.serve('studio', '/api/watches', [
      watchJson(
        agentID: 'a-2',
        id: 'w-2',
        name: 'the queue',
        lastFiredAt: now
            .subtract(const Duration(minutes: 2))
            .millisecondsSinceEpoch,
      ),
    ]);

    await signals.refresh('local');
    await signals.refresh('studio');

    expect(
      [for (final watch in signals.tripped) watch.name],
      ['the queue', 'the gate'],
    );
    expect(
      [for (final watch in signals.tripped) watch.host],
      ['studio', 'local'],
    );
    expect(signals.manyHosts, isTrue);
  });

  test('a second host’s blocked request becomes an inbox ask', () async {
    final (:fixture, :inbox, :signals) = await start();
    serveEmpty(fixture, 'local');
    serveEmpty(fixture, 'studio');
    fixture.serve('studio', '/api/network/pending', {
      'pending': [blockJson(agentID: 'a-2', host: 'api.example.com')],
    });

    await signals.refresh('studio');

    final ask = inbox.asks.singleWhere((ask) => ask.kind == AskKind.network);
    expect(ask.host, 'studio');
    expect(ask.title, 'api.example.com was blocked');
  });

  test('a second host’s held draft becomes an inbox ask', () async {
    final (:fixture, :inbox, :signals) = await start();
    serveEmpty(fixture, 'local');
    serveEmpty(fixture, 'studio');
    fixture.serve('studio', '/api/automations/review/drafts', [
      draftJson(id: 'd-1', agentID: 'a-2'),
    ]);

    await signals.refresh('studio');

    final ask = inbox.asks.singleWhere(
      (ask) => ask.kind == AskKind.reviewDraft,
    );
    expect(ask.host, 'studio');
  });

  test('a failed read keeps that host’s last answer', () async {
    final (:fixture, :inbox, :signals) = await start();
    serveEmpty(fixture, 'local');
    fixture.serve('local', '/api/watches', [
      watchJson(
        agentID: 'a-1',
        id: 'w-1',
        lastFiredAt: now
            .subtract(const Duration(minutes: 4))
            .millisecondsSinceEpoch,
      ),
    ]);
    await signals.refresh('local');
    expect(signals.tripped, hasLength(1));

    // The watches route now fails: the rows it reported before stay.
    fixture.bridges['local']!.reply(
      'GET',
      '/api/watches',
      const FakeReply(500, body: '{"error":"offline"}'),
    );
    await signals.refresh('local');

    expect(signals.tripped, hasLength(1));
    expect(signals.tripped.single.host, 'local');
  });

  test('one route failing does not lose the others', () async {
    final (:fixture, :inbox, :signals) = await start();
    serveEmpty(fixture, 'local');
    fixture.serve('local', '/api/watches', [
      watchJson(
        agentID: 'a-1',
        id: 'w-1',
        lastFiredAt: now
            .subtract(const Duration(minutes: 4))
            .millisecondsSinceEpoch,
      ),
    ]);
    await signals.refresh('local');
    expect(signals.tripped, hasLength(1));

    // The watches read fails, but the blocks route still answers: the tripped
    // watches stay and the block is handed over.
    fixture.bridges['local']!.reply(
      'GET',
      '/api/watches',
      const FakeReply(500, body: '{"error":"offline"}'),
    );
    fixture.serve('local', '/api/network/pending', {
      'pending': [blockJson(agentID: 'a-1', host: 'api.example.com')],
    });
    await signals.refresh('local');

    expect(signals.tripped, hasLength(1));
    expect(
      inbox.asks.where((ask) => ask.kind == AskKind.network),
      hasLength(1),
    );
  });

  test('a host never answered contributes nothing', () async {
    final (:fixture, :inbox, :signals) = await start();
    serveEmpty(fixture, 'local');
    fixture.serve('local', '/api/watches', [
      watchJson(
        agentID: 'a-1',
        id: 'w-1',
        lastFiredAt: now
            .subtract(const Duration(minutes: 4))
            .millisecondsSinceEpoch,
      ),
    ]);
    // `studio`'s routes are not scripted, so each read is refused with 404.

    await signals.refresh('local');
    await signals.refresh('studio');

    expect(signals.tripped, hasLength(1));
    expect(signals.tripped.single.host, 'local');
    expect(inbox.asks, isEmpty);
  });

  test('a refresh during a read runs once after it', () async {
    final (:fixture, :inbox, :signals) = await start();
    serveEmpty(fixture, 'local');
    fixture.serve('local', '/api/watches', [
      watchJson(agentID: 'a-1', id: 'w-1'),
    ], delay: const Duration(milliseconds: 50));

    // The first read is in flight; the second refresh is asked meanwhile and
    // runs once, after it.
    final first = signals.refresh('local');
    final second = signals.refresh('local');
    await Future.wait([first, second]);

    expect(reads(fixture, 'local', '/api/watches'), 2);
  });

  test('the interval re-reads every host', () async {
    final (:fixture, :inbox, :signals) = await start(
      interval: const Duration(milliseconds: 10),
    );
    serveEmpty(fixture, 'local');
    serveEmpty(fixture, 'studio');

    signals.start();
    await eventually(
      () =>
          reads(fixture, 'local', '/api/watches') >= 1 &&
          reads(fixture, 'studio', '/api/watches') >= 1,
    );
    await eventually(
      () =>
          reads(fixture, 'local', '/api/watches') >= 2 &&
          reads(fixture, 'studio', '/api/watches') >= 2,
      reason: 'the interval reads every host again',
    );
  });

  test("a forgotten host's asks leave the inbox", () async {
    final (:fixture, :inbox, :signals) = await start();
    serveEmpty(fixture, 'local');
    fixture.serve('local', '/api/watches', [
      watchJson(
        agentID: 'a-1',
        id: 'w-1',
        lastFiredAt: now
            .subtract(const Duration(minutes: 4))
            .millisecondsSinceEpoch,
      ),
    ]);
    fixture.serve('local', '/api/network/pending', {
      'pending': [blockJson(agentID: 'a-1', host: 'api.example.com')],
    });
    fixture.serve('local', '/api/automations/review/drafts', [
      draftJson(id: 'd-1', agentID: 'a-1'),
    ]);

    await signals.refresh('local');
    expect(signals.tripped, hasLength(1));
    expect(
      inbox.asks.where((ask) => ask.kind == AskKind.network),
      hasLength(1),
    );
    expect(
      inbox.asks.where((ask) => ask.kind == AskKind.reviewDraft),
      hasLength(1),
    );

    fixture.registry.forget('local');
    await settle();

    expect(signals.tripped, isEmpty);
    expect(inbox.asks, isEmpty);
  });
}
