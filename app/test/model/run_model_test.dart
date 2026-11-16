import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/run_model.dart';
import 'package:marshal_app/src/ui/factory/run_screen.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// One digest task: [stages] are name/state pairs in the order they ran.
RunTask _task(
  int n, {
  String title = 'Read the item',
  String status = 'active',
  List<int> dependsOn = const [],
  List<(String, String)> stages = const [('implement', 'done')],
  int fixRounds = 0,
  String? commit,
}) => RunTask(
  n: n,
  title: title,
  dependsOn: dependsOn,
  status: status,
  startedAt: 1763352000000,
  endedAt: 1763352060000,
  execType: '',
  commit: commit == null ? null : RunCommit(base: 'base-1', head: commit),
  fixRounds: fixRounds,
  stages: [
    for (final (name, state) in stages) RunStage(name: name, state: state),
  ],
);

/// A digest over [tasks], active and never finished.
SDDRun _run(List<RunTask> tasks) => SDDRun(
  active: true,
  planName: 'F6 plan 2',
  planPath: '',
  branch: 'sdd/run-1',
  totalTasks: tasks.length,
  doneTasks: 0,
  currentTask: 1,
  phase: 'verify',
  detail: 'go test ./... is running',
  fixRound: 0,
  maxFixRounds: 3,
  tokensUsed: 0,
  tokensMax: 0,
  finished: false,
  succeeded: false,
  baseRef: 'main',
  startedAt: 1763351900000,
  endedAt: 0,
  phaseStartedAt: 0,
  error: '',
  tasks: tasks,
);

void main() {
  test('cells follow task state', () {
    final model = RunModel(
      agentID: 'a-1',
      run: _run([
        _task(
          1,
          stages: const [
            ('implement', 'done'),
            ('verify', 'failed'),
            ('review', 'active'),
          ],
          fixRounds: 2,
          commit: 'a1b2c3d4e5',
        ),
      ]),
    );
    addTearDown(model.dispose);

    final row = model.tasks.single;
    expect(row.cell(RunLane.implement).state, CellState.passed);
    expect(row.cell(RunLane.verify).state, CellState.failed);
    expect(row.cell(RunLane.review).state, CellState.running);
    // A stage the digest did not name is pending, never passed.
    expect(row.cell(RunLane.commit).state, CellState.pending);
    // The review lane carries the repair rounds; the commit lane the short sha.
    expect(row.cell(RunLane.review).fixRounds, 2);
    expect(row.cell(RunLane.commit).commit, 'a1b2c3d');
  });

  test('graph layers follow depends on', () {
    final model = RunModel(
      agentID: 'a-1',
      run: _run([
        _task(1),
        _task(2, dependsOn: const [1]),
        _task(3, dependsOn: const [1]),
        _task(4, dependsOn: const [2, 3]),
      ]),
    );
    addTearDown(model.dispose);

    final graph = model.graph;
    Map<int, int> layers = {
      for (final node in graph.nodes) node.number: node.layer,
    };
    expect(layers, {1: 0, 2: 1, 3: 1, 4: 2});
    expect(graph.columns, 3);
    // One edge per declared dependency, and none invented.
    expect(graph.edges.map((edge) => '${edge.from}->${edge.to}'), [
      '1->2',
      '1->3',
      '2->4',
      '3->4',
    ]);
    // Two tasks share the middle layer, so they sit in two rows.
    expect(graph.rows, 2);
  });

  test('a cycle-free plan without depends-on is a chain', () {
    final model = RunModel(
      agentID: 'a-1',
      run: _run([_task(1), _task(2), _task(3)]),
    );
    addTearDown(model.dispose);

    final graph = model.graph;
    expect(
      {for (final node in graph.nodes) node.number: node.layer},
      {1: 0, 2: 1, 3: 2},
    );
    expect(graph.columns, 3);
    expect(graph.rows, 1);
    expect(graph.edges.map((edge) => '${edge.from}->${edge.to}'), [
      '1->2',
      '2->3',
    ]);
  });

  test('selecting a cell opens the role\'s transcript', () async {
    final opened = <String>[];
    final model = RunModel(agentID: 'a-1', run: _run([_task(1), _task(2)]));
    addTearDown(model.dispose);

    // The model names the task a selected cell belongs to, which is the
    // conversation the screen opens.
    model.select(RunLane.verify, 2);
    expect(model.selectedCell, 'Verify/2');
    expect(model.selectedTask?.number, 2);

    // Selecting the same cell again closes it.
    model.select(RunLane.verify, 2);
    expect(model.selectedCell, isNull);
    expect(opened, isEmpty);
  });

  test('answer posts', () async {
    final posted = <String>[];
    final model = RunModel(
      agentID: 'a-1',
      run: _run([_task(1)]),
      answer: (answer) async => posted.add(answer),
    );
    addTearDown(model.dispose);

    await model.answer('Regenerate them in the same commit');

    expect(posted, ['Regenerate them in the same commit']);
    expect(model.failure, isNull);
  });

  test('a refused answer says why', () async {
    final model = RunModel(
      agentID: 'a-1',
      run: _run([_task(1)]),
      answer: (answer) async => throw StateError('the bridge refused it'),
    );
    addTearDown(model.dispose);

    await model.answer('Anything');

    expect(model.failure, contains('the bridge refused it'));
  });

  testWidgets('tapping a lane cell asks the host for that role', (
    tester,
  ) async {
    final opened = <String>[];
    final model = RunModel(agentID: 'a-1', run: _run([_task(1)]));
    addTearDown(model.dispose);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: RunScreen(
          model: model,
          now: DateTime.utc(2026, 11, 17, 4, 12),
          onOpenCell: (lane, task) => opened.add('$lane/$task'),
        ),
      ),
    );
    await tester.pump();

    await tester.tap(find.text('Passed').first);
    await tester.pump();

    expect(opened, ['Implement/1']);
    expect(model.selectedCell, 'Implement/1');
  });
}
