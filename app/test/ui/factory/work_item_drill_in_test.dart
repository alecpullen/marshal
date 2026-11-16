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

  FactoryItem item({String state = 'implementing'}) => factoryItem(
    state: state,
    workers: [
      factoryWorker(agentId: 'agent-imp', sessionId: 'sess-imp'),
      factoryWorker(
        agentId: 'agent-rev',
        sessionId: 'sess-rev',
        role: 'reviewer',
      ),
    ],
    history: [factoryEvent(1, 'created', 'the draft was created')],
  );

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
