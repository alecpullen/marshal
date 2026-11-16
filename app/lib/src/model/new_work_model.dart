// The routes are named for what they do and their fields stay private, so the
// parameters cannot be initializing formals: Dart has no private named
// parameter.
// ignore_for_file: prefer_initializing_formals

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/transport.dart';

/// What a "Plan first" started, in the terms a screen needs: the planner's id,
/// the project it plans in, and the workspace it owns (F6 decision 8).
///
/// It is a value of this directory rather than the bridge's own
/// [PlannerView], because `ui/` reads `model/` and never `client/` (F1's
/// boundary).
@immutable
class StartedPlanner {
  const StartedPlanner({
    required this.id,
    required this.projectRoot,
    required this.workspaceID,
  });

  /// Reads the planner the bridge started.
  factory StartedPlanner.of(PlannerView view) => StartedPlanner(
    id: view.id,
    projectRoot: view.projectRoot,
    workspaceID: view.workspaceID,
  );

  final String id;
  final String projectRoot;
  final String workspaceID;
}

/// What a new work item may start (spec decision 11). Only an agent is built
/// in this phase; a "Plan first" run and a work item are drawn in the planned
/// state, naming F6, so nothing pretends to work.
enum NewWorkKind {
  agent('Agent'),
  planFirst('Plan first'),
  workItem('Work item');

  const NewWorkKind(this.word);

  final String word;

  /// Whether this phase can start it. "Work item" arrives with the handoff
  /// review (F6 plan 2, task 2); an agent and a planner both start here, the
  /// second as a planning thread that hands off when its plan is ready.
  bool get planned => this == NewWorkKind.workItem;
}

/// The choices the dialog opens with: the project's root and the workspace it
/// allows come from the project, and the rest say what this phase does.
@immutable
class NewWorkDefaults {
  const NewWorkDefaults({
    required this.project,
    required this.baseBranch,
    required this.environment,
    required this.permissionMode,
    required this.workspace,
    required this.host,
    this.model,
  });

  /// What a project offers before anything is chosen.
  ///
  /// Only two of these come from the project: its root, and the workspace its
  /// isolation answer allows. [baseBranch] is null because no route lists a
  /// project's branches, and the request then carries no `baseRef` — the
  /// bridge picks the project's own default, rather than the dialog naming a
  /// branch it made up. [environment] and [permissionMode] are what this
  /// phase does (a host process, mode `act`), not fields the project reports;
  /// the record says so.
  factory NewWorkDefaults.fromProject(
    ProjectStatus project, {
    String host = 'local',
  }) => NewWorkDefaults(
    project: project.root,
    baseBranch: null,
    environment: 'Local',
    permissionMode: 'Act',
    workspace: project.isolation == 'available'
        ? isolatedWorktree
        : projectWorkspace,
    host: host,
  );

  /// The chip's value when the branch is the project's own: the spawn request
  /// then omits `baseRef` and the bridge decides.
  static const projectDefaultBranch = 'The project default';

  /// The workspace chip's two values: run in the project, or in an isolated
  /// worktree of it.
  static const isolatedWorktree = 'Isolated worktree';
  static const projectWorkspace = 'Project';

  final String project;

  /// The base branch the dialog opens with, or null for the project's default
  /// (the request carries no `baseRef`).
  final String? baseBranch;

  final String environment;
  final String? model;
  final String permissionMode;
  final String workspace;
  final String host;
}

/// One chip the dialog draws: what it is set to, and the values a menu offers
/// for it (spec decision 11).
///
/// A chip with fewer than two [options] has nothing to choose this phase, and
/// the dialog draws it as a plain pill carrying [note] — never a menu whose
/// only entry is the value already set.
@immutable
class NewWorkChip {
  const NewWorkChip(
    this.name,
    this.value, {
    this.options = const [],
    this.note,
  });

  /// The chip's name, as the dialog writes it: `Base branch`.
  final String name;

  /// The value it holds now.
  final String value;

  /// Every value a menu offers, the current one included.
  final List<String> options;

  /// What a pill's tooltip says when there is nothing to choose: either why
  /// the value is what it is, or the phase that brings the choice.
  final String? note;

  /// Whether a menu is worth drawing: a menu that offers one value — the one
  /// already set — changes nothing.
  bool get chooseable => options.length > 1;
}

/// One new-work dialog's state: the prompt, the kind, the chips, and the
/// three lists that fill the prompt (spec decision 11).
///
/// The lists hold the bridge's own declarations — [RepoIssue] and [Recipe] —
/// rather than a second shape per list, so the dialog and the route cannot
/// disagree about a field.
///
/// The routes are functions so a test drives every list and the spawn with no
/// socket, the way the other models do (model/README.md).
class NewWorkModel extends ChangeNotifier {
  NewWorkModel({
    required this.defaults,
    required Future<RecentPrompts> Function() recentPrompts,
    required Future<List<RepoIssue>> Function(String project) issues,
    required Future<List<Recipe>> Function() recipes,
    required Future<SpawnResult> Function(SpawnRequest request) spawn,
    Future<PlannerView> Function(PlannerCreateRequest request)? plan,
    String? repoId,
    bool issuesFromRepo = false,
    Map<String, List<String>> options = const {},
    Map<String, String> notes = const {},
    Future<List<String>> Function()? projects,
  }) : _recentPrompts = recentPrompts,
       _issues = issues,
       _recipes = recipes,
       _spawn = spawn,
       _plan = plan,
       repoID = repoId,
       _issuesFromRepo = issuesFromRepo,
       _options = options,
       _notes = notes,
       _projects = projects,
       _choices = {
         'Project': defaults.project,
         'Base branch':
             defaults.baseBranch ?? NewWorkDefaults.projectDefaultBranch,
         'Environment': defaults.environment,
         'Model': defaults.model ?? 'The project default',
         'Permission mode': defaults.permissionMode,
         'Workspace': defaults.workspace,
         'Host': defaults.host,
       };

  /// The dialog a project's own routes back.
  ///
  /// [repoId] is a registered repo's id — what `GET /api/repos` reports, and
  /// what `GET /api/repos/{id}/issues` is keyed by. No bridge route names the
  /// repo of a project root yet, so a caller that has none passes none, and
  /// the Issues list is then empty for a stated reason (a project with no
  /// registered repo) rather than an invented one.
  factory NewWorkModel.forProject(
    ProjectStatus project,
    BridgeClient client, {
    String host = 'local',
    String? repoId,
  }) => NewWorkModel(
    defaults: NewWorkDefaults.fromProject(project, host: host),
    recentPrompts: () => client.recentPrompts(),
    issues: (root) async {
      final id = repoId;
      if (id == null || id.isEmpty) return const [];
      try {
        return await client.issues(id);
      } on BridgeFailure {
        // A repo whose registration names no forge, or whose forge refused
        // the credentials, serves no issues: the dialog says so and its other
        // lists still load.
        return const [];
      }
    },
    recipes: () async {
      try {
        return await client.recipes();
      } on BridgeFailure {
        return const [];
      }
    },
    spawn: (request) => client.spawn(request),
    issuesFromRepo: repoId != null && repoId.isNotEmpty,
    // What a menu can offer this phase, from data that exists: the two
    // permission modes the session's mode route takes, the two workspaces
    // the project's isolation answer allows, and the daemon's own projects.
    options: {
      'Permission mode': const ['Ask'],
      'Workspace': project.isolation == 'available'
          ? const [NewWorkDefaults.projectWorkspace]
          : const [],
    },
    // The rest have one value this phase, and a pill says why rather than
    // opening a menu that changes nothing.
    notes: {
      'Base branch': 'The project default branch; F6 brings the choice',
      'Environment': 'A local spawn; F6 brings the choice',
      'Model': 'The project default; F6 brings the models by role',
      'Host': 'The thread runs here; F7 lists hosts',
      if (project.isolation != 'available' && project.isolation.isNotEmpty)
        'Workspace': project.isolation,
    },
    projects: () async => [for (final p in await client.projects()) p.root],
  );

  /// The values the dialog opened with.
  final NewWorkDefaults defaults;

  final Future<RecentPrompts> Function() _recentPrompts;
  final Future<List<RepoIssue>> Function(String project) _issues;
  final Future<List<Recipe>> Function() _recipes;
  final Future<SpawnResult> Function(SpawnRequest request) _spawn;

  /// Starts a planner instead of an agent, for "Plan first" (F6 decision 8).
  /// Null leaves the kind planned: a host that cannot start a planner never
  /// offers to.
  final Future<PlannerView> Function(PlannerCreateRequest request)? _plan;

  /// The repository a planner is started against. The planner routes require
  /// one, and no route names the repo of a project root yet, so a project the
  /// caller could not name a repo for cannot start a planning thread.
  final String? repoID;

  final Map<String, String> _choices;

  /// What a caller says each chip may be set to, apart from the value it
  /// opens with.
  final Map<String, List<String>> _options;

  /// What a pill's tooltip says when there is nothing to choose.
  final Map<String, String> _notes;

  /// The route that names the daemon's projects, when the dialog can ask it.
  final Future<List<String>> Function()? _projects;

  /// Values a route supplied after the model was built: the project menu's
  /// alternatives come from `GET /api/projects`, which is read in [load].
  final Map<String, List<String>> _loaded = {};

  /// Whether the issues list came from a registered repo's own route.
  final bool _issuesFromRepo;

  NewWorkKind _kind = NewWorkKind.agent;
  String _prompt = '';
  List<RepoIssue> _issueList = const [];
  List<String> _recentList = const [];
  List<Recipe> _recipeList = const [];
  bool _loading = false;
  String? _failure;
  SpawnResult? _started;
  PlannerView? _startedPlanner;

  /// What the dialog will start.
  NewWorkKind get kind => _kind;

  /// What it should do.
  String get prompt => _prompt;

  /// Every chip's value by its name, in the order the dialog draws them.
  Map<String, String> get choices => Map.unmodifiable(_choices);

  /// The chips the dialog draws, in order: each with its value, the values a
  /// menu offers, and — where nothing can be chosen — the line its pill's
  /// tooltip gives.
  List<NewWorkChip> get chips => [
    for (final entry in _choices.entries)
      NewWorkChip(
        entry.key,
        entry.value,
        options: _optionsFor(entry.key, entry.value),
        note: _notes[entry.key],
      ),
  ];

  /// Every value the menu for [name] offers: the current one first, then what
  /// the caller declared, then what a route supplied.
  List<String> _optionsFor(String name, String value) {
    final seen = <String>{};
    return [
      for (final option in [value, ...?_options[name], ...?_loaded[name]])
        if (seen.add(option)) option,
    ];
  }

  List<String> get chipNames => List.unmodifiable(_choices.keys);

  /// The issues the project's registered repo offers, once [load] has run.
  List<RepoIssue> get issues => _issueList;

  /// Whether the Issues list can be asked at all: it needs a registered repo,
  /// because an issue belongs to a remote and a project is a checkout. False
  /// means an empty [issues] list is "no repo named", not "the repo has no
  /// open issues".
  bool get issuesFromRepo => _issuesFromRepo;

  /// The prompts worth offering again.
  List<String> get recentPrompts => _recentList;

  /// The recipes that fill the prompt.
  List<Recipe> get recipes => _recipeList;

  bool get loading => _loading;

  /// Why the last spawn failed, in the bridge's words.
  String? get failure => _failure;

  /// The agent the last [start] made, if it made one.
  SpawnResult? get started => _started;

  /// The planner the last [start] made, for a "Plan first" (F6 decision 8).
  StartedPlanner? get startedPlanner =>
      _startedPlanner == null ? null : StartedPlanner.of(_startedPlanner!);

  /// Whether Start can post: a prompt, and a kind this phase can start.
  bool get canStart => _prompt.trim().isNotEmpty && !_kind.planned;

  /// Loads the three lists, and the project menu's alternatives.
  Future<void> load() async {
    _loading = true;
    _failure = null;
    notifyListeners();
    try {
      final recent = await _recentPrompts();
      _recentList = List.unmodifiable(recent.prompts);
      _issueList = List.unmodifiable(await _issues(defaults.project));
      _recipeList = List.unmodifiable(await _recipes());
    } catch (error) {
      _failure = '$error';
    }
    await _loadProjects();
    _loading = false;
    notifyListeners();
  }

  /// Reads the project menu's alternatives. A route that refuses leaves the
  /// chip with the one project it started with, rather than failing the
  /// dialog: the project set is an extra, not one of the three lists.
  Future<void> _loadProjects() async {
    final projects = _projects;
    if (projects == null) return;
    try {
      _loaded['Project'] = [
        for (final root in await projects())
          if (root != defaults.project) root,
      ];
    } catch (_) {
      // The current project is still the chip's value.
    }
  }

  /// Chooses the kind. A planned kind is refused here and drawn disabled:
  /// the dialog never posts something F6 has not built.
  void selectKind(NewWorkKind kind) {
    if (kind.planned || kind == _kind) return;
    _kind = kind;
    notifyListeners();
  }

  /// Sets one chip to one of the values its menu offered.
  ///
  /// A value the menu does not offer is refused: the set is the whole promise
  /// of the menu, so a caller cannot set a chip to something the dialog never
  /// drew.
  void setChoice(String name, String value) {
    final current = _choices[name];
    if (current == null || current == value) return;
    if (!_optionsFor(name, current).contains(value)) return;
    _choices[name] = value;
    notifyListeners();
  }

  /// Replaces the prompt, which an issue, a recent prompt or a recipe does.
  void prefill(String text) {
    if (_prompt == text) return;
    _prompt = text;
    notifyListeners();
  }

  /// Posts the spawn request and keeps what came back.
  ///
  /// The request carries only what the dialog actually knows: a base branch
  /// when one was chosen (never the placeholder for the project's own
  /// default), and the workspace choice as the `isolated` flag the bridge
  /// reads — not as a made-up workspace name.
  ///
  /// "Plan first" starts a planner instead (F6 decision 8): the prompt becomes
  /// the planner's first turn, and the thread the dialog opens is a planning
  /// thread.
  Future<void> start() async {
    if (!canStart) return;
    _failure = null;
    notifyListeners();
    if (_kind == NewWorkKind.planFirst) {
      await _startPlanning();
      return;
    }
    final label = _choices['Permission mode']!;
    final mode = _modeWire(label);
    if (mode == null) {
      // An unknown mode is surfaced, never coerced: defaulting to `act` would
      // silently start the most permissive mode there is.
      _failure = 'The bridge takes the modes act and ask, not "$label".';
      notifyListeners();
      return;
    }
    try {
      final baseBranch = _choices['Base branch'];
      _started = await _spawn(
        SpawnRequest(
          project: _choices['Project']!,
          prompt: _prompt.trim(),
          mode: mode,
          baseRef: baseBranch == NewWorkDefaults.projectDefaultBranch
              ? null
              : baseBranch,
          isolated: _choices['Workspace'] == NewWorkDefaults.isolatedWorktree,
        ),
      );
    } catch (error) {
      _failure = '$error';
    }
    notifyListeners();
  }

  /// Starts the planner a "Plan first" asks for, and keeps the one it made.
  ///
  /// The planner routes require a registered repository, so a project whose
  /// repo this dialog was not told fails here in the bridge's words rather
  /// than starting something that cannot plan.
  Future<void> _startPlanning() async {
    final plan = _plan;
    if (plan == null) {
      _failure = 'This project cannot start a planner yet.';
      notifyListeners();
      return;
    }
    try {
      _startedPlanner = await plan(
        PlannerCreateRequest(
          projectRoot: _choices['Project']!,
          repoID: repoID ?? '',
          mode: _choices['Permission mode'] == 'Ask'
              ? 'default'
              : 'bypassPermissions',
        ),
      );
    } catch (error) {
      _failure = '$error';
    }
    notifyListeners();
  }

  /// The permission mode as the wire spells it, or null when the label is not
  /// one the bridge takes.
  static String? _modeWire(String label) => switch (label.toLowerCase()) {
    'act' => 'act',
    'ask' => 'ask',
    _ => null,
  };

  /// A sheet that closes disposes the model while [load] is still waiting on
  /// a route; the answer that lands afterwards must not notify a disposed
  /// notifier.
  bool _disposed = false;

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }

  @override
  void notifyListeners() {
    if (_disposed) return;
    super.notifyListeners();
  }
}
