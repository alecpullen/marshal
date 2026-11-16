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
