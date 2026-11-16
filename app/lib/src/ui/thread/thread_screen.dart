import 'package:flutter/services.dart';

import '../../model/stack_store.dart';
import '../../model/thread_ref.dart' as model;
import '../../model/thread_view_state.dart';
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
  ThreadKey('i', 'inspector', 'The inspector arrives in F4'),
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
    this.state,
    this.onOpenNode,
    this.onShare,
    this.onStop,
    this.onNewWork,
    this.onCopyRow,
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

  /// Copies the cursor row. Selection provides the real copy in Task 5; this
  /// is the key's action until then.
  final void Function(String nodeId)? onCopyRow;

  @override
  State<ThreadScreen> createState() => _ThreadScreenState();
}

class _ThreadScreenState extends State<ThreadScreen> {
  final _focus = FocusNode(debugLabel: 'thread');

  @override
  void dispose() {
    _focus.dispose();
    super.dispose();
  }

  List<TranscriptRow> get _rows => flattenRows(widget.store, widget.view);

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
    // `i` is reserved for the inspector (F4): it is bound and does nothing,
    // so a person pressing it is not surprised by a different action.
    return KeyEventResult.ignored;
  }

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    final content = state == null
        ? _thread(context)
        : _stateOnly(context, state);
    return Focus(
      focusNode: _focus,
      autofocus: true,
      onKeyEvent: _onKey,
      child: content,
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
        ),
        const Hairline(),
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
                  onOpenNode: widget.onOpenNode,
                ),
        ),
        if (widget.composer != null)
          Padding(
            padding: const EdgeInsets.fromLTRB(
              Layout.pageSide,
              Space.s8,
              Layout.pageSide,
              Space.s16,
            ),
            child: widget.composer,
          ),
      ],
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
