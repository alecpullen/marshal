import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/client/event_stream.dart';
import 'package:marshal_app/src/client/host.dart';
import 'package:marshal_app/src/model/fleet_store.dart';
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/pulls_store.dart';

import '../test/support/fake_bridge.dart';
import '../test/support/sockets.dart';

/// F5's end-to-end flow, driven on a device where one exists (f5 plan 2,
/// task 5): an agent ships a pull request whose check fails, the app dispatches
/// a worker in one click, the fix is pushed, the check passes, and the card
/// reaches ready — without a refresh, and without ever showing the old head's
/// checks as current.
///
/// The fixture is the same in-process fake bridge the unit tests use, standing
/// in for the forge, so the flow needs no daemon, no keychain and no network.
/// The UI half of the same flow is `test/ui/pulls/fix_dispatch_test.dart` and
/// `pulls_place_test.dart`, which run everywhere; this is the same calls on a
/// real device.
void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('a failing check is fixed and the card reaches ready', (
    tester,
  ) async {
    HttpOverrides.global = null;
    final socket = await socketBridge('f5.sock');
    final bridge = socket.bridge;
    FakeReply json(Object body, [int status = 200]) => FakeReply(
      status,
      headers: const {'content-type': 'application/json'},
      body: jsonEncode(body),
    );

    const id = 'acme/widgets#131';
    contract.PullRecord record({
      required contract.PullState state,
      required List<contract.PullCheck> checks,
      String head = 'old-head',
      contract.PullFix? fix,
      int updatedAt = 1000,
    }) => contract.PullRecord(
      id: id,
      repoID: 'acme/widgets',
      projectRoot: '/home/u/marshal',
      number: 131,
      url: 'https://forge.example/acme/widgets/pull/131',
      title: 'Quiet capture mode',
      headRef: 'agent/quiet-capture',
      baseRef: 'main',
      headSHA: head,
      draft: false,
      additions: 412,
      deletions: 96,
      origin: contract.PullOrigin.pullOriginAgent,
      agentID: 'a-1',
      state: state,
      checks: checks,
      reviews: '2 reviews passed',
      fix: fix,
      observedAt: updatedAt,
      updatedAt: updatedAt,
    );

    final failing = record(
      state: contract.PullState.pullFailing,
      checks: const [
        contract.PullCheck(
          name: 'build',
          state: contract.CheckState.checkPassed,
        ),
        contract.PullCheck(
          name: 'unit',
          state: contract.CheckState.checkFailed,
        ),
      ],
    );
    final beingFixed = record(
      state: contract.PullState.pullBeingFixed,
      checks: failing.checks,
      fix: const contract.PullFix(
        id: 'f-1',
        check: 'unit',
        headSHA: 'old-head',
        mode: 'fix',
        startedAt: 2000,
      ),
      updatedAt: 2000,
    );

    bridge
      ..reply('GET', '/api/agents', json(const []))
      ..reply('GET', '/api/projects', json(const []))
      ..reply(
        'GET',
        '/api/inbox',
        json(
          const contract.Inbox(
            groups: contract.InboxGroups(
              needsYou: [],
              running: [],
              ready: [],
              earlier: [],
            ),
            queue: [],
          ).toJson(),
        ),
      )
      ..reply('GET', '/api/budgets', const FakeReply(404, body: '{}'))
      ..reply(
        'GET',
        '/api/pulls',
        json({
          'pulls': [failing.toJson()],
        }),
      )
      ..reply(
        'POST',
        '/api/pulls/${Uri.encodeComponent(id)}/fix',
        json(beingFixed.toJson(), 202),
      );
    final frames = bridge.stream('GET', '/api/events');

    final transport = socket.transport;
    final client = BridgeClient(transport);
    final registry = HostRegistry(
      discovery: () async => [
        HostSpec(name: 'local', kind: HostKind.local, client: client),
      ],
      fleetFor: (client) => EventStream(transport, '/api/events', jitter: 0),
    );
    await registry.discover();
    final store = FleetStore(registry);
    addTearDown(store.dispose);
    final pulls = PullsModel(
      PullsStore(registry, store),
      client: (_) => client,
    );
    addTearDown(pulls.dispose);

    /// Waits for [check], letting the sockets answer meanwhile: the fixture
    /// is real I/O, so the test says what it waits for rather than how long.
    Future<void> until(bool Function() check, String reason) async {
      for (var i = 0; i < 200; i++) {
        if (check()) return;
        await tester.pump(const Duration(milliseconds: 10));
        await tester.runAsync(
          () => Future<void>.delayed(const Duration(milliseconds: 5)),
        );
      }
      fail('never became true: $reason');
    }

    // The agent shipped a pull request, and its check failed.
    await until(
      () => (pulls.groups[PullGroup.failing] ?? const []).isNotEmpty,
      'the failing pull request arrives',
    );
    expect(pulls.readyCount, 0);
    expect(
      pulls.groups[PullGroup.failing]!.single.checkSummary,
      '1 failing: unit',
    );

    // One click: the worker is dispatched against the failing check.
    await pulls.fix(pulls.groups[PullGroup.failing]!.single);
    final dispatched = bridge.requests.lastWhere(
      (request) => request.method == 'POST',
    );
    expect(dispatched.uri, '/api/pulls/${Uri.encodeComponent(id)}/fix');
    expect(jsonDecode(dispatched.body), {'check': 'unit', 'mode': 'fix'});
    expect(
      (pulls.groups[PullGroup.beingFixed] ?? const []),
      hasLength(1),
      reason: 'an accepted dispatch is being fixed, not fixed',
    );

    // The fix is pushed: the head moves, and the old head's evidence goes with
    // it. The checks have not settled, so nothing claims they passed.
    frames.write(
      'data: ${jsonEncode({'kind': contract.DeltaKind.deltaPull.wire, 'pull': record(state: contract.PullState.pullInProgress, checks: const [], head: 'new-head', updatedAt: 3000).toJson()})}\n\n',
    );
    await until(
      () => (pulls.groups[PullGroup.inProgress] ?? const []).isNotEmpty,
      'the new head is in progress',
    );
    expect(
      pulls.groups[PullGroup.inProgress]!.single.checks,
      isEmpty,
      reason: 'the old head’s checks are never shown as current',
    );

    // The new head's checks report, and every one of them passed.
    frames.write(
      'data: ${jsonEncode({
        'kind': contract.DeltaKind.deltaPull.wire,
        'pull': record(
          state: contract.PullState.pullReady,
          checks: const [
            contract.PullCheck(name: 'build', state: contract.CheckState.checkPassed),
            contract.PullCheck(name: 'unit', state: contract.CheckState.checkPassed),
          ],
          head: 'new-head',
          updatedAt: 4000,
        ).toJson(),
      })}\n\n',
    );
    await until(() => pulls.readyCount == 1, 'the card reaches ready');
    expect(pulls.groups[PullGroup.beingFixed], isEmpty);
    expect(pulls.groups[PullGroup.failing], isEmpty);
    expect(
      pulls.groups[PullGroup.ready]!.single.checkSummary,
      '2 of 2 checks passed',
    );
  });
}
