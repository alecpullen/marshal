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
  Future<void> runSchedule(String id) =>
      _post('/api/schedules/${Uri.encodeComponent(id)}/run', null);

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
    Request(
      method: 'DELETE',
      path: '/api/recipes/${Uri.encodeComponent(name)}',
    ),
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
  Future<void> runRecipe(String name, RecipeRun run) =>
      _post('/api/recipes/${Uri.encodeComponent(name)}/run', run);

  /// GET /api/watches: every watch the bridge's sources report.
  Future<List<WatchInfo>> watches() =>
      _listOf('/api/watches', WatchInfo.fromJson);

  /// POST /api/reroutes/{id}/undo: puts back the binding a tripped watch
  /// changed. [id] is the reroute record's id.
  Future<void> undoReroute(String id) =>
      _post('/api/reroutes/${Uri.encodeComponent(id)}/undo', null);

  // Bots: the review bot and the CI fixer (F7).

  /// GET /api/projects/settings?project=…: the part of the project's settings
  /// the Automations screens read.
  Future<ProjectAutomations> projectAutomations(String project) => _objectOf(
    '/api/projects/settings',
    ProjectAutomations.fromJson,
    query: {'project': project},
  );

  /// GET /api/automations/review/drafts: the held reviews, newest first.
  Future<List<ReviewDraft>> reviewDrafts({String? project, String? status}) =>
      _listOf(
        '/api/automations/review/drafts',
        ReviewDraft.fromJson,
        query: {
          if (project != null) 'project': project,
          if (status != null) 'status': status,
        },
      );

  /// GET /api/automations/review/drafts/{id}.
  Future<ReviewDraft> reviewDraft(String id) => _objectOf(
    '/api/automations/review/drafts/${Uri.encodeComponent(id)}',
    ReviewDraft.fromJson,
  );

  /// PUT /api/automations/review/drafts/{id}: the edit a person made.
  Future<ReviewDraft> editReviewDraft(
    String id, {
    required List<Finding> findings,
    required String summary,
  }) async {
    final response = await _send(
      Request(
        method: 'PUT',
        path: '/api/automations/review/drafts/${Uri.encodeComponent(id)}',
        body: {'findings': findings, 'summary': summary},
      ),
    );
    return ReviewDraft.fromJson(_object(response));
  }

  /// POST /api/automations/review/drafts/{id}/post: posts the review.
  Future<ReviewDraft> postReviewDraft(String id) async {
    final response = await _send(
      Request(
        method: 'POST',
        path: '/api/automations/review/drafts/${Uri.encodeComponent(id)}/post',
      ),
    );
    return ReviewDraft.fromJson(_object(response));
  }

  /// POST /api/automations/review/drafts/{id}/discard.
  Future<void> discardReviewDraft(String id) => _post(
    '/api/automations/review/drafts/${Uri.encodeComponent(id)}/discard',
    null,
  );

  /// POST /api/automations/review/drafts/{id}/send-to-author: hands the
  /// findings to the agent that owns the pull request.
  Future<void> sendDraftToAuthor(
    String id, {
    List<String> findingIds = const [],
  }) => _post(
    '/api/automations/review/drafts/${Uri.encodeComponent(id)}'
    '/send-to-author',
    {'findingIds': findingIds},
  );

  /// POST /api/automations/review/run: reviews a pull request now.
  Future<void> runReviewBot({required String repoID, required int number}) =>
      _post('/api/automations/review/run', {
        'repoId': repoID,
        'number': number,
      });

  /// GET /api/automations/ci/history: the fixer's runs, newest first.
  Future<List<CIHistory>> ciHistory({String? project}) => _listOf(
    '/api/automations/ci/history',
    CIHistory.fromJson,
    query: {if (project != null) 'project': project},
  );

  /// GET /api/automations/ci/history/{id}.
  Future<CIHistory> ciHistoryEntry(String id) => _objectOf(
    '/api/automations/ci/history/${Uri.encodeComponent(id)}',
    CIHistory.fromJson,
  );

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
    T Function(Map<String, dynamic> json) fromJson, {
    Map<String, String> query = const {},
  }) async => fromJson(_object(await _send(Request(path: path, query: query))));

  Future<List<T>> _listOf<T>(
    String path,
    T Function(Map<String, dynamic> json) fromJson, {
    Map<String, String> query = const {},
  }) async {
    final response = await _send(Request(path: path, query: query));
    final values = _decode(response) as List<dynamic>;
    return [
      for (final value in values) fromJson(value as Map<String, dynamic>),
    ];
  }

  Future<void> _post(String path, Object? body) async {
    await _send(Request(method: 'POST', path: path, body: body));
  }
}
