import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/expected_checks.dart';
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
        expectedChecks: [
          ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
          ExpectedCheck(kind: 'commit_status', name: 'unit'),
        ],
        maxRounds: 4,
        plannerAuthority: HandoffAuthority.ordering,
      ),
    );
    addTearDown(model.dispose);

    expect(model.sourceOf(HandoffField.repository), FieldSource.inherited);
    expect(model.words(HandoffField.repository), 'repo-1');
    expect(model.words(HandoffField.implementer), 'local-fast');
    expect(model.words(HandoffField.reviewer), 'local-strong');
    expect(model.words(HandoffField.checks), 'build (App 42), unit');
    expect(model.words(HandoffField.rounds), '4');
    expect(model.words(HandoffField.environment), 'On this host');
    expect(model.words(HandoffField.authority), HandoffAuthority.ordering);
    // Nothing was overridden, so the draft is the project's own settings.
    final draft = model.draft;
    expect(draft.config.repoID, 'repo-1');
    expect(draft.config.targetBranch, 'main');
    expect(draft.config.maxRounds, 4);
    expect(
      draft.config.expectedChecks.map(
        (check) => (check.kind, check.name, check.appID),
      ),
      [('check_run', 'build', 42), ('commit_status', 'unit', 0)],
    );
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
            {'appId': 42, 'kind': 'check_run', 'name': 'build'},
            {'appId': 0, 'kind': 'commit_status', 'name': 'unit'},
          ],
        },
      }),
      hostID: 'local',
    );

    expect(defaults.repoID, 'repo-1');
    expect(defaults.hostID, 'local', reason: 'the project names no host');
    expect(defaults.targetBranch, 'main');
    expect(defaults.expectedChecks, [
      const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
      const ExpectedCheck(kind: 'commit_status', name: 'unit'),
    ]);
    expect(defaults.maxRounds, 5);
    expect(defaults.native, isTrue, reason: 'no workspace is named');

    final model = _handoff(defaults: defaults);
    addTearDown(model.dispose);
    expect(model.sourceOf(HandoffField.repository), FieldSource.inherited);
    expect(model.words(HandoffField.repository), 'repo-1');
    expect(model.words(HandoffField.checks), 'build (App 42), unit');
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

  test('the checks keep their exact keys through the handoff', () {
    // A check is its kind, its name and its app: the same name on two apps
    // is two checks, a colon can belong to a name or a context, and case is
    // the forge's. None of that survives a list reduced to names.
    final model = _handoff(
      defaults: const ProjectDefaults(
        repoID: 'repo-1',
        expectedChecks: [
          ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
          ExpectedCheck(kind: 'check_run', name: 'build', appID: 7),
          ExpectedCheck(kind: 'commit_status', name: 'ci:lint'),
          ExpectedCheck(kind: 'check_run', name: 'Windows:Build', appID: 1234),
        ],
        maxRounds: 3,
      ),
    );
    addTearDown(model.dispose);

    expect(
      model.words(HandoffField.checks),
      'build (App 42), build (App 7), ci:lint, Windows:Build (App 1234)',
    );
    final draft = model.draft;
    expect(
      draft.config.expectedChecks.map(
        (check) => (check.kind, check.name, check.appID),
      ),
      [
        ('check_run', 'build', 42),
        ('check_run', 'build', 7),
        ('commit_status', 'ci:lint', 0),
        ('check_run', 'Windows:Build', 1234),
      ],
    );
    // The working draft holds the same keys, as the canonical raw lines.
    final content = model.draftContent;
    expect(content.checksMode, 'custom');
    expect(content.noRequiredCIAcknowledged, isFalse);
    expect(
      content.expectedChecksText,
      [
        'check_run:build:42',
        'check_run:build:7',
        'commit_status:ci:lint',
        'check_run:Windows:Build:1234',
      ].join('\n'),
    );
  });

  test('no required checks is acknowledged, not derived', () {
    // A project that states no checks is followed until the person says the
    // work needs none: the draft inherits rather than silently requiring
    // nothing.
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1', maxRounds: 3),
    );
    addTearDown(model.dispose);

    expect(model.noRequiredCIAcknowledged, isFalse);
    expect(model.words(HandoffField.checks), 'None required');
    expect(model.draftContent.checksMode, 'inherit');
    expect(model.draft.config.noRequiredCIAcknowledged, isFalse);

    // The acknowledgement reaches both drafts, and it is never read back out
    // of the list.
    model.setNoRequiredCIAcknowledged(true);
    expect(model.noRequiredCIAcknowledged, isTrue);
    expect(model.draftContent.checksMode, 'none');
    expect(model.draftContent.noRequiredCIAcknowledged, isTrue);
    expect(model.draft.config.noRequiredCIAcknowledged, isTrue);

    // Taking it back follows the project again.
    model.setNoRequiredCIAcknowledged(false);
    expect(model.draftContent.checksMode, 'inherit');

    // Stating a check takes the acknowledgement back, and acknowledging
    // clears the stated checks: the two are never both live, so the working
    // draft and the item's config never encode different work.
    model.set(HandoffField.checks, const [
      ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
    ]);
    expect(model.draftContent.checksMode, 'custom');
    expect(model.draftContent.expectedChecksText, 'check_run:build:42');
    expect(model.draft.config.noRequiredCIAcknowledged, isFalse);

    model.setNoRequiredCIAcknowledged(true);
    expect(model.draftContent.checksMode, 'none');
    expect(model.draft.config.noRequiredCIAcknowledged, isTrue);
    expect(
      model.draft.config.expectedChecks,
      isEmpty,
      reason: 'the acknowledgement means no check is required',
    );
    expect(model.draftContent.expectedChecksText, '');
    expect(model.words(HandoffField.checks), 'None required');
  });

  test('checks text that does not parse is kept as typed', () {
    // A typo the parser refuses is neither shown away nor rewritten: the
    // text stays exactly as typed, the parser says what is wrong in its own
    // words, and nothing is approvable until it is fixed.
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1', maxRounds: 3),
    );
    addTearDown(model.dispose);
    const typed = 'check_run:build:42\nbuild without a kind';

    model.setChecksText(typed);

    expect(model.checksText, typed);
    expect(model.checksError, contains('"build without a kind" is neither'));
    // What did parse is kept beside the text, never dropped from it.
    expect(model.draft.config.expectedChecks.map((check) => check.name), [
      'build',
    ]);
    expect(model.canApprove, isFalse);
    expect(
      model.blockers.where((blocker) => blocker.contains('is neither')),
      isNotEmpty,
    );
    // The draft states the text as canonical lines rather than the typo,
    // which cannot be posted as a set of checks.
    expect(model.draftContent.expectedChecksText, 'check_run:build:42');

    // Fixing the text clears the refusal and the draft follows it again.
    model.setChecksText('check_run:build:42');
    expect(model.checksError, isNull);
    expect(model.canApprove, isTrue);
    expect(
      model.draft.config.expectedChecks.map(
        (check) => (check.kind, check.name, check.appID),
      ),
      [('check_run', 'build', 42)],
    );

    // Putting the field back to the project's value forgets the typed text.
    model.reset(HandoffField.checks);
    expect(model.checksText, '');
    expect(model.draftContent.checksMode, 'inherit');
  });

  test('a check the bridge sent is never dropped for having no name', () {
    // The old mapping dropped empty names as if they were noise; a check the
    // project states is the bridge's to refuse, not this mapping's to lose.
    final defaults = ProjectDefaults.fromSettings(
      ProjectSettings.fromJson({
        'intake': {'repoId': 'repo-1'},
        'factoryDefaults': {
          'expectedChecks': [
            {'appId': 0, 'kind': 'commit_status', 'name': ''},
            {'appId': 42, 'kind': 'check_run', 'name': 'build'},
          ],
        },
      }),
      hostID: 'local',
    );

    expect(defaults.expectedChecks, [
      const ExpectedCheck(kind: 'commit_status', name: ''),
      const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
    ]);
  });
}
