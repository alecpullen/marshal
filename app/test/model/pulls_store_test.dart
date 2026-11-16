import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/pulls_store.dart';
import 'package:marshal_app/src/model/thread_ref.dart';

import 'support.dart';

/// One pull request as a test writes one: every field it does not name is the
/// quietest value there is.
PullRecord pull({
  String repo = 'acme/widgets',
  int number = 1,
  String title = 'Quiet capture mode',
  String head = 'a1b2c3d4',
  String? agentID,
  String? factoryItem,
  PullState state = PullState.pullReady,
  List<PullCheck> checks = const [
    PullCheck(name: 'build', state: CheckState.checkPassed),
  ],
  String? unavailable,
  int updatedAt = 0,
}) => PullRecord(
  id: '$repo#$number',
  repoID: repo,
  projectRoot: '/home/u/alpha',
  number: number,
  url: 'https://forge.example/$repo/pull/$number',
  title: title,
  headRef: 'agent/quiet-capture',
  baseRef: 'main',
  headSHA: head,
  draft: false,
  additions: 412,
  deletions: 96,
  origin: agentID != null
      ? PullOrigin.pullOriginAgent
      : PullOrigin.pullOriginAutomation,
  agentID: agentID,
  factoryItem: factoryItem,
  state: state,
  checks: checks,
  reviews: '2 reviews passed',
  unavailable: unavailable,
  observedAt: updatedAt,
  updatedAt: updatedAt,
);

/// One frame of the fleet's `pull` delta.
Map<String, Object?> pullDelta({PullRecord? record, String? removed}) => {
  'kind': DeltaKind.deltaPull.wire,
  if (record != null) 'pull': record.toJson(),
  if (removed != null) 'removed': removed,
};

void main() {
  /// A store over one host whose bridge serves [records] at `/api/pulls`.
  Future<(ModelFixture, PullsStore)> storeFor(List<PullRecord> records) async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [agent(id: 'a-1')],
      ),
    ]);
    await fixture.refresh();
    fixture.serve('local', '/api/pulls', {
      'pulls': [for (final record in records) record.toJson()],
    });
    final store = PullsStore(fixture.registry, fixture.store);
    addTearDown(store.dispose);
    await eventually(() => store.all().isNotEmpty, reason: 'the index is read');
    return (fixture, store);
  }

  test('the index is read per host', () async {
    final (_, store) = await storeFor([
      pull(number: 1, title: 'One'),
      pull(number: 2, title: 'Two'),
    ]);

    expect([for (final r in store.all()) r.title], ['One', 'Two']);
    expect(store.byId('local', 'acme/widgets#1')?.title, 'One');
    expect(store.byId('other', 'acme/widgets#1'), isNull);
  });

  test('a delta updates a record', () async {
    final (fixture, store) = await storeFor([pull(number: 1)]);

    fixture.delta(
      'local',
      pullDelta(
        record: pull(
          number: 1,
          title: 'A new title',
          state: PullState.pullFailing,
          checks: const [
            PullCheck(name: 'build', state: CheckState.checkFailed),
          ],
          updatedAt: 2000,
        ),
      ),
    );
    await eventually(
      () => store.byId('local', 'acme/widgets#1')?.title == 'A new title',
      reason: 'the delta is applied',
    );

    final record = store.byId('local', 'acme/widgets#1')!;
    expect(record.state, PullState.pullFailing);
    expect(record.checks.single.state, CheckState.checkFailed);
    expect(store.all(), hasLength(1), reason: 'a delta is not a second record');
  });

  test('a moved head shows no old checks', () async {
    final (fixture, store) = await storeFor([
      pull(
        number: 1,
        head: 'old-head',
        checks: const [
          PullCheck(name: 'build', state: CheckState.checkPassed),
          PullCheck(name: 'unit', state: CheckState.checkPassed),
        ],
      ),
    ]);
    expect(store.all().single.checks, hasLength(2));

    // The bridge discards the old head's evidence before it announces the new
    // head (F5 decision 3); the app keeps none of it either.
    fixture.delta(
      'local',
      pullDelta(
        record: pull(
          number: 1,
          head: 'new-head',
          state: PullState.pullInProgress,
          checks: const [],
          updatedAt: 3000,
        ),
      ),
    );
    await eventually(
      () => store.byId('local', 'acme/widgets#1')?.headSHA == 'new-head',
      reason: 'the moved head arrives',
    );

    expect(store.all().single.checks, isEmpty);
  });

  test('a removed record leaves the index', () async {
    final (fixture, store) = await storeFor([pull(number: 1)]);

    fixture.delta('local', pullDelta(removed: 'acme/widgets#1'));
    await eventually(
      () => store.all().isEmpty,
      reason: 'the removal is applied',
    );
  });

  test('forThread finds the records a thread shipped', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [agent(id: 'a-1')],
      ),
    ]);
    await fixture.refresh();
    fixture.serve('local', '/api/pulls', {
      'pulls': [
        pull(number: 1, agentID: 'a-1', updatedAt: 1000).toJson(),
        pull(number: 2, agentID: 'a-1', updatedAt: 2000).toJson(),
        pull(number: 3).toJson(),
      ],
    });
    final store = PullsStore(fixture.registry, fixture.store);
    addTearDown(store.dispose);
    await eventually(() => store.all().length == 3);

    final thread = threadFor('local', agent(id: 'a-1'));
    expect([for (final r in store.forThread(thread)) r.number], [2, 1]);
  });
}
