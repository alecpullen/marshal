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

  /// GET /api/agents/{id}/diff: what the agent changed, and its unified diff.
  Future<DiffResult> diff(String agentId) => _objectOf(
    '/api/agents/${Uri.encodeComponent(agentId)}/diff',
    DiffResult.fromJson,
  );

  /// GET /api/agents/{id}/gate: the stored verify record, or null when the
  /// gate has not run (the bridge answers 204 with no body).
  Future<GateRecord?> gate(String agentId) async {
    final response = await _send(
      Request(path: '/api/agents/${Uri.encodeComponent(agentId)}/gate'),
    );
    if (response.body.isEmpty) return null;
    return GateRecord.fromJson(_object(response));
  }

  /// POST /api/agents/{id}/verify: runs the gate now and returns its record.
  Future<GateRecord> verify(String agentId) => _objectOf(
    '/api/agents/${Uri.encodeComponent(agentId)}/verify',
    GateRecord.fromJson,
    method: 'POST',
  );

  /// GET /api/agents/{id}/files?path=: one directory of the agent's
  /// workspace, directories first. The empty path is the workspace root.
  Future<FileList> files(String agentId, String path) => _objectOf(
    '/api/agents/${Uri.encodeComponent(agentId)}/files',
    FileList.fromJson,
    query: {'path': path},
  );

  /// GET /api/agents/{id}/file?path=: one regular file's content, capped by
  /// the agent.
  Future<FileContent> file(String agentId, String path) => _objectOf(
    '/api/agents/${Uri.encodeComponent(agentId)}/file',
    FileContent.fromJson,
    query: {'path': path},
  );

  /// GET /api/agents/{id}/commit-draft: the message the agent would commit.
  Future<CommitDraft> commitDraft(String agentId) => _objectOf(
    '/api/agents/${Uri.encodeComponent(agentId)}/commit-draft',
    CommitDraft.fromJson,
  );

  /// GET /api/sessions/{id}/step-diffs: the session's steps, each with its
  /// part of the diff.
  Future<StepDiffs> stepDiffs(String sessionId) => _objectOf(
    '/api/sessions/${Uri.encodeComponent(sessionId)}/step-diffs',
    StepDiffs.fromJson,
  );

  /// GET /api/sessions/{id}/nodes/{nodeId}: one node's wire form and the
  /// detail the projection left out.
  Future<NodeDetailResponse> node(
    String sessionId,
    String nodeId, [
    int subagentId = 0,
  ]) => _objectOf(
    '/api/sessions/${Uri.encodeComponent(sessionId)}/nodes/'
    '${Uri.encodeComponent(nodeId)}',
    NodeDetailResponse.fromJson,
    query: {if (subagentId != 0) 'subagent': '$subagentId'},
  );

  /// GET /api/sessions/{id}/last-request: the newest model request, or null
  /// when the session has made none.
  Future<LastRequest?> lastRequest(String sessionId) async {
    final response = await _send(
      Request(
        path: '/api/sessions/${Uri.encodeComponent(sessionId)}/last-request',
      ),
    );
    if (response.body.isEmpty) return null;
    return LastRequest.fromJson(_object(response));
  }

  /// GET /api/agents/{id}/review/comments: the change set's comments, oldest
  /// first.
  Future<List<ReviewComment>> reviewComments(String agentId) async {
    final response = await _send(
      Request(
        path: '/api/agents/${Uri.encodeComponent(agentId)}/review/comments',
      ),
    );
    final values = (_decode(response) as Map<String, dynamic>)['comments'];
    return [
      for (final value in (values as List<dynamic>? ?? const []))
        ReviewComment.fromJson(value as Map<String, dynamic>),
    ];
  }

  /// POST /api/agents/{id}/review/comments: leaves a comment on a line. The
  /// bridge delivers it to the agent as steering, and answers with the
  /// comment it stored.
  Future<ReviewComment> addReviewComment(
    String agentId,
    ReviewCommentRequest request,
  ) => _objectOf(
    '/api/agents/${Uri.encodeComponent(agentId)}/review/comments',
    ReviewComment.fromJson,
    method: 'POST',
    body: request,
  );

  /// POST …/review/comments/{cid}/resolve: closes a comment's thread.
  Future<void> resolveReviewComment(String agentId, String commentId) => _post(
    '/api/agents/${Uri.encodeComponent(agentId)}/review/comments/'
    '${Uri.encodeComponent(commentId)}/resolve',
    null,
  );

  /// POST /api/agents/{id}/preview/{port}: the URL the agent's app is served
  /// at, for a port its workspace declares under `[preview]`.
  Future<String> preview(String agentId, int port) async {
    final response = await _send(
      Request(
        method: 'POST',
        path: '/api/agents/${Uri.encodeComponent(agentId)}/preview/$port',
      ),
    );
    final json = _object(response);
    // The route answers {"url": …} (web/bridge/preview.go): nothing else is
    // sent, so a shape change fails here rather than yielding an empty URL
    // from a key nobody writes.
    return (json['url'] ?? '') as String;
  }

  /// POST /api/agents/{id}/exit: commits, verifies and ships the work.
  Future<ExitResult> exit(String agentId, ExitRequest request) => _objectOf(
    '/api/agents/${Uri.encodeComponent(agentId)}/exit',
    ExitResult.fromJson,
    method: 'POST',
    body: request,
  );

  /// POST /api/agents/{id}/discard: throws the agent's work away.
  Future<void> discard(String agentId) =>
      _post('/api/agents/${Uri.encodeComponent(agentId)}/discard', null);

  /// GET /api/agents/{id}/patch: the change set as a patch file's text.
  Future<String> patch(String agentId) async {
    final response = await _send(
      Request(path: '/api/agents/${Uri.encodeComponent(agentId)}/patch'),
    );
    return response.body;
  }

  /// POST /api/agents/{id}/terminal: opens a shell in the agent's workspace
  /// and answers its id. Opening one asks for the size it starts at.
  Future<TerminalOpened> openTerminal(
    String agentId, {
    required int cols,
    required int rows,
  }) => _objectOf(
    '/api/agents/${Uri.encodeComponent(agentId)}/terminal',
    TerminalOpened.fromJson,
    method: 'POST',
    body: TerminalResize(cols: cols, rows: rows),
  );

  /// GET …/terminal/{tid}/events: the shell's output stream. The caller
  /// starts it.
  EventStream terminalEvents(String agentId, String terminalId) => EventStream(
    _transport,
    '/api/agents/${Uri.encodeComponent(agentId)}/terminal/'
    '${Uri.encodeComponent(terminalId)}/events',
  );

  /// POST …/terminal/{tid}/input: keystrokes, base64 as the wire carries
  /// them.
  Future<void> terminalInput(String agentId, String terminalId, String data) =>
      _post(
        '/api/agents/${Uri.encodeComponent(agentId)}/terminal/'
        '${Uri.encodeComponent(terminalId)}/input',
        TerminalInput(data: data),
      );

  /// POST …/terminal/{tid}/resize: the shell's size in character cells.
  Future<void> terminalResize(
    String agentId,
    String terminalId,
    int cols,
    int rows,
  ) => _post(
    '/api/agents/${Uri.encodeComponent(agentId)}/terminal/'
    '${Uri.encodeComponent(terminalId)}/resize',
    TerminalResize(cols: cols, rows: rows),
  );

  /// POST …/terminal/{tid}/release: hands the agent back.
  Future<void> terminalRelease(String agentId, String terminalId) => _post(
    '/api/agents/${Uri.encodeComponent(agentId)}/terminal/'
    '${Uri.encodeComponent(terminalId)}/release',
    null,
  );

  /// DELETE …/terminal/{tid}: ends the shell.
  Future<void> closeTerminal(String agentId, String terminalId) async {
    await _send(
      Request(
        method: 'DELETE',
        path:
            '/api/agents/${Uri.encodeComponent(agentId)}/terminal/'
            '${Uri.encodeComponent(terminalId)}',
      ),
    );
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
    String method = 'GET',
    Object? body,
  }) async => fromJson(
    _object(
      await _send(
        Request(method: method, path: path, query: query, body: body),
      ),
    ),
  );

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
