// The routes are named for what they do and their fields stay private, so the
// parameters cannot be initializing formals: Dart has no private named
// parameter.
// ignore_for_file: prefer_initializing_formals

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
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
  bool _readOnly;
  String? _readOnlyReason;
  String _permissionMode;
  String? _model;
  String? _failure;

  /// Whether a turn is running: a send steers, and Stop is offered.
  bool get running => _running;

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
  void setRunning(bool running) {
    if (_running == running) return;
    _running = running;
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
      }
      view.draft = '';
    } catch (error) {
      _failure = _words(error);
    }
    notifyListeners();
  }

  /// Stops the running turn.
  Future<void> stop() async {
    _failure = null;
    try {
      await _cancel();
      _running = false;
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

  /// Reads the session's committed access tuple, which decides whether the
  /// composer is read-only.
  ///
  /// A session whose mode is `ask` has granted no editing, so it is read-only
  /// here; the reason names the mode rather than a failure.
  Future<void> refreshAccess() async {
    try {
      final state = await _access();
      _readOnly = state.mode != 'act';
      _readOnlyReason = _readOnly
          ? 'This session is in ${state.mode} mode: it can be read but not driven.'
          : null;
      _permissionMode = state.mode == 'act' ? 'Act' : 'Ask';
    } catch (error) {
      _failure = _words(error);
    }
    notifyListeners();
  }

  static String _words(Object error) =>
      error is Exception ? error.toString() : '$error';
}
