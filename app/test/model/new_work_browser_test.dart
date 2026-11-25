@TestOn('browser')
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/new_work_model.dart';

/// Sending a new agent runs in a browser, and the browser's arithmetic is not
/// the VM's: under dart2js `1 << 32` is 0, so a bound written that way is a
/// range `Random.nextInt` refuses and the request is never built. Every VM
/// test passes anyway, which is how Send came to do nothing in the Studio
/// while the suite was green.
///
/// The test runs on Chrome for exactly that reason: it pins the send path where
/// the arithmetic that broke it lives.
void main() {
  const project = ProjectStatus(
    root: '/home/u/marshal',
    available: true,
    trust: TrustState.trustTrusted,
    isolation: 'available',
  );

  test('Send reaches the spawn route with a request id', () async {
    final spawns = <SpawnRequest>[];
    final model = NewWorkModel(
      defaults: NewWorkDefaults.fromProject(project),
      recentPrompts: () async => const RecentPrompts(prompts: []),
      issues: (root) async => const [],
      recipes: () async => const [],
      spawn: (request) async {
        spawns.add(request);
        return const SpawnResult(agentID: 'a-9');
      },
    );
    addTearDown(model.dispose);

    model.prefill('Fix the login redirect');
    await model.start();

    expect(model.failure, isNull);
    expect(spawns, hasLength(1));
    expect(spawns.single.requestID, isNotEmpty);
    expect(spawns.single.prompt, 'Fix the login redirect');
    // The ID a retry of the same draft carries: minting it is what threw.
    expect(model.started?.agentID, 'a-9');
  });
}
