import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart'
    show ProjectSettingsVersion;
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart' show BridgeFailure;
import 'package:marshal_app/src/model/handoff_model.dart' show FieldSource;
import 'package:marshal_app/src/model/project_model.dart';
import 'package:marshal_app/src/model/section_model.dart' show SaveState;

/// The settings the fixture bridge holds: an agent profile, a mode, an intake
/// and a factory default block, with no role override of its own.
ProjectSettings stored({int maxRounds = 3, String agentProfile = 'balanced'}) =>
    ProjectSettings.fromJson({
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
}
