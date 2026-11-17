import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' hide CheckState;
import 'package:marshal_app/src/model/factory_store.dart';
import 'package:marshal_app/src/model/revision_model.dart';
import 'package:marshal_app/src/model/work_item_model.dart';
import 'package:marshal_app/src/ui/factory/revision_sheet.dart';
import 'package:marshal_app/src/ui/factory/work_item_screen.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';
import '../../support/kit.dart';

/// F6 plan 1, task 5: a worker's conversation in the item's centre, the way
/// back, and a plan revision proposed against the approved one.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  /// One effect as a worker-state fixture writes one.
  FactoryEffect effect(String id, {String status = 'dispatched'}) =>
      FactoryEffect.fromJson({
        'id': id,
        'kind': 'implement',
        'status': status,
        'workerRole': 'implementer',
        'round': 1,
        'approvalRevision': 1,
        'worker': null,
        'expectedHead': 'head-1',
        'expectedBase': 'base-1',
        'createdAt': '2026-11-17T09:10:00Z',
        'outcome': null,
      });

  /// The item: four workers the item's own effects say different things
  /// about — one dispatched, one completed, one parked, one with no effect
  /// this build can read.
  FactoryItem item({
    String state = 'implementing',
    List<FactoryEvent>? history,
  }) => factoryItem(
    state: state,
    workers: [
      factoryWorker(agentId: 'agent-imp', sessionId: 'sess-imp'),
      factoryWorker(
        agentId: 'agent-rev',
        sessionId: 'sess-rev',
        role: 'reviewer',
      ),
      factoryWorker(
        agentId: 'agent-park',
        sessionId: 'sess-park',
        parked: true,
      ),
      factoryWorker(
        agentId: 'agent-unknown',
        sessionId: 'sess-unknown',
        role: 'reviewer',
        spawnEffectId: 'effect-never-seen',
      ),
    ],
    effects: [
      effect('effect-implementer'),
      effect('effect-reviewer', status: 'completed'),
    ],
    history: history ?? [factoryEvent(1, 'created', 'the draft was created')],
  );

  /// One pump of the screen with the whole tree the same every time, so the
  /// State survives a re-pump: only [subject] varies.
  Future<void> show(
    WidgetTester tester,
    FactoryItem subject, {
    Widget? center,
    double height = 2000,
  }) => pumpKit(
    tester,
    WorkItemScreen(
      item: WorkItemView.of('local', subject),
      now: now,
      center: center,
      onBack: () {},
    ),
    width: 1000,
    height: height,
  );

  Future<void> openAgents(WidgetTester tester) async {
    await tester.tap(
      find.descendant(of: find.byType(Tabs), matching: find.text('Agents')),
    );
    await tester.pump();
  }

  testWidgets('drill-in shows the worker\u2019s transcript', (tester) async {
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item()),
        now: now,
        center: const Text('the worker\u2019s transcript'),
        onBack: () {},
      ),
      width: 1000,
      height: 1600,
    );
    expect(find.text('the worker\u2019s transcript'), findsOneWidget);
    // The activity is not drawn while a worker's conversation is.
    expect(find.text('Activity'), findsNothing);
  });

  testWidgets('back returns to the item\u2019s activity', (tester) async {
    var backed = 0;
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item()),
        now: now,
        center: const Text('the worker\u2019s transcript'),
        onBack: () => backed++,
      ),
      width: 1000,
      height: 1600,
    );
    await tester.tap(find.text('Back to activity'));
    await tester.pump();
    expect(backed, 1);
  });

  testWidgets('the Agents tab opens a worker\u2019s conversation', (
    tester,
  ) async {
    WorkItemWorker? opened;
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item()),
        now: now,
        onOpenWorker: (worker) => opened = worker,
      ),
      width: 1000,
      height: 2000,
    );
    await tester.tap(
      find.descendant(of: find.byType(Tabs), matching: find.text('Agents')),
    );
    await tester.pump();
    await tester.tap(find.text('agent-rev'));
    await tester.pump();
    expect(opened, isNotNull);
    expect(opened!.agentID, 'agent-rev');
    expect(opened!.sessionID, 'sess-rev');
  });

  testWidgets('the Agents tab names each worker\u2019s confirmed state', (
    tester,
  ) async {
    await show(tester, item());
    await openAgents(tester);

    // One word per worker, from the effect the item holds for it: only a
    // dispatched effect is working.
    expect(find.text('Working'), findsOneWidget);
    expect(find.text('Finished'), findsOneWidget);
    expect(find.text('Parked'), findsOneWidget);
    expect(find.text('Unknown'), findsOneWidget);

    final rows = tester
        .widget<WorkItemScreen>(find.byType(WorkItemScreen))
        .item
        .workers;
    expect(rows.map((row) => (row.words, row.stateWords, row.agentID)), [
      ('The implementer', 'Working', 'agent-imp'),
      ('The reviewer', 'Finished', 'agent-rev'),
      ('The implementer', 'Parked', 'agent-park'),
      ('The reviewer', 'Unknown', 'agent-unknown'),
    ], reason: 'a parked or unreadable worker is never called Working');
    // The identity stays inspectable: the row names the agent it opens.
    expect(find.text('agent-park'), findsOneWidget);
  });

  testWidgets('a time says how long ago, with its exact time behind it', (
    tester,
  ) async {
    await show(tester, item());
    expect(find.text('2h 30m'), findsOneWidget);
    expect(
      hasTip(tester, find.text('2h 30m'), '2026-11-17 09:30 UTC'),
      isTrue,
      reason: 'the exact time belongs in the tip behind the relative one',
    );
  });

  testWidgets('a time this build cannot read is unknown, not 1970', (
    tester,
  ) async {
    await show(
      tester,
      item(
        history: [
          factoryEvent(1, 'created', 'the draft was created', at: 'not-a-time'),
        ],
      ),
    );
    final row = tester.widget<ActivityRow>(find.byType(ActivityRow));
    expect(row.age, 'Unknown');
    expect(row.ageTip, isNull, reason: 'there is no exact time to show');
    expect(find.text('Unknown'), findsOneWidget);
    expect(
      RegExp(r'^\d+d$').hasMatch(row.age!),
      isFalse,
      reason: 'an unreadable time is never drawn as an epoch age',
    );
  });

  testWidgets('incoming activity keeps the reading position', (tester) async {
    List<FactoryEvent> history(int count) => [
      for (var i = 1; i <= count; i++)
        factoryEvent(i, 'implement_started', 'step $i'),
    ];

    await show(tester, item(history: history(30)), height: 600);
    await tester.drag(find.byType(PageBody), const Offset(0, -300));
    await tester.pump();
    final read = tester
        .state<ScrollableState>(find.byType(Scrollable))
        .position
        .pixels;
    expect(read, greaterThan(0), reason: 'the test scrolled the page');

    // The store polls and one more entry arrives, appended: the reader stays
    // where they were.
    await show(tester, item(history: history(31)), height: 600);
    await tester.pump();
    expect(find.text('step 31'), findsOneWidget);
    expect(
      tester.state<ScrollableState>(find.byType(Scrollable)).position.pixels,
      read,
      reason: 'a new entry does not move a person who is reading',
    );
  });

  testWidgets('propose sends the revision', (tester) async {
    String? sent;
    final model = RevisionModel(
      approved: '## Task 1\nRead the item.\n',
      spec: 'Ship the work item thread.',
      baseRevision: 1,
      baseHash: 'hash-1',
      submit: (plan) async => sent = plan,
    );
    addTearDown(model.dispose);
    await pumpKit(
      tester,
      RevisionSheet(model: model),
      width: 900,
      height: 1600,
    );

    expect(find.textContaining('Read the item'), findsWidgets);
    await tester.enterText(
      find.byType(EditableText),
      '## Task 1\nRead the item first.\n',
    );
    await tester.pump();
    await tester.tap(find.text('Send the proposal'));
    await tester.pump();
    expect(sent, '## Task 1\nRead the item first.\n');
    expect(model.baseRevision, 1);
    expect(model.baseHash, 'hash-1');
  });

  test('a proposal names its base and carries the plan', () async {
    final subject = item();
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        factoryItems: [subject],
      ),
    ]);
    final store = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(store.dispose);
    await store.refresh('local');
    fixture.bridges['local']!.reply(
      'POST',
      '/api/factory/items/${subject.id}/commands',
      FakeReply(
        200,
        headers: const {'content-type': 'application/json'},
        body: jsonEncode({'item': subject.toJson()}),
      ),
    );

    await store.proposeRevision(
      'local',
      subject.id,
      plan: '## Task 1\nRead the item first.\n',
      spec: 'Ship the work item thread.',
      baseRevision: 1,
      baseHash: 'hash-1',
    );

    final post = fixture
        .requests('local')
        .lastWhere((request) => request.method == 'POST');
    final body = jsonDecode(post.body) as Map<String, dynamic>;
    expect(body['kind'], 'revision_propose');
    final revision = body['revision'] as Map<String, dynamic>;
    expect(revision['baseRevision'], 1);
    expect(revision['baseHash'], 'hash-1');
    expect(revision['plan'], '## Task 1\nRead the item first.\n');
    expect(body['expectedVersion'], subject.version);
  });
}
