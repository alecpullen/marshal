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
///
/// Keystrokes go through one queue: a keystroke typed before the shell has an
/// id is held and sent once it does, and a second keystroke is never posted
/// while the first is in flight, so two bodies cannot reach the pty out of
/// order (findings/terminal.md, "Fit with the bridge's terminal routes").
class TerminalSession extends ChangeNotifier {
  TerminalSession(
    this._client, {
    required this.agentId,
    this.idleLimit = const Duration(minutes: 2),
    Future<void> Function(Duration duration)? wait,
  }) : _wait = wait ?? ((duration) => Future<void>.delayed(duration));

  /// A session with no bridge: what a fixture or the gallery draws.
  /// [shellError] is why a shell failed to start, so a fixture can show that
  /// state (a named parameter cannot be the private field itself).
  TerminalSession.seeded({this.agentId = '', String? shellError})
    : _client = null,
      _error = shellError,
      idleLimit = const Duration(minutes: 2),
      _wait = ((duration) => Future<void>.delayed(duration));

  final BridgeClient? _client;
  final String agentId;
  final Future<void> Function(Duration) _wait;

  /// How long the agent may stay held without input.
  final Duration idleLimit;

  /// The largest piece of one keystroke body, in bytes: the wire accepts
  /// 64 KiB, and the web client sends 16 KiB at a time. Pastes are cut here.
  static const inputChunk = 16 * 1024;

  /// Keystrokes not yet sent, oldest first: typed before the shell had an id,
  /// or queued behind one already in flight.
  final _pending = <String>[];

  /// The one request at a time the keystrokes are sent through. Each send is
  /// chained onto it, so the pty sees them in the order they were typed.
  Future<void> _queue = Future<void>.value();

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
  String? _error;
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

  /// Why the shell never started, when the bridge said so on the chunk that
  /// ended its stream. Null while it is running, or when it exited cleanly.
  String? get error => _error;

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
    // Keys typed while the shell was opening waited for this id.
    _flush();
  }

  /// Opens the shell again after it failed to start: the old stream is
  /// dropped, its id forgotten, and a fresh terminal asked for.
  Future<void> reopen({int cols = 80, int rows = 24}) async {
    if (_client == null) return;
    await _subscription?.cancel();
    _subscription = null;
    _stream?.stop();
    _stream = null;
    _terminalId = null;
    _running = false;
    _exit = null;
    _error = null;
    _pending.clear();
    notifyListeners();
    await open(cols: cols, rows: rows);
  }

  /// Sends keystrokes. The first one holds the agent.
  ///
  /// A keystroke typed before the shell has an id is held, not dropped, and
  /// sent once it arrives; each body is posted only when the one before it has
  /// been answered.
  void input(String data) {
    if (data.isEmpty) return;
    if (_client == null) return;
    if (!_paused) {
      _paused = true;
      notifyListeners();
    }
    _pending.addAll(chunks(data));
    _flush();
    _scheduleIdle();
  }

  /// Posts what is pending, one body at a time, through [inputChunk] pieces.
  ///
  /// Everything is chained onto [_queue], so the pty sees the keystrokes in
  /// the order they were typed even while an earlier request is in flight.
  void _flush() {
    final client = _client;
    if (client == null) return;
    _queue = _queue.then((_) async {
      final id = _terminalId;
      // Still opening: [open] flushes again when the id arrives.
      if (id == null) return;
      while (_pending.isNotEmpty) {
        final chunk = _pending.removeAt(0);
        try {
          // The wire carries keystrokes base64-encoded; the bridge decodes
          // them. A refused keystroke is dropped: the shell's own failure
          // arrives on its stream, where it is shown.
          await client.terminalInput(
            agentId,
            id,
            base64Encode(utf8.encode(chunk)),
          );
        } catch (_) {
          // Nothing to retry: keystrokes are text that has already been
          // typed, and re-sending it out of order is worse than losing it.
        }
      }
    });
  }

  /// [data] cut into pieces of at most [inputChunk] UTF-8 bytes, on character
  /// boundaries so a multi-byte character is never split across two bodies.
  static List<String> chunks(String data) {
    if (utf8.encode(data).length <= inputChunk) return [data];
    final out = <String>[];
    final piece = StringBuffer();
    var size = 0;
    for (final rune in data.runes) {
      final char = String.fromCharCode(rune);
      final length = utf8.encode(char).length;
      if (size + length > inputChunk) {
        out.add(piece.toString());
        piece.clear();
        size = 0;
      }
      piece.write(char);
      size += length;
    }
    if (piece.isNotEmpty) out.add(piece.toString());
    return out;
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
    // Keystrokes typed for a shell that is closing have nowhere to go.
    _pending.clear();
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
    // Why the shell never started, on the chunk that ends its stream: a
    // shell that failed this way ends silently unless the reason is kept.
    final error = json['error'];
    if (error is String && error.isNotEmpty) {
      _error = error;
      _running = false;
      notifyListeners();
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
