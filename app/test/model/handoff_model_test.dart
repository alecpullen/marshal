import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/handoff_model.dart';

/// A handoff for a project whose defaults name no repository: what a project
/// states before anything is chosen.
HandoffModel _handoff({
  List<FactoryCreate>? created,
  ProjectDefaults defaults = const ProjectDefaults(maxRounds: 3),
  String plan = '## Task 1\nRead the item.\n## Task 2\nApprove it.\n',
  String spec = 'Ship the work item thread.',
}) {
  final drafts = created ?? <FactoryCreate>[];
  return HandoffModel(
    defaults: defaults,
    projectRoot: '/home/u/marshal',
    title: 'Work item thread',
    plan: plan,
    spec: spec,
    create: (draft) async {
      drafts.add(draft);
      return FactoryItem.fromJson({
        'id': 'item-9',
        'title': draft.title,
        'state': 'draft',
        'stage': 'plan',
        'repoId': draft.config.repoID,
        'projectRoot': draft.config.projectRoot,
        'hostId': draft.config.hostID,
        'branch': 'factory/item-9',
        'baseSha': 'base-1',
        'version': 1,
        'draft': {
          'requestId': draft.requestID,
          'title': draft.title,
          'plan': draft.plan,
          'spec': draft.spec,
          'config': draft.config.toJson(),
        },
        'approvals': [],
        'grants': [],
        'rounds': [],
        'workers': [],
        'prs': [],
        'effects': [],
        'history': [],
        'createdAt': '2026-11-17T04:00:00Z',
        'approvedAt': '',
        'updatedAt': '2026-11-17T04:00:00Z',
      });
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
}
