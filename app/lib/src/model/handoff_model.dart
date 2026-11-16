import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';

// The create route and the defaults are passed in, so their fields stay
// private: Dart has no private named parameter.
// ignore_for_file: prefer_initializing_formals

/// Where a handoff field's value came from (F6 decision 8): the project's
/// defaults, or a choice the person made in the sheet.
enum FieldSource {
  inherited('Inherited'),
  overridden('Overridden');

  const FieldSource(this.word);

  /// The word the sheet marks the field with.
  final String word;
}

/// The fields a handoff states, in the order the sheet draws them.
///
/// They are named rather than positional so a test and the sheet cannot
/// disagree about which field a value belongs to.
abstract final class HandoffField {
  /// What the work is: its title, its plan and its specification.
  static const scope = 'Scope';

  /// What repository the work lands in.
  static const repository = 'Repository';

  /// Where it runs: on this host, or in a workspace.
  static const environment = 'Environment';

  /// The model the implementer runs as.
  static const implementer = 'Implementer model';

  /// The model the reviewer runs as.
  static const reviewer = 'Reviewer model';

  /// What must pass before the work is ready.
  static const checks = 'Expected checks';

  /// How many repair rounds it may take.
  static const rounds = 'Round allowance';

  /// What the planner may decide alone.
  static const authority = 'Planner authority';

  /// Every field, in the order the sheet draws them.
  static const all = [
    scope,
    repository,
    environment,
    implementer,
    reviewer,
    checks,
    rounds,
    authority,
  ];
}

/// What a project's factory defaults say (web/bridge/factory_types.go's
/// `FactoryDefaults`): every field a handoff inherits unless the person
/// overrides it.
///
/// A value the project does not state is empty here, and the model then
/// falls back to what the bridge itself would do rather than inventing a
/// project default.
@immutable
class ProjectDefaults {
  const ProjectDefaults({
    this.repoID = '',
    this.hostID = '',
    this.targetBranch = '',
    this.workspace = '',
    this.native = true,
    this.implementerModel = '',
    this.reviewerModel = '',
    this.expectedChecks = const [],
    this.maxRounds = 3,
    this.plannerAuthority = HandoffAuthority.nothing,
  });

  /// The repository the project's work lands in. Empty means the project
  /// names none, and a handoff that leaves it empty is blocked.
  final String repoID;

  final String hostID;

  /// The branch a pull request lands on.
  final String targetBranch;

  /// The workspace the work runs in, when it does not run natively.
  final String workspace;

  /// Whether the work runs on this host.
  final bool native;

  final String implementerModel;
  final String reviewerModel;

  /// Every check that must pass, by the name the forge gives it.
  final List<String> expectedChecks;

  /// How many repair rounds the work may take.
  final int maxRounds;

  /// What the planner may decide alone.
  final String plannerAuthority;
}

/// What a planner may decide without asking (F6 decision 8).
abstract final class HandoffAuthority {
  /// Nothing: every decision that changes the work stops for a person.
  static const nothing = 'Nothing without asking';

  /// The order the plan's tasks are worked in, and how they are split.
  static const ordering = 'The order of the plan';

  /// Anything the plan itself already states.
  static const plan = 'Anything the plan states';

  /// Every value the sheet's menu offers.
  static const options = [nothing, ordering, plan];
}

/// One handoff review (F6 decision 8): the plan a planning thread produced,
/// the settings it will run with, and what approving authorises.
///
/// Every field is either inherited from the project's factory defaults or
/// overridden here; [sourceOf] says which, and [reset] puts a field back.
/// [blockers] is what stops approval, in plain words, so the sheet can disable
/// its one primary action and say why rather than letting the bridge refuse it.
class HandoffModel extends ChangeNotifier {
  HandoffModel({
    required this.defaults,
    required this.projectRoot,
    required this.title,
    required this.plan,
    required this.spec,
    required Future<FactoryItem> Function(FactoryCreate draft) create,
    List<String> repositories = const [],
    String? hostID,
  }) : _create = create,
       repositories = List.unmodifiable(repositories),
       hostID = hostID ?? defaults.hostID,
       _overrides = {};

  /// The project's factory defaults: what every unset field inherits.
  final ProjectDefaults defaults;

  final String projectRoot;

  /// What the item is called.
  final String title;

  /// The plan text the person approved, verbatim.
  final String plan;

  /// The specification text, verbatim.
  final String spec;

  /// The host whose factory the item is created on.
  final String hostID;

  final Future<FactoryItem> Function(FactoryCreate draft) _create;

  /// The repositories the bridge has registered, as ids: the choices the
  /// sheet's repository menu offers. A project states no repository of its
  /// own yet, so the person picks one and the choice is the override.
  final List<String> repositories;
  final Map<String, Object?> _overrides;

  String? _failure;
  FactoryItem? _created;
  int _requests = 0;

  /// Why the last approval failed, in the bridge's words.
  String? get failure => _failure;

  /// The item the last [approve] created, if it created one.
  FactoryItem? get created => _created;

  /// Where [field]'s value came from.
  FieldSource sourceOf(String field) => _overrides.containsKey(field)
      ? FieldSource.overridden
      : FieldSource.inherited;

  /// Whether the person has changed [field].
  bool isOverridden(String field) => _overrides.containsKey(field);

  /// The value [field] runs with: the person's, or the project's.
  Object? valueOf(String field) {
    if (_overrides.containsKey(field)) return _overrides[field];
    return switch (field) {
      HandoffField.scope => title,
      HandoffField.repository => defaults.repoID,
      HandoffField.environment => _environment(defaults),
      HandoffField.implementer => defaults.implementerModel,
      HandoffField.reviewer => defaults.reviewerModel,
      HandoffField.checks => defaults.expectedChecks,
      HandoffField.rounds => defaults.maxRounds,
      HandoffField.authority => defaults.plannerAuthority,
      _ => null,
    };
  }

  /// The value [field] runs with, as words the sheet draws.
  String words(String field) => switch (valueOf(field)) {
    final List<String> values =>
      values.isEmpty ? 'None required' : values.join(', '),
    final String value => value.trim().isEmpty ? 'Not stated' : value.trim(),
    final Object value => '$value',
    null => 'Not stated',
  };

  /// Sets one field to the person's own value, which marks it overridden.
  void set(String field, Object value) {
    if (!HandoffField.all.contains(field)) return;
    _overrides[field] = value;
    notifyListeners();
  }

  /// Puts one field back to the project's value.
  void reset(String field) {
    if (_overrides.remove(field) == null) return;
    notifyListeners();
  }

  /// What stops approval, in plain words. Empty means Approve may be pressed.
  List<String> get blockers => List.unmodifiable([
    if (_repoID.isEmpty) 'Choose the repository the work is on.',
    if (plan.trim().isEmpty) 'There is no plan text to hand over.',
    if (_rounds < 1) 'The round allowance must be at least one.',
    if (title.trim().isEmpty) 'Give the work a name.',
  ]);

  /// Whether every field the bridge needs is stated.
  bool get canApprove => blockers.isEmpty;

  /// What approving authorises, in plain words (F6 decision 8).
  String get consequence {
    final rounds = _rounds;
    final authority = valueOf(HandoffField.authority) as String;
    return 'An implementer works on $_repoWords and the reviewer reads the '
        'change. It repairs up to $rounds '
        '${rounds == 1 ? 'round' : 'rounds'} and stops at a pull request. '
        'The planner decides $authority alone.';
  }

  /// The draft the sheet's fields describe. Approving posts exactly this.
  FactoryCreate get draft => FactoryCreate(
    requestID: requestID(),
    title: title.trim(),
    plan: plan,
    spec: spec,
    config: FactoryConfig(
      repoID: _repoID,
      projectRoot: projectRoot,
      hostID: hostID,
      targetBranch: _targetBranch,
      workspace: _workspace,
      native: _native,
      implementerRouting: _routing(_model(HandoffField.implementer)),
      reviewerRouting: _routing(_model(HandoffField.reviewer)),
      reviewerInstructions: '',
      implementerAuto: true,
      expectedChecks: [
        for (final name in _checks)
          FactoryCheckKey(kind: 'check_run', name: name, appID: 0),
      ],
      noRequiredCIAcknowledged: _checks.isEmpty,
      maxRounds: _rounds,
      ciPollSeconds: 30,
      ciWaitSeconds: 900,
    ),
  );

  /// A fresh request id, so a replayed approval names the same request.
  String requestID() =>
      'app-handoff-$projectRoot-${DateTime.now().microsecondsSinceEpoch}-'
      '${_requests++}';

  /// Posts the draft and keeps the item it made. It refuses while a blocker
  /// stands, so a screen cannot post what it is showing as blocked.
  Future<FactoryItem?> approve() async {
    if (!canApprove) {
      _failure = blockers.first;
      notifyListeners();
      return null;
    }
    _failure = null;
    notifyListeners();
    try {
      _created = await _create(draft);
    } catch (error) {
      _failure = '$error';
      notifyListeners();
      return null;
    }
    notifyListeners();
    return _created;
  }

  // The effective values, each read once so a getter cannot disagree with
  // the draft it builds.
  String get _repoID => (valueOf(HandoffField.repository) as String).trim();
  int get _rounds => (valueOf(HandoffField.rounds) as num).toInt();
  String get _targetBranch =>
      (valueOf(HandoffField.environment) as String) == _workspaceEnvironment
      ? ''
      : defaults.targetBranch;
  String get _workspace =>
      (valueOf(HandoffField.environment) as String) == _workspaceEnvironment
      ? (defaults.workspace.isEmpty ? 'workspace' : defaults.workspace)
      : '';
  bool get _native =>
      (valueOf(HandoffField.environment) as String) != _workspaceEnvironment;
  List<String> get _checks =>
      (valueOf(HandoffField.checks) as List<String>).toList();
  String _model(String field) => (valueOf(field) as String).trim();

  String get _repoWords => _repoID.isEmpty ? 'the repository' : _repoID;

  /// The environment as the sheet words it.
  static String _environment(ProjectDefaults defaults) =>
      defaults.native ? nativeEnvironment : _workspaceEnvironment;

  /// The environment's two values: on this host, or in a workspace.
  static const nativeEnvironment = 'On this host';
  static const _workspaceEnvironment = 'In a workspace';

  /// The environment choices the sheet offers.
  static const environments = [nativeEnvironment, _workspaceEnvironment];

  /// A model name as the bridge's routing payload, or null for the bridge's
  /// own routing.
  static Object? _routing(String model) =>
      model.isEmpty ? null : {'model': model};
}
