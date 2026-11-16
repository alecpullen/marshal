import 'dart:async';
import 'dart:math';

import 'package:flutter/foundation.dart';

import 'transport.dart';

/// The synthetic event web/bridge/sse.go sends when it can no longer replay
/// from the client's position (web/bridge/events.go). It is a nudge, not data:
/// the client fetches a fresh snapshot instead (F1 decision 7).
const overflowNudge = '{"type":"replay_overflow"}';

/// What one fleet stream is doing (F1 decision 7).
enum StreamState {
  /// A connection is being made, or a lost one is being retried.
  connecting,

  /// The bridge is talking: an event or a heartbeat arrived recently.
  live,

  /// The connection stands, but nothing has arrived for a while. The last data
  /// is shown, plainly marked with its age.
  stale,

  /// The stream will not be retried: the bridge refused the token. Only a new
  /// token changes that (decision 6).
  stopped,
}

/// One event from the fleet stream.
class StreamEvent {
  const StreamEvent({required this.id, required this.data});

  /// The bridge's sequence number; zero for an event it did not number.
  final int id;

  /// The event's JSON, verbatim.
  final String data;

  @override
  String toString() => 'StreamEvent($id)';
}

/// The fleet stream: server-sent events over a [Transport], resumed after a
/// drop, with a state a screen can trust.
///
/// Time is injected ([now] and [wait]) so a test advances a clock instead of
/// sleeping.
class EventStream {
  EventStream(
    this._transport,
    this.path, {
    Map<String, String> query = const {},
    DateTime Function()? now,
    Future<void> Function(Duration)? wait,
    this.staleAfter = const Duration(seconds: 10),
    this.firstBackoff = const Duration(milliseconds: 500),
    this.maxBackoff = const Duration(seconds: 30),
    this.jitter = 0.2,
    Random? random,
  }) : _query = query,
       _now = now ?? DateTime.now,
       _wait = wait ?? _delay,
       _random = random ?? Random() {
    _backoff = firstBackoff;
  }

  final Transport _transport;
  final String path;

  /// How long a live stream may stay silent before it is stale. A heartbeat
  /// counts as contact, so only a dead connection reaches it.
  final Duration staleAfter;

  /// The first retry's delay; each further retry doubles it, up to
  /// [maxBackoff].
  final Duration firstBackoff;
  final Duration maxBackoff;

  /// How much longer than the delay a retry may wait: up to [jitter] of it,
  /// so several clients do not retry in step.
  final double jitter;

  final Map<String, String> _query;
  final DateTime Function() _now;
  final Future<void> Function(Duration) _wait;
  final Random _random;

  final _events = StreamController<StreamEvent>.broadcast();
  final _gaps = StreamController<void>.broadcast();
  final _state = ValueNotifier<StreamState>(StreamState.stopped);

  /// The events the bridge sends, as they arrive.
  Stream<StreamEvent> get events => _events.stream;

  /// The bridge could not replay from the last event id, so what the client
  /// holds may have missed something: fetch a snapshot again.
  Stream<void> get gaps => _gaps.stream;

  /// What the stream is doing now.
  ValueListenable<StreamState> get state => _state;

  /// When the last event or heartbeat arrived, or null before the first.
  DateTime? get lastEventAt => _lastEventAt;
  DateTime? _lastEventAt;

  /// The last id the bridge sent, which a reconnect resumes from. Zero means
  /// the start of the stream.
  int _lastEventId = 0;
  int _eventId = 0;
  final _data = <String>[];

  bool _running = false;
  late Duration _backoff;
  int _watchGeneration = 0;

  /// Connects, and keeps the connection: after a drop it retries with backoff
  /// until [stop], or until the bridge refuses the credentials.
  void start() {
    if (_running) return;
    _running = true;
    _state.value = StreamState.connecting;
    unawaited(_run());
  }

  /// Stops the stream for good; a stopped stream does not retry.
  void stop() {
    _running = false;
    _watchGeneration++;
    if (_state.value != StreamState.stopped) {
      _state.value = StreamState.stopped;
    }
  }

  /// The loop: connect, read until the connection ends, back off, repeat.
  Future<void> _run() async {
    while (_running) {
      _state.value = StreamState.connecting;
      try {
        await _consume();
      } on Unauthorized {
        // Decision 6: the same token cannot start working, so this is
        // terminal rather than a retry on a timer.
        _running = false;
        _watchGeneration++;
        _state.value = StreamState.stopped;
        return;
      } catch (_) {
        // A dropped connection, a refused route, a dead socket: every one is
        // retried below, with backoff. The state stays connecting meanwhile.
      }
      if (!_running) return;
      _state.value = StreamState.connecting;
      await _wait(_nextBackoff());
    }
  }

  /// Reads one connection's lines until it ends.
  Future<void> _consume() async {
    final request = Request(
      path: path,
      query: {..._query, if (_lastEventId > 0) 'lastEventId': '$_lastEventId'},
    );
    await for (final line in _transport.lines(request)) {
      if (!_running) return;
      _touch();
      _parse(line);
    }
  }

  /// Records contact: a line arrived, so the stream is live, and the silence
  /// that makes it stale starts again from now.
  void _touch() {
    _lastEventAt = _now();
    if (_state.value != StreamState.live) {
      // A line proves the connection works, so the next drop starts at the
      // first delay again, and a stale stream is live once more.
      _backoff = firstBackoff;
      _state.value = StreamState.live;
    }
    _restartWatchdog();
  }

  /// Declares the stream stale when nothing arrives for [staleAfter]. Each
  /// call supersedes the last, so only the newest deadline counts.
  void _restartWatchdog() {
    final generation = ++_watchGeneration;
    unawaited(
      _wait(staleAfter).then((_) {
        if (!_running || generation != _watchGeneration) return;
        if (_state.value != StreamState.live) return;
        _state.value = StreamState.stale;
      }),
    );
  }

  /// Reads one server-sent-event line (web/bridge/sse.go writes the frames).
  void _parse(String line) {
    if (line.isEmpty) {
      _flush();
      return;
    }
    if (line.startsWith(':')) {
      // A comment: the bridge's heartbeat. `_touch` has already counted it as
      // contact; there is nothing else to read.
      return;
    }
    if (line.startsWith('id:')) {
      final id = int.tryParse(line.substring(3).trim());
      if (id != null && id > 0) {
        _eventId = id;
        _lastEventId = id;
      }
      return;
    }
    if (line.startsWith('data:')) {
      var value = line.substring(5);
      // One space after the colon is punctuation, not data.
      if (value.startsWith(' ')) value = value.substring(1);
      _data.add(value);
    }
    // Any other field is ignored, as the protocol says.
  }

  /// Ends one event: a blank line, or the body's end.
  void _flush() {
    final id = _eventId;
    _eventId = 0;
    if (_data.isEmpty) return;
    final data = _data.join('\n');
    _data.clear();
    if (data == overflowNudge) {
      // The bridge cannot replay from where this client was, so it cannot be
      // resumed from there either: start over and refetch.
      _lastEventId = 0;
      _gaps.add(null);
      return;
    }
    _events.add(StreamEvent(id: id, data: data));
  }

  /// The next retry's delay: [firstBackoff], doubling to [maxBackoff], plus up
  /// to [jitter] of it.
  Duration _nextBackoff() {
    final base = _backoff;
    final doubled = base * 2;
    _backoff = doubled > maxBackoff ? maxBackoff : doubled;
    if (jitter <= 0) return base;
    final factor = 1 + _random.nextDouble() * jitter;
    return Duration(microseconds: (base.inMicroseconds * factor).round());
  }
}

/// The production wait: a timer.
Future<void> _delay(Duration duration) => Future<void>.delayed(duration);
