import '../../model/inspector_state.dart';
import '../../model/thread_inspector_models.dart';
import '../kit/kit.dart';
import 'changes_tab.dart';
import 'files_tab.dart';
import 'inspect_tab.dart';
import 'preview_tab.dart';
import 'terminal_tab.dart';

/// What an inspector tab draws for an agent thread: the tab widget, given the
/// model it reads. `ui/` may name these; only `main.dart` may build them.
Widget inspectorBody(
  ThreadInspectorModels models,
  InspectorTab tab, {
  StateKind? state,
  String? shipped,
}) => switch (tab) {
  InspectorTab.changes => ChangesTab(
    model: models.changes,
    state: state,
    shipped: shipped,
  ),
  InspectorTab.files => FilesTab(model: models.files, state: state),
  InspectorTab.terminal => TerminalTab(session: models.terminal, state: state),
  InspectorTab.preview => PreviewTab(model: models.preview, state: state),
  InspectorTab.inspect => InspectTab(model: models.inspect, state: state),
  // An agent thread's set never asks for one of F6's tabs, and a shrink is
  // honest where a placeholder would invent a task.
  _ => const SizedBox.shrink(),
};
