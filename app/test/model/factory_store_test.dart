import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
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

  group('observedChecks', () {
    /// A store over one host whose bridge reports [items].
    Future<FactoryStore> storeOf(List<FactoryItem> items) async {
      final fixture = await ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/alpha')],
          factoryItems: items,
        ),
      ]);
      final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
      addTearDown(factory.dispose);
      await factory.refresh('local');
      return factory;
    }

    test('keeps the greatest observation of each check', () async {
      final factory = await storeOf([
        factoryItem(
          rounds: [
            factoryRound(
              number: 1,
              ci: factoryCI(
                checks: [factoryCheck(observedAt: '2026-11-17T09:10:00Z')],
              ),
            ),
            factoryRound(
              number: 2,
              ci: factoryCI(
                checks: [factoryCheck(observedAt: '2026-11-17T09:40:00Z')],
              ),
            ),
          ],
        ),
      ]);

      final observed = factory.observedChecks(projectRoot: '/home/u/alpha');

      expect(observed, hasLength(1));
      expect(observed.single.observedAt, '2026-11-17T09:40:00Z');
      expect(observed.single.revision, 2, reason: 'the round that saw it');
    });

    test('equal observations keep the one first seen', () async {
      // The bridge stamps UTC, so RFC3339 sorts as text. Two observations
      // stamped alike are equal, and the first in bridge order is kept.
      final factory = await storeOf([
        factoryItem(
          id: 'item-1',
          rounds: [
            factoryRound(
              number: 2,
              ci: factoryCI(
                checks: [factoryCheck(observedAt: '2026-11-17T09:10:00Z')],
              ),
            ),
          ],
        ),
        factoryItem(
          id: 'item-2',
          rounds: [
            factoryRound(
              number: 5,
              ci: factoryCI(
                checks: [factoryCheck(observedAt: '2026-11-17T09:10:00Z')],
              ),
            ),
          ],
        ),
      ]);

      final observed = factory.observedChecks(projectRoot: '/home/u/alpha');

      expect(observed, hasLength(1));
      expect(observed.single.revision, 2);
    });

    test('the same name under different apps stays distinct', () async {
      final factory = await storeOf([
        factoryItem(
          rounds: [
            factoryRound(
              ci: factoryCI(
                checks: [
                  factoryCheck(appID: 42),
                  factoryCheck(appID: 7),
                  factoryCheck(kind: 'commit_status', appID: 0),
                ],
              ),
            ),
          ],
        ),
      ]);

      final observed = factory.observedChecks(projectRoot: '/home/u/alpha');

      expect(observed, hasLength(3));
      expect(
        observed
            .map((each) => (each.check.kind, each.check.name, each.check.appID))
            .toSet(),
        {
          ('check_run', 'build', 42),
          ('check_run', 'build', 7),
          ('commit_status', 'build', 0),
        },
      );
    });

    test('reads only the project asked about', () async {
      final factory = await storeOf([
        factoryItem(
          id: 'item-1',
          rounds: [
            factoryRound(
              ci: factoryCI(checks: [factoryCheck(name: 'build')]),
            ),
          ],
        ),
        factoryItem(
          id: 'item-2',
          project: '/home/u/beta',
          rounds: [
            factoryRound(
              ci: factoryCI(checks: [factoryCheck(name: 'other')]),
            ),
          ],
        ),
      ]);

      final observed = factory.observedChecks(projectRoot: '/home/u/alpha');

      expect(observed.map((each) => each.check.name), ['build']);
    });

    test('reads only the repository asked about, or every one', () async {
      final factory = await storeOf([
        factoryItem(
          id: 'item-1',
          repo: 'repo-1',
          rounds: [
            factoryRound(
              ci: factoryCI(checks: [factoryCheck(name: 'build')]),
            ),
          ],
        ),
        factoryItem(
          id: 'item-2',
          repo: 'repo-2',
          rounds: [
            factoryRound(
              ci: factoryCI(checks: [factoryCheck(name: 'other')]),
            ),
          ],
        ),
      ]);

      expect(
        factory
            .observedChecks(projectRoot: '/home/u/alpha', repoID: 'repo-1')
            .map((each) => each.check.name),
        ['build'],
      );
      expect(
        factory
            .observedChecks(projectRoot: '/home/u/alpha', repoID: 'repo-2')
            .map((each) => each.check.name),
        ['other'],
      );
      // A project that names no repository has no repository filter to apply.
      expect(
        factory
            .observedChecks(projectRoot: '/home/u/alpha')
            .map((each) => each.check.name),
        ['build', 'other'],
      );
    });

    test('a round without CI is no evidence, and changes nothing', () async {
      final factory = await storeOf([
        factoryItem(
          rounds: [
            factoryRound(number: 1),
            factoryRound(number: 2, ci: factoryCI(checks: const [])),
          ],
        ),
      ]);
      var notified = 0;
      factory.addListener(() => notified++);
      final before = factory.items();

      final observed = factory.observedChecks(projectRoot: '/home/u/alpha');

      expect(observed, isEmpty);
      expect(notified, 0, reason: 'reading evidence notifies nobody');
      expect(
        identical(factory.items().single, before.single),
        isTrue,
        reason: 'the items it read are not rebuilt',
      );
      expect(factory.byId('local', 'item-1')!.rounds, hasLength(2));
    });
  });
}
