import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart'
    show ProjectSettingsVersion;
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart' show BridgeFailure;
import 'package:marshal_app/src/model/expected_checks.dart';
import 'package:marshal_app/src/model/handoff_model.dart' show FieldSource;
import 'package:marshal_app/src/model/project_model.dart';
import 'package:marshal_app/src/model/section_model.dart' show SaveState;

/// The settings the fixture bridge holds: an agent profile, a mode, an intake
/// and a factory default block, with no role override of its own. Expected
/// checks are read and written exactly, so the fixture states them as the
/// wire does.
ProjectSettings stored({
  int maxRounds = 3,
  String agentProfile = 'balanced',
  List<Map<String, Object>> expectedChecks = const [],
}) => ProjectSettings.fromJson({
  'routing': {'profile': agentProfile, 'model': 'kept'},
  'mode': 'act',
  'auto': true,
  'shipTarget': 'merge',
  'intake': {
    'repoId': 'repo-1',
    'labels': ['studio'],
  },
  'factoryDefaults': {
    'targetBranch': 'main',
    'maxRounds': maxRounds,
    'ciPollSeconds': 30,
    'ciWaitSeconds': 900,
    if (expectedChecks.isNotEmpty) 'expectedChecks': expectedChecks,
  },
});

/// A model over [settings], whose reads and writes a test scripts.
class Scripted {
  Scripted({
    ProjectSettings? settings,
    String etag = 'v1',
    Future<ProjectSettingsVersion> Function(ProjectSettings, String?)? write,
  }) : current = settings ?? stored(),
       currentTag = etag,
       // Dart has no private named parameter.
       // ignore: prefer_initializing_formals
       _write = write;

  ProjectSettings current;
  String currentTag;
  final Future<ProjectSettingsVersion> Function(ProjectSettings, String?)?
  _write;

  /// Every write the model sent, oldest first: the document and the tag.
  final writes = <({ProjectSettings settings, String? etag})>[];

  ProjectModel model() => ProjectModel(
    root: '/home/u/alpha',
    name: 'alpha',
    now: () => DateTime.utc(2026, 11, 17, 4, 12),
    read: () async =>
        ProjectSettingsVersion(settings: current, etag: currentTag),
    write: (settings, etag) async {
      writes.add((settings: settings, etag: etag));
      final scripted = _write;
      if (scripted != null) return scripted(settings, etag);
      current = settings;
      currentTag = 'v2';
      return ProjectSettingsVersion(settings: settings, etag: currentTag);
    },
  );
}

void main() {
  test('a read fills the draft, and a save names the tag it read', () async {
    final fixture = Scripted();
    final model = fixture.model();
    addTearDown(model.dispose);

    await model.load();

    expect(model.settings, isNotNull);
    expect(model.etag, 'v1');
    expect(model.draft!.agentProfile, 'balanced');
    expect(model.dirty, isFalse);

    model.setRounds('5');
    expect(model.dirty, isTrue);
    expect(model.hasUnsavedEdits, isTrue);

    await model.save();

    expect(fixture.writes, hasLength(1));
    expect(fixture.writes.single.etag, 'v1');
    expect(fixture.writes.single.settings.factoryDefaults!.maxRounds, 5);
    expect(model.etag, 'v2');
    expect(model.dirty, isFalse);
    expect(model.saveState, SaveState.clean);
  });

  test('a stale save keeps the edit', () async {
    final fixture = Scripted(
      write: (settings, etag) async =>
          throw BridgeFailure(412, 'the settings changed elsewhere'),
    );
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    model.setRounds('7');
    await model.save();

    expect(model.saveState, SaveState.stale);
    expect(model.stale, isTrue);
    expect(model.saveError, 'the settings changed elsewhere');
    // The edit is kept, so it can be saved against what is there now.
    expect(model.draft!.rounds, '7');
    expect(model.hasUnsavedEdits, isTrue);
  });

  test('a reload after a stale save keeps the edit', () async {
    final fixture = Scripted(
      write: (settings, etag) async =>
          throw BridgeFailure(412, 'the settings changed elsewhere'),
    );
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    model.setRounds('7');
    await model.save();
    expect(model.stale, isTrue);

    // Another surface saved five rounds in between.
    fixture.current = stored(maxRounds: 5);
    fixture.currentTag = 'v2';

    await model.reloadAfterStale();

    expect(model.settings!.factoryDefaults!.maxRounds, 5);
    expect(model.etag, 'v2');
    expect(model.draft!.rounds, '7');
    expect(model.saveState, SaveState.dirty);
    expect(model.stale, isFalse);
  });

  test('role overrides follow the agent default and reset', () async {
    final model = Scripted().model();
    addTearDown(model.dispose);
    await model.load();

    // With no profile of its own, the implementer follows the agent default.
    expect(
      model.sourceOf(ProjectField.implementerProfile),
      FieldSource.inherited,
    );
    expect(model.draft!.effectiveImplementer, 'balanced');

    // An override is marked as one and takes effect at once.
    model.setRoleProfile('implementer', 'deep');
    expect(
      model.sourceOf(ProjectField.implementerProfile),
      FieldSource.overridden,
    );
    expect(model.draft!.effectiveImplementer, 'deep');
    expect(model.draft!.effectiveReviewer, 'balanced');

    // Reset puts it back on the agent default, which follows a later change.
    model.resetRole('implementer');
    expect(
      model.sourceOf(ProjectField.implementerProfile),
      FieldSource.inherited,
    );
    model.setAgentProfile('fast');
    expect(model.draft!.effectiveImplementer, 'fast');
    expect(model.draft!.effectiveReviewer, 'fast');
  });

  test('an unstated field is absent, not a fallback', () async {
    final fixture = Scripted();
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    // Clearing the counts and returning Auto to inherited store no value.
    model.setRounds('');
    model.setCIPoll('');
    model.setAuto(AutoChoice.inherit);
    model.setIsolation(IsolationChoice.inherit);
    model.setTargetBranch('');
    await model.save();

    final written = fixture.writes.single.settings.toJson();
    expect(written.containsKey('auto'), isFalse);
    expect(written.containsKey('isolated'), isFalse);
    final factory = written['factoryDefaults'] as Map<String, dynamic>;
    expect(factory.containsKey('maxRounds'), isFalse);
    expect(factory.containsKey('ciPollSeconds'), isFalse);
    expect(factory.containsKey('targetBranch'), isFalse);
    // A member this build does not edit is kept.
    expect((written['routing'] as Map<String, dynamic>)['model'], 'kept');
  });

  test('a profile override keeps the route members it did not set', () async {
    final fixture = Scripted();
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    model.setRoleProfile('implementer', 'deep');
    await model.save();

    final factory =
        fixture.writes.single.settings.toJson()['factoryDefaults']
            as Map<String, dynamic>;
    expect(factory['implementerRouting'], {'profile': 'deep'});

    // The agent's own route keeps its unknown member.
    final routing =
        fixture.writes.single.settings.toJson()['routing']
            as Map<String, dynamic>;
    expect(routing, {'profile': 'balanced', 'model': 'kept'});
  });

  test('an unsaved edit is discarded back to the stored document', () async {
    final model = Scripted().model();
    addTearDown(model.dispose);
    await model.load();

    model.setTargetBranch('release');
    expect(model.dirty, isTrue);

    model.discardEdits();

    expect(model.draft!.targetBranch, 'main');
    expect(model.dirty, isFalse);
  });

  test('expected checks keep their exact keys through a save', () async {
    final keys = [
      {'kind': 'check_run', 'name': 'build', 'appId': 42},
      {'kind': 'check_run', 'name': 'build', 'appId': 7},
      {'kind': 'commit_status', 'name': 'ci:lint', 'appId': 0},
      {'kind': 'check_run', 'name': 'Windows:Build', 'appId': 1234},
    ];
    final fixture = Scripted(settings: stored(expectedChecks: keys));
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    // The same name on two apps is two checks, a colon can belong to a name
    // or a context, and case is the forge's.
    final draft = model.draft!;
    expect(draft.expectedChecksChoice, ExpectedChecksChoice.custom);
    expect(draft.expectedChecks, [
      const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
      const ExpectedCheck(kind: 'check_run', name: 'build', appID: 7),
      const ExpectedCheck(kind: 'commit_status', name: 'ci:lint'),
      const ExpectedCheck(
        kind: 'check_run',
        name: 'Windows:Build',
        appID: 1234,
      ),
    ]);
    // The text is the canonical raw lines, which parse back to the same keys.
    expect(
      draft.expectedChecksText,
      [
        'check_run:build:42',
        'check_run:build:7',
        'commit_status:ci:lint',
        'check_run:Windows:Build:1234',
      ].join('\n'),
    );
    expect(
      parseExpectedChecks(draft.expectedChecksText).checks,
      draft.expectedChecks,
    );
    expect(model.sourceOf(ProjectField.expectedChecks), FieldSource.inherited);

    await model.save();

    // The write holds exactly the keys it read, and nothing else moved.
    final written = fixture.writes.single.settings.toJson();
    final factory = written['factoryDefaults'] as Map<String, dynamic>;
    expect(factory['expectedChecks'], keys);
    expect(factory['targetBranch'], 'main');
    expect(factory['ciPollSeconds'], 30);
    expect(written['mode'], 'act');
    expect((written['routing'] as Map<String, dynamic>)['model'], 'kept');
    expect((written['intake'] as Map<String, dynamic>)['repoId'], 'repo-1');
  });

  test('clearing the expected checks follows the defaults again', () async {
    final fixture = Scripted(
      settings: stored(
        expectedChecks: [
          {'kind': 'commit_status', 'name': 'ci', 'appId': 0},
        ],
      ),
    );
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    model.clearExpectedChecks();

    expect(model.draft!.expectedChecksChoice, ExpectedChecksChoice.inherit);
    expect(model.draft!.expectedChecks, isEmpty);
    expect(model.draft!.expectedChecksText, '');
    expect(model.sourceOf(ProjectField.expectedChecks), FieldSource.overridden);

    await model.save();

    final factory =
        fixture.writes.single.settings.toJson()['factoryDefaults']
            as Map<String, dynamic>;
    // Inherit is the one state that clears a stored list.
    expect(factory.containsKey('expectedChecks'), isFalse);
    // The rest of the factory block survives the clearing.
    expect(factory['maxRounds'], 3);
    expect(factory['targetBranch'], 'main');
  });

  test('invalid raw check text is kept and the save is refused', () async {
    final fixture = Scripted();
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    model.setExpectedChecksText('check_run:build:42\nxyz');

    // The text is kept verbatim, with the checks that parsed beside it.
    expect(model.draft!.expectedChecksText, 'check_run:build:42\nxyz');
    expect(model.draft!.expectedChecks, [
      const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
    ]);
    expect(model.dirty, isTrue);

    await model.save();

    // Nothing was sent, and the refusal is the parser's own words.
    expect(fixture.writes, isEmpty);
    expect(model.saveState, SaveState.failed);
    expect(
      model.saveError,
      'Each check must be check_run:<name>:<app id> or '
      'commit_status:<context>; "xyz" is neither.',
    );
    // The edit survives the refusal, so it can be fixed and saved.
    expect(model.draft!.expectedChecksText, 'check_run:build:42\nxyz');
    expect(model.dirty, isTrue);

    model.setExpectedChecksText('check_run:build:42');
    await model.save();
    expect(fixture.writes, hasLength(1));
    expect(model.saveState, SaveState.clean);
    final factory =
        fixture.writes.single.settings.toJson()['factoryDefaults']
            as Map<String, dynamic>;
    expect(factory['expectedChecks'], [
      {'kind': 'check_run', 'name': 'build', 'appId': 42},
    ]);
  });

  test('expected checks reset to the stored document', () async {
    final fixture = Scripted(
      settings: stored(
        expectedChecks: [
          {'kind': 'check_run', 'name': 'build', 'appId': 42},
        ],
      ),
    );
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    model.clearExpectedChecks();
    expect(model.sourceOf(ProjectField.expectedChecks), FieldSource.overridden);

    model.reset(ProjectField.expectedChecks);

    // Reset restores the stored choice, list and text.
    expect(model.sourceOf(ProjectField.expectedChecks), FieldSource.inherited);
    expect(model.draft!.expectedChecksChoice, ExpectedChecksChoice.custom);
    expect(model.draft!.expectedChecks, [
      const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
    ]);
    expect(model.draft!.expectedChecksText, 'check_run:build:42');

    // Resetting again restores the same stored state, whatever was typed.
    model.setExpectedChecks([
      const ExpectedCheck(kind: 'commit_status', name: 'ci'),
    ]);
    expect(model.sourceOf(ProjectField.expectedChecks), FieldSource.overridden);
    model.reset(ProjectField.expectedChecks);
    expect(model.sourceOf(ProjectField.expectedChecks), FieldSource.inherited);
    expect(model.draft!.expectedChecksChoice, ExpectedChecksChoice.custom);
    expect(model.draft!.expectedChecks, [
      const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
    ]);
    expect(model.draft!.expectedChecksText, 'check_run:build:42');
  });

  test('expected checks reset to inherit when none are stated', () async {
    final fixture = Scripted();
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    model.setExpectedChecks([
      const ExpectedCheck(kind: 'commit_status', name: 'ci'),
    ]);
    expect(model.sourceOf(ProjectField.expectedChecks), FieldSource.overridden);

    model.reset(ProjectField.expectedChecks);

    expect(model.draft!.expectedChecksChoice, ExpectedChecksChoice.inherit);
    expect(model.draft!.expectedChecks, isEmpty);
    expect(model.draft!.expectedChecksText, '');
  });

  test('a stale save keeps the checks edit', () async {
    final fixture = Scripted(
      write: (settings, etag) async =>
          throw BridgeFailure(412, 'the settings changed elsewhere'),
    );
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    model.setExpectedChecksText('check_run:build:42');
    await model.save();

    // The refusal is a conflict, not a problem with the text: the edit is
    // kept to be saved against what is there now.
    expect(model.saveState, SaveState.stale);
    expect(model.stale, isTrue);
    expect(model.saveError, 'the settings changed elsewhere');
    expect(model.draft!.expectedChecksText, 'check_run:build:42');
    expect(model.draft!.expectedChecks, [
      const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
    ]);
  });

  test('an empty custom list is written, and only inherit clears it', () async {
    final fixture = Scripted();
    final model = fixture.model();
    addTearDown(model.dispose);
    await model.load();

    model.setExpectedChecks(const []);

    // A custom choice with no checks is a statement the person made.
    expect(model.draft!.expectedChecksChoice, ExpectedChecksChoice.custom);
    expect(model.sourceOf(ProjectField.expectedChecks), FieldSource.overridden);

    await model.save();

    final written =
        fixture.writes.single.settings.toJson()['factoryDefaults']
            as Map<String, dynamic>;
    expect(written['expectedChecks'], isEmpty);

    // Inherit is the one state that clears it entirely.
    model.clearExpectedChecks();
    await model.save();
    final cleared =
        fixture.writes.last.settings.toJson()['factoryDefaults']
            as Map<String, dynamic>;
    expect(cleared.containsKey('expectedChecks'), isFalse);
  });

  test('an inherit-only draft removes an empty factory block', () {
    final settings = ProjectSettings.fromJson({
      'intake': {'repoId': 'repo-1'},
      'factoryDefaults': {
        'expectedChecks': [
          {'kind': 'commit_status', 'name': 'ci', 'appId': 0},
        ],
      },
    });
    final draft = ProjectDraft.of(settings).copyWith(
      expectedChecksChoice: ExpectedChecksChoice.inherit,
      expectedChecks: const <ExpectedCheck>[],
      expectedChecksText: '',
    );

    // The checks switch runs before the empty-block branch, so inherit
    // alone still removes a factory block that held nothing else.
    expect(draft.toJson(settings).containsKey('factoryDefaults'), isFalse);
  });

  group('expected check text', () {
    test('colons in a context and a name survive the round trip', () {
      final checks = [
        const ExpectedCheck(kind: 'commit_status', name: 'ci:lint'),
        const ExpectedCheck(
          kind: 'check_run',
          name: 'Windows:Build',
          appID: 1234,
        ),
        const ExpectedCheck(kind: 'check_run', name: 'build:', appID: 7),
      ];
      final text = formatExpectedChecks(checks);
      expect(
        text,
        [
          'commit_status:ci:lint',
          'check_run:Windows:Build:1234',
          'check_run:build::7',
        ].join('\n'),
      );
      final parsed = parseExpectedChecks(text);
      expect(parsed.error, isNull);
      expect(parsed.checks, checks);
      expect(parsed.text, text);
    });

    test('blank lines are skipped, and empty text states no checks', () {
      final parsed = parseExpectedChecks(' \n\ncheck_run:build:42\n  \n');
      expect(parsed.error, isNull);
      expect(parsed.checks, [
        const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
      ]);
      final empty = parseExpectedChecks(' \n\t\n');
      expect(empty.checks, isEmpty);
      expect(empty.error, isNull);
      expect(empty.text, ' \n\t\n');
    });

    test('each way a line can be wrong is named', () {
      for (final line in [
        'xyz',
        'build:42',
        'Check_Run:build:42',
        'check_run:build',
        'check_run::42',
        'check_run:build:0',
        'check_run:build:-3',
        'check_run:build:x',
        'commit_status:',
        'commit_status',
      ]) {
        final parsed = parseExpectedChecks(line);
        expect(
          parsed.error,
          'Each check must be check_run:<name>:<app id> or '
          'commit_status:<context>; "$line" is neither.',
          reason: line,
        );
        expect(parsed.text, line, reason: line);
        expect(parsed.checks, isEmpty, reason: line);
      }
    });

    test('a bad line keeps the checks that parsed before it', () {
      final parsed = parseExpectedChecks('commit_status:ci:lint\nnope');
      expect(parsed.checks, [
        const ExpectedCheck(kind: 'commit_status', name: 'ci:lint'),
      ]);
      expect(
        parsed.error,
        'Each check must be check_run:<name>:<app id> or '
        'commit_status:<context>; "nope" is neither.',
      );
    });

    test('one check parses from one line, or refuses', () {
      expect(
        ExpectedCheck.parse('check_run:build:42'),
        const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
      );
      expect(() => ExpectedCheck.parse('xyz'), throwsFormatException);
      expect(() => ExpectedCheck.parse(''), throwsFormatException);
    });

    test('a wire key is read and written without loss', () {
      const check = ExpectedCheck(kind: 'check_run', name: 'Build', appID: 42);
      final key = check.toKey();
      expect(ExpectedCheck.of(key), check);
      expect(key.toJson(), {'kind': 'check_run', 'name': 'Build', 'appId': 42});
      expect(check.line, 'check_run:Build:42');
      expect(check.words, 'Build (App 42)');
      const status = ExpectedCheck(kind: 'commit_status', name: 'ci/lint');
      expect(status.line, 'commit_status:ci/lint');
      expect(status.words, 'ci/lint');
      // The same name on two apps is two checks, and case is the forge's.
      expect(
        check,
        isNot(const ExpectedCheck(kind: 'check_run', name: 'Build', appID: 43)),
      );
      expect(
        check,
        isNot(const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42)),
      );
    });
  });
}
