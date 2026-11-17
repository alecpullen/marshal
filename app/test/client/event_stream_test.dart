import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/event_stream.dart';
import 'package:marshal_app/src/client/transport.dart';

/// The fleet stream, held to F1 decision 7: the parse, the resume, the
/// backoff, staleness, the terminal 401 and the replay gap.
///
/// Time is a [FakeClock] and the connection is a [ScriptedTransport], so no
/// test sleeps and none touches a socket.
void main() {
  late ScriptedTransport transport;
  late FakeClock clock;
  late EventStream stream;
  late List<StreamEvent> events;
  late List<void> gaps;

  setUp(() {
    transport = ScriptedTransport();
    clock = FakeClock(DateTime.utc(2026, 11, 17, 4));
    events = [];
    gaps = [];
    stream = EventStream(
      transport,
      '/api/events',
      now: clock.now,
      wait: clock.wait,
      jitter: 0,
      // The window this test advances the clock over, named rather than
      // inherited: the production default outlasts the bridge's 25s heartbeat.
      staleAfter: const Duration(seconds: 10),
    );
    stream.events.listen(events.add);
    stream.gaps.listen(gaps.add);
  });

  tearDown(() => stream.stop());

  test('parses multi-line data', () async {
    stream.start();
    await pumpEventQueue();
    expect(stream.state.value, StreamState.connecting);

    // A heartbeat first: contact, but no event.
    send(transport.last, ': hb\n\n');
    await pumpEventQueue();
    expect(events, isEmpty, reason: 'a heartbeat is not an event');
    expect(stream.state.value, StreamState.live);

    send(transport.last, 'id: 3\ndata: one\ndata: two\n\n');
    await pumpEventQueue();

    expect(events, hasLength(1));
    expect(events.single.id, 3);
    expect(events.single.data, 'one\ntwo');
    expect(stream.state.value, StreamState.live);
    expect(stream.lastEventAt, clock.now());
  });

  test('resumes from the last id', () async {
    stream.start();
    await pumpEventQueue();
    expect(transport.requests, hasLength(1));
    expect(transport.requests.single.query, isNot(contains('lastEventId')));

    send(transport.last, 'id: 7\ndata: {"kind":"activity"}\n\n');
    await pumpEventQueue();
    await transport.last.close();
    await pumpEventQueue();

    // The drop starts the backoff; the retry carries the id it reached.
    expect(clock.waits.last, const Duration(milliseconds: 500));
    clock.advance(const Duration(milliseconds: 500));
    await pumpEventQueue();

    expect(transport.requests, hasLength(2));
    expect(transport.requests.last.query['lastEventId'], '7');
  });

  test('backs off and caps', () async {
    stream.start();
    await pumpEventQueue();

    // 500 ms, doubling, to 30 s, and no further.
    const want = [
      Duration(milliseconds: 500),
      Duration(seconds: 1),
      Duration(seconds: 2),
      Duration(seconds: 4),
      Duration(seconds: 8),
      Duration(seconds: 16),
      Duration(seconds: 30),
      Duration(seconds: 30),
    ];
    for (final delay in want) {
      await transport.last.close();
      await pumpEventQueue();
      expect(clock.waits.last, delay, reason: 'the retry after a drop');
      clock.advance(delay);
      await pumpEventQueue();
    }
    expect(transport.requests, hasLength(want.length + 1));
  });

  test('goes stale without events', () async {
    stream.start();
    await pumpEventQueue();
    send(transport.last, 'id: 1\ndata: {"kind":"inbox"}\n\n');
    await pumpEventQueue();
    expect(stream.state.value, StreamState.live);

    clock.advance(const Duration(seconds: 9));
    await pumpEventQueue();
    expect(stream.state.value, StreamState.live);

    clock.advance(const Duration(seconds: 1));
    await pumpEventQueue();
    expect(stream.state.value, StreamState.stale);
  });

  test('returns to live', () async {
    stream.start();
    await pumpEventQueue();
    send(transport.last, 'id: 1\ndata: {"kind":"inbox"}\n\n');
    await pumpEventQueue();
    clock.advance(const Duration(seconds: 10));
    await pumpEventQueue();
    expect(stream.state.value, StreamState.stale);

    // A heartbeat is contact: the connection is alive, whatever it carried.
    send(transport.last, ': hb\n\n');
    await pumpEventQueue();
    expect(stream.state.value, StreamState.live);
  });

  test('stops on unauthorized', () async {
    stream.start();
    await pumpEventQueue();
    transport.last.addError(Unauthorized());
    await pumpEventQueue();

    expect(stream.state.value, StreamState.stopped);
    expect(transport.requests, hasLength(1), reason: 'no retry follows a 401');
    expect(clock.waits, isEmpty);
  });

  test('reports a replay gap', () async {
    stream.start();
    await pumpEventQueue();
    send(transport.last, 'id: 5\ndata: {"kind":"activity"}\n\n');
    await pumpEventQueue();
    expect(gaps, isEmpty);

    // The bridge cannot replay from 5: it nudges the client to refetch.
    send(transport.last, 'data: {"type":"replay_overflow"}\n\n');
    await pumpEventQueue();
    expect(gaps, hasLength(1));
    expect(events, hasLength(1), reason: 'the nudge is not an event');

    // The remembered id is reset, so the retry starts from the beginning.
    await transport.last.close();
    await pumpEventQueue();
    clock.advance(const Duration(milliseconds: 500));
    await pumpEventQueue();
    expect(transport.requests.last.query, isNot(contains('lastEventId')));
  });
}

/// Feeds one server-sent-event frame to a scripted connection the way a
/// transport delivers it: one element per line, and no trailing empty element
/// for the frame's final newline.
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
class ScriptedTransport implements Transport {
  /// One request per connection attempted, in order.
  final requests = <Request>[];

  final connections = <StreamController<String>>[];

  /// The newest connection.
  StreamController<String> get last => connections.last;

  @override
  Future<Response> send(Request request) =>
      throw UnimplementedError('ScriptedTransport has no send');

  @override
  Stream<String> lines(Request request) {
    requests.add(request);
    final connection = StreamController<String>();
    connections.add(connection);
    return connection.stream;
  }
}

/// A clock a test advances, so a backoff or a stale deadline passes at once.
class FakeClock {
  FakeClock(this._now);

  DateTime _now;

  /// The durations every wait asked for, in order.
  final waits = <Duration>[];

  final _pending = <_Wait>[];

  DateTime now() => _now;

  Future<void> wait(Duration duration) {
    waits.add(duration);
    final wait = _Wait(_now.add(duration), Completer<void>());
    _pending.add(wait);
    return wait.completer.future;
  }

  /// Moves time on, completing every wait that is now due and letting the
  /// stream run until it waits again.
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
