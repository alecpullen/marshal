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
        query: {'project': ?project, 'status': ?status},
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
    query: {'project': ?project},
  );

  /// GET /api/automations/ci/history/{id}.
  Future<CIHistory> ciHistoryEntry(String id) => _objectOf(
    '/api/automations/ci/history/${Uri.encodeComponent(id)}',
    CIHistory.fromJson,
  );

  // Library: skills, plugins and memory (F7).

  /// GET /api/library/skills: the installed skills of [scope].
  Future<List<SkillEntry>> skills({
    required String scope,
    String? project,
  }) async {
    final body = await _objectOf(
      '/api/library/skills',
      SkillList.fromJson,
      query: {'scope': scope, 'project': ?project},
    );
    return body.skills;
  }

  /// POST /api/library/skills/preview: stages a skill so a person can see
  /// what it is before it is installed.
  Future<SkillPreview> previewSkill(SkillStage stage) async {
    final response = await _send(
      Request(method: 'POST', path: '/api/library/skills/preview', body: stage),
    );
    return SkillPreview.fromJson(_object(response));
  }

  /// POST /api/library/skills/confirm: installs the staged skill.
  Future<void> confirmSkill(SkillConfirm body) =>
      _post('/api/library/skills/confirm', body);

  /// POST /api/library/skills/discard: drops a staged skill.
  Future<void> discardSkill(String stagingToken) => _post(
    '/api/library/skills/discard',
    SkillDiscard(stagingToken: stagingToken),
  );

  /// DELETE /api/library/skills/{name}?scope=…: removes an installed skill.
  Future<void> removeSkill(
    String name, {
    required String scope,
    String? project,
  }) => _send(
    Request(
      method: 'DELETE',
      path: '/api/library/skills/${Uri.encodeComponent(name)}',
      query: {'scope': scope, 'project': ?project},
    ),
  );

  /// GET /api/library/plugins: the installed plugins of [scope].
  Future<List<PluginEntry>> plugins({
    required String scope,
    String? project,
  }) async {
    final body = await _objectOf(
      '/api/library/plugins',
      PluginList.fromJson,
      query: {'scope': scope, 'project': ?project},
    );
    return body.plugins;
  }

  /// POST /api/library/plugins/scan: scans a plugin before it is installed.
  Future<PluginScan> scanPlugin(PluginStage stage) async {
    final response = await _send(
      Request(method: 'POST', path: '/api/library/plugins/scan', body: stage),
    );
    return PluginScan.fromJson(_object(response));
  }

  /// POST /api/library/plugins/confirm: installs the scanned plugin.
  Future<void> confirmPlugin(PluginConfirm body) =>
      _post('/api/library/plugins/confirm', body);

  /// POST /api/library/plugins/discard: drops a scanned plugin.
  Future<void> discardPlugin(String scanToken) => _post(
    '/api/library/plugins/discard',
    PluginDiscard(scanToken: scanToken),
  );

  /// DELETE /api/library/plugins/{name}?scope=…: removes an installed plugin.
  Future<void> removePlugin(
    String name, {
    required String scope,
    String? project,
  }) => _send(
    Request(
      method: 'DELETE',
      path: '/api/library/plugins/${Uri.encodeComponent(name)}',
      query: {'scope': scope, 'project': ?project},
    ),
  );

  /// GET /api/library/memory?project=…: the project's memory.
  Future<List<MemoryEntry>> memory({
    required String project,
    String? scope,
  }) async {
    final body = await _objectOf(
      '/api/library/memory',
      MemoryList.fromJson,
      query: {'project': project, 'scope': ?scope},
    );
    return body.entries;
  }

  /// DELETE /api/library/memory/{id}?project=….
  Future<void> deleteMemory(int id, {required String project}) => _send(
    Request(
      method: 'DELETE',
      path: '/api/library/memory/$id',
      query: {'project': project},
    ),
  );

  /// GET /api/library/memory/suggestions?project=…: the memories another
  /// project holds too, and the scope each could be promoted to.
  Future<List<MemorySuggestion>> memorySuggestions({
    required String project,
  }) async {
    final body = await _objectOf(
      '/api/library/memory/suggestions',
      MemorySuggestions.fromJson,
      query: {'project': project},
    );
    return body.suggestions;
  }

  /// POST /api/library/memory/{id}/promote?project=…: widens a memory's
  /// scope. It is the person's decision, never automatic.
  Future<void> promoteMemory(
    int id,
    MemoryPromote promote, {
    required String project,
  }) => _post(
    '/api/library/memory/$id/promote',
    promote,
    query: {'project': project},
  );

  /// POST /api/library/memory/{id}/confidence?project=….
  Future<void> setMemoryConfidence(
    int id,
    MemoryConfidence confidence, {
    required String project,
  }) => _post(
    '/api/library/memory/$id/confidence',
    confidence,
    query: {'project': project},
  );

  // MCP clients and their factory grants (F7).

  /// GET /api/clients: every registered MCP client.
  Future<List<MCPClient>> clients() =>
      _listOf('/api/clients', MCPClient.fromJson);

  /// POST /api/clients: registers one. Its token is in the answer, once.
  Future<CreateClientResult> createClient(CreateClientRequest request) async {
    final response = await _send(
      Request(method: 'POST', path: '/api/clients', body: request),
    );
    return CreateClientResult.fromJson(_object(response));
  }

  /// DELETE /api/clients/{id}.
  Future<void> deleteClient(String id) => _send(
    Request(method: 'DELETE', path: '/api/clients/${Uri.encodeComponent(id)}'),
  );

  /// GET /api/clients/{clientId}/factory-grants/{projectId}: one pair's
  /// stored grant and the version a write must name.
  Future<FactoryGrantState> factoryGrant(String clientID, String projectID) =>
      _objectOf(
        '/api/clients/${Uri.encodeComponent(clientID)}/factory-grants/'
        '${Uri.encodeComponent(projectID)}',
        FactoryGrantState.fromJson,
      );

  /// PUT the whole grant plus the version it was edited from. A 409 carries
  /// the state that won, so the caller keeps the edit and offers a reload.
  Future<FactoryGrantState> putFactoryGrant(
    String clientID,
    String projectID,
    PutGrantRequest request,
  ) async {
    final response = await _send(
      Request(
        method: 'PUT',
        path:
            '/api/clients/${Uri.encodeComponent(clientID)}/factory-grants/'
            '${Uri.encodeComponent(projectID)}',
        body: request,
      ),
    );
    return FactoryGrantState.fromJson(_object(response));
  }

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

  Future<void> _post(
    String path,
    Object? body, {
    Map<String, String> query = const {},
  }) async {
    await _send(Request(method: 'POST', path: path, query: query, body: body));
  }
}
