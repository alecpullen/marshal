import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/event_stream.dart';

/// A shell in the agent's workspace (f4-inspector.md decision 9).
///
/// Opening it posts the terminal route and follows its event stream. Output
/// is decoded through one long-lived UTF-8 decoder, so a character split
/// across two chunks arrives whole. Typing holds the agent: the first
/// keystroke pauses it, and it resumes on [handBack] or after [idleLimit]
/// without input. Time is injected as [wait], so a test advances it instead
/// of sleeping.
class TerminalSession extends ChangeNotifier {
  TerminalSession(
    this._client, {
    required this.agentId,
    this.idleLimit = const Duration(minutes: 2),
    Future<void> Function(Duration duration)? wait,
  }) : _wait = wait ?? ((duration) => Future<void>.delayed(duration));

  /// A session with no bridge: what a fixture or the gallery draws.
  TerminalSession.seeded({this.agentId = ''})
    : _client = null,
      idleLimit = const Duration(minutes: 2),
      _wait = ((duration) => Future<void>.delayed(duration));

  final BridgeClient? _client;
  final String agentId;
  final Future<void> Function(Duration) _wait;

  /// How long the agent may stay held without input.
  final Duration idleLimit;

  final _output = StreamController<String>.broadcast();
  StreamSubscription<StreamEvent>? _subscription;
  EventStream? _stream;
  late final ByteConversionSink _decoder = utf8.decoder.startChunkedConversion(
    _OutputSink(_output),
  );

  String? _terminalId;
  bool _paused = false;
  bool _running = false;
  int _generation = 0;
  String? _exit;
  bool _disposed = false;

  /// The shell's output, as text.
  Stream<String> get output => _output.stream;

  String? get terminalId => _terminalId;

  /// Whether a shell is open.
  bool get isOpen => _terminalId != null;

  /// Whether the agent is held because someone is typing here.
  bool get agentPaused => _paused;

  /// Whether the shell is still running.
  bool get running => _running;

  /// The shell's exit code, once it has one.
  String? get exit => _exit;

  /// Opens the shell and follows its stream.
  Future<void> open({int cols = 80, int rows = 24}) async {
    final client = _client;
    if (client == null || _terminalId != null) return;
    final opened = await client.openTerminal(agentId, cols: cols, rows: rows);
    _terminalId = opened.terminalID;
    _running = true;
    notifyListeners();
    final stream = client.terminalEvents(agentId, opened.terminalID);
    _stream = stream;
    _subscription = stream.events.listen(_onEvent);
    stream.start();
  }

  /// Sends keystrokes. The first one holds the agent.
  void input(String data) {
    if (data.isEmpty) return;
    final client = _client;
    final id = _terminalId;
    if (client == null || id == null) return;
    if (!_paused) {
      _paused = true;
      notifyListeners();
    }
    // The wire carries keystrokes base64-encoded; the bridge decodes them.
    unawaited(
      client.terminalInput(agentId, id, base64Encode(utf8.encode(data))),
    );
    _scheduleIdle();
  }

  /// Tells the shell its size.
  void resize(int cols, int rows) {
    final client = _client;
    final id = _terminalId;
    if (client == null || id == null) return;
    unawaited(client.terminalResize(agentId, id, cols, rows));
  }

  /// Hands the agent back: it resumes working, and the shell stays open.
  Future<void> handBack() async {
    final client = _client;
    final id = _terminalId;
    _generation++;
    if (!_paused) return;
    _paused = false;
    notifyListeners();
    if (client == null || id == null) return;
    await client.terminalRelease(agentId, id);
  }

  /// Ends the shell.
  Future<void> close() async {
    final client = _client;
    final id = _terminalId;
    await _subscription?.cancel();
    _subscription = null;
    _stream?.stop();
    _stream = null;
    _running = false;
    _terminalId = null;
    if (client != null && id != null) await client.closeTerminal(agentId, id);
    if (!_output.isClosed) await _output.close();
    if (!_disposed) {
      _disposed = true;
      notifyListeners();
    }
  }

  /// Waits out [idleLimit]; if no keystroke came in that time, hands back.
  void _scheduleIdle() {
    final generation = ++_generation;
    unawaited(() async {
      await _wait(idleLimit);
      if (_disposed || generation != _generation) return;
      await handBack();
    }());
  }

  void _onEvent(StreamEvent event) {
    Object? json;
    try {
      json = jsonDecode(event.data);
    } on FormatException {
      return;
    }
    if (json is! Map<String, dynamic>) return;
    final data = json['data'];
    if (data is String && data.isNotEmpty) {
      try {
        _decoder.add(base64Decode(data));
      } on FormatException {
        // A chunk the bridge did not base64 is not output.
      }
    }
    final exit = json['exit'];
    if (exit is int) {
      _exit = '$exit';
      _running = false;
      notifyListeners();
    }
  }

  @override
  void dispose() {
    _disposed = true;
    unawaited(_subscription?.cancel());
    _stream?.stop();
    if (!_output.isClosed) unawaited(_output.close());
    super.dispose();
  }
}

/// The sink the one long-lived UTF-8 decoder writes into: every decoded run
/// goes to the session's output stream.
class _OutputSink implements Sink<String> {
  _OutputSink(this._controller);

  final StreamController<String> _controller;

  @override
  void add(String data) {
    if (!_controller.isClosed) _controller.add(data);
  }

  @override
  void close() {}
}
