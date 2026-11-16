import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/new_work_model.dart';

/// A transport that answers scripted routes and records what was asked, so a
/// test pins the routes the model calls without a socket.
class _RecordingTransport implements Transport {
  _RecordingTransport(this.answers);

  /// One scripted answer per path; an unscripted path is a 404.
  final Map<String, Response> answers;
  final paths = <String>[];

  @override
  Future<Response> send(Request request) async {
    paths.add(request.path);
    return answers[request.path] ??
        const Response(status: 404, body: '{"error":"no such route"}');
  }

  @override
  Stream<String> lines(Request request) =>
      throw StateError('the new-work model opens no stream');
}

const _project = ProjectStatus(
  root: '/home/u/marshal',
  available: true,
  trust: TrustState.trustTrusted,
  isolation: 'available',
);

const _recent = '/api/prompts/recent';
const _recipes = '/api/recipes';
const _issues = '/api/repos/owner%2Fname/issues';

/// A model whose four routes are recorded, so a test sees what was asked.
class _Routes {
  final spawns = <SpawnRequest>[];
  List<RepoIssue> issues = const [
    RepoIssue(
      number: 42,
      title: 'Fix the login redirect',
      body: 'The return path is lost.',
      url: 'https://github.com/owner/name/issues/42',
    ),
  ];
  List<String> recent = const ['Clean up the fixtures'];
  List<Recipe> recipes = const [
    Recipe(
      name: 'fix-a-bug',
      title: 'Fix a bug',
      kind: 'prompt',
      prompt: 'Find the bug, fix it, and add a test.',
    ),
  ];
  bool fail = false;

  Future<RecentPrompts> recentPrompts() async => RecentPrompts(prompts: recent);

  Future<List<RepoIssue>> issueList(String project) async => issues;

  Future<List<Recipe>> recipeList() async => recipes;

  Future<SpawnResult> spawn(SpawnRequest request) async {
    if (fail) throw Exception('refused');
    spawns.add(request);
    return const SpawnResult(agentID: 'a-9');
  }

  NewWorkModel model() => NewWorkModel(
    defaults: NewWorkDefaults.fromProject(
      const ProjectStatus(
        root: '/home/u/marshal',
        available: true,
        trust: TrustState.trustTrusted,
        isolation: 'available',
      ),
    ),
    recentPrompts: recentPrompts,
    issues: issueList,
    recipes: recipeList,
    spawn: spawn,
  );
}

void main() {
  test('the defaults come from the project', () {
    final model = _Routes().model();

    expect(model.choices['Project'], '/home/u/marshal');
    expect(model.choices['Base branch'], 'main');
    expect(model.choices['Permission mode'], 'Act');
    expect(model.choices['Workspace'], 'Isolated worktree');
    expect(model.kind, NewWorkKind.agent);
    expect(model.canStart, isFalse, reason: 'nothing to start yet');
    model.dispose();
  });

  test('the three lists load', () async {
    final model = _Routes().model();
    await model.load();

    expect(model.issues.single.title, 'Fix the login redirect');
    expect(model.recentPrompts.single, 'Clean up the fixtures');
    expect(model.recipes.single.name, 'fix-a-bug');
    expect(model.recipes.single.title, 'Fix a bug');
    model.dispose();
  });

  test('an issue fills the prompt', () async {
    final model = _Routes().model();
    await model.load();

    model.prefill(model.issues.single.title);

    expect(model.prompt, 'Fix the login redirect');
    expect(model.canStart, isTrue);
    model.dispose();
  });

  test('start posts the request and keeps the new thread', () async {
    final routes = _Routes();
    final model = routes.model();
    model.prefill('Fix the login redirect');

    await model.start();

    final request = routes.spawns.single;
    expect(request.project, '/home/u/marshal');
    expect(request.prompt, 'Fix the login redirect');
    expect(request.mode, 'act');
    expect(request.baseRef, 'main');
    expect(model.started?.agentID, 'a-9');
    model.dispose();
  });

  test('a planned kind is refused and cannot start', () {
    final model = _Routes().model();
    model.prefill('Do the work');

    model.selectKind(NewWorkKind.workItem);

    expect(model.kind, NewWorkKind.agent, reason: 'F6 brings the work item');
    expect(model.canStart, isTrue);
    model.dispose();
  });

  test('a refused spawn says why and starts nothing', () async {
    final routes = _Routes()..fail = true;
    final model = routes.model();
    model.prefill('Do the work');

    await model.start();

    expect(model.started, isNull);
    expect(model.failure, contains('refused'));
    model.dispose();
  });

  test('forProject asks the routes that serve the lists', () async {
    final transport = _RecordingTransport({
      _recent: const Response(
        status: 200,
        body: '{"prompts":["Clean up the fixtures"]}',
      ),
      _recipes: const Response(
        status: 200,
        body:
            '[{"name":"fix-a-bug","title":"Fix a bug","kind":"prompt",'
            '"prompt":"Find the bug and add a test."}]',
      ),
      _issues: const Response(
        status: 200,
        body:
            '[{"Number":42,"Title":"Fix the login redirect",'
            '"Body":"The return path is lost.",'
            '"URL":"https://github.com/owner/name/issues/42"}]',
      ),
    });
    final model = NewWorkModel.forProject(
      _project,
      BridgeClient(transport),
      repoId: 'owner/name',
    );

    await model.load();

    expect(
      transport.paths,
      containsAll([_recent, _recipes, _issues]),
      reason: 'the lists come from the routes the bridge serves',
    );
    expect(model.recentPrompts.single, 'Clean up the fixtures');
    expect(model.recipes.single.name, 'fix-a-bug');
    expect(model.recipes.single.prompt, contains('add a test'));
    expect(model.issues.single.number, 42);
    expect(model.issues.single.title, 'Fix the login redirect');
    expect(model.issuesFromRepo, isTrue);
    expect(model.failure, isNull);
    model.dispose();
  });

  test('a project with no registered repo asks for no issues', () async {
    final transport = _RecordingTransport({
      _recent: const Response(status: 200, body: '{"prompts":[]}'),
      _recipes: const Response(status: 200, body: '[]'),
    });
    final model = NewWorkModel.forProject(_project, BridgeClient(transport));

    await model.load();

    expect(
      transport.paths,
      isNot(contains(startsWith('/api/repos/'))),
      reason:
          'an issue belongs to a repo, and no route names this '
          'project\'s repo',
    );
    expect(model.issues, isEmpty);
    expect(
      model.issuesFromRepo,
      isFalse,
      reason: 'an empty list here is "no repo named", not "no issues"',
    );
    expect(model.failure, isNull, reason: 'the other lists still load');
    model.dispose();
  });

  test(
    'a route that refuses leaves an empty list, not a failed dialog',
    () async {
      final transport = _RecordingTransport({
        _recent: const Response(
          status: 200,
          body: '{"prompts":["Clean up the fixtures"]}',
        ),
        _recipes: const Response(
          status: 500,
          body: '{"error":"the recipe store could not be read"}',
        ),
      });
      final model = NewWorkModel.forProject(_project, BridgeClient(transport));

      await model.load();

      expect(model.recipes, isEmpty, reason: 'the fallback is an empty list');
      expect(model.recentPrompts.single, 'Clean up the fixtures');
      expect(
        model.failure,
        isNull,
        reason: 'one refused list does not fail the dialog',
      );
      model.dispose();
    },
  );
}
