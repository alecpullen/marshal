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

  /// GET /api/recipes: every recipe the bridge stores, its built-ins
  /// included, for the new-work dialog's Recipes list.
  Future<List<Recipe>> recipes() => _listOf('/api/recipes', Recipe.fromJson);

  /// GET /api/repos/{id}/issues: one registered repo's open issues, from the
  /// forge its registration names. [repoId] is the id `GET /api/repos`
  /// reports — not a project root: a project is a local checkout and an issue
  /// belongs to the remote repo.
  Future<List<RepoIssue>> issues(String repoId) => _listOf(
    '/api/repos/${Uri.encodeComponent(repoId)}/issues',
    RepoIssue.fromJson,
  );

  /// GET /api/events: the fleet stream, which the caller starts.
  EventStream fleetEvents() => EventStream(_transport, '/api/events');

  /// GET /api/sessions/{id}/stack: one transcript's tree at its current
  /// revision. [subagentId] names a child transcript; 0 is the parent's.
  Future<StackSnapshot> stack(String sessionId, {int subagentId = 0}) =>
      _objectOf(
        '/api/sessions/${Uri.encodeComponent(sessionId)}/stack',
        StackSnapshot.fromJson,
        query: {if (subagentId != 0) 'subagent': '$subagentId'},
      );

  /// GET /api/events?sessionId=: one session's stream, whose `session/update`
  /// notifications carry its stack patches. The caller starts it.
  EventStream sessionEvents(String sessionId) =>
      EventStream(_transport, '/api/events', query: {'sessionId': sessionId});

  /// POST /api/sessions/{id}/prompt: starts a turn.
  Future<void> prompt(String sessionId, PromptRequest request) =>
      _post('/api/sessions/${Uri.encodeComponent(sessionId)}/prompt', request);

  /// POST /api/sessions/{id}/steer: redirects the turn already running.
  Future<void> steer(String sessionId, SteerRequest request) =>
      _post('/api/sessions/${Uri.encodeComponent(sessionId)}/steer', request);

  /// POST /api/sessions/{id}/cancel: stops the turn.
  Future<void> cancel(String sessionId) =>
      _post('/api/sessions/${Uri.encodeComponent(sessionId)}/cancel', null);

  /// POST /api/sessions/{id}/mode: switches the permission mode.
  Future<void> setMode(String sessionId, ModeRequest request) =>
      _post('/api/sessions/${Uri.encodeComponent(sessionId)}/mode', request);

  /// GET /api/sessions/{id}/access: the session's committed access tuple.
  /// A mode without editing is a session the user can only read.
  Future<AccessState> access(String sessionId) => _objectOf(
    '/api/sessions/${Uri.encodeComponent(sessionId)}/access',
    AccessState.fromJson,
  );

  /// POST /api/agents: starts an agent, which is a new thread.
  Future<SpawnResult> spawn(SpawnRequest request) async {
    final response = await _send(
      Request(method: 'POST', path: '/api/agents', body: request),
    );
    return SpawnResult.fromJson(_object(response));
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
