import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart' show ProjectSettingsVersion;
import '../client/contract.g.dart';
import '../client/transport.dart' show BridgeFailure;
import 'expected_checks.dart';
import 'fleet_store.dart' show describeFailure;
import 'handoff_model.dart' show FieldSource;
import 'section_model.dart' show SaveState, UnsavedEditsSource;
import 'thread_ref.dart' show ThreadStatus;

// The schedule and watch types the Automations tab lists reach their screens
// through the model, so `ui/` reads `model/` and never `client/`
// (test/boundary_test.dart).
export '../client/contract.g.dart' show Schedule, WatchInfo;

// The readers and the writer are passed in, so their fields stay private: Dart
// has no private named parameter.
// ignore_for_file: prefer_initializing_formals

/// The two interaction modes a project may default new agents to.
const projectModes = ['ask', 'act'];

/// The three places a finished change can land (F6 decision 12).
const projectShipTargets = ['merge', 'push', 'patch'];

/// `auto` is a tri-state: inherit, an explicit on, or an explicit revocation.
///
/// Inherit is not a mode and is never collapsed into false: the settings
/// record stores an absent value for it, and only a deliberate choice writes a
/// boolean.
enum AutoChoice { inherit, on, off }

/// `isolated` is a tri-state too: unset leaves the caller's default.
enum IsolationChoice { inherit, isolated, shared }

/// The copyWith sentinel: a parameter left at this value keeps the draft's
/// own, so an empty list and an inherit choice can still be set on purpose.
const _unset = _Unset();

class _Unset {
  const _Unset();
}

/// One editable field of a project's defaults, as the Defaults tab lists it
/// (F6 decision 12).
///
/// A field is named rather than positional so the tab and a test cannot
/// disagree about which value a row shows.
enum ProjectField {
  agentProfile,
  implementerProfile,
  reviewerProfile,
  mode,
  auto,
  isolation,
  shipTarget,
  targetBranch,
  rounds,
  ciPoll,
  ciWait,
  expectedChecks,
  reviewerInstructions,
  plannerDelegation,
}

/// One project's settings as a person is editing them.
///
/// It is a typed view of [ProjectSettings], not a second wire shape: [toJson]
/// produces the document the bridge stores, keeping every member this build
/// does not edit — an unknown routing member, a workspace, a stored host — so
/// opening the page and saving an unrelated field never drops one.
@immutable
class ProjectDraft {
  const ProjectDraft({
    this.agentProfile = '',
    this.implementerProfile = '',
    this.reviewerProfile = '',
    this.mode = '',
    this.auto = AutoChoice.inherit,
    this.isolation = IsolationChoice.inherit,
    this.shipTarget = '',
    this.targetBranch = '',
    this.rounds = '',
    this.ciPoll = '',
    this.ciWait = '',
    this.expectedChecksChoice = ExpectedChecksChoice.inherit,
    this.expectedChecks = const [],
    this.expectedChecksText = '',
    this.reviewerInstructions = '',
    this.plannerDelegation = false,
    this.designatedClient = '',
  });

  /// Reads [settings] as the tab draws it. An absent value stays empty, which
  /// is what "inherit" looks like.
  factory ProjectDraft.of(ProjectSettings settings) {
    final factory = settings.factoryDefaults;
    final expectedChecks = [
      for (final check in factory?.expectedChecks ?? const <FactoryCheckKey>[])
        ExpectedCheck.of(check),
    ];
    return ProjectDraft(
      agentProfile: profileOf(settings.routing),
      implementerProfile: profileOf(factory?.implementerRouting),
      reviewerProfile: profileOf(factory?.reviewerRouting),
      mode: settings.mode ?? '',
      auto: switch (settings.auto) {
        true => AutoChoice.on,
        false => AutoChoice.off,
        null => AutoChoice.inherit,
      },
      isolation: switch (settings.isolated) {
        true => IsolationChoice.isolated,
        false => IsolationChoice.shared,
        null => IsolationChoice.inherit,
      },
      shipTarget: settings.shipTarget ?? '',
      targetBranch: factory?.targetBranch ?? '',
      rounds: _number(factory?.maxRounds),
      ciPoll: _number(factory?.ciPollSeconds),
      ciWait: _number(factory?.ciWaitSeconds),
      expectedChecksChoice: expectedChecks.isEmpty
          ? ExpectedChecksChoice.inherit
          : ExpectedChecksChoice.custom,
      expectedChecks: expectedChecks,
      expectedChecksText: formatExpectedChecks(expectedChecks),
      reviewerInstructions: factory?.reviewerInstructions ?? '',
      plannerDelegation: settings.plannerDelegationEnabled ?? false,
      designatedClient: settings.designatedMCPClientID ?? '',
    );
  }

  /// The model profile new agents of the project run as.
  final String agentProfile;

  /// The implementer's own profile, or "" when it follows [agentProfile].
  final String implementerProfile;

  /// The reviewer's own profile, or "" when it follows [agentProfile].
  final String reviewerProfile;

  final String mode;
  final AutoChoice auto;
  final IsolationChoice isolation;
  final String shipTarget;

  final String targetBranch;

  /// Counts are held as typed text, so a cleared field stays absent rather
  /// than becoming a zero the bridge would store.
  final String rounds;
  final String ciPoll;
  final String ciWait;

  /// Whether the project states its own checks.
  ///
  /// [ExpectedChecksChoice.inherit] states nothing, which is the only state
  /// that clears a stored list: `FactoryDefaults.ExpectedChecks` carries
  /// `omitempty`, so an empty custom list serialises as absent too, and only
  /// inherit restores the follow-the-defaults behaviour.
  final ExpectedChecksChoice expectedChecksChoice;

  /// The project's own checks, when it states any.
  final List<ExpectedCheck> expectedChecks;

  /// The checks as the canonical raw lines the person edits. Kept verbatim
  /// while it does not parse, so an edit is never silently dropped.
  final String expectedChecksText;

  final String reviewerInstructions;
  final bool plannerDelegation;
  final String designatedClient;

  /// This draft with the named fields replaced.
  ///
  /// The checks members take a sentinel rather than `??`, so an empty list,
  /// an empty text and [ExpectedChecksChoice.inherit] can be set on purpose.
  ProjectDraft copyWith({
    String? agentProfile,
    String? implementerProfile,
    String? reviewerProfile,
    String? mode,
    AutoChoice? auto,
    IsolationChoice? isolation,
    String? shipTarget,
    String? targetBranch,
    String? rounds,
    String? ciPoll,
    String? ciWait,
    Object? expectedChecksChoice = _unset,
    Object? expectedChecks = _unset,
    Object? expectedChecksText = _unset,
    String? reviewerInstructions,
    bool? plannerDelegation,
    String? designatedClient,
  }) => ProjectDraft(
    agentProfile: agentProfile ?? this.agentProfile,
    implementerProfile: implementerProfile ?? this.implementerProfile,
    reviewerProfile: reviewerProfile ?? this.reviewerProfile,
    mode: mode ?? this.mode,
    auto: auto ?? this.auto,
    isolation: isolation ?? this.isolation,
    shipTarget: shipTarget ?? this.shipTarget,
    targetBranch: targetBranch ?? this.targetBranch,
    rounds: rounds ?? this.rounds,
    ciPoll: ciPoll ?? this.ciPoll,
    ciWait: ciWait ?? this.ciWait,
    expectedChecksChoice: expectedChecksChoice == _unset
        ? this.expectedChecksChoice
        : expectedChecksChoice as ExpectedChecksChoice,
    expectedChecks: expectedChecks == _unset
        ? this.expectedChecks
        : expectedChecks as List<ExpectedCheck>,
    expectedChecksText: expectedChecksText == _unset
        ? this.expectedChecksText
        : expectedChecksText as String,
    reviewerInstructions: reviewerInstructions ?? this.reviewerInstructions,
    plannerDelegation: plannerDelegation ?? this.plannerDelegation,
    designatedClient: designatedClient ?? this.designatedClient,
  );

  /// What the implementer runs as right now: its own profile, or the agent
  /// default it follows (F6 decision 12).
  String get effectiveImplementer =>
      implementerProfile.trim().isEmpty ? agentProfile : implementerProfile;

  /// What the reviewer runs as right now, the same way.
  String get effectiveReviewer =>
      reviewerProfile.trim().isEmpty ? agentProfile : reviewerProfile;

  /// The document the bridge stores: every member it already holds, with the
  /// fields this draft edits replaced. A field the person did not state is
  /// absent, never a fallback.
  Map<String, dynamic> toJson(ProjectSettings stored) {
    final json = stored.toJson();
    _putProfile(json, 'routing', stored.routing, agentProfile);
    _putOrRemove(json, 'mode', mode.trim());
    switch (auto) {
      case AutoChoice.inherit:
        json.remove('auto');
      case AutoChoice.on:
        json['auto'] = true;
      case AutoChoice.off:
        json['auto'] = false;
    }
    switch (isolation) {
      case IsolationChoice.inherit:
        json.remove('isolated');
      case IsolationChoice.isolated:
        json['isolated'] = true;
      case IsolationChoice.shared:
        json['isolated'] = false;
    }
    _putOrRemove(json, 'shipTarget', shipTarget);

    final factory = <String, dynamic>{...?stored.factoryDefaults?.toJson()};
    _putProfile(
      factory,
      'implementerRouting',
      stored.factoryDefaults?.implementerRouting,
      implementerProfile,
    );
    _putProfile(
      factory,
      'reviewerRouting',
      stored.factoryDefaults?.reviewerRouting,
      reviewerProfile,
    );
    _putOrRemove(factory, 'targetBranch', targetBranch.trim());
    _putCount(factory, 'maxRounds', rounds);
    _putCount(factory, 'ciPollSeconds', ciPoll);
    _putCount(factory, 'ciWaitSeconds', ciWait);
    switch (expectedChecksChoice) {
      case ExpectedChecksChoice.inherit:
        factory.remove('expectedChecks');
      case ExpectedChecksChoice.custom:
        factory['expectedChecks'] = [
          for (final check in expectedChecks) check.toKey().toJson(),
        ];
    }
    _putOrRemove(factory, 'reviewerInstructions', reviewerInstructions.trim());
    if (factory.isEmpty) {
      json.remove('factoryDefaults');
    } else {
      json['factoryDefaults'] = factory;
    }

    if (plannerDelegation) {
      json['plannerDelegationEnabled'] = true;
      _putOrRemove(json, 'designatedMCPClientId', designatedClient.trim());
    } else {
      // Off stores no client at all: a client that was never designated
      // answers nothing, and a stale id would name one that could.
      json.remove('plannerDelegationEnabled');
      json.remove('designatedMCPClientId');
    }
    return json;
  }

  /// The profile one routing object names, or "" when it names none.
  static String profileOf(Object? routing) {
    if (routing is Map<String, dynamic>) {
      final profile = routing['profile'];
      if (profile is String) return profile;
    }
    return '';
  }

  /// Sets or removes `profile` on [target]'s routing object, keeping every
  /// other member the bridge sent.
  static void _putProfile(
    Map<String, dynamic> target,
    String key,
    Object? routing,
    String profile,
  ) {
    final route = <String, dynamic>{
      if (routing is Map<String, dynamic>) ...routing,
    };
    final wanted = profile.trim();
    if (wanted.isEmpty) {
      route.remove('profile');
    } else {
      route['profile'] = wanted;
    }
    if (route.isEmpty) {
      target.remove(key);
    } else {
      target[key] = route;
    }
  }

  static void _putOrRemove(
    Map<String, dynamic> target,
    String key,
    String value,
  ) {
    if (value.isEmpty) {
      target.remove(key);
    } else {
      target[key] = value;
    }
  }

  /// A count the person typed: a positive whole number is stored, anything
  /// else (empty, zero, not a number) is an absent field.
  static void _putCount(Map<String, dynamic> target, String key, String text) {
    final value = int.tryParse(text.trim());
    if (value == null || value <= 0) {
      target.remove(key);
    } else {
      target[key] = value;
    }
  }

  static String _number(int? value) =>
      value == null || value <= 0 ? '' : '$value';
}

/// One plan run as a project page lists it (F6 decision 12): the agent it runs
/// on, what it is doing and how far it has got.
///
/// It is a view of the contract's `RunListEntry`, so the page draws model data
/// and never touches the wire types (`app/test/boundary_test.dart`). The digest
/// travels beside the words, so the shell can open the run's own thread from a
/// row without a second read.
@immutable
class ProjectRun {
  const ProjectRun({
    required this.agentID,
    required this.name,
    required this.project,
    required this.at,
    required this.status,
    required this.headline,
    required this.line,
    required this.progress,
    this.digest,
  });

  factory ProjectRun.of(RunListEntry entry) {
    final raw = entry.run;
    final digest = raw is Map<String, dynamic> ? _readDigest(raw) : null;
    final error = (entry.error ?? '').trim();
    final phase = (digest?.phase ?? '').trim();
    final branch = (digest?.branch ?? '').trim();
    final line = error.isNotEmpty
        ? error
        : [
            if (phase.isNotEmpty) phase,
            if (branch.isNotEmpty) branch,
          ].join(' · ');
    final progress = digest == null
        ? 'No digest yet'
        : '${digest.doneTasks} of ${digest.totalTasks}';
    return ProjectRun(
      agentID: entry.agentID,
      name: (entry.name ?? '').trim(),
      project: entry.project,
      at: DateTime.fromMillisecondsSinceEpoch(entry.at),
      status: _statusOf(digest),
      headline: progress,
      line: line.isEmpty ? 'On ${entry.agentID}' : line,
      progress: progress,
      digest: raw is Map<String, dynamic> ? raw : null,
    );
  }

  /// The agent whose session the run is.
  final String agentID;

  /// What the bridge calls the run, or "" when it names none.
  final String name;

  /// The project the agent works in.
  final String project;

  /// When the digest last arrived.
  final DateTime at;

  /// What the run's thread row says it is doing.
  final ThreadStatus status;

  /// How far it has got, in one line.
  final String headline;

  /// What it is doing now: its phase and branch, or its error.
  final String line;

  /// How many tasks are done, in words.
  final String progress;

  /// The digest the list carried, raw, so a thread opened from this row can
  /// draw the run's lanes.
  final Map<String, dynamic>? digest;

  /// A run's digest, when the entry carries one this build can read.
  static SDDRun? _readDigest(Map<String, dynamic> raw) {
    try {
      return SDDRun.fromJson(raw);
    } catch (_) {
      return null;
    }
  }

  static ThreadStatus _statusOf(SDDRun? digest) {
    if (digest == null) return ThreadStatus.idle;
    if (digest.finished) {
      return digest.succeeded ? ThreadStatus.ready : ThreadStatus.failed;
    }
    return digest.active ? ThreadStatus.running : ThreadStatus.idle;
  }
}

/// One repository mirror's freshness, as the Health tab reads it.
@immutable
class ProjectMirror {
  const ProjectMirror({
    required this.repoID,
    required this.present,
    required this.ageSeconds,
    required this.head,
  });

  final String repoID;
  final bool present;
  final int ageSeconds;
  final String head;
}

/// Whether a project's default workspace resolves and is built.
@immutable
class ProjectWorkspaceHealth {
  const ProjectWorkspaceHealth({
    required this.ref,
    required this.resolves,
    required this.built,
    required this.error,
  });

  final String ref;
  final bool resolves;
  final bool built;
  final String error;
}

/// A project's health as the Health tab draws it (F6 decision 12).
@immutable
class ProjectHealthView {
  const ProjectHealthView({
    required this.gateRunnable,
    required this.mirrors,
    required this.orphans,
    required this.trust,
    required this.verifyBuild,
    required this.verifyTest,
    this.workspace,
  });

  factory ProjectHealthView.of(ProjectHealth health) => ProjectHealthView(
    gateRunnable: health.gateRunnable,
    mirrors: [
      for (final mirror in health.mirrorFresh)
        ProjectMirror(
          repoID: mirror.repoID,
          present: mirror.present,
          ageSeconds: mirror.ageSeconds ?? 0,
          head: mirror.head ?? '',
        ),
    ],
    orphans: health.orphanWorktrees,
    trust: health.trust,
    verifyBuild: health.verify.build,
    verifyTest: health.verify.test,
    workspace: health.workspaceResolves == null
        ? null
        : ProjectWorkspaceHealth(
            ref: health.workspaceResolves!.ref,
            resolves: health.workspaceResolves!.resolves,
            built: health.workspaceResolves!.built,
            error: health.workspaceResolves!.error ?? '',
          ),
  );

  /// "yes", "no" or "unknown": whether the verify gate can run.
  final String gateRunnable;

  final List<ProjectMirror> mirrors;
  final List<String> orphans;
  final String trust;

  /// The gate's commands, or "" when unset.
  final String verifyBuild;
  final String verifyTest;

  final ProjectWorkspaceHealth? workspace;
}

/// One row of a discovery report, in words the Health tab prints.
@immutable
class DiscoveryRow {
  const DiscoveryRow(this.name, this.description, this.value);

  final String name;
  final String description;
  final String value;
}

/// A repository discovery report as the Health tab draws it (F6 decision 12).
@immutable
class DiscoveryView {
  const DiscoveryView({
    required this.status,
    required this.forge,
    required this.revision,
    required this.branch,
    required this.error,
    required this.capabilities,
    required this.facts,
    required this.recommended,
    required this.gaps,
    this.worktreePath = '',
    this.worktreeDirty = false,
  });

  factory DiscoveryView.of(DiscoveryReport report) => DiscoveryView(
    status: report.status,
    forge: report.forge,
    revision: report.revision ?? '',
    branch: report.branch ?? '',
    error: report.error ?? '',
    worktreePath: report.worktree?.path ?? '',
    worktreeDirty: report.worktree?.dirty == true,
    capabilities: [
      for (final capability in report.capabilities)
        DiscoveryRow(
          capability.key,
          (capability.detail ?? '').isEmpty
              ? 'A capability the forge offers'
              : capability.detail!,
          _word(capability.state.wire),
        ),
    ],
    facts: [
      for (final fact in report.facts)
        DiscoveryRow(
          fact.kind,
          (fact.detail ?? '').isEmpty
              ? 'Detected in ${fact.source}'
              : fact.detail!,
          fact.value,
        ),
    ],
    recommended: [
      for (final suggestion in report.recommended)
        DiscoveryRow(suggestion.kind, suggestion.reason, suggestion.value),
    ],
    gaps: [
      for (final gap in report.gaps)
        DiscoveryRow(gap.key, gap.detail, _word(gap.state.wire)),
    ],
  );

  final String status;
  final String forge;
  final String revision;
  final String branch;
  final String error;
  final String worktreePath;
  final bool worktreeDirty;
  final List<DiscoveryRow> capabilities;
  final List<DiscoveryRow> facts;
  final List<DiscoveryRow> recommended;
  final List<DiscoveryRow> gaps;

  /// A capability state in words: the bridge's own spelling, spaced.
  static String _word(String state) => state.trim().replaceAll('_', ' ');
}

/// A project's forge automations, as the Automations tab summarises them.
@immutable
class ProjectAutomations {
  const ProjectAutomations({
    required this.reviewBotOn,
    required this.reviewBotRepo,
    required this.ciFixerOn,
    required this.ciFixerBranches,
    required this.autoFix,
  });

  factory ProjectAutomations.of(Automations automations) => ProjectAutomations(
    reviewBotOn: automations.reviewBot?.enabled,
    reviewBotRepo: automations.reviewBot?.repoID ?? '',
    ciFixerOn: automations.ciFixer?.enabled,
    ciFixerBranches: automations.ciFixer?.branches ?? const [],
    autoFix: automations.autoFix,
  );

  final bool? reviewBotOn;
  final String reviewBotRepo;
  final bool? ciFixerOn;
  final List<String> ciFixerBranches;
  final bool? autoFix;
}

/// One project's page state (F6 decision 12): its settings and the ETag that
/// names them, its health, its discovery report and the runs it has.
///
/// Saves are conditional: every write sends the tag the screen read as
/// `If-Match`, so a change another surface made in between is never
/// overwritten. A refusal that names a tag the bridge no longer holds is
/// [SaveState.stale]: the edit is kept, the page says so, and a reload puts
/// the bridge's document beneath it.
///
/// The model reads settings, health, runs and discovery and nothing else; the
/// board's items come from the one factory store that polls them (F6 decision
/// 14) and the tab draws what this model holds (design-system.md §5).
class ProjectModel extends ChangeNotifier implements UnsavedEditsSource {
  ProjectModel({
    required this.root,
    required this.name,
    required Future<ProjectSettingsVersion> Function() read,
    required Future<ProjectSettingsVersion> Function(
      ProjectSettings settings,
      String? etag,
    )
    write,
    Future<ProjectHealth> Function()? readHealth,
    Future<List<RunListEntry>> Function()? readRuns,
    Future<DiscoveryReport> Function(String repoID)? discover,
    Future<List<Schedule>> Function()? readSchedules,
    Future<List<WatchInfo>> Function()? readWatches,
    String? Function(String agentID)? projectOfAgent,
    DateTime Function()? now,
  }) : _read = read,
       _write = write,
       _readHealth = readHealth,
       _readRuns = readRuns,
       _discover = discover,
       _readSchedules = readSchedules,
       _readWatches = readWatches,
       _projectOfAgent = projectOfAgent,
       _now = now ?? DateTime.now;

  /// The project's root, its id everywhere else in the app.
  final String root;

  /// What the page calls it: its directory's name.
  final String name;

  final Future<ProjectSettingsVersion> Function() _read;
  final Future<ProjectSettingsVersion> Function(
    ProjectSettings settings,
    String? etag,
  )
  _write;

  /// The health report's reader. Null leaves the Health tab saying that it
  /// cannot read one rather than drawing an empty report.
  final Future<ProjectHealth> Function()? _readHealth;

  /// The run list's reader. Null leaves the Work tab's runs section saying
  /// that it is not read here.
  final Future<List<RunListEntry>> Function()? _readRuns;

  final Future<DiscoveryReport> Function(String repoID)? _discover;

  /// The schedules and watches read for the Automations tab, and the resolver
  /// that says which project a watch's agent belongs to. All three are
  /// optional: a host that wires none leaves the tab with its forge rows.
  final Future<List<Schedule>> Function()? _readSchedules;
  final Future<List<WatchInfo>> Function()? _readWatches;
  final String? Function(String agentID)? _projectOfAgent;

  final DateTime Function() _now;

  ProjectSettings? _stored;
  String? _etag;
  ProjectDraft? _draft;
  SaveState _saveState = SaveState.clean;
  String? _saveError;
  bool _loading = false;
  bool _loaded = false;
  String? _error;
  ProjectHealthView? _health;
  bool _healthLoading = false;
  String? _healthError;
  List<ProjectRun> _runs = const [];
  bool _runsLoading = false;
  String? _runsError;
  DiscoveryView? _discovery;
  bool _discovering = false;
  String? _discoveryError;

  /// The project's own schedules and the watches its agents started, as the
  /// Automations tab lists them. Empty until a read answers, and after a read
  /// that fails: the tab draws what it has.
  List<Schedule> _schedules = const [];
  List<WatchInfo> _watches = const [];

  /// The document the bridge last answered with, or null before the first
  /// read.
  ProjectSettings? get settings => _stored;

  /// The ETag naming [settings], or null when the bridge sent none.
  String? get etag => _etag;

  /// The settings as the person is editing them.
  ProjectDraft? get draft => _draft;

  /// The first read is in flight.
  bool get loading => _loading;

  /// Whether the settings were read at least once.
  bool get loaded => _loaded;

  /// Why the last settings read failed, in the bridge's own words.
  String? get error => _error;

  /// Where the draft stands: clean, edited, saving, refused or stale.
  SaveState get saveState => _saveState;

  /// Why the last save failed, in the bridge's own words.
  String? get saveError => _saveError;

  /// Whether the draft holds a change the bridge has not accepted.
  bool get dirty => _saveState != SaveState.clean;

  /// Whether the last save was refused because the settings moved on — a
  /// conflict, not an error the person made.
  bool get stale => _saveState == SaveState.stale;

  /// The gate, the mirrors, the retained workspaces and the trust state.
  ProjectHealthView? get health => _health;
  bool get healthLoading => _healthLoading;
  String? get healthError => _healthError;

  /// The project's plan runs, as `GET /api/runs` lists them.
  List<ProjectRun> get runs => _runs;
  bool get runsLoading => _runsLoading;
  String? get runsError => _runsError;

  /// The last discovery report, or null before one is run.
  DiscoveryView? get discovery => _discovery;
  bool get discovering => _discovering;
  String? get discoveryError => _discoveryError;

  /// The project's forge automations, or null when it configures none.
  ProjectAutomations? get automations {
    final configured = _stored?.automations;
    return configured == null ? null : ProjectAutomations.of(configured);
  }

  /// The schedules this project owns, as the Automations tab lists them: the
  /// bridge's list filtered to those whose project is this root.
  List<Schedule> get automationSchedules => _schedules;

  /// The watches this project's agents started: the bridge's list filtered to
  /// those whose agent resolves to this root.
  List<WatchInfo> get automationWatches => _watches;

  /// The labels an intake issue must carry to be picked up, and none when the
  /// project watches every issue of its repository.
  List<String> get intakeLabels => _stored?.intake.labels ?? const [];

  /// The repository discovery probes: the intake wiring's repo, or "" when the
  /// project names none, in which case discovery is not offered.
  String get repoID => _stored?.intake.repoID ?? '';

  /// When the settings were last read, for a stale banner's age.
  DateTime get lastRead => _lastRead ?? _now();
  DateTime? _lastRead;

  /// Whether [field] holds a value the person chose rather than one inherited
  /// from the project's defaults.
  ///
  /// A role's profile is inherited while it names none of its own; every other
  /// field is overridden when the draft differs from the stored document.
  FieldSource sourceOf(ProjectField field) {
    final draft = _draft;
    final stored = _stored;
    if (draft == null || stored == null) return FieldSource.inherited;
    final base = ProjectDraft.of(stored);
    final overridden = switch (field) {
      ProjectField.agentProfile => draft.agentProfile != base.agentProfile,
      ProjectField.implementerProfile => draft.implementerProfile.isNotEmpty,
      ProjectField.reviewerProfile => draft.reviewerProfile.isNotEmpty,
      ProjectField.mode => draft.mode != base.mode,
      ProjectField.auto => draft.auto != base.auto,
      ProjectField.isolation => draft.isolation != base.isolation,
      ProjectField.shipTarget => draft.shipTarget != base.shipTarget,
      ProjectField.targetBranch => draft.targetBranch != base.targetBranch,
      ProjectField.rounds => draft.rounds != base.rounds,
      ProjectField.ciPoll => draft.ciPoll != base.ciPoll,
      ProjectField.ciWait => draft.ciWait != base.ciWait,
      ProjectField.expectedChecks =>
        draft.expectedChecksChoice != base.expectedChecksChoice ||
            !listEquals(draft.expectedChecks, base.expectedChecks),
      ProjectField.reviewerInstructions =>
        draft.reviewerInstructions != base.reviewerInstructions,
      ProjectField.plannerDelegation =>
        draft.plannerDelegation != base.plannerDelegation,
    };
    return overridden ? FieldSource.overridden : FieldSource.inherited;
  }

  /// Reads the settings, and the health and runs beside them, showing loading
  /// while the first read is in flight and the bridge's refusal in place when
  /// it fails.
  Future<void> load() async {
    if (_loading) return;
    _loading = true;
    _error = null;
    notifyListeners();
    try {
      final version = await _read();
      _stored = version.settings;
      _etag = version.etag;
      _draft = ProjectDraft.of(version.settings);
      _saveState = SaveState.clean;
      _saveError = null;
      _lastRead = _now();
      _loaded = true;
    } on BridgeFailure catch (failure) {
      _error = describeFailure(failure);
    } catch (error) {
      _error = describeFailure(error);
    } finally {
      _loading = false;
      notifyListeners();
    }
    // The schedules and watches the Automations tab lists: read in the same
    // load, and never failing it. A list the bridge will not answer is empty,
    // and the page's own state already says the read failed.
    await _readAutomations();
  }

  /// Reads the project's schedules and its agents' watches, each filtered to
  /// this root. A failed read leaves that list empty rather than failing the
  /// page: the Automations tab draws what it has.
  Future<void> _readAutomations() async {
    final readSchedules = _readSchedules;
    final readWatches = _readWatches;
    final projectOf = _projectOfAgent;
    if (readSchedules == null && readWatches == null) return;
    if (readSchedules != null) {
      try {
        _schedules = List.unmodifiable([
          for (final schedule in await readSchedules())
            if (schedule.project == root) schedule,
        ]);
      } catch (_) {
        _schedules = const [];
      }
    }
    if (readWatches != null && projectOf != null) {
      try {
        _watches = List.unmodifiable([
          for (final watch in await readWatches())
            if (projectOf(watch.agentID) == root) watch,
        ]);
      } catch (_) {
        _watches = const [];
      }
    }
    notifyListeners();
  }

  /// Reads the health report. A failure is its own line in the Health tab; it
  /// never fails the settings the page is editing.
  Future<void> loadHealth() async {
    final read = _readHealth;
    if (read == null || _healthLoading) return;
    _healthLoading = true;
    _healthError = null;
    notifyListeners();
    try {
      _health = ProjectHealthView.of(await read());
    } on BridgeFailure catch (failure) {
      _healthError = describeFailure(failure);
    } catch (error) {
      _healthError = describeFailure(error);
    } finally {
      _healthLoading = false;
      notifyListeners();
    }
  }

  /// Reads the project's plan runs.
  Future<void> loadRuns() async {
    final read = _readRuns;
    if (read == null || _runsLoading) return;
    _runsLoading = true;
    _runsError = null;
    notifyListeners();
    try {
      _runs = List.unmodifiable([
        for (final entry in await read()) ProjectRun.of(entry),
      ]);
    } on BridgeFailure catch (failure) {
      _runsError = describeFailure(failure);
    } catch (error) {
      _runsError = describeFailure(error);
    } finally {
      _runsLoading = false;
      notifyListeners();
    }
  }

  /// Probes the project's repository for what it has and what it is missing.
  /// Discovery never writes anything, so it is safe to run at any time.
  Future<void> runDiscovery() async {
    final probe = _discover;
    final repo = repoID;
    if (probe == null || repo.isEmpty || _discovering) return;
    _discovering = true;
    _discoveryError = null;
    notifyListeners();
    try {
      _discovery = DiscoveryView.of(await probe(repo));
    } on BridgeFailure catch (failure) {
      _discoveryError = describeFailure(failure);
    } catch (error) {
      _discoveryError = describeFailure(error);
    } finally {
      _discovering = false;
      notifyListeners();
    }
  }

  /// Sets the model profile new agents of the project run as.
  void setAgentProfile(String profile) =>
      _edit((draft) => draft.copyWith(agentProfile: profile));

  /// Sets one factory role's own profile, which marks it overridden.
  void setRoleProfile(String role, String profile) => _edit(
    (draft) => role == 'implementer'
        ? draft.copyWith(implementerProfile: profile)
        : draft.copyWith(reviewerProfile: profile),
  );

  void setMode(String mode) => _edit((draft) => draft.copyWith(mode: mode));
  void setAuto(AutoChoice choice) =>
      _edit((draft) => draft.copyWith(auto: choice));
  void setIsolation(IsolationChoice choice) =>
      _edit((draft) => draft.copyWith(isolation: choice));
  void setShipTarget(String target) =>
      _edit((draft) => draft.copyWith(shipTarget: target));
  void setTargetBranch(String branch) =>
      _edit((draft) => draft.copyWith(targetBranch: branch));
  void setRounds(String rounds) =>
      _edit((draft) => draft.copyWith(rounds: rounds));
  void setCIPoll(String seconds) =>
      _edit((draft) => draft.copyWith(ciPoll: seconds));
  void setCIWait(String seconds) =>
      _edit((draft) => draft.copyWith(ciWait: seconds));

  /// Sets the project's own checks, which stops them following the defaults.
  void setExpectedChecks(List<ExpectedCheck> checks) => _edit(
    (draft) => draft.copyWith(
      expectedChecksChoice: ExpectedChecksChoice.custom,
      expectedChecks: checks,
      expectedChecksText: formatExpectedChecks(checks),
    ),
  );

  /// Types the project's own checks as the canonical raw lines. Text that
  /// does not parse is kept verbatim, with the checks that did parse beside
  /// it, and [save] refuses until it does.
  void setExpectedChecksText(String text) => _edit(
    (draft) => draft.copyWith(
      expectedChecksChoice: ExpectedChecksChoice.custom,
      expectedChecks: parseExpectedChecks(text).checks,
      expectedChecksText: text,
    ),
  );

  /// Returns the checks to the bridge's follow-the-defaults behaviour: the
  /// project states none of its own.
  void clearExpectedChecks() => _edit(
    (draft) => draft.copyWith(
      expectedChecksChoice: ExpectedChecksChoice.inherit,
      expectedChecks: const <ExpectedCheck>[],
      expectedChecksText: '',
    ),
  );

  void setReviewerInstructions(String text) =>
      _edit((draft) => draft.copyWith(reviewerInstructions: text));
  void setPlannerDelegation(bool enabled) =>
      _edit((draft) => draft.copyWith(plannerDelegation: enabled));
  void setDesignatedClient(String client) =>
      _edit((draft) => draft.copyWith(designatedClient: client));

  /// Puts one role back on the agent default: its own profile is removed, so
  /// it follows the project again.
  void resetRole(String role) => setRoleProfile(role, '');

  /// Puts [field] back the way the bridge holds it.
  void reset(ProjectField field) {
    final stored = _stored;
    if (stored == null) return;
    final base = ProjectDraft.of(stored);
    _edit(
      (draft) => switch (field) {
        ProjectField.agentProfile => draft.copyWith(
          agentProfile: base.agentProfile,
        ),
        ProjectField.implementerProfile => draft.copyWith(
          implementerProfile: '',
        ),
        ProjectField.reviewerProfile => draft.copyWith(reviewerProfile: ''),
        ProjectField.mode => draft.copyWith(mode: base.mode),
        ProjectField.auto => draft.copyWith(auto: base.auto),
        ProjectField.isolation => draft.copyWith(isolation: base.isolation),
        ProjectField.shipTarget => draft.copyWith(shipTarget: base.shipTarget),
        ProjectField.targetBranch => draft.copyWith(
          targetBranch: base.targetBranch,
        ),
        ProjectField.rounds => draft.copyWith(rounds: base.rounds),
        ProjectField.ciPoll => draft.copyWith(ciPoll: base.ciPoll),
        ProjectField.ciWait => draft.copyWith(ciWait: base.ciWait),
        ProjectField.expectedChecks => draft.copyWith(
          expectedChecksChoice: base.expectedChecksChoice,
          expectedChecks: base.expectedChecks,
          expectedChecksText: base.expectedChecksText,
        ),
        ProjectField.reviewerInstructions => draft.copyWith(
          reviewerInstructions: base.reviewerInstructions,
        ),
        ProjectField.plannerDelegation => draft.copyWith(
          plannerDelegation: base.plannerDelegation,
          designatedClient: base.designatedClient,
        ),
      },
    );
  }

  /// Saves the draft with the tag the page read as `If-Match`.
  ///
  /// It shows saving while the write is in flight; a refusal keeps the edit
  /// and says why, and one that names a tag the bridge no longer holds is
  /// stale, which offers a reload (F6 decision 12).
  Future<void> save() async {
    final stored = _stored;
    final draft = _draft;
    if (stored == null || draft == null || _saveState == SaveState.saving) {
      return;
    }
    // Raw check text that does not parse is kept as typed so it can be fixed;
    // the save is refused here, in the parser's own words, rather than
    // storing something other than what the person sees.
    if (draft.expectedChecksChoice == ExpectedChecksChoice.custom) {
      final error = parseExpectedChecks(draft.expectedChecksText).error;
      if (error != null) {
        _refuse(null, error);
        return;
      }
    }
    _saveState = SaveState.saving;
    _saveError = null;
    notifyListeners();
    final ProjectSettings next;
    try {
      next = ProjectSettings.fromJson(draft.toJson(stored));
    } catch (error) {
      _refuse(null, describeFailure(error));
      return;
    }
    try {
      final version = await _write(next, _etag);
      _stored = version.settings;
      _etag = version.etag;
      _draft = ProjectDraft.of(version.settings);
      _saveState = SaveState.clean;
      _saveError = null;
      _lastRead = _now();
      notifyListeners();
    } on BridgeFailure catch (failure) {
      _refuse(failure.status, describeFailure(failure));
    } catch (error) {
      _refuse(null, describeFailure(error));
    }
  }

  /// After the bridge refused an edit as stale: reads the settings again and
  /// keeps the edit, so it can be saved against what is there now.
  Future<void> reloadAfterStale() async {
    final edit = _draft;
    _saveState = SaveState.clean;
    _saveError = null;
    notifyListeners();
    await load();
    if (edit == null || _stored == null) return;
    _draft = edit;
    _saveState = SaveState.dirty;
    notifyListeners();
  }

  @override
  bool get hasUnsavedEdits => dirty;

  @override
  void discardEdits() {
    final stored = _stored;
    if (stored == null) return;
    _draft = ProjectDraft.of(stored);
    _saveState = SaveState.clean;
    _saveError = null;
    notifyListeners();
  }

  /// Saves the refusal: a conflict is stale, which keeps the edit and offers a
  /// reload; anything else is a failure the person corrects.
  void _refuse(int? status, String message) {
    _saveState = status == 409 || status == 412
        ? SaveState.stale
        : SaveState.failed;
    _saveError = message;
    notifyListeners();
  }

  /// Applies [change] to the draft and marks the page dirty.
  void _edit(ProjectDraft Function(ProjectDraft draft) change) {
    final draft = _draft;
    if (draft == null) return;
    _draft = change(draft);
    if (_saveState != SaveState.saving) _saveState = SaveState.dirty;
    _saveError = null;
    notifyListeners();
  }
}
