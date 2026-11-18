// The routes are named for what they do and their fields stay private, so the
// parameters cannot be initializing formals: Dart has no private named
// parameter.
// ignore_for_file: prefer_initializing_formals

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'fleet_store.dart' show describeFailure;
import 'thread_view_state.dart';

/// One thread's composer: whether a turn is running, whether the session can
/// only be read, and the routes a send, a stop and a mode change post.
///
/// The routes are functions rather than a client so a test drives every
/// outcome — accepted, refused, read-only — with no socket (model/README.md).
/// The draft belongs to the thread, in [ThreadViewState], so it survives a
/// reconnect and a screen that rebuilds.
class ComposerModel extends ChangeNotifier {
  ComposerModel({
    required Future<void> Function(String text) prompt,
    required Future<void> Function(String text) steer,
    required Future<void> Function() cancel,
    required Future<void> Function(String mode) setMode,
    required Future<AccessState> Function() access,
    required this.view,
    bool running = false,
    bool readOnly = false,
    String? readOnlyReason,
    String permissionMode = 'Ask',
    String? model,
  }) : _prompt = prompt,
       _steer = steer,
       _cancel = cancel,
       _setMode = setMode,
       _access = access,
       _running = running,
       _readOnly = readOnly,
       _readOnlyReason = readOnlyReason,
       _permissionMode = permissionMode,
       _model = model;

  /// The composer that drives one session's own routes.
  factory ComposerModel.forSession(
    BridgeClient client,
    String sessionId, {
    required ThreadViewState view,
    bool running = false,
  }) => ComposerModel(
    prompt: (text) => client.prompt(sessionId, PromptRequest(text: text)),
    steer: (text) => client.steer(sessionId, SteerRequest(text: text)),
    cancel: () => client.cancel(sessionId),
    setMode: (mode) => client.setMode(sessionId, ModeRequest(mode: mode)),
    access: () => client.access(sessionId),
    view: view,
    running: running,
  );

  /// The thread whose draft this composer keeps.
  final ThreadViewState view;

  final Future<void> Function(String text) _prompt;
  final Future<void> Function(String text) _steer;
  final Future<void> Function() _cancel;
  final Future<void> Function(String mode) _setMode;
  final Future<AccessState> Function() _access;

  bool _running;
  bool _cancellationRequested = false;
  bool _readOnly;
  String? _readOnlyReason;
  String _permissionMode;
  String? _model;
  String? _failure;

  /// Whether a turn is running: a send steers, and Stop is offered.
  bool get running => _running;

  /// Whether a stop has been asked for and the session has not yet reported
  /// that the turn ended.
  ///
  /// Cancelling is a request, so Steer and Stop stay offered and the composer
  /// says it is waiting. Only [setRunning] with the session's own report
  /// confirms the stop.
  bool get cancellationRequested => _cancellationRequested;

  /// Whether the session can only be read. The composer is then one line
  /// saying why (spec decision 12).
  bool get readOnly => _readOnly;

  /// Why the session is read-only, in words.
  String? get readOnlyReason => _readOnlyReason;

  /// The permission mode chip's label.
  String get permissionMode => _permissionMode;

  /// The model chip's label, or null for no chip.
  String? get model => _model;

  /// Why the last send failed, in the bridge's words.
  String? get failure => _failure;

  /// Takes the running flag from what the session reports, so a screen that
  /// reopens a live thread offers Steer and Stop.
  ///
  /// This is the only confirmation of a stop: taking the session's report
  /// clears any cancellation the composer asked for.
  void setRunning(bool running) {
    // A report that the turn is still running does not confirm a stop: only
    // the session saying it ended clears the request. An unchanged false
    // report may still be that confirmation, so it is not short-circuited.
    if (_running == running && (running || !_cancellationRequested)) return;
    _running = running;
    if (!running) _cancellationRequested = false;
    notifyListeners();
  }

  /// Sends the draft: a steer while a turn runs, a prompt otherwise.
  ///
  /// A prompt starts a turn, so the composer becomes a controller of it; the
  /// draft is cleared only once the bridge accepted it.
  Future<void> send(String text) async {
    final message = text.trim();
    if (message.isEmpty || _readOnly) return;
    _failure = null;
    try {
      if (_running) {
        await _steer(message);
      } else {
        await _prompt(message);
        _running = true;
        // A new turn is not the cancelled one: no request is pending on it.
        _cancellationRequested = false;
      }
      view.draft = '';
    } catch (error) {
      _failure = _words(error);
    }
    notifyListeners();
  }

  /// Asks the session to stop the running turn.
  ///
  /// Cancelling is a request, not a confirmed stop: the composer says it is
  /// waiting and keeps Steer and Stop until the session reports the turn's
  /// state through [setRunning].
  Future<void> stop() async {
    _failure = null;
    try {
      await _cancel();
      _cancellationRequested = true;
    } catch (error) {
      _failure = _words(error);
    }
    notifyListeners();
  }

  /// Switches the permission mode and takes what the session committed.
  Future<void> selectMode(String mode) async {
    if (mode == _permissionMode) return;
    _failure = null;
    final before = _permissionMode;
    _permissionMode = mode;
    notifyListeners();
    try {
      await _setMode(mode);
      await refreshAccess();
    } catch (error) {
      _permissionMode = before;
      _failure = _words(error);
      notifyListeners();
    }
  }

  /// Reads the session's committed access tuple, which the mode chip names.
  ///
  /// The mode never makes the composer read-only: a session in plan or default
  /// mode can still be prompted, and the bridge asks for approval where it
  /// needs one, so this only reports the mode.
  ///
  /// It is started unawaited when a thread opens and at each turn boundary, so
  /// the answer can arrive after the thread's composer is gone: a disposed
  /// model is left alone rather than notified.
  Future<void> refreshAccess() async {
    try {
      final state = await _access();
      if (_disposed) return;
      _permissionMode = switch (state.mode) {
        'ask' => 'Ask',
        'act' => state.auto ? 'Act · Auto' : 'Act',
        _ => _permissionMode,
      };
    } catch (error) {
      if (_disposed) return;
      _failure = _words(error);
    }
    if (_disposed) return;
    notifyListeners();
  }

  /// Whether [dispose] has run: a read this model started may still land.
  bool _disposed = false;

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }

  static String _words(Object error) => describeFailure(error);
}
