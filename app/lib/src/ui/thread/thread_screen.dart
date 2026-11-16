import 'dart:async';

import 'package:flutter/services.dart';

import '../../model/inbox_model.dart' as asks_model;
import '../../model/inspector_state.dart';
import '../../model/selection.dart';
import '../../model/stack_store.dart';
import '../../model/thread_ref.dart' as model;
import '../../model/thread_view_state.dart';
import '../inspector/inspector.dart';
import '../kit/kit.dart';
import 'thread_header.dart';
import 'transcript.dart';

/// One browse key: what it does, and the words its tooltip uses.
@immutable
class ThreadKey {
  const ThreadKey(this.key, this.action, this.description);

  /// The key as the spec writes it: `Esc`, `j`, `Enter`, `⌘N`.
  final String key;

  /// What it does, in one word for the action it invokes.
  final String action;

  /// What a tooltip says about it. One map, so a tooltip and the behaviour
  /// cannot disagree.
  final String description;
}

/// Every key the thread screen answers (spec decision 7), in one place. The
/// screen reads it to bind its keys; anything that explains a key reads the
/// same entry.
const threadKeys = <ThreadKey>[
  ThreadKey('Esc', 'browse', 'Leave the cursor where it is'),
  ThreadKey('j', 'down', 'Next row'),
  ThreadKey('k', 'up', 'Previous row'),
  ThreadKey('J', 'downTen', 'Ten rows down'),
  ThreadKey('K', 'upTen', 'Ten rows up'),
  ThreadKey('g', 'first', 'First row'),
  ThreadKey('G', 'last', 'Last row'),
  ThreadKey('Enter', 'density', 'Cycle outline, comfortable and full'),
  ThreadKey('z', 'fold', 'Fold or unfold the cursor row'),
  ThreadKey('y', 'copy', 'Copy the cursor row'),
  ThreadKey('⌘C', 'copySelection', 'Copy the selection'),
  ThreadKey('⌘A', 'selectAll', 'Select every row'),
  ThreadKey('i', 'inspector', 'Show or close the inspector'),
  ThreadKey('⌘\\', 'inspectorWidth', 'The inspector: docked, expanded, closed'),
  ThreadKey('⌘N', 'newWork', 'Start a new agent'),
];

/// What [key] says about itself, for a tooltip.
String threadKeyTip(String key) {
  for (final entry in threadKeys) {
    if (entry.key == key) return '${entry.description}  ·  ${entry.key}';
  }
  return key;
}

/// A thread (design-system.md §5, spec decisions 3–10): its header, its live
/// transcript, and the field that drives it.
///
/// It is an embeddable widget: it takes the thread's store and view state and
/// draws them, so F6 can put it beside an inspector and a test can pump it
/// with fixture data. It never fetches; the store does.
class ThreadScreen extends StatefulWidget {
  const ThreadScreen({
    super.key,
    required this.thread,
    required this.store,
    required this.view,
    this.branch,
    this.environment,
    this.composer,
    this.inspector,
    this.inspectorBody,
    this.state,
    this.onOpenNode,
    this.onShare,
    this.onStop,
    this.onNewWork,
    this.onCopyRow,
    this.selection,
    this.asks = const [],
    this.onAnswer,
    this.now,
  });

  /// The thread this screen is showing.
  final model.ThreadRef thread;

  /// The session's tree.
  final StackStore store;

  /// The user's choices for this thread.
  final ThreadViewState view;

  /// The branch the thread works on, when the fleet reports one.
  final String? branch;

  /// The environment it runs in, when the fleet reports one.
  final String? environment;

  /// The composer, in Task 6. Null draws the transcript alone.
  final Widget? composer;

  /// The inspector beside the transcript (F4). Null draws the transcript
  /// alone; the tab set comes from the thread's kind.
  final InspectorState? inspector;

  /// What each inspector tab draws. Null draws the frame's placeholder: a
  /// tab whose screen a later task brings says so (§7).
  final Widget Function(InspectorTab tab)? inspectorBody;

  /// The transcript's data state, when it is not ready (§7).
  final StateKind? state;

  /// Opens a node's detail.
  final void Function(String nodeId)? onOpenNode;

  /// Shares a read-only status link; null until F7.
  final VoidCallback? onShare;

  /// Stops the thread.
  final VoidCallback? onStop;

  /// Opens the new-work dialog (`⌘N`).
  final VoidCallback? onNewWork;

  /// Copies the cursor row (`y`). The free selection copies with `⌘C`, which
  /// this screen owns so one instance serves both.
  final void Function(String nodeId)? onCopyRow;

  /// The text selection across the transcript. Null gives the screen its own,
  /// which it disposes with itself.
  final TranscriptSelection? selection;

  /// The asks waiting in this thread; each is pinned at its step.
  final List<asks_model.Ask> asks;

  /// Answers an ask where it is pinned.
  final void Function(asks_model.Ask ask, asks_model.AskAction action)?
  onAnswer;

  /// What a waiting time is measured against.
  final DateTime? now;

  @override
  State<ThreadScreen> createState() => _ThreadScreenState();
}

class _ThreadScreenState extends State<ThreadScreen> {
  final _focus = FocusNode(debugLabel: 'thread');
  late final TranscriptSelection _selection =
      widget.selection ?? TranscriptSelection(textOf: widget.store.plainText);

  @override
  void dispose() {
    _focus.dispose();
    if (widget.selection == null) _selection.dispose();
    super.dispose();
  }

  List<TranscriptRow> get _rows => flattenRows(widget.store, widget.view);

  /// The node ids the list draws, in order: what a selection is expressed in.
  List<String> get _order => [for (final row in _rows) row.id];

  /// The cursor's position in the row list, or null when nothing is cursored.
  int? get _cursorIndex {
    final cursor = widget.view.cursor;
    if (cursor == null) return null;
    final index = _rows.indexWhere((row) => row.id == cursor);
    return index < 0 ? null : index;
  }

  void _moveCursor(int delta) {
    final rows = _rows;
    if (rows.isEmpty) return;
    final current = _cursorIndex;
    final next = current == null
        ? (delta > 0 ? 0 : rows.length - 1)
        : (current + delta).clamp(0, rows.length - 1);
    widget.view.cursor = rows[next].id;
  }

  KeyEventResult _onKey(FocusNode node, KeyEvent event) {
    if (event is KeyUpEvent) return KeyEventResult.ignored;
    final key = event.logicalKey;
    final shift = HardwareKeyboard.instance.isShiftPressed;
    final meta = HardwareKeyboard.instance.isMetaPressed;
    final control = HardwareKeyboard.instance.isControlPressed;

    if ((meta || control) && key == LogicalKeyboardKey.keyN) {
      widget.onNewWork?.call();
      return KeyEventResult.handled;
    }
    if ((meta || control) && key == LogicalKeyboardKey.keyC) {
      final text = _selection.copy(widget.store, _order);
      if (text.isNotEmpty) {
        unawaited(Clipboard.setData(ClipboardData(text: text)));
      }
      return KeyEventResult.handled;
    }
    if ((meta || control) && key == LogicalKeyboardKey.keyA) {
      _selection.selectAll(_order);
      return KeyEventResult.handled;
    }
    if ((meta || control) && key == LogicalKeyboardKey.backslash) {
      widget.inspector?.cycleWidth();
      return KeyEventResult.handled;
    }
    if (!meta && !control && key == LogicalKeyboardKey.keyI) {
      final inspector = widget.inspector;
      if (inspector != null) {
        inspector.setWidth(
          inspector.width == InspectorWidth.collapsed
              ? InspectorWidth.docked
              : InspectorWidth.collapsed,
        );
        return KeyEventResult.handled;
      }
    }
    if (key == LogicalKeyboardKey.escape) {
      // Leaving browse mode leaves the cursor where it is; the next `j` or
      // `k` starts again from there.
      widget.view.cursor = null;
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.keyJ) {
      _moveCursor(shift ? 10 : 1);
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.keyK) {
      _moveCursor(shift ? -10 : -1);
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.keyG) {
      final rows = _rows;
      if (rows.isNotEmpty) {
        widget.view.cursor = shift ? rows.last.id : rows.first.id;
      }
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.enter ||
        key == LogicalKeyboardKey.numpadEnter) {
      widget.view.cycleDensity();
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.keyZ) {
      final cursor = widget.view.cursor;
      if (cursor != null) widget.view.toggleFold(cursor);
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.keyY) {
      final cursor = widget.view.cursor;
      if (cursor != null) widget.onCopyRow?.call(cursor);
      return KeyEventResult.handled;
    }
    return KeyEventResult.ignored;
  }

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    final inspector = widget.inspector;
    Widget content(BuildContext context) =>
        state == null ? _thread(context) : _stateOnly(context, state);
    return Focus(
      focusNode: _focus,
      autofocus: true,
      onKeyEvent: _onKey,
      child: inspector == null
          ? content(context)
          : AnimatedBuilder(
              animation: inspector,
              builder: (context, _) => content(context),
            ),
    );
  }

  Widget _thread(BuildContext context) {
    final store = widget.store;
    if (store.status == StackStatus.loading) {
      return const StateMessage.loading(lines: 4);
    }
    if (store.status == StackStatus.unsupported) {
      return StateMessage.unavailable(
        host: widget.thread.host,
        lastSeen: 'now',
      );
    }
    if (store.status == StackStatus.error) {
      return StateMessage.error(
        message: 'The transcript could not be read.',
        retry: StateAction('Try again', onTap: store.refresh),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        ThreadHeader(
          thread: widget.thread,
          branch: widget.branch,
          environment: widget.environment,
          onShare: widget.onShare,
          onStop: widget.onStop,
          // Below the medium breakpoint the inspector is a sheet, opened
          // here (decision 3).
          onInspect: _inspectorBelowBreakpoint ? _openInspector : null,
        ),
        const Hairline(),
        Expanded(child: _body(context, store)),
      ],
    );
  }

  /// Every tab this thread kind has (decision 2). F6 brings the work item's
  /// and the plan run's sets; until then those kinds have none.
  List<InspectorTab> get _inspectorTabs => switch (widget.thread.kind) {
    model.ThreadKind.agent => const [
      InspectorTab.changes,
      InspectorTab.files,
      InspectorTab.terminal,
      InspectorTab.preview,
      InspectorTab.inspect,
    ],
    model.ThreadKind.workItem || model.ThreadKind.planRun => const [],
  };

  /// Whether the inspector is a sheet below the medium breakpoint.
  bool get _inspectorBelowBreakpoint =>
      widget.inspector != null &&
      _inspectorTabs.isNotEmpty &&
      MediaQuery.sizeOf(context).width < Layout.medium;

  /// The transcript, or its outline beside an expanded inspector, with the
  /// inspector at the right (decision 3).
  Widget _body(BuildContext context, StackStore store) {
    final inspector = widget.inspector;
    if (inspector == null || _inspectorTabs.isEmpty) {
      return _conversation(store);
    }
    if (MediaQuery.sizeOf(context).width < Layout.medium) {
      return _conversation(store);
    }
    final expanded = inspector.width == InspectorWidth.expanded;
    return Row(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        // Expanded takes the window and narrows the conversation to step
        // headlines; docked keeps the conversation and gives the inspector
        // its column.
        if (expanded)
          SizedBox(width: Layout.inspector, child: _outline(context, store))
        else
          Expanded(child: _conversation(store)),
        const Hairline.vertical(),
        if (expanded)
          Expanded(child: _panel(context))
        else
          SizedBox(
            width: inspector.width == InspectorWidth.collapsed
                ? Sizes.row
                : Layout.inspector,
            child: _panel(context),
          ),
      ],
    );
  }

  /// The transcript and the composer: what the thread is for.
  Widget _conversation(StackStore store) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      Expanded(
        child: _rowsOf(store).isEmpty
            ? StateMessage.empty(
                message: 'Nothing has happened in this thread yet.',
                action: StateAction(
                  'Start an agent',
                  onTap: widget.onNewWork ?? () {},
                ),
              )
            : Transcript(
                store: store,
                view: widget.view,
                selection: _selection,
                asks: widget.asks,
                onAnswer: widget.onAnswer,
                now: widget.now,
                onOpenNode: _openNode,
              ),
      ),
      _composer(),
    ],
  );

  /// The conversation narrowed to step headlines, what an expanded inspector
  /// leaves beside it (decision 3). Selecting one inspects it.
  Widget _outline(BuildContext context, StackStore store) {
    // Each turn's steps, numbered as the transcript numbers them.
    final steps = <(String, String, String)>[];
    for (final root in store.roots) {
      var n = 0;
      for (final child in store.node(root)?.children ?? const <String>[]) {
        final step = store.node(child)?.step;
        if (step == null) continue;
        n++;
        steps.add((child, '$n', step.headline));
      }
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Expanded(
          child: steps.isEmpty
              ? StateMessage.empty(
                  message: 'No steps to outline yet.',
                  action: StateAction(
                    'Start an agent',
                    onTap: widget.onNewWork ?? () {},
                  ),
                )
              : ListView.builder(
                  padding: const EdgeInsets.all(Space.s12),
                  itemCount: steps.length,
                  itemBuilder: (context, i) {
                    final (id, number, headline) = steps[i];
                    return StepHeader(
                      number: number,
                      headline: headline,
                      live: store.node(id)?.live ?? false,
                      onTap: () => _openNode(id),
                    );
                  },
                ),
        ),
        _composer(),
      ],
    );
  }

  /// The composer at the foot of the conversation, when the host gives one.
  Widget _composer() {
    final composer = widget.composer;
    if (composer == null) return const SizedBox.shrink();
    return Padding(
      padding: const EdgeInsets.fromLTRB(
        Layout.pageSide,
        Space.s8,
        Layout.pageSide,
        Space.s16,
      ),
      child: composer,
    );
  }

  /// The inspector column, as the host's bodies and this screens's tab set
  /// build it.
  Widget _panel(BuildContext context) {
    final inspector = widget.inspector!;
    return Inspector(
      state: inspector,
      tabs: _inspectorTabs,
      body: _inspectorBodyFor,
      selection: _selectionLabel,
      onTab: inspector.chooseTab,
      onCycleWidth: inspector.cycleWidth,
    );
  }

  /// What an inspector tab draws: the host's, or the placeholder that says
  /// which task brings it (§7).
  Widget _inspectorBodyFor(InspectorTab tab) {
    final build = widget.inspectorBody;
    if (build != null) return build(tab);
    return StateMessage.planned(
      name: Inspector.labelFor(tab),
      note: 'This tab arrives in a later task of F4',
    );
  }

  /// The name of the node the inspector is following, or null while it
  /// follows the live end.
  String? get _selectionLabel {
    final id = widget.inspector?.selectedNode;
    if (id == null) return null;
    final node = widget.store.node(id);
    if (node == null) return id;
    final name =
        node.step?.headline ??
        node.tool?.display ??
        node.message?.content ??
        node.completion?.report ??
        '';
    final line = name.split('\n').first.trim();
    return line.isEmpty ? id : line;
  }

  /// Opens a node: the inspector follows it, and the host may open it too.
  void _openNode(String nodeId) {
    widget.inspector?.select(nodeId);
    widget.onOpenNode?.call(nodeId);
  }

  /// Opens the inspector over the conversation, below the medium breakpoint.
  Future<void> _openInspector() async {
    if (widget.inspector == null || _inspectorTabs.isEmpty) return;
    await Sheet.show<void>(
      context,
      Sheet(
        title: 'Inspector',
        child: SizedBox(
          width: Layout.inspector,
          height: Layout.inspector,
          child: _panel(context),
        ),
      ),
    );
  }

  List<TranscriptRow> _rowsOf(StackStore store) =>
      flattenRows(store, widget.view);

  Widget _stateOnly(BuildContext context, StateKind state) => switch (state) {
    StateKind.loading => const StateMessage.loading(lines: 4),
    StateKind.empty => StateMessage.empty(
      message: 'Nothing has happened in this thread yet.',
      action: StateAction('Start an agent', onTap: () {}),
    ),
    StateKind.stale => const StateMessage.stale(
      age: '4m',
      detail: 'The transcript may be behind',
    ),
    StateKind.error => StateMessage.error(
      message: 'The transcript could not be read.',
      retry: StateAction('Try again', onTap: () {}),
    ),
    StateKind.unavailable => StateMessage.unavailable(
      host: widget.thread.host,
      lastSeen: '4m',
      action: StateAction('Try again', onTap: () {}),
    ),
    StateKind.planned => StateMessage.planned(
      name: widget.thread.title,
      note: 'This thread opens in a later phase',
    ),
  };
}
