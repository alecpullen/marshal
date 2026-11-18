import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/host.dart';
import 'package:marshal_app/src/model/factory_store.dart';
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/thread_ref.dart';

import '../support/fake_bridge.dart';
import 'support.dart';

/// An answer command as a screen sends one: only the fields it sets.
FactoryCommand answerCommand() => FactoryCommand.fromJson({
  'requestId': '',
  'kind': 'answer',
  'decisionId': 'decision-1',
  'answer': 'the rounds',
  'reason': '',
  'expectedVersion': 0,
  'additionalRounds': 0,
  'replacement': null,
  'implementerRouting': null,
  'reviewerRouting': null,
  'confirmPreviousWorkerStopped': false,
});

void main() {
  test('reads a host\u2019s items and keeps them', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        factoryItems: [
          factoryItem(
            id: 'item-1',
            workers: [factoryWorker(agentId: 'agent-1')],
          ),
          factoryItem(id: 'item-2', project: '/home/u/beta'),
        ],
      ),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);

    await factory.refresh('local');

    expect(factory.items(host: 'local').map((item) => item.id), [
      'item-1',
      'item-2',
    ]);
    expect(factory.items(projectRoot: '/home/u/alpha'), hasLength(1));
    expect(factory.byId('local', 'item-2')?.projectRoot, '/home/u/beta');
    expect(factory.workerAgentIds, {'agent-1'});
    expect(factory.loaded('local'), isTrue);
    expect(factory.failure('local'), isNull);
  });

  test('a worker is not an independent agent', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        agents: [agent(id: 'agent-1')],
        projects: [project('/home/u/alpha')],
        factoryItems: [
          factoryItem(
            id: 'item-1',
            workers: [factoryWorker(agentId: 'agent-1')],
          ),
        ],
      ),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);
    await fixture.refresh();
    await factory.refresh('local');

    final nav = buildNavModel(
      const Place.overview(),
      fixture.store,
      factory: factory,
    );

    expect(nav.projects.single.current.map((thread) => thread.id), ['item-1']);
    expect(nav.projects.single.current.single.kind, ThreadKind.workItem);
    expect(nav.projects.single.current.single.status, ThreadStatus.running);
  });

  test('a historical worker is not an independent agent', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        agents: [agent(id: 'agent-1')],
        projects: [project('/home/u/alpha')],
        factoryItems: [
          factoryItem(
            id: 'item-1',
            state: 'closed',
            workers: [factoryWorker(agentId: 'agent-1')],
          ),
        ],
      ),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);
    await fixture.refresh();
    await factory.refresh('local');

    final nav = buildNavModel(
      const Place.overview(),
      fixture.store,
      factory: factory,
    );

    // The item is what the row lists; the agent is still the factory's, so it
    // is not an independent agent row of its own.
    final row = nav.projects.single;
    expect(row.current.map((thread) => thread.id), ['item-1']);
    expect(row.current.single.kind, ThreadKind.workItem);
    expect(row.current.single.status, ThreadStatus.idle);
    expect(factory.workerAgentIds, {'agent-1'});
  });

  test('a version conflict reloads the item and keeps the action', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        factoryItems: [factoryItem(id: 'item-1', version: 3)],
      ),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);
    await factory.refresh('local');

    final bridge = fixture.bridges['local']!;
    bridge
      ..reply(
        'POST',
        '/api/factory/items/item-1/commands',
        const FakeReply(
          409,
          headers: {'content-type': 'application/json'},
          body: '{"error":"conflict","code":"conflict"}',
        ),
      )
      ..reply(
        'GET',
        '/api/factory/items/item-1',
        FakeReply(
          200,
          headers: const {'content-type': 'application/json'},
          body: jsonEncode({
            'item': factoryItem(id: 'item-1', version: 4).toJson(),
          }),
        ),
      );

    final item = await factory.command('local', 'item-1', answerCommand());

    expect(item.version, 4);
    expect(factory.byId('local', 'item-1')?.version, 4);
    expect(factory.conflictOf('local', 'item-1'), FactoryStore.conflictWords);

    final post = fixture
        .requests('local')
        .lastWhere((request) => request.method == 'POST');
    final body = jsonDecode(post.body) as Map<String, dynamic>;
    // The command named the version that was on screen, and a fresh request
    // id, so the bridge can refuse a decision made against a moved item.
    expect(body['expectedVersion'], 3);
    expect(body['requestId'], isNotEmpty);
    expect(body['decisionId'], 'decision-1');

    factory.clearConflict('local', 'item-1');
    expect(factory.conflictOf('local', 'item-1'), isNull);
  });

  test('a successful read records when it happened', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        factoryItems: [factoryItem(id: 'item-1')],
      ),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);

    expect(factory.lastReadAt('local'), isNull);

    await factory.refresh('local');
    expect(factory.lastReadAt('local'), fixture.clock.now());

    // The store's clock, not the wall clock: the time it records is the one
    // the fixture moved.
    fixture.clock.advance(const Duration(minutes: 2));
    await factory.refresh('local');
    expect(
      factory.lastReadAt('local'),
      DateTime.utc(2026, 11, 17, 4, 2),
      reason: 'the injected clock is what a read time comes from',
    );
    expect(factory.lastReadAt('local'), fixture.clock.now());
  });

  test('a failed read keeps the time of the last one that worked', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        factoryItems: [factoryItem(id: 'item-1')],
      ),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);
    await factory.refresh('local');
    final read = factory.lastReadAt('local');
    expect(read, isNotNull);
    expect(factory.byId('local', 'item-1'), isNotNull);

    fixture.clock.advance(const Duration(minutes: 2));
    fixture.bridges['local']!.reply(
      'GET',
      '/api/factory/items',
      const FakeReply(
        500,
        headers: {'content-type': 'application/json'},
        body: '{"error":"offline"}',
      ),
    );
    await factory.refresh('local');

    // The age stays the last read that worked: that is what makes "2m old"
    // true, and a failed read is not a read of an emptier list.
    expect(factory.lastReadAt('local'), read);
    expect(factory.failure('local'), 'offline');
    expect(factory.byId('local', 'item-1'), isNotNull);
  });

  test('a host that has not answered has no read time', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        factoryItems: [factoryItem(id: 'item-1')],
      ),
      const FixtureHost(name: 'studio', kind: HostKind.server),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);

    await factory.refresh('local');

    expect(factory.lastReadAt('local'), isNotNull);
    expect(factory.lastReadAt('studio'), isNull);
    expect(factory.loaded('studio'), isFalse);
  });
}
