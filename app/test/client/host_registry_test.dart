import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/event_stream.dart';
import 'package:marshal_app/src/client/host.dart';
import 'package:marshal_app/src/client/transport.dart';

/// The host registry, held to F1 decision 8: a host's reach follows its fleet
/// stream, an unreachable host is kept with its last contact, and a refused
/// token is terminal.
void main() {
  late _ScriptedTransport transport;
  late _FakeClock clock;
  late HostRegistry registry;
  late Host host;

  setUp(() async {
    transport = _ScriptedTransport();
    clock = _FakeClock(DateTime.utc(2026, 11, 17, 4));
    registry = HostRegistry(
      discovery: () async => [
        HostSpec(
          name: 'local',
          kind: HostKind.local,
          client: BridgeClient(transport),
        ),
      ],
      // The stream a host follows, with this test's clock and connection.
      fleetFor: (client) => EventStream(
        transport,
        '/api/events',
        now: clock.now,
        wait: clock.wait,
        jitter: 0,
      ),
    );
    addTearDown(registry.dispose);
    await registry.discover();
    host = registry.hosts.single;
  });

  test('a stopped daemon becomes stale then unreachable', () async {
    await pumpEventQueue();
    expect(host.reach, HostReach.connecting);
    expect(host.lastContact, isNull);

    // The daemon answers: a heartbeat is contact.
    send(transport.last, ': hb\n\n');
    await pumpEventQueue();
    expect(host.reach, HostReach.live);
    expect(host.lastContact, clock.now());

    // Then it goes quiet for the whole stale window.
    clock.advance(const Duration(seconds: 10));
    await pumpEventQueue();
    expect(host.reach, HostReach.stale);

    // The connection dies: the stream is reconnecting, so nothing is
    // reachable, and the host keeps the time it was last seen.
    await transport.last.close();
    await pumpEventQueue();
    expect(host.reach, HostReach.unreachable);
    expect(host.lastContact, clock.now().subtract(const Duration(seconds: 10)));
  });

  test('a returning daemon becomes live', () async {
    send(transport.last, ': hb\n\n');
    await pumpEventQueue();
    await transport.last.close();
    await pumpEventQueue();
    expect(host.reach, HostReach.unreachable);

    // The retry goes out after the first backoff, and the daemon answers.
    clock.advance(const Duration(milliseconds: 500));
    await pumpEventQueue();
    expect(transport.requests, hasLength(2));
    send(transport.last, ': hb\n\n');
    await pumpEventQueue();
    expect(host.reach, HostReach.live);
  });

  test('an unreachable host is kept', () async {
    send(transport.last, ': hb\n\n');
    await pumpEventQueue();
    await transport.last.close();
    await pumpEventQueue();
    expect(host.reach, HostReach.unreachable);
    final contact = host.lastContact;
    expect(contact, isNotNull);

    // Discovery runs again: the host is already known, so it is neither
    // duplicated nor forgotten for being down.
    await registry.discover();
    await pumpEventQueue();
    expect(registry.hosts, hasLength(1));
    expect(identical(registry.hosts.single, host), isTrue);
    expect(host.reach, HostReach.unreachable);
    expect(host.lastContact, contact);
  });

  test('a wrong token marks the host unauthorized', () async {
    await pumpEventQueue();
    transport.last.addError(Unauthorized());
    await pumpEventQueue();

    expect(host.reach, HostReach.unauthorized);
    expect(
      transport.requests,
      hasLength(1),
      reason: 'a refused token is never retried',
    );
  });
}

/// Feeds one server-sent-event frame the way a transport delivers it: one
/// element per line, and no trailing empty element for the final newline.
void send(StreamController<String> connection, String frame) {
  final lines = frame.split('\n');
  if (lines.isNotEmpty && lines.last.isEmpty) {
    lines.removeLast();
  }
  for (final line in lines) {
    connection.add(line);
  }
}

/// A [Transport] whose every connection is a stream the test writes to.
class _ScriptedTransport implements Transport {
  final requests = <Request>[];
  final connections = <StreamController<String>>[];

  StreamController<String> get last => connections.last;

  @override
  Future<Response> send(Request request) async =>
      const Response(status: 200, body: '{}');

  @override
  Stream<String> lines(Request request) {
    requests.add(request);
    final connection = StreamController<String>();
    connections.add(connection);
    return connection.stream;
  }
}

/// A clock a test advances, so a backoff or a stale deadline passes at once.
class _FakeClock {
  _FakeClock(this._now);

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
