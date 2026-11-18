import 'dart:async';

import '../client/bridge_client.dart';
import 'changes_model.dart';
import 'files_model.dart';
import 'inspect_model.dart';
import 'preview_model.dart';
import 'terminal_session.dart';

/// The five models an agent thread's inspector tabs read, owned by one object
/// so a rebuild does not rebuild them and closing the thread releases them.
class ThreadInspectorModels {
  ThreadInspectorModels(
    BridgeClient client, {
    required String agentId,
    required String sessionId,
    String root = '',
    int subagentId = 0,
  }) : changes = ChangesModel(client, agentId: agentId, sessionId: sessionId),
       files = FilesModel(client, agentId: agentId, root: root),
       terminal = TerminalSession(client, agentId: agentId),
       preview = PreviewModel(client, agentId: agentId),
       inspect = InspectModel(
         client,
         sessionId: sessionId,
         subagentId: subagentId,
       );

  final ChangesModel changes;
  final FilesModel files;
  final TerminalSession terminal;
  final PreviewModel preview;
  final InspectModel inspect;

  bool _started = false;
  bool _disposed = false;

  /// Starts the reads the holder owns when a thread opens. Files and Terminal
  /// start from their own tab widgets, and Preview has no port source in this
  /// build, so it draws the state it is in rather than a fabricated one.
  void start() {
    if (_started) return;
    _started = true;
    unawaited(changes.refresh());
    unawaited(inspect.loadRequest());
  }

  /// Releases all five. It is called when the thread place closes, and a
  /// second call does nothing: a screen that closes twice is not a fault.
  void dispose() {
    if (_disposed) return;
    _disposed = true;
    changes.dispose();
    files.dispose();
    terminal.dispose();
    preview.dispose();
    inspect.dispose();
  }
}
