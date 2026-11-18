import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';
import 'drafts_model.dart';
import 'expected_checks.dart';
import 'fleet_store.dart' show describeFailure;
import 'planning_model.dart';

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

  /// Every check that must pass, exactly as the forge names it: its kind, its
  /// name and, for a `check_run`, the app that runs it.
  final List<ExpectedCheck> expectedChecks;

  /// How many repair rounds the work may take.
  final int maxRounds;

  /// What the planner may decide alone.
  final String plannerAuthority;

  /// The factory defaults a project's settings state (F6 decision 8): what a
  /// handoff review inherits from the project the plan was made for.
  ///
  /// Only what the document carries is inherited. The implementer's and
  /// reviewer's profiles are left unstated because the document holds a
  /// routing payload there rather than a profile name, and the planner's
  /// authority has no project setting at all; the sheet says the project
  /// states those not, and the person chooses them.
  factory ProjectDefaults.fromSettings(
    ProjectSettings settings, {
    required String hostID,
  }) {
    final factory = settings.factoryDefaults;
    final statedHost = (factory?.hostID ?? '').trim();
    final workspace = (factory?.workspace ?? '').trim();
    return ProjectDefaults(
      repoID: (settings.intake.repoID ?? '').trim(),
      // The host the factory runs on: the project's own, or the one that
      // holds the project when the project names none.
      hostID: statedHost.isEmpty ? hostID : statedHost,
      targetBranch: (factory?.targetBranch ?? '').trim(),
      workspace: workspace,
      // A project that names a workspace runs its factory work there; one
      // that names none runs it on the host that holds the project.
      native: workspace.isEmpty,
      expectedChecks: [
        // Every key the bridge sent is kept, in its order: a check with an
        // empty name is the bridge's to refuse, not this mapping's to drop.
        for (final check
            in factory?.expectedChecks ?? const <FactoryCheckKey>[])
          ExpectedCheck.of(check),
      ],
      maxRounds: factory?.maxRounds ?? 3,
    );
  }
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
    DraftsModel? drafts,
    String? hostID,
    String? settingsVersion,
    Future<String> Function(String projectRoot)? readSettingsVersion,
    this.reviewedSnapshot,
    this.plannerID,
    Future<FactoryItem> Function(
      String plannerID,
      String snapshotID,
      FactoryCreate request,
    )?
    handoffSnapshot,
  }) : _create = create,
       _handoffSnapshot = handoffSnapshot,
       repositories = List.unmodifiable(repositories),
       drafts = drafts,
       hostID = hostID ?? defaults.hostID,
       _reviewed = settingsVersion,
       _readVersion = readSettingsVersion,
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

  /// The capture this review is bound to, when the review came from a planning
  /// thread's frozen snapshot. Null leaves the review unbound: the new-work
  /// dialog has no capture, and the plan text the sheet shows is what it sends.
  final PlannerSnapshot? reviewedSnapshot;

  /// The planner the capture came from, which the snapshot handoff route names.
  final String? plannerID;

  /// The capture-bound route: the bridge rewrites the draft's plan and spec
  /// from the snapshot's frozen artifacts and verifies every captured file's
  /// hash before it stores anything. Null leaves [approve] on the create route.
  final Future<FactoryItem> Function(
    String plannerID,
    String snapshotID,
    FactoryCreate request,
  )?
  _handoffSnapshot;

  /// Whether this review binds the draft it submits to a frozen capture: the
  /// snapshot, the planner it belongs to and the handoff route all present.
  bool get boundToSnapshot =>
      reviewedSnapshot != null && plannerID != null && _handoffSnapshot != null;

  /// The repositories the bridge has registered, as ids: the choices the
  /// sheet's repository menu offers. A project states no repository of its
  /// own yet, so the person picks one and the choice is the override.
  final List<String> repositories;

  /// The working draft this review autosaves as (F6 decision 10). Null leaves
  /// the review unsaved, which a host that keeps no drafts does.
  final DraftsModel? drafts;
  final Map<String, Object?> _overrides;

  String? _failure;
  FactoryItem? _created;

  /// The request id this review posts, made once and kept for the review's
  /// lifetime: a deliberate retry after an uncertain failure replays the same
  /// request, so the bridge answers the item it already made instead of
  /// creating a second one. A new review is a new model and names a new
  /// request.
  String? _requestID;

  /// One number per review, so two reviews opened in the same microsecond
  /// still name different requests.
  static int _reviews = 0;
  final int _review = _reviews++;

  /// Whether the person acknowledged that this work requires no checks at
  /// all. Theirs to choose, never derived: a project that states no checks is
  /// still followed until they say otherwise.
  bool _noRequiredCIAcknowledged = false;

  /// The expected checks as the person typed them, verbatim, or null while
  /// they have typed none and the field follows the project. Text the parser
  /// refuses is kept here rather than replaced by the checks that happened to
  /// parse (the project editor keeps its own text the same way).
  String? _checksText;

  /// The settings version this review is bound to, and the one in force when
  /// an approval last checked. [settingsConflict] is worded from the two.
  String? _reviewed;
  String? _current;
  bool _moved = false;

  /// Reads the combined settings version; null when the caller supplied none,
  /// which leaves the review unbound rather than blocking it.
  final Future<String> Function(String projectRoot)? _readVersion;

  /// Why the last approval failed, in the bridge's words.
  String? get failure => _failure;

  /// The item the last [approve] created, if it created one.
  FactoryItem? get created => _created;

  /// Whether the person acknowledged that this work requires no checks. The
  /// acknowledgement is theirs to give or take back; it is never read out of
  /// how many checks are stated.
  bool get noRequiredCIAcknowledged => _noRequiredCIAcknowledged;

  /// The settings-version conflict, in the words the sheet needs: the version
  /// this review displayed and the one now in force. Null when they agree, or
  /// when the bridge could not answer either.
  ///
  /// It is never worded as a generic failure: nothing failed. The settings the
  /// review would freeze moved, so approving this page would freeze settings
  /// nobody read.
  String? get settingsConflict {
    if (!_moved) return null;
    final reviewed = _reviewed ?? 'unversioned';
    final current = _current ?? 'unversioned';
    return 'The settings moved after this review was opened (reviewed '
        '$reviewed, now $current). Reopen the review to read the current '
        'settings; approving this page would freeze settings nobody read.';
  }

  /// Reads the settings version again and binds this review to it the first
  /// time. It answers false when the settings moved, so an approval refuses
  /// rather than freezing a version nobody read.
  ///
  /// A version the bridge cannot answer is stated, never guessed: the review
  /// proceeds unbound, exactly as it did before this check existed, rather
  /// than blocking on a settings read the bridge refused.
  Future<bool> checkSettings() async {
    final read = _readVersion;
    if (read == null) return true;
    final String current;
    try {
      current = await read(projectRoot);
    } catch (_) {
      _current = null;
      _moved = false;
      notifyListeners();
      return true;
    }
    _current = current;
    _reviewed ??= current;
    _moved = _reviewed != null && _reviewed != current;
    notifyListeners();
    return !_moved;
  }

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
    final List<ExpectedCheck> checks =>
      checks.isEmpty
          ? 'None required'
          : [for (final check in checks) check.words].join(', '),
    final String value => value.trim().isEmpty ? 'Not stated' : value.trim(),
    final Object value => '$value',
    null => 'Not stated',
  };

  /// Sets one field to the person's own value, which marks it overridden.
  ///
  /// The expected checks are typed as raw lines, so a set of them is written
  /// as those lines and kept verbatim. The sheet does not come through here
  /// for them: its field types through [setChecksText], which is the same
  /// path with the text the person actually wrote.
  void set(String field, Object value) {
    if (!HandoffField.all.contains(field)) return;
    if (field == HandoffField.checks && value is List<ExpectedCheck>) {
      setChecksText(formatExpectedChecks(value));
      return;
    }
    _overrides[field] = value;
    _autosave();
    notifyListeners();
  }

  /// Puts one field back to the project's value.
  ///
  /// Resetting the expected checks also takes back the no-checks
  /// acknowledgement: they cannot both be live, so the project's own checks
  /// come back with nothing claiming they are unused.
  void reset(String field) {
    final overridden = _overrides.remove(field) != null;
    final acknowledged =
        field == HandoffField.checks && _noRequiredCIAcknowledged;
    if (field == HandoffField.checks) {
      _checksText = null;
      _noRequiredCIAcknowledged = false;
    }
    if (!overridden && !acknowledged) return;
    _autosave();
    notifyListeners();
  }

  /// Records the person's acknowledgement that this work requires no checks,
  /// or takes it back. Like a field edit it autosaves the draft, so the
  /// acknowledgement survives a page that closes.
  ///
  /// Acknowledging clears the stated checks: the working draft sends
  /// `checksMode: 'none'` for an acknowledgement, which drops stated checks,
  /// so keeping them here would make the draft and the item's config encode
  /// different work. Taking the acknowledgement back states no checks either;
  /// the project is followed again until the person states some.
  void setNoRequiredCIAcknowledged(bool acknowledged) {
    _noRequiredCIAcknowledged = acknowledged;
    if (acknowledged) {
      _checksText = null;
      _overrides[HandoffField.checks] = const <ExpectedCheck>[];
    } else {
      _dropEmptyChecksOverride();
    }
    _autosave();
    notifyListeners();
  }

  /// The expected checks as the person typed them, verbatim: their own raw
  /// lines, or the project's as canonical lines while they have typed none.
  String get checksText => _checksText ?? formatExpectedChecks(_checks);

  /// Why the typed checks text cannot be read as checks, in the parser's own
  /// words — the ones the project editor shows — or null when it can.
  String? get checksError => parseExpectedChecks(checksText).error;

  /// Types the expected checks as canonical raw lines.
  ///
  /// Text that does not parse is kept verbatim, with the checks that did
  /// parse beside it, so nothing the person typed is rewritten away; the
  /// [blockers] list refuses approval until it can be read. Stating a check
  /// also takes back the no-checks acknowledgement, because the two cannot
  /// both be live. A field with no checks in it is not an override at all:
  /// the project's checks are followed again until the person states some,
  /// while the field itself stays as empty as they left it.
  void setChecksText(String text) {
    final parsed = parseExpectedChecks(text);
    _checksText = text;
    _overrides[HandoffField.checks] = parsed.checks;
    if (parsed.checks.isNotEmpty) _noRequiredCIAcknowledged = false;
    _dropEmptyChecksOverride();
    _autosave();
    notifyListeners();
  }

  /// Forgets a checks override that holds nothing.
  ///
  /// An empty override says "overridden to no checks", which is not a choice
  /// a cleared field makes: the project's checks still apply, so the row reads
  /// as inherited and the draft encodes `inherit` with it. The one empty
  /// override that stays is the acknowledgement's own, which is what makes
  /// the stated checks empty while `checksMode` says `none`.
  void _dropEmptyChecksOverride() {
    if (_noRequiredCIAcknowledged) return;
    final checks = _overrides[HandoffField.checks];
    if (checks is List<ExpectedCheck> && checks.isEmpty) {
      _overrides.remove(HandoffField.checks);
    }
  }

  /// Queues the draft the review is, two seconds after the last edit (F6
  /// decision 10): a review that is never approved is not lost.
  void _autosave() => drafts?.scheduleSave(draftContent);

  /// Saves the queued draft now. The sheet calls this as it closes, so an edit
  /// made inside the autosave window is not lost to a timer.
  Future<void> flush() async => drafts?.saveNow();

  /// The review as a working draft holds it: the editor's own values, with a
  /// blank override meaning "follow the project's".
  WorkingDraftContent get draftContent => WorkingDraftContent(
    title: title,
    plan: plan,
    spec: spec,
    repoID: _repoID,
    targetBranchOverride: defaults.targetBranch,
    workspaceOverride: _workspace,
    implementerOverride: _model(HandoffField.implementer),
    reviewerOverride: _model(HandoffField.reviewer),
    reviewerInstructions: '',
    maxRoundsOverride: '$_rounds',
    ciPollOverride: '',
    ciWaitOverride: '',
    // The checks mode states which of the three shapes the review holds: the
    // person acknowledged that nothing is required ('none'), the project
    // states its own checks ('custom'), or neither — the item follows the
    // project rather than silently requiring nothing ('inherit').
    checksMode: _noRequiredCIAcknowledged
        ? 'none'
        : (_checks.isEmpty ? 'inherit' : 'custom'),
    expectedChecksText: formatExpectedChecks(_checks),
    native: _native,
    nativeChosen: true,
    implementerAuto: true,
    noRequiredCIAcknowledged: _noRequiredCIAcknowledged,
  );

  /// What stops approval, in plain words. Empty means the action may be
  /// pressed.
  List<String> get blockers => List.unmodifiable([
    if (_repoID.isEmpty) 'Choose the repository the work is on.',
    if (planToSend.trim().isEmpty) 'There is no plan text to hand over.',
    if (_rounds < 1) 'The round allowance must be at least one.',
    if (title.trim().isEmpty) 'Give the work a name.',
    // Checks text that cannot be read is not approvable: posting it would
    // drop every line the parser refused, which is exactly what the sheet
    // shows the person as typed.
    ?checksError,
    ?settingsConflict,
  ]);

  /// Whether every field the bridge needs is stated.
  bool get canApprove => blockers.isEmpty;

  /// The plan text this review sends. With a capture bound it is the frozen
  /// plan the bridge froze, never the sheet's editable copy: the capture is
  /// the evidence the person reviewed.
  String get planToSend => boundToSnapshot ? reviewedSnapshot!.plan : plan;

  /// The specification text this review sends: the capture's frozen spec when
  /// one is bound, else the sheet's own.
  String get specToSend => boundToSnapshot ? reviewedSnapshot!.spec : spec;

  /// The selection the draft binds to its capture, when one is bound: the base
  /// commit the capture was taken at, every file it froze with the operation
  /// and the hashes it recorded, and the capture's digest.
  ///
  /// It is null for an unbound review — the new-work dialog reviewed no
  /// capture — because "no selection" and "an empty selection" are different
  /// approvals, and only the first one is true there.
  EditSnapshot? get editSnapshot {
    final snapshot = reviewedSnapshot;
    if (!boundToSnapshot || snapshot == null) return null;
    return EditSnapshot(
      baseCommit: snapshot.baseCommit,
      files: [
        for (final file in snapshot.files)
          SelectedFile(
            path: file.path,
            operation: file.operation,
            baseSHA256: file.baseSHA256,
            contentSHA256: file.contentSHA256,
            executable: file.executable,
          ),
      ],
      sha256: snapshot.sha256,
    );
  }

  /// What the one action does, in plain words (F6 decision 8).
  ///
  /// Creating a factory item is not starting work: the bridge stores a draft,
  /// and the item runs when it is approved in the factory. The words say so
  /// rather than promising a started job.
  String get consequence {
    final rounds = _rounds;
    final authority = valueOf(HandoffField.authority) as String;
    final snapshot = reviewedSnapshot;
    final draft = boundToSnapshot && snapshot != null
        ? 'This creates a factory draft from the frozen capture '
              '${_short(snapshot.id)} — its plan, its specification and the '
              'files it verified, not the text edited here. Changing the '
              'evidence means taking a fresh capture.'
        : 'This creates a factory draft from the plan text shown here. '
              'Changing the plan means editing it and reviewing it again.';
    return '$draft Work starts when the item is approved in the factory; '
        'nothing runs before that. It then works on $_repoWords, repairs up to '
        '$rounds ${rounds == 1 ? 'round' : 'rounds'} and stops at a pull '
        'request, and the reviewer reads the change. The planner decides '
        '$authority alone.';
  }

  /// The draft the sheet's fields describe. The action posts exactly this.
  FactoryCreate get draft => FactoryCreate(
    requestID: requestID(),
    title: title.trim(),
    plan: planToSend,
    spec: specToSend,
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
      expectedChecks: [for (final check in _checks) check.toKey()],
      noRequiredCIAcknowledged: _noRequiredCIAcknowledged,
      maxRounds: _rounds,
      ciPollSeconds: 30,
      ciWaitSeconds: 900,
    ),
    editSnapshot: editSnapshot,
  );

  /// This review's request id, made once and kept: two approvals in one review
  /// name the same request, so the second one replays the first rather than
  /// creating a second item. A new review names a new one.
  String requestID() => _requestID ??=
      'app-handoff-$projectRoot-${DateTime.now().microsecondsSinceEpoch}-'
      '$_review';

  /// Posts the draft and keeps the item it made, through the capture's handoff
  /// route when a snapshot is bound and the create route otherwise.
  ///
  /// It refuses while a blocker stands, so a screen cannot post what it is
  /// showing as blocked, and it reads the settings version once more first: an
  /// approval that would freeze settings nobody read is refused, not posted. A
  /// failure leaves [failure] set and posts nothing more: a retry is the
  /// person's decision, and its stable request id makes it a replay.
  Future<FactoryItem?> approve() async {
    if (!canApprove) {
      _failure = blockers.first;
      notifyListeners();
      return null;
    }
    if (!await checkSettings()) {
      _failure = settingsConflict;
      notifyListeners();
      return null;
    }
    _failure = null;
    notifyListeners();
    final request = draft;
    final snapshot = reviewedSnapshot;
    final planner = plannerID;
    final handoff = _handoffSnapshot;
    try {
      _created = boundToSnapshot && snapshot != null && planner != null
          ? await handoff!(planner, snapshot.id, request)
          : await _create(request);
    } catch (error) {
      _failure = describeFailure(error);
      notifyListeners();
      return null;
    }
    notifyListeners();
    return _created;
  }

  /// An id or a digest, short enough to read on one line.
  static String _short(String value) =>
      value.length <= 8 ? value : value.substring(0, 8);

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
  List<ExpectedCheck> get _checks =>
      (valueOf(HandoffField.checks) as List<ExpectedCheck>).toList();
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
