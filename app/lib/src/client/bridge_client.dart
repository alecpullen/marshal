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

  /// GET /api/usage?range=&by=: what the fleet spent over a window, grouped by
  /// day, project, role or model.
  Future<UsageReport> usage({String range = '7d', String by = 'project'}) =>
      _objectOf(
        '/api/usage',
        UsageReport.fromJson,
        query: {'range': range, 'by': by},
      );

  /// GET /api/audit?limit=: the audit log, newest first.
  Future<List<AuditEvent>> audit({int limit = 50}) => _listOf(
    '/api/audit',
    AuditEvent.fromJson,
    query: {'limit': '$limit'},
  );

  /// GET /api/disk: the bytes each part of the state directory takes.
  Future<DiskStatus> disk() => _objectOf('/api/disk', DiskStatus.fromJson);

  /// POST /api/prune: reclaims unreferenced mirrors and orphaned work
  /// directories, and answers what it freed.
  Future<PruneResult> prune() =>
      _objectOf('/api/prune', PruneResult.fromJson, method: 'POST');

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

  /// GET /api/repos: every registered repository, with the forge and the
  /// credential it is reached with.
  Future<List<Repo>> repos() => _listOf('/api/repos', Repo.fromJson);

  /// POST /api/repos: registers one, or replaces the one with its id.
  Future<Repo> registerRepo(Repo repo) =>
      _objectOf('/api/repos', Repo.fromJson, method: 'POST', body: repo);

  /// DELETE /api/repos/{id}.
  Future<void> removeRepo(String id) => _send(
    Request(method: 'DELETE', path: '/api/repos/${Uri.encodeComponent(id)}'),
  );

  /// GET /api/credentials: every forge credential. It carries no value, only
  /// whether the secret it names exists.
  Future<List<Credential>> credentials() =>
      _listOf('/api/credentials', Credential.fromJson);

  /// POST /api/credentials: stores one and answers what the bridge kept.
  Future<Credential> putCredential(CredentialInput input) => _objectOf(
    '/api/credentials',
    Credential.fromJson,
    method: 'POST',
    body: input,
  );

  /// DELETE /api/credentials/{id}: refused while a repo still names it.
  Future<void> deleteCredential(String id) => _send(
    Request(
      method: 'DELETE',
      path: '/api/credentials/${Uri.encodeComponent(id)}',
    ),
  );

  /// GET /api/models: the user-global config's providers, presets, roles and
  /// caps. Providers and presets are user-global only.
  Future<ModelsConfig> models() =>
      _objectOf('/api/models', ModelsConfig.fromJson);

  /// PUT /api/models/providers: sends the named providers; a null one is
  /// removed.
  Future<void> setProviders(ProvidersUpdate update) =>
      _put('/api/models/providers', update);

  /// PUT /api/models/providers/{name}/key: stores a provider's key. Nothing
  /// on any route answers it back.
  Future<void> setProviderKey(String name, String key) => _put(
    '/api/models/providers/${Uri.encodeComponent(name)}/key',
    ProviderKey(key: key),
  );

  /// PUT /api/models/presets: sends the named presets; a null one is removed.
  Future<void> setPresets(PresetsUpdate update) =>
      _put('/api/models/presets', update);

  /// PUT /api/models/routing: the profiles, the default profile and the active
  /// preset, in the user's global config.
  Future<void> setRouting(RoutingUpdate update) =>
      _put('/api/models/routing', update);

  /// POST /api/models/probe: lists the models a provider answers with. A probe
  /// that fails is its answer, not a refusal.
  Future<ProbeResult> probeProvider(String name) => _objectOf(
    '/api/models/probe',
    ProbeResult.fromJson,
    method: 'POST',
    body: ProbeRequest(name: name),
  );

  /// PUT /api/budgets: writes the caps to the user's global config and
  /// answers the report the engine stored, which may clamp what was sent.
  Future<BudgetReport> saveBudgets(Budgets budgets) => _objectOf(
    '/api/budgets',
    BudgetReport.fromJson,
    method: 'PUT',
    body: BudgetsUpdate(budgets: budgets),
  );

  /// GET /api/secrets/status: which backend holds the secrets and whether it
  /// answers.
  Future<SecretsStatus> secretsStatus() =>
      _objectOf('/api/secrets/status', SecretsStatus.fromJson);

  /// GET /api/secrets: references only, each as `vault:<path>`. A value has no
  /// route out.
  Future<List<String>> secrets({String? prefix}) async {
    final response = await _send(
      Request(path: '/api/secrets', query: {'prefix': ?prefix}),
    );
    final list = SecretList.fromJson(_object(response));
    return list.refs;
  }

  /// PUT /api/secrets/{ref}: writes a value. It goes one way.
  Future<void> putSecret(String ref, String value) => _send(
    Request(
      method: 'PUT',
      path: _secretPath(ref),
      body: SecretWrite(value: value),
    ),
  );

  /// DELETE /api/secrets/{ref}.
  Future<void> deleteSecret(String ref) =>
      _send(Request(method: 'DELETE', path: _secretPath(ref)));

  /// The `vault:` prefix stripped and every slash kept, so a nested path is
  /// addressed as the bridge's own route does.
  String _secretPath(String ref) {
    final bare = ref.startsWith('vault:') ? ref.substring(6) : ref;
    return '/api/secrets/${bare.split('/').map(Uri.encodeComponent).join('/')}';
  }

  /// GET /api/network?view=hosts: the egress hosts with the rule that covers
  /// each, for one workspace, one agent, or all of them.
  Future<NetHosts> networkHosts({String? workspace, String? agent}) =>
      _objectOf(
        '/api/network',
        NetHosts.fromJson,
        query: {
          'view': 'hosts',
          if (workspace != null && workspace.isNotEmpty) 'workspace': workspace,
          if (agent != null && agent.isNotEmpty) 'agent': agent,
        },
      );

  /// GET /api/network?view=requests: the connection records themselves.
  Future<List<NetRecord>> networkRequests({String? workspace, String? agent}) =>
      _listOf(
        '/api/network',
        NetRecord.fromJson,
        query: {
          'view': 'requests',
          if (workspace != null && workspace.isNotEmpty) 'workspace': workspace,
          if (agent != null && agent.isNotEmpty) 'agent': agent,
        },
      );

  /// GET /api/network?view=agents: one row per agent that reached anything.
  Future<List<NetAgentRow>> networkAgents({String? workspace}) => _listOf(
    '/api/network',
    NetAgentRow.fromJson,
    query: {
      'view': 'agents',
      if (workspace != null && workspace.isNotEmpty) 'workspace': workspace,
    },
  );

  /// GET /api/network/pending: the blocked requests still awaiting a decision,
  /// oldest first.
  Future<List<NetPending>> networkPending({String? agent}) async {
    final response = await _send(
      Request(
        path: '/api/network/pending',
        query: {if (agent != null && agent.isNotEmpty) 'agent': agent},
      ),
    );
    return NetPendingList.fromJson(_object(response)).pending;
  }

  /// POST /api/network/decisions: answers one blocked request.
  Future<NetDecisionResult> networkDecision(NetDecision decision) => _objectOf(
    '/api/network/decisions',
    NetDecisionResult.fromJson,
    method: 'POST',
    body: decision,
  );

  /// GET /api/workspaces: the gallery of environment templates, with their
  /// versions and how many agents run on each.
  Future<List<EnvListItem>> environments() =>
      _listOf('/api/workspaces', EnvListItem.fromJson);

  /// GET /api/workspaces/{name}: one template's source, its parse and its
  /// record. Without [version] the draft is read when there is one.
  Future<EnvLoaded> environment(String name, {int? version}) => _objectOf(
    '/api/workspaces/${Uri.encodeComponent(name)}',
    EnvLoaded.fromJson,
    query: {if (version != null) 'version': '$version'},
  );

  /// POST /api/workspaces: starts a template from [from], which is `blank`,
  /// `starter:<name>`, `devcontainer:<path>` or `snapshot:<agent>`.
  Future<EnvMeta> createEnvironment(String name, String from) => _objectOf(
    '/api/workspaces',
    EnvMeta.fromJson,
    method: 'POST',
    body: EnvCreate(name: name, from: from),
  );

  /// PUT /api/workspaces/{name}/draft: writes the source and answers its
  /// parse.
  Future<EnvLoaded> saveEnvironmentDraft(String name, String source) =>
      _objectOf(
        '/api/workspaces/${Uri.encodeComponent(name)}/draft',
        EnvLoaded.fromJson,
        method: 'PUT',
        body: EnvDraft(source: source),
      );

  /// POST /api/workspaces/{name}/publish: publishes the draft and answers the
  /// version it made.
  Future<EnvVersion> publishEnvironment(String name) => _objectOf(
    '/api/workspaces/${Uri.encodeComponent(name)}/publish',
    EnvVersion.fromJson,
    method: 'POST',
    body: const <String, Object?>{},
  );

  /// DELETE /api/workspaces/{name}.
  Future<void> deleteEnvironment(String name) => _send(
    Request(
      method: 'DELETE',
      path: '/api/workspaces/${Uri.encodeComponent(name)}',
    ),
  );

  /// GET /api/workspaces/{name}/builds: the versions, the warm pool and the
  /// median start times.
  Future<EnvBuilds> environmentBuilds(String name) => _objectOf(
    '/api/workspaces/${Uri.encodeComponent(name)}/builds',
    EnvBuilds.fromJson,
  );

  /// POST /api/workspaces/{name}/builds: starts a build and answers which
  /// version it is building.
  Future<EnvBuildStart> startEnvironmentBuild(String name, {int? version}) =>
      _objectOf(
        '/api/workspaces/${Uri.encodeComponent(name)}/builds',
        EnvBuildStart.fromJson,
        method: 'POST',
        body: {'version': ?version},
      );

  /// GET …/builds/{n}/events: the build's log, from its first line. The
  /// caller starts it.
  EventStream environmentBuildLog(String name, int n) => EventStream(
    _transport,
    '/api/workspaces/${Uri.encodeComponent(name)}/builds/$n/events',
  );

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
    return (json['url'] ?? json['previewUrl'] ?? '') as String;
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

  Future<void> _put(
    String path,
    Object? body, {
    Map<String, String> query = const {},
  }) async {
    await _send(Request(method: 'PUT', path: path, query: query, body: body));
  }
}
