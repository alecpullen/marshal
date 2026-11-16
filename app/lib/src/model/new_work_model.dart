// The routes are named for what they do and their fields stay private, so the
// parameters cannot be initializing formals: Dart has no private named
// parameter.
// ignore_for_file: prefer_initializing_formals

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/transport.dart';

/// What a new work item may start (spec decision 11). Only an agent is built
/// in this phase; a "Plan first" run and a work item are drawn in the planned
/// state, naming F6, so nothing pretends to work.
enum NewWorkKind {
  agent('Agent'),
  planFirst('Plan first'),
  workItem('Work item');

  const NewWorkKind(this.word);

  final String word;

  /// Whether this phase can start it. The other two arrive with F6.
  bool get planned => this != NewWorkKind.agent;
}

/// The choices the dialog opens with, taken from the project rather than
/// invented: the project's root, its base branch, the environment, the model,
/// the permission mode, the workspace and the host.
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
  factory NewWorkDefaults.fromProject(
    ProjectStatus project, {
    String host = 'local',
  }) => NewWorkDefaults(
    project: project.root,
    baseBranch: 'main',
    environment: 'Local',
    permissionMode: 'Act',
    workspace: project.isolation == 'available'
        ? 'Isolated worktree'
        : 'Project',
    host: host,
  );

  final String project;
  final String baseBranch;
  final String environment;
  final String? model;
  final String permissionMode;
  final String workspace;
  final String host;
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
    bool issuesFromRepo = false,
  }) : _recentPrompts = recentPrompts,
       _issues = issues,
       _recipes = recipes,
       _spawn = spawn,
       _issuesFromRepo = issuesFromRepo,
       _choices = {
         'Project': defaults.project,
         'Base branch': defaults.baseBranch,
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
  );

  /// The values the dialog opened with.
  final NewWorkDefaults defaults;

  final Future<RecentPrompts> Function() _recentPrompts;
  final Future<List<RepoIssue>> Function(String project) _issues;
  final Future<List<Recipe>> Function() _recipes;
  final Future<SpawnResult> Function(SpawnRequest request) _spawn;

  final Map<String, String> _choices;

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

  /// What the dialog will start.
  NewWorkKind get kind => _kind;

  /// What it should do.
  String get prompt => _prompt;

  /// Every chip's value by its name, in the order the dialog draws them.
  Map<String, String> get choices => Map.unmodifiable(_choices);

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

  /// Whether Start can post: a prompt, and a kind this phase can start.
  bool get canStart => _prompt.trim().isNotEmpty && !_kind.planned;

  /// Loads the three lists.
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
    _loading = false;
    notifyListeners();
  }

  /// Chooses the kind. A planned kind is refused here and drawn disabled:
  /// the dialog never posts something F6 has not built.
  void selectKind(NewWorkKind kind) {
    if (kind.planned || kind == _kind) return;
    _kind = kind;
    notifyListeners();
  }

  /// Sets one chip.
  void setChoice(String name, String value) {
    if (_choices[name] == value) return;
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
  Future<void> start() async {
    if (!canStart) return;
    _failure = null;
    notifyListeners();
    try {
      _started = await _spawn(
        SpawnRequest(
          project: _choices['Project']!,
          prompt: _prompt.trim(),
          mode: _modeWire(_choices['Permission mode']!),
          baseRef: _choices['Base branch'],
          workspace: _choices['Workspace'],
        ),
      );
    } catch (error) {
      _failure = '$error';
    }
    notifyListeners();
  }

  /// The permission mode as the wire spells it.
  static String _modeWire(String label) => switch (label.toLowerCase()) {
    'act' => 'act',
    'ask' => 'ask',
    _ => 'act',
  };
}
