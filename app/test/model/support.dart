import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/event_stream.dart';
import 'package:marshal_app/src/client/host.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/fleet_store.dart';

import '../support/fake_bridge.dart';
import '../support/sockets.dart';

/// One agent as a test writes one: every field it does not name is the
/// quietest value there is.
AgentStatus agent({
  String id = 'a-1',
  String project = '/home/u/alpha',
  String? name,
  AgentState status = AgentState.agentIdle,
  int? changedFiles,
  String? prUrl,
  PendingRequest? pending,
  String? activity,
  DateTime? lastActive,
}) => AgentStatus(
  id: id,
  project: project,
  name: name,
  status: status,
  changedFiles: changedFiles,
  prUrl: prUrl,
  pending: pending,
  activity: activity,
  updatedAt: (lastActive ?? DateTime.utc(2026, 11, 17, 4)).toIso8601String(),
);

/// One project as a test writes one.
ProjectStatus project(String root) => ProjectStatus(
  root: root,
  available: true,
  trust: TrustState.trustTrusted,
  isolation: 'available',
);

/// One host the fixture serves, and what its bridge answers with.
class FixtureHost {
  const FixtureHost({
    required this.name,
    this.kind = HostKind.local,
    this.agents = const [],
    this.projects = const [],
    this.queue = const [],
    this.budget,
    this.factoryItems = const [],
  });

  final String name;
  final HostKind kind;
  final List<AgentStatus> agents;
  final List<ProjectStatus> projects;
  final List<InboxItem> queue;
  final BudgetReport? budget;

  /// The work items its bridge reports (F6).
  final List<FactoryItem> factoryItems;
}

/// One work item as a test writes one: every field it does not name is the
/// quietest value there is.
FactoryItem factoryItem({
  String id = 'item-1',
  String title = 'Work item thread',
  String state = 'implementing',
  String stage = '',
  String project = '/home/u/alpha',
  String repo = 'repo-1',
  int version = 1,
  String plan = '## Task 1\nRead the item.\n## Task 2\nApprove it.\n',
  List<FactoryWorker> workers = const [],
  List<FactoryRound> rounds = const [],
  List<FactoryPR> prs = const [],
  List<FactoryEffect> effects = const [],
  List<FactoryEvent> history = const [],
  FactoryDecision? decision,
  FactoryCompletion? completion,
  DateTime? updatedAt,
}) => FactoryItem.fromJson({
  'id': id,
  'title': title,
  'state': state,
  'stage': stage,
  'repoId': repo,
  'projectRoot': project,
  'hostId': 'local',
  'branch': 'factory/$id',
  'baseSha': 'base-1',
  'version': version,
  'draft': {
    'requestId': 'req-1',
    'title': title,
    'plan': plan,
    'spec': 'Ship the work item thread.',
    'config': {
      'repoId': repo,
      'projectRoot': project,
      'hostId': 'local',
      'targetBranch': 'main',
      'workspace': '',
      'native': true,
      'implementerRouting': null,
      'reviewerRouting': null,
      'reviewerInstructions': '',
      'implementerAuto': true,
      'expectedChecks': [],
      'noRequiredCiAcknowledged': true,
      'maxRounds': 3,
      'ciPollSeconds': 30,
      'ciWaitSeconds': 900,
    },
  },
  'approvals': [],
  'grants': [],
  'rounds': [for (final round in rounds) round.toJson()],
  'workers': [for (final worker in workers) worker.toJson()],
  'prs': [for (final pr in prs) pr.toJson()],
  'effects': [for (final effect in effects) effect.toJson()],
  'decision': decision?.toJson(),
  'history': [for (final event in history) event.toJson()],
  if (completion != null) 'completion': completion.toJson(),
  'createdAt': '2026-11-17T09:00:00Z',
  'approvedAt': '2026-11-17T09:05:00Z',
  'updatedAt': (updatedAt ?? DateTime.utc(2026, 11, 17, 10)).toIso8601String(),
});

/// One CI check as a test writes one: a check run names the app that runs it,
/// a commit status its own context.
FactoryCheck factoryCheck({
  String kind = 'check_run',
  String name = 'build',
  int appID = 42,
  String observedAt = '2026-11-17T09:10:00Z',
}) => FactoryCheck.fromJson({
  'key': {'kind': kind, 'name': name, 'appId': appID},
  'id': 'check-$name-$appID',
  'sha': 'head-1',
  'status': 'completed',
  'conclusion': 'success',
  'url': '',
  'logTail': '',
  'observedAt': observedAt,
});

/// One round's CI as a test writes one.
FactoryCI factoryCI({
  List<FactoryCheck> checks = const [],
  String observedAt = '2026-11-17T09:10:00Z',
}) => FactoryCI.fromJson({
  'headSha': 'head-1',
  'checks': [for (final check in checks) check.toJson()],
  'complete': true,
  'observedAt': observedAt,
});

/// One round as a test writes one; a null [ci] is a round that recorded none.
FactoryRound factoryRound({int number = 1, FactoryCI? ci}) =>
    FactoryRound.fromJson({
      'number': number,
      'approvalRevision': 1,
      'implementEffectId': 'effect-$number',
      'baseSha': 'base-1',
      'headSha': 'head-$number',
      'treeSha': 'tree-$number',
      'ci': ci?.toJson(),
      'startedAt': '2026-11-17T09:00:00Z',
      'finishedAt': '2026-11-17T09:20:00Z',
    });

/// One factory worker as a test writes one.
FactoryWorker factoryWorker({
  String agentId = 'agent-1',
  String sessionId = 'sess-1',
  String role = 'implementer',
  bool parked = false,
  String? spawnEffectId,
}) => FactoryWorker.fromJson({
  'agentId': agentId,
  'sessionId': sessionId,
  'role': role,
  'spawnEffectId': spawnEffectId ?? 'effect-$role',
  'parked': parked,
  'subagents': {'enabled': false, 'concurrency': 0},
});

/// One linked pull request as a test writes one: the item's own record of the
/// reviewed identity a merge names.
FactoryPR factoryPR({
  int number = 412,
  String nodeID = 'node-412',
  String headSHA = 'head-412',
  String baseRef = 'main',
  String state = 'open',
  bool draft = false,
}) => FactoryPR.fromJson({
  'number': number,
  'url': 'https://forge.test/o/r/pull/$number',
  'nodeId': nodeID,
  'headRef': 'factory/item-1',
  'baseRef': baseRef,
  'headSha': headSHA,
  'baseSha': 'base-1',
  'state': state,
  'draft': draft,
});

/// One history entry as a test writes one.
FactoryEvent factoryEvent(
  int sequence,
  String kind,
  String message, {
  String effectId = '',
  String at = '2026-11-17T09:30:00Z',
}) => FactoryEvent.fromJson({
  'sequence': sequence,
  'at': at,
  'kind': kind,
  'message': message,
  'effectId': effectId,
});

/// A store over in-process fake bridges, one per host: the fixture every
/// model test reads fleet state through, so no test touches the developer's
/// daemon, config or network.
class ModelFixture {
  ModelFixture._({
    required this.bridges,
    required this.frames,
    required this.registry,
    required this.store,
    required this.clock,
    required this.clients,
  });

  /// One fake bridge per host, by host name.
  final Map<String, FakeBridge> bridges;

  /// The fleet stream of each host, which a test writes deltas to.
  final Map<String, FakeStream> frames;

  final HostRegistry registry;
  final FleetStore store;
  final FixtureClock clock;

  /// The client that reaches each host, by host name: what a model that
  /// writes (an answer, a spawn) posts through.
  final Map<String, BridgeClient> clients;

  /// The client for [host].
  BridgeClient clientFor(String host) => clients[host]!;

  /// Starts the fixture: one bridge per host, the snapshot routes they
  /// answer with, and a store over the hosts discovery finds.
  static Future<ModelFixture> start(
    List<FixtureHost> hosts, {
    DateTime? now,
  }) async {
    // flutter_test's binding answers every HttpClient with 400; the fake
    // bridge is on this machine, so the real client is wanted back.
    HttpOverrides.global = null;
    final clock = FixtureClock(now ?? DateTime.utc(2026, 11, 17, 4));
    final bridges = <String, FakeBridge>{};
    final frames = <String, FakeStream>{};
    final transports = <String, Transport>{};
    final clients = <String, BridgeClient>{};
    final streams = <String, EventStream>{};

    for (final host in hosts) {
      final box = await socketBridge('${host.name}.sock');
      final bridge = box.bridge;
      bridges[host.name] = bridge;
      frames[host.name] = bridge.stream('GET', '/api/events');
      bridge
        ..reply(
          'GET',
          '/api/agents',
          _json([for (final a in host.agents) a.toJson()]),
        )
        ..reply(
          'GET',
          '/api/projects',
          _json([for (final p in host.projects) p.toJson()]),
        )
        ..reply(
          'GET',
          '/api/inbox',
          _json(
            Inbox(
              groups: const InboxGroups(
                needsYou: [],
                running: [],
                ready: [],
                earlier: [],
              ),
              queue: host.queue,
            ).toJson(),
          ),
        )
        ..reply(
          'GET',
          '/api/factory/items',
          _json({
            'items': [for (final item in host.factoryItems) item.toJson()],
          }),
        )
        ..reply(
          'GET',
          '/api/budgets',
          host.budget == null
              ? const FakeReply(404, body: '{"error":"no budgets"}')
              : _json(host.budget!.toJson()),
        );
      final transport = box.transport;
      transports[host.name] = transport;
      clients[host.name] = BridgeClient(transport);
    }

    final registry = HostRegistry(
      discovery: () async => [
        for (final host in hosts)
          HostSpec(
            name: host.name,
            kind: host.kind,
            client: clients[host.name]!,
          ),
      ],
      fleetFor: (client) {
        final name = clients.entries
            .firstWhere((entry) => identical(entry.value, client))
            .key;
        return streams[name] = EventStream(
          transports[name]!,
          '/api/events',
          now: clock.now,
          wait: clock.wait,
          jitter: 0,
          // The window the store's own tests advance the clock over, named
          // rather than inherited: the production default outlasts the
          // bridge's 25s heartbeat.
          staleAfter: const Duration(seconds: 10),
        );
      },
    );
    await registry.discover();
    final store = FleetStore(registry, now: clock.now);
    final fixture = ModelFixture._(
      bridges: bridges,
      frames: frames,
      registry: registry,
      store: store,
      clock: clock,
      clients: clients,
    );
    addTearDown(() async {
      store.dispose();
      registry.dispose();
      for (final bridge in bridges.values) {
        await bridge.close();
      }
    });
    return fixture;
  }

  /// Reads every host's snapshot.
  Future<void> refresh() async {
    for (final name in bridges.keys) {
      await store.refresh(name);
    }
  }

  /// Every request [host]'s bridge received, oldest first.
  List<RecordedRequest> requests(String host) =>
      List.unmodifiable(bridges[host]!.requests);

  /// Replaces what [host]'s bridge answers `GET path` with from now on.
  ///
  /// [delay] holds the answer back, so a test can have a read in flight while
  /// it does something else — the reply is chosen when the request arrives,
  /// so re-serving the route afterwards changes the next request only.
  void serve(String host, String path, Object body, {Duration? delay}) =>
      bridges[host]!.reply(
        'GET',
        path,
        FakeReply(
          200,
          headers: const {'content-type': 'application/json'},
          body: jsonEncode(body),
          delay: delay,
        ),
      );

  /// Sends one fleet delta, as the bridge's own frame, to [host]'s stream.
  void delta(String host, Map<String, Object?> json) {
    frames[host]!.write('data: ${jsonEncode(json)}\n\n');
  }

  /// Sends the replay-overflow nudge that makes the client fetch again.
  void overflow(String host) {
    frames[host]!.write('data: {"type":"replay_overflow"}\n\n');
  }
}

FakeReply _json(Object body) => FakeReply(
  200,
  headers: const {'content-type': 'application/json'},
  body: jsonEncode(body),
);

/// Lets the event loop and the fake bridges run, so a delta written to a
/// stream has been delivered, applied and notified.
Future<void> settle() async {
  for (var i = 0; i < 5; i++) {
    await pumpEventQueue();
    await Future<void>.delayed(const Duration(milliseconds: 5));
  }
}

/// Waits for [check] to hold, letting the bridges answer meanwhile: a
/// snapshot and a delta both land asynchronously, and a test should say what
/// it waits for rather than how long.
Future<void> eventually(bool Function() check, {String reason = ''}) async {
  for (var i = 0; i < 200; i++) {
    if (check()) return;
    await pumpEventQueue();
    await Future<void>.delayed(const Duration(milliseconds: 5));
  }
  fail('condition never became true${reason.isEmpty ? '' : ': $reason'}');
}

/// A clock a test advances, so a backoff, a stale deadline or an age passes
/// at once (`test/client/event_stream_test.dart`'s FakeClock).
class FixtureClock {
  FixtureClock(this._now);

  DateTime _now;
  final _pending = <_Wait>[];

  DateTime now() => _now;

  Future<void> wait(Duration duration) {
    final wait = _Wait(_now.add(duration), Completer<void>());
    _pending.add(wait);
    return wait.completer.future;
  }

  /// Moves time on, completing every wait that is now due.
  void advance(Duration duration) {
    _now = _now.add(duration);
    final due = [
      for (final wait in _pending)
        if (!wait.deadline.isAfter(_now)) wait,
    ];
    due.sort((a, b) => a.deadline.compareTo(b.deadline));
    for (final wait in due) {
      _pending.remove(wait);
      wait.completer.complete();
    }
  }
}

class _Wait {
  _Wait(this.deadline, this.completer);

  final DateTime deadline;
  final Completer<void> completer;
}
