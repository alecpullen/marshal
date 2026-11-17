import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/drafts_model.dart';
import 'package:marshal_app/src/model/handoff_model.dart';
import 'package:marshal_app/src/model/planning_model.dart';

/// The factory item a route answers with, built from the draft it was handed.
FactoryItem _item(String id, FactoryCreate draft) => FactoryItem.fromJson({
  'id': id,
  'title': draft.title,
  'state': 'draft',
  'stage': 'plan',
  'repoId': draft.config.repoID,
  'projectRoot': draft.config.projectRoot,
  'hostId': draft.config.hostID,
  'branch': 'factory/$id',
  'baseSha': 'base-1',
  'version': 1,
  'draft': {
    'requestId': draft.requestID,
    'title': draft.title,
    'plan': draft.plan,
    'spec': draft.spec,
    'config': draft.config.toJson(),
  },
  'approvals': <Object?>[],
  'grants': <Object?>[],
  'rounds': <Object?>[],
  'workers': <Object?>[],
  'prs': <Object?>[],
  'effects': <Object?>[],
  'history': <Object?>[],
  'createdAt': '2026-11-17T04:00:00Z',
  'approvedAt': '',
  'updatedAt': '2026-11-17T04:00:00Z',
});

/// The capture a planning thread froze, as its reply carries it.
const _snapshot = PlannerSnapshot(
  id: 'snap-1',
  sha256: 'a1b2c3d4e5f6',
  baseCommit: 'c0ffee1',
  plan: '## Task 1\nFrozen plan.\n',
  spec: 'Frozen specification.',
  files: [
    PlannerSnapshotFile(
      path: 'lib/a.dart',
      operation: 'modify',
      baseSHA256: 'b1',
      contentSHA256: 'c1',
      executable: false,
      diff: '--- a/lib/a.dart\n+++ b/lib/a.dart\n',
    ),
    PlannerSnapshotFile(
      path: 'lib/new.dart',
      operation: 'create',
      contentSHA256: 'c2',
      executable: false,
      diff: '--- /dev/null\n+++ b/lib/new.dart\n',
    ),
  ],
  rejected: [
    PlannerRejection(path: '../etc/passwd', reason: 'outside the project'),
  ],
);

/// A handoff for a project whose defaults name no repository: what a project
/// states before anything is chosen.
HandoffModel _handoff({
  List<FactoryCreate>? created,
  ProjectDefaults defaults = const ProjectDefaults(maxRounds: 3),
  String plan = '## Task 1\nRead the item.\n## Task 2\nApprove it.\n',
  String spec = 'Ship the work item thread.',
  Future<FactoryItem> Function(FactoryCreate draft)? create,
  PlannerSnapshot? snapshot,
  String? plannerID,
  Future<FactoryItem> Function(
    String plannerID,
    String snapshotID,
    FactoryCreate request,
  )?
  handoffSnapshot,
}) {
  final drafts = created ?? <FactoryCreate>[];
  return HandoffModel(
    defaults: defaults,
    projectRoot: '/home/u/marshal',
    title: 'Work item thread',
    plan: plan,
    spec: spec,
    reviewedSnapshot: snapshot,
    plannerID: plannerID,
    handoffSnapshot: handoffSnapshot,
    create: (draft) async {
      drafts.add(draft);
      return create == null ? _item('item-9', draft) : create(draft);
    },
  );
}

void main() {
  test('fields default from the project', () {
    final model = _handoff(
      defaults: const ProjectDefaults(
        repoID: 'repo-1',
        targetBranch: 'main',
        implementerModel: 'local-fast',
        reviewerModel: 'local-strong',
        expectedChecks: ['build', 'unit'],
        maxRounds: 4,
        plannerAuthority: HandoffAuthority.ordering,
      ),
    );
    addTearDown(model.dispose);

    expect(model.sourceOf(HandoffField.repository), FieldSource.inherited);
    expect(model.words(HandoffField.repository), 'repo-1');
    expect(model.words(HandoffField.implementer), 'local-fast');
    expect(model.words(HandoffField.reviewer), 'local-strong');
    expect(model.words(HandoffField.checks), 'build, unit');
    expect(model.words(HandoffField.rounds), '4');
    expect(model.words(HandoffField.environment), 'On this host');
    expect(model.words(HandoffField.authority), HandoffAuthority.ordering);
    // Nothing was overridden, so the draft is the project's own settings.
    final draft = model.draft;
    expect(draft.config.repoID, 'repo-1');
    expect(draft.config.targetBranch, 'main');
    expect(draft.config.maxRounds, 4);
    expect(draft.config.expectedChecks.map((check) => check.name), [
      'build',
      'unit',
    ]);
  });

  test('an override is marked', () {
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1', maxRounds: 3),
    );
    addTearDown(model.dispose);

    model.set(HandoffField.rounds, 7);

    expect(model.sourceOf(HandoffField.rounds), FieldSource.overridden);
    expect(model.words(HandoffField.rounds), '7');
    // Everything the person did not touch stays inherited.
    expect(model.sourceOf(HandoffField.repository), FieldSource.inherited);
    expect(model.draft.config.maxRounds, 7);
  });

  test('reset returns to inherited', () {
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1', maxRounds: 3),
    );
    addTearDown(model.dispose);

    model.set(HandoffField.rounds, 7);
    model.reset(HandoffField.rounds);

    expect(model.sourceOf(HandoffField.rounds), FieldSource.inherited);
    expect(model.words(HandoffField.rounds), '3');
    expect(model.draft.config.maxRounds, 3);
  });

  test('approve is blocked with a reason', () async {
    // A project that names no repository cannot approve: the bridge needs one.
    final created = <FactoryCreate>[];
    final model = _handoff(created: created);
    addTearDown(model.dispose);

    expect(model.blockers, isNotEmpty);
    expect(model.blockers.first, contains('repository'));
    expect(model.canApprove, isFalse);

    final item = await model.approve();

    expect(item, isNull);
    expect(created, isEmpty);
    expect(model.failure, contains('repository'));
  });

  test('approve creates the item and opens its thread', () async {
    final created = <FactoryCreate>[];
    final model = _handoff(
      created: created,
      defaults: const ProjectDefaults(repoID: 'repo-1', maxRounds: 3),
    );
    addTearDown(model.dispose);

    expect(model.canApprove, isTrue);
    final item = await model.approve();
    expect(model.failure, isNull, reason: 'the create route failed');

    expect(item, isNotNull);
    expect(item!.id, 'item-9');
    expect(model.created?.id, 'item-9');
    // The draft that was posted is exactly what the sheet showed.
    expect(created.single.config.repoID, 'repo-1');
    expect(created.single.config.projectRoot, '/home/u/marshal');
    expect(created.single.plan, model.plan);
    expect(created.single.spec, model.spec);
    expect(created.single.requestID, isNotEmpty);
  });

  test('the consequence says what approving authorises', () {
    final model = _handoff(
      defaults: const ProjectDefaults(
        repoID: 'repo-1',
        maxRounds: 2,
        plannerAuthority: HandoffAuthority.nothing,
      ),
    );
    addTearDown(model.dispose);

    expect(model.consequence, contains('repo-1'));
    expect(model.consequence, contains('2 rounds'));
    expect(model.consequence, contains(HandoffAuthority.nothing));
  });

  test('a project\'s settings are what its handoff inherits', () {
    // The live wiring reads the project's settings when a plan is handed off
    // (F6 decision 8), so what the sheet shows as inherited is the document
    // the project page edits rather than the host alone. Without the mapping
    // the repository is unstated and approval is blocked.
    final defaults = ProjectDefaults.fromSettings(
      ProjectSettings.fromJson({
        'workspace': 'alpha',
        'intake': {'repoId': 'repo-1'},
        'factoryDefaults': {
          'targetBranch': 'main',
          'maxRounds': 5,
          'expectedChecks': [
            {'appId': 42, 'kind': 'check', 'name': 'build'},
            {'appId': 42, 'kind': 'check', 'name': 'unit'},
          ],
        },
      }),
      hostID: 'local',
    );

    expect(defaults.repoID, 'repo-1');
    expect(defaults.hostID, 'local', reason: 'the project names no host');
    expect(defaults.targetBranch, 'main');
    expect(defaults.expectedChecks, ['build', 'unit']);
    expect(defaults.maxRounds, 5);
    expect(defaults.native, isTrue, reason: 'no workspace is named');

    final model = _handoff(defaults: defaults);
    addTearDown(model.dispose);
    expect(model.sourceOf(HandoffField.repository), FieldSource.inherited);
    expect(model.words(HandoffField.repository), 'repo-1');
    expect(model.words(HandoffField.checks), 'build, unit');
    expect(model.words(HandoffField.rounds), '5');
    expect(
      model.canApprove,
      isTrue,
      reason: 'the repository is inherited, not left for the person to guess',
    );
    expect(model.draft.config.repoID, 'repo-1');
    expect(model.draft.config.targetBranch, 'main');
    expect(model.draft.config.maxRounds, 5);
  });

  test('a project that names a workspace runs its work there', () {
    final defaults = ProjectDefaults.fromSettings(
      ProjectSettings.fromJson({
        'intake': {'repoId': 'repo-1'},
        'factoryDefaults': {'workspace': 'ws-1', 'hostId': 'builder'},
      }),
      hostID: 'local',
    );

    expect(defaults.workspace, 'ws-1');
    expect(defaults.native, isFalse);
    expect(defaults.hostID, 'builder', reason: 'the project names its host');
  });

  test('a project that states no defaults leaves the sheet saying so', () {
    final defaults = ProjectDefaults.fromSettings(
      ProjectSettings.fromJson({'intake': <String, dynamic>{}}),
      hostID: 'local',
    );

    final model = _handoff(defaults: defaults);
    addTearDown(model.dispose);
    expect(model.hostID, 'local');
    expect(model.words(HandoffField.repository), 'Not stated');
    expect(
      model.canApprove,
      isFalse,
      reason: 'the sheet blocks on what the project does not state',
    );
    expect(model.blockers, contains('Choose the repository the work is on.'));
  });

  test('a bound capture is what the snapshot route posts', () async {
    final created = <FactoryCreate>[];
    final handed = <(String, String, FactoryCreate)>[];
    final model = _handoff(
      created: created,
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      // The sheet's own copy differs from the capture's, so the assertion
      // proves which one is sent.
      plan: '## Task 1\nThe editable copy.\n',
      spec: 'The editable specification.',
      snapshot: _snapshot,
      plannerID: 'planner-7',
      handoffSnapshot: (plannerID, snapshotID, request) async {
        handed.add((plannerID, snapshotID, request));
        return _item('item-frozen', request);
      },
    );
    addTearDown(model.dispose);

    expect(model.boundToSnapshot, isTrue);
    final item = await model.approve();

    expect(created, isEmpty, reason: 'the create route is not the bound one');
    expect(handed, hasLength(1));
    expect(handed.single.$1, 'planner-7');
    expect(handed.single.$2, 'snap-1');
    final request = handed.single.$3;
    // The capture's frozen bytes, not the sheet's editable copies.
    expect(request.plan, '## Task 1\nFrozen plan.\n');
    expect(request.spec, 'Frozen specification.');
    // The evidence the draft is bound to.
    final edit = request.editSnapshot;
    expect(edit, isNotNull);
    expect(edit!.baseCommit, 'c0ffee1');
    expect(edit.sha256, 'a1b2c3d4e5f6');
    expect(edit.files.map((file) => file.path), ['lib/a.dart', 'lib/new.dart']);
    expect(edit.files.map((file) => file.operation), ['modify', 'create']);
    expect(edit.files.first.baseSHA256, 'b1');
    expect(edit.files.first.contentSHA256, 'c1');
    expect(edit.files.first.executable, isFalse);
    expect(edit.files[1].contentSHA256, 'c2');
    // The item the bridge answered is the one the review reports.
    expect(item?.id, 'item-frozen');
    expect(model.created?.id, 'item-frozen');
    expect(model.failure, isNull);
  });

  test('a review with no capture keeps the create route', () async {
    final created = <FactoryCreate>[];
    final handed = <FactoryCreate>[];
    final model = _handoff(
      created: created,
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      handoffSnapshot: (plannerID, snapshotID, request) async {
        handed.add(request);
        return _item('item-frozen', request);
      },
    );
    addTearDown(model.dispose);

    expect(model.boundToSnapshot, isFalse);
    final item = await model.approve();

    expect(handed, isEmpty);
    expect(created, hasLength(1));
    expect(created.single.editSnapshot, isNull);
    expect(created.single.plan, model.plan);
    expect(created.single.spec, model.spec);
    expect(item?.id, 'item-9');
    expect(model.created?.id, 'item-9');
  });

  test('two approvals in one review name the same request', () async {
    final posts = <FactoryCreate>[];
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      create: (draft) async {
        posts.add(draft);
        return _item('item-9', draft);
      },
    );
    addTearDown(model.dispose);
    await model.approve();
    await model.approve();

    expect(posts, hasLength(2));
    expect(posts[0].requestID, posts[1].requestID);
    expect(posts[0].requestID, model.requestID());
    expect(model.created?.id, 'item-9');
  });

  test('a new review names a different request', () async {
    final first = <FactoryCreate>[];
    final one = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      create: (draft) async {
        first.add(draft);
        return _item('item-9', draft);
      },
    );
    addTearDown(one.dispose);
    await one.approve();

    final second = <FactoryCreate>[];
    final two = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      create: (draft) async {
        second.add(draft);
        return _item('item-9', draft);
      },
    );
    addTearDown(two.dispose);
    await two.approve();

    expect(first.single.requestID, isNot(second.single.requestID));
  });

  test(
    'a failed create leaves the review failed and posts nothing more',
    () async {
      final posts = <FactoryCreate>[];
      final model = _handoff(
        defaults: const ProjectDefaults(repoID: 'repo-1'),
        create: (draft) async {
          posts.add(draft);
          throw StateError('the bridge refused the create');
        },
      );
      addTearDown(model.dispose);

      final item = await model.approve();

      expect(item, isNull);
      expect(model.created, isNull);
      expect(model.failure, contains('the bridge refused the create'));
      // Nothing retries by itself: one press is one post, and the person
      // decides whether to press again.
      expect(posts, hasLength(1));
    },
  );

  // Closing the review is the last chance to write what the autosave window
  // still holds. The sheet calls `flush()` as it closes, so this is the test
  // that an edit made inside the window is not lost to a timer.
  test('closing the review writes the edit the window still holds', () async {
    final creates = <WorkingDraftCreateRequest>[];
    final drafts = DraftsModel(
      projectRoot: '/home/u/marshal',
      // Long enough never to fire on its own: the flush is what writes.
      autosave: const Duration(seconds: 30),
      create: (request) async {
        creates.add(request);
        return WorkingDraft(
          id: 'd-1',
          projectRoot: '/home/u/marshal',
          version: 1,
          state: 'editing',
          content: request.content,
          updatedAt: '2026-11-17T04:00:00Z',
        );
      },
      save: (id, request) async => throw UnimplementedError(),
      read: (id) async => throw UnimplementedError(),
      list: (root) async => const [],
      remove: (id, request) async {},
    );
    addTearDown(drafts.dispose);
    final model = HandoffModel(
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      projectRoot: '/home/u/marshal',
      title: 'Work item thread',
      plan: '## Task 1\nRead the item.\n',
      spec: 'Ship the work item thread.',
      drafts: drafts,
      create: (draft) async => _item('item-9', draft),
    );
    addTearDown(model.dispose);

    model.set(HandoffField.rounds, 5);
    expect(creates, isEmpty, reason: 'the autosave window has not passed');

    await model.flush();

    expect(creates, hasLength(1));
    expect(creates.single.content.maxRoundsOverride, '5');
  });
}
