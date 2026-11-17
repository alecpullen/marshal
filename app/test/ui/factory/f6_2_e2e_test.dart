import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart'
    show ProjectSettingsVersion;
import 'package:marshal_app/src/client/contract.g.dart' hide CheckState;
import 'package:marshal_app/src/model/factory_store.dart';
import 'package:marshal_app/src/model/handoff_model.dart';
import 'package:marshal_app/src/model/planning_model.dart';
import 'package:marshal_app/src/model/project_model.dart';
import 'package:marshal_app/src/model/run_model.dart';
import 'package:marshal_app/src/model/thread_ref.dart';
import 'package:marshal_app/src/model/work_item_model.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// F6 plan 2's end-to-end flows: a plan becomes a work item through one sheet,
/// and a plan run is followed to its first commit.
///
/// It is a model-level end-to-end rather than `integration_test`: this machine
/// has no Linux toolchain for a device app (recorded in
/// `docs/studio-flutter/f6-2-planning-runs.md`), and a widget test cannot host
/// these flows — a request posted from a tapped callback runs in the test's
/// fake-async zone, where the fixture bridge's real HTTP never completes. The
/// screens' drawing and their actions are covered by `planning_test.dart`,
/// `handoff_sheet_test.dart`, `project_page_test.dart` and
/// `f6_1_e2e_test.dart`'s neighbours; this file covers the flows themselves.
void main() {
  test('a plan becomes a work item through one sheet', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
    ]);
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);

    // The planning thread (F6 decision 8): the person describes the work, the
    // planner drafts it, and the plan is frozen for review.
    final plan = PlanningModel(
      plannerID: 'p-1',
      events: (after) async => [
        PlannerEvent(
          sequence: 1,
          kind: 'planner.started',
          data: const {'mode': 'bypassPermissions'},
        ),
        PlannerEvent(
          sequence: 2,
          kind: 'turn.text',
          data: const {
            'text': 'Two tasks: read the intake, then ship the project page.',
          },
        ),
      ],
      promptPlanner: (request) async =>
          PlannerTurnReply(requestID: request.requestID, state: 'dispatched'),
      cancelPlanner: () async {},
      answerPlanner: (requestID, response) async {},
      capture: (request) async => PlannerSnapshotReply(
        id: 'snap-1',
        sha256: '9f2c1a55',
        baseCommit: 'base-1',
        artifacts: [
          PlannerSnapshotArtifact(
            kind: 'plan',
            content: '## Task 1\nRead the intake.\n',
            sha256: 'a1',
          ),
          PlannerSnapshotArtifact(
            kind: 'spec',
            content: 'Ship the project page.\n',
            sha256: 'a2',
          ),
        ],
        files: const [],
        createdAt: '2026-11-17T04:00:00Z',
        rejected: const [],
      ),
    );
    addTearDown(plan.dispose);

    await plan.refresh();
    await plan.prompt('Plan the project page');
    // The journal's own lines come first; the person's prompt follows them.
    final said = plan.messages.firstWhere((message) => message.fromUser);
    expect(said.text, 'Plan the project page');
    expect(
      plan.messages.any((message) => message.text.contains('Two tasks')),
      isTrue,
      reason: 'the planner\u2019s own words are read back from its journal',
    );

    final frozen = await plan.snapshot(
      plan: '## Task 1\nRead the intake.\n',
      spec: 'Ship the project page.\n',
    );
    expect(frozen, isNotNull);
    expect(frozen!.plan, contains('Read the intake'));
    expect(frozen.spec, 'Ship the project page.\n');

    // One review sheet: what it inherits, one override and its reset, and what
    // approving authorises (F6 decision 8).
    final handoff = HandoffModel(
      defaults: const ProjectDefaults(repoID: 'repo-1', hostID: 'local'),
      projectRoot: '/home/u/alpha',
      title: 'Project page',
      plan: frozen.plan,
      spec: frozen.spec,
      create: (draft) => factory.create('local', draft),
    );
    addTearDown(handoff.dispose);

    expect(handoff.sourceOf(HandoffField.repository), FieldSource.inherited);
    expect(handoff.words(HandoffField.repository), 'repo-1');
    expect(handoff.canApprove, isTrue);
    expect(handoff.consequence, contains('3 rounds'));

    handoff.set(HandoffField.rounds, 5);
    expect(handoff.sourceOf(HandoffField.rounds), FieldSource.overridden);
    handoff.reset(HandoffField.rounds);
    expect(handoff.sourceOf(HandoffField.rounds), FieldSource.inherited);

    // Approving posts exactly the plan the snapshot froze, and the bridge
    // answers with the item it made.
    final draftItem = factoryItem(
      id: 'item-1',
      title: 'Project page',
      state: 'draft',
      plan: frozen.plan,
      project: '/home/u/alpha',
    );
    fixture.bridges['local']!.reply(
      'POST',
      '/api/factory/items',
      FakeReply(
        201,
        headers: const {'content-type': 'application/json'},
        body: jsonEncode({'item': draftItem.toJson()}),
      ),
    );

    final created = await handoff.approve();
    expect(handoff.failure, isNull);
    expect(created, isNotNull);
    expect(created!.id, 'item-1');

    final posted = jsonDecode(
      fixture
          .requests('local')
          .lastWhere(
            (request) =>
                request.method == 'POST' &&
                request.uri.startsWith('/api/factory/items'),
          )
          .body,
    ) as Map<String, dynamic>;
    expect(posted['plan'], frozen.plan);
    expect(posted['spec'], frozen.spec);
    expect(posted['title'], 'Project page');
    expect((posted['config'] as Map<String, dynamic>)['repoId'], 'repo-1');

    // The item is the factory store's, in Plan, and it opens as a work item's
    // thread — the sheet was the only page between the plan and the thread.
    final item = factory.byId('local', 'item-1');
    expect(item, isNotNull);
    expect(WorkItemView.of('local', item!).stage, Stage.plan);
    expect(threadOfItem('local', item).kind, ThreadKind.workItem);
    expect(threadOfItem('local', item).status, ThreadStatus.needsYou);
  });

  test('a plan run is followed to its first commit', () async {
    SDDRun digest({
      required int done,
      required int current,
      required String phase,
      required List<Map<String, Object?>> tasks,
      Map<String, Object?>? gate,
    }) => SDDRun.fromJson({
      'active': true,
      'planName': 'F6 plan 2',
      'branch': 'sdd/run-1',
      'totalTasks': 2,
      'doneTasks': done,
      'currentTask': current,
      'phase': phase,
      'fixRound': 0,
      'maxFixRounds': 3,
      'tokensUsed': 0,
      'tokensMax': 200000,
      'finished': false,
      'succeeded': false,
      'startedAt': 1763351900000,
      'tasks': tasks,
      'gate': gate,
    });

    Map<String, Object?> task(
      int n,
      String title,
      String status, {
      List<int> dependsOn = const [],
      int? endedAt,
      Map<String, Object?>? commit,
      List<Map<String, Object?>> stages = const [],
    }) => {
      'n': n,
      'title': title,
      'dependsOn': dependsOn,
      'status': status,
      'startedAt': 1763351900000,
      'endedAt': ?endedAt,
      'commit': commit,
      'fixRounds': 0,
      'stages': stages,
    };

    final started = digest(
      done: 0,
      current: 1,
      phase: 'implement',
      tasks: [
        task(
          1,
          'Read the item',
          'active',
          stages: [
            {'name': 'implement', 'state': 'active'},
          ],
        ),
        task(2, 'Approve it', 'pending', dependsOn: [1]),
      ],
    );

    final answered = <String>[];
    final run = RunModel(
      agentID: 'a-run',
      run: started,
      answer: (words) async => answered.add(words),
    );
    addTearDown(run.dispose);

    expect(run.running, isTrue);
    expect(run.progress, 'implement · 0 of 2');
    expect(run.tasks.first.commitSHA, isEmpty);
    expect(run.tasks.first.cell(RunLane.implement).state, CellState.running);

    // The first task reaches its commit, and the run stops on a question.
    final committed = digest(
      done: 1,
      current: 2,
      phase: 'verify',
      gate: {'taskN': 2, 'question': 'Regenerate the fixtures in this commit?'},
      tasks: [
        task(
          1,
          'Read the item',
          'done',
          endedAt: 1763351990000,
          commit: {'base': 'base-1', 'head': 'abcdef1234567'},
          stages: [
            {'name': 'implement', 'state': 'done'},
            {
              'name': 'verify',
              'state': 'done',
              'detail': 'go test ./... passed',
            },
            {'name': 'review', 'state': 'done'},
            {'name': 'commit', 'state': 'done'},
          ],
        ),
        task(
          2,
          'Approve it',
          'active',
          dependsOn: [1],
          stages: [
            {'name': 'implement', 'state': 'active'},
          ],
        ),
      ],
    );
    run.apply(committed);

    final first = run.tasks.first;
    for (final lane in RunLane.all) {
      expect(
        first.cell(lane).state,
        CellState.passed,
        reason: 'the ${lane.toLowerCase()} lane passed for task 1',
      );
    }
    expect(first.commitSHA, 'abcdef1');
    expect(first.cells[RunLane.commit]!.commit, 'abcdef1');
    expect(run.progress, 'verify · 1 of 2');
    expect(run.hasGate, isTrue);
    expect(run.gateTask, 2);
    expect(run.gateQuestion, 'Regenerate the fixtures in this commit?');
    // The graph has a layer per dependency step, and the timeline has a bar
    // per stage the run reported.
    expect(run.graph.nodes, hasLength(2));
    expect(run.timeline, isNotEmpty);

    await run.answer('yes, in this commit');
    expect(answered, ['yes, in this commit']);

    // The project page lists the same run, with the digest it can open.
    final projects = ProjectModel(
      root: '/home/u/alpha',
      name: 'alpha',
      read: () async => ProjectSettingsVersion(
        settings: ProjectSettings.fromJson({
          'intake': {'repoId': 'repo-1'},
        }),
        etag: 'v1',
      ),
      write: (settings, etag) async =>
          ProjectSettingsVersion(settings: settings, etag: 'v2'),
      readRuns: () async => [
        RunListEntry.fromJson({
          'agentId': 'a-run',
          'name': 'Plan run',
          'project': '/home/u/alpha',
          'run': committed.toJson(),
          'at': 1763352000000,
        }),
      ],
    );
    addTearDown(projects.dispose);
    await projects.loadRuns();

    final listed = projects.runs.single;
    expect(listed.progress, '1 of 2');
    expect(listed.status, ThreadStatus.running);
    expect(listed.line, 'verify · sdd/run-1');
    expect(listed.digest, isNotNull);

    // A run opened from that row draws the same tasks.
    final opened = RunModel.fromDigest(listed.agentID, listed.digest!);
    addTearDown(opened.dispose);
    expect(opened.tasks.first.commitSHA, 'abcdef1');
  });
}
