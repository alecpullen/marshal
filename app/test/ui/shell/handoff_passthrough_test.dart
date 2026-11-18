import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/handoff_model.dart';
import 'package:marshal_app/src/model/planning_model.dart';
import 'package:marshal_app/src/model/thread_ref.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

/// A planner whose last capture froze a plan and a specification.
Future<PlanningModel> _planner() async {
  final model = PlanningModel(
    plannerID: 'planner-7',
    events: (after) async => const [],
    promptPlanner: (request) => throw UnimplementedError(),
    cancelPlanner: () async {},
    answerPlanner: (requestID, response) async {},
    capture: (request) async => PlannerSnapshotReply(
      id: 'snap-1',
      sha256: 'a1b2c3d4e5f6',
      baseCommit: 'c0ffee1',
      artifacts: const [
        PlannerSnapshotArtifact(
          kind: 'plan',
          content: '## Task 1\nFrozen plan.\n',
          sha256: 'p1',
        ),
        PlannerSnapshotArtifact(
          kind: 'spec',
          content: 'Frozen specification.',
          sha256: 's1',
        ),
      ],
      files: const [],
      createdAt: '2026-11-17T04:00:00Z',
      rejected: const [],
    ),
  );
  addTearDown(model.dispose);
  await model.snapshot();
  return model;
}

final _thread = ThreadRef(
  host: 'local',
  id: 'a-1',
  project: '/home/u/marshal',
  title: 'Planning',
  kind: ThreadKind.agent,
  status: ThreadStatus.idle,
  headline: '',
  lastActive: DateTime.utc(2026, 11, 17, 4),
);

void main() {
  // The one binding that makes a handoff review review the capture: without
  // it the sheet still opens, still creates a draft, and silently sends text
  // nobody captured. Only reading the arguments the place passes can catch
  // that, so this is the test for it (`_PlanningPlace._handOff`).
  test('the review is bound to the capture the person reviewed', () async {
    final planner = await _planner();
    final latest = planner.latest;
    expect(latest, isNotNull);

    (String, String, String)? seen;
    String? seenPlan;
    String? seenSpec;
    String? seenPlanner;
    PlannerSnapshot? seenSnapshot;
    final built = <HandoffModel>[];

    Future<HandoffModel> build(
      String host,
      String project,
      String title,
      String plan,
      String spec, {
      String? plannerID,
      PlannerSnapshot? snapshot,
    }) async {
      seen = (host, project, title);
      seenPlan = plan;
      seenSpec = spec;
      seenPlanner = plannerID;
      seenSnapshot = snapshot;
      final review = HandoffModel(
        defaults: const ProjectDefaults(repoID: 'repo-1'),
        projectRoot: project,
        title: title,
        plan: plan,
        spec: spec,
        reviewedSnapshot: snapshot,
        plannerID: plannerID,
        handoffSnapshot: (planner, snapshotID, request) =>
            throw UnimplementedError(),
        create: (draft) => throw UnimplementedError(),
      );
      built.add(review);
      return review;
    }

    final review = await reviewForPlanner(build, _thread, planner);
    for (final each in built) {
      addTearDown(each.dispose);
    }

    // The project the plan belongs to travels, and the capture is the same one
    // the model holds, not a copy or a null.
    expect(seen, (_thread.host, _thread.project, _thread.title));
    expect(seenPlan, '## Task 1\nFrozen plan.\n');
    expect(seenSpec, 'Frozen specification.');
    expect(seenPlanner, 'planner-7');
    expect(seenSnapshot, same(latest));
    // And the review the person sees is the bound one: it sends the capture's
    // bytes and names the planner the route needs.
    expect(review.boundToSnapshot, isTrue);
    expect(review.plannerID, 'planner-7');
    expect(review.editSnapshot?.sha256, 'a1b2c3d4e5f6');
    expect(review.editSnapshot?.baseCommit, 'c0ffee1');
  });
}
