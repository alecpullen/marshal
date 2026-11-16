import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/new_work_model.dart';

/// A model whose four routes are recorded, so a test sees what was asked.
class _Routes {
  final spawns = <SpawnRequest>[];
  List<NewWorkIssue> issues = const [
    NewWorkIssue('42', 'Fix the login redirect'),
  ];
  List<String> recent = const ['Clean up the fixtures'];
  List<NewWorkRecipe> recipes = const [
    NewWorkRecipe('Fix a bug', 'Find the bug, fix it, and add a test.'),
  ];
  bool fail = false;

  Future<RecentPrompts> recentPrompts() async => RecentPrompts(prompts: recent);

  Future<List<NewWorkIssue>> issueList(String project) async => issues;

  Future<List<NewWorkRecipe>> recipeList() async => recipes;

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
    expect(model.recipes.single.name, 'Fix a bug');
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
}
