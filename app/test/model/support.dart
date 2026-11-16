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
  });

  final String name;
  final HostKind kind;
  final List<AgentStatus> agents;
  final List<ProjectStatus> projects;
  final List<InboxItem> queue;
  final BudgetReport? budget;
}

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
      final bridge = await FakeBridge.onLoopback();
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
          '/api/budgets',
          host.budget == null
              ? const FakeReply(404, body: '{"error":"no budgets"}')
              : _json(host.budget!.toJson()),
        );
      final transport = serverTransport(
        bridge.base,
        token: () async => 'unused',
      );
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
  void serve(String host, String path, Object body) => bridges[host]!.reply(
    'GET',
    path,
    FakeReply(
      200,
      headers: const {'content-type': 'application/json'},
      body: jsonEncode(body),
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
