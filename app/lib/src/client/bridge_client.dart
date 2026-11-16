import 'dart:convert';

import 'contract.g.dart';
import 'event_stream.dart';
import 'transport.dart';

/// The bridge's typed routes: one method per route F2 needs, each returning a
/// generated type and failing with a [BridgeFailure] when the bridge refuses.
///
/// A bridge client carries no policy and holds no state: the host and the
/// screens above it decide what to do with an answer.
class BridgeClient {
  BridgeClient(this._transport);

  final Transport _transport;

  /// GET /api/daemon: what the local daemon reports about itself.
  Future<DaemonInfo> daemon() => _objectOf('/api/daemon', DaemonInfo.fromJson);

  /// GET /api/agents: every agent the bridge supervises.
  Future<List<AgentStatus>> agents() =>
      _listOf('/api/agents', AgentStatus.fromJson);

  /// GET /api/projects: every project the daemon knows.
  Future<List<ProjectStatus>> projects() =>
      _listOf('/api/projects', ProjectStatus.fromJson);

  /// GET /api/inbox: the needs-you queue and the groups behind it.
  Future<Inbox> inbox() => _objectOf('/api/inbox', Inbox.fromJson);

  /// GET /api/pending: the submissions waiting on an operator's decision.
  ///
  /// The fleet plan's sketch of this method named [PendingRequest], which is a
  /// parked agent's approval or question; GET /api/pending answers
  /// [PendingSubmission] (web/bridge/intake.go), which is what `approve` and
  /// `deny` below act on.
  Future<List<PendingSubmission>> pending() =>
      _listOf('/api/pending', PendingSubmission.fromJson);

  /// POST /api/permissions/{toolCallId}: answer one parked approval.
  Future<void> answerPermission(String toolCallId, Decision decision) =>
      _post('/api/permissions/${Uri.encodeComponent(toolCallId)}', decision);

  /// POST /api/questions/{questionId}: answer one parked question.
  Future<void> answerQuestion(String questionId, Answers answers) =>
      _post('/api/questions/${Uri.encodeComponent(questionId)}', answers);

  /// POST /api/pending/{id}/approve: start the submission's agent.
  Future<ApproveResult> approveIntake(String id) async {
    final response = await _send(
      Request(
        method: 'POST',
        path: '/api/pending/${Uri.encodeComponent(id)}/approve',
      ),
    );
    return ApproveResult.fromJson(_object(response));
  }

  /// POST /api/pending/{id}/deny: refuse the submission.
  Future<void> denyIntake(String id) =>
      _post('/api/pending/${Uri.encodeComponent(id)}/deny', null);

  /// GET /api/budgets: today's spend against the caps.
  Future<BudgetReport> budgets() =>
      _objectOf('/api/budgets', BudgetReport.fromJson);

  /// GET /api/prompts/recent: the prompts worth offering again.
  Future<RecentPrompts> recentPrompts() =>
      _objectOf('/api/prompts/recent', RecentPrompts.fromJson);

  /// GET /api/events: the fleet stream, which the caller starts.
  EventStream fleetEvents() => EventStream(_transport, '/api/events');

  // Automations: schedules and recipes (F7).

  /// GET /api/schedules: every cron entry the bridge holds.
  Future<List<Schedule>> schedules() =>
      _listOf('/api/schedules', Schedule.fromJson);

  /// POST /api/schedules without an id, PUT /api/schedules/{id} with one:
  /// without an id it creates, with one it replaces.
  Future<Schedule> saveSchedule(Schedule schedule) async {
    final id = schedule.id;
    final response = await _send(
      Request(
        method: id.isEmpty ? 'POST' : 'PUT',
        path: id.isEmpty
            ? '/api/schedules'
            : '/api/schedules/${Uri.encodeComponent(id)}',
        body: schedule,
      ),
    );
    return Schedule.fromJson(_object(response));
  }

  /// DELETE /api/schedules/{id}.
  Future<void> deleteSchedule(String id) => _send(
    Request(
      method: 'DELETE',
      path: '/api/schedules/${Uri.encodeComponent(id)}',
    ),
  );

  /// POST /api/schedules/{id}/run: starts the schedule now.
  Future<void> runSchedule(String id) => _post(
    '/api/schedules/${Uri.encodeComponent(id)}/run',
    null,
  );

  /// GET /api/recipes: every recipe, built-in and the user's.
  Future<List<Recipe>> recipes() => _listOf('/api/recipes', Recipe.fromJson);

  /// PUT /api/recipes/{name}: stores the recipe under its own name. The
  /// bridge refuses a built-in.
  Future<Recipe> saveRecipe(Recipe recipe) async {
    final response = await _send(
      Request(
        method: 'PUT',
        path: '/api/recipes/${Uri.encodeComponent(recipe.name)}',
        body: recipe,
      ),
    );
    return Recipe.fromJson(_object(response));
  }

  /// DELETE /api/recipes/{name}. The bridge refuses a built-in.
  Future<void> deleteRecipe(String name) => _send(
    Request(method: 'DELETE', path: '/api/recipes/${Uri.encodeComponent(name)}'),
  );

  /// POST /api/recipes/{name}/copy: stores a copy under [as] and answers it.
  Future<Recipe> copyRecipe(String name, String as) async {
    final response = await _send(
      Request(
        method: 'POST',
        path: '/api/recipes/${Uri.encodeComponent(name)}/copy',
        body: RecipeCopy(name: as),
      ),
    );
    return Recipe.fromJson(_object(response));
  }

  /// POST /api/recipes/{name}/run: starts the recipe where [run] says.
  Future<void> runRecipe(String name, RecipeRun run) => _post(
    '/api/recipes/${Uri.encodeComponent(name)}/run',
    run,
  );

  /// GET /api/watches: every watch the bridge's sources report.
  Future<List<WatchInfo>> watches() =>
      _listOf('/api/watches', WatchInfo.fromJson);

  /// POST /api/reroutes/{id}/undo: puts back the binding a tripped watch
  /// changed. [id] is the reroute record's id.
  Future<void> undoReroute(String id) =>
      _post('/api/reroutes/${Uri.encodeComponent(id)}/undo', null);

  Future<Response> _send(Request request) async {
    final response = await _transport.send(request);
    if (!response.ok) {
      throw bridgeFailure(response.status, response.body);
    }
    return response;
  }

  Object? _decode(Response response) =>
      response.body.isEmpty ? null : jsonDecode(response.body);

  Map<String, dynamic> _object(Response response) =>
      _decode(response) as Map<String, dynamic>;

  Future<T> _objectOf<T>(
    String path,
    T Function(Map<String, dynamic> json) fromJson,
  ) async => fromJson(_object(await _send(Request(path: path))));

  Future<List<T>> _listOf<T>(
    String path,
    T Function(Map<String, dynamic> json) fromJson,
  ) async {
    final response = await _send(Request(path: path));
    final values = _decode(response) as List<dynamic>;
    return [
      for (final value in values) fromJson(value as Map<String, dynamic>),
    ];
  }

  Future<void> _post(String path, Object? body) async {
    await _send(Request(method: 'POST', path: path, body: body));
  }
}
