import 'dart:async';

import 'package:flutter/gestures.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';

import '../../model/selection.dart';
import '../../model/stack_store.dart';
import '../kit/kit.dart';

/// The selection one transcript is making, and the rows it can hit-test.
///
/// A row joins by wrapping itself in a [SelectableRow]; the layer hit-tests
/// only what is built, and the anchors it produces are model positions, so a
/// selection made across built rows still copies the rows between them.
class SelectionLayer extends StatefulWidget {
  const SelectionLayer({
    super.key,
    required this.selection,
    required this.store,
    required this.order,
    required this.controller,
    required this.child,
  });

  /// The selection this layer makes.
  final TranscriptSelection selection;

  /// The store the copied text comes from.
  final StackStore store;

  /// The node ids the list is drawing, in order.
  final List<String> order;

  /// The list's scroll controller, which the edge auto-scroll drives.
  final ScrollController controller;

  /// The list.
  final Widget child;

  @override
  State<SelectionLayer> createState() => SelectionLayerState();
}

/// The layer's state: the row registry, the pointer handling and the copy.
///
/// It is public so a test can drive a selection without a gesture, and so the
/// thread screen can copy what the keys select.
class SelectionLayerState extends State<SelectionLayer> {
  final keys = <String, GlobalKey>{};

  Timer? _auto;
  Offset? _last;
  int _clicks = 0;
  DateTime? _lastClickAt;
  Offset? _lastClickPosition;
  OverlayEntry? _menu;

  TranscriptSelection get selection => widget.selection;

  /// Registers a built row, so the layer can hit-test it.
  void register(String nodeId, GlobalKey key) => keys[nodeId] = key;

  /// Forgets a row the list no longer builds.
  void unregister(String nodeId, GlobalKey key) {
    if (keys[nodeId] == key) keys.remove(nodeId);
  }

  @override
  void dispose() {
    _auto?.cancel();
    _menu?.remove();
    super.dispose();
  }

  /// The text the selection would copy.
  String selectedText() => selection.copy(widget.store, widget.order);

  /// Puts the selection on the clipboard.
  Future<void> copy() async {
    final text = selectedText();
    if (text.isEmpty) return;
    await Clipboard.setData(ClipboardData(text: text));
  }

  /// The anchor at a global point, hit-testing the built rows. A point above
  /// or below them clamps to the nearest row, which is what a drag past the
  /// edge should do.
  TextAnchor? anchorAt(Offset global) {
    String? bestId;
    var bestDistance = double.infinity;
    RenderParagraph? bestParagraph;
    for (final entry in keys.entries) {
      final context = entry.value.currentContext;
      if (context == null) continue;
      final box = context.findRenderObject() as RenderBox?;
      if (box == null || !box.attached) continue;
      final top = box.localToGlobal(Offset.zero).dy;
      final bottom = top + box.size.height;
      final distance = global.dy < top
          ? top - global.dy
          : (global.dy > bottom ? global.dy - bottom : 0.0);
      if (distance >= bestDistance) continue;
      final paragraph = _paragraphIn(box);
      if (paragraph == null) continue;
      bestDistance = distance;
      bestId = entry.key;
      bestParagraph = paragraph;
    }
    if (bestId == null || bestParagraph == null) return null;
    final local = bestParagraph.globalToLocal(global);
    final clamped = Offset(
      local.dx,
      local.dy.clamp(0.0, bestParagraph.size.height),
    );
    return TextAnchor(
      bestId,
      bestParagraph.getPositionForOffset(clamped).offset,
    );
  }

  /// The first laid-out paragraph under [root].
  static RenderParagraph? _paragraphIn(RenderObject root) {
    RenderParagraph? found;
    void visit(RenderObject node) {
      if (found != null) return;
      if (node is RenderParagraph) {
        found = node;
        return;
      }
      node.visitChildren(visit);
    }

    visit(root);
    return found;
  }

  void _down(PointerDownEvent event) {
    if (event.buttons == kSecondaryButton) {
      _showMenu(event.position);
      return;
    }
    // A mouse reports which button it pressed; a touch or trackpad tap
    // reports none. Anything but a reported non-primary button starts a
    // selection.
    if (event.kind == PointerDeviceKind.mouse &&
        event.buttons != 0 &&
        (event.buttons & kPrimaryButton) == 0) {
      return;
    }
    final at = anchorAt(event.position);
    if (at == null) return;
    _last = event.position;
    final now = DateTime.now();
    final previous = _lastClickAt;
    final samePlace =
        _lastClickPosition != null &&
        (event.position - _lastClickPosition!).distance < 4;
    _clicks =
        previous != null &&
            now.difference(previous) < const Duration(milliseconds: 400) &&
            samePlace
        ? _clicks + 1
        : 1;
    _lastClickAt = now;
    _lastClickPosition = event.position;
    if (HardwareKeyboard.instance.isShiftPressed) {
      selection.extend(at);
      return;
    }
    if (_clicks == 2) {
      selection.selectWord(at);
      return;
    }
    if (_clicks >= 3) {
      selection.selectLine(at);
      return;
    }
    selection.begin(at);
  }

  void _move(PointerMoveEvent event) {
    if (_last == null) return;
    _last = event.position;
    final at = anchorAt(event.position);
    if (at != null) selection.extend(at);
    _auto ??= Timer.periodic(const Duration(milliseconds: 16), (_) => _tick());
  }

  void _up(PointerUpEvent event) {
    _last = null;
    _auto?.cancel();
    _auto = null;
  }

  /// Scrolls while the pointer is held near an edge, taking the selection
  /// with it.
  void _tick() {
    final last = _last;
    if (last == null || !widget.controller.hasClients) return;
    final viewport = context.findRenderObject() as RenderBox?;
    if (viewport == null) return;
    final top = viewport.localToGlobal(Offset.zero).dy;
    final bottom = top + viewport.size.height;
    var delta = 0.0;
    if (last.dy > bottom - Sizes.touch) {
      delta = (last.dy - (bottom - Sizes.touch)).clamp(Space.s2, Space.s32);
    } else if (last.dy < top + Sizes.touch) {
      delta = -((top + Sizes.touch) - last.dy).clamp(Space.s2, Space.s32);
    }
    if (delta == 0) return;
    final position = widget.controller.position;
    widget.controller.jumpTo(
      (position.pixels + delta).clamp(0.0, position.maxScrollExtent),
    );
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      final at = anchorAt(last);
      if (at != null) selection.extend(at);
    });
  }

  /// The Copy menu where the pointer was. It is the kit's `Box` and `Btn`: a
  /// popover anchored to a point rather than to a control.
  void _showMenu(Offset global) {
    final overlay = Overlay.of(context);
    _menu?.remove();
    final entry = OverlayEntry(
      builder: (context) => Stack(
        children: [
          Positioned.fill(
            child: GestureDetector(
              behavior: HitTestBehavior.opaque,
              onTap: _hideMenu,
            ),
          ),
          Positioned(
            left: global.dx,
            top: global.dy,
            child: Box(
              child: Btn(
                'Copy',
                onTap: () {
                  unawaited(copy());
                  _hideMenu();
                },
              ),
            ),
          ),
        ],
      ),
    );
    _menu = entry;
    overlay.insert(entry);
  }

  void _hideMenu() {
    _menu?.remove();
    _menu = null;
  }

  @override
  Widget build(BuildContext context) {
    return SelectionScope(
      state: this,
      child: CallbackShortcuts(
        bindings: {
          const SingleActivator(LogicalKeyboardKey.keyC, meta: true): () =>
              unawaited(copy()),
          const SingleActivator(LogicalKeyboardKey.keyC, control: true): () =>
              unawaited(copy()),
          const SingleActivator(LogicalKeyboardKey.keyA, meta: true): () =>
              selection.selectAll(widget.order),
          const SingleActivator(LogicalKeyboardKey.keyA, control: true): () =>
              selection.selectAll(widget.order),
        },
        child: Listener(
          onPointerDown: _down,
          onPointerMove: _move,
          onPointerUp: _up,
          child: widget.child,
        ),
      ),
    );
  }
}

/// The layer the rows below a transcript register with.
class SelectionScope extends InheritedWidget {
  const SelectionScope({super.key, required this.state, required super.child});

  final SelectionLayerState state;

  static SelectionLayerState? maybeOf(BuildContext context) =>
      context.dependOnInheritedWidgetOfExactType<SelectionScope>()?.state;

  @override
  bool updateShouldNotify(SelectionScope old) => old.state != state;
}

/// One row that a selection can pass through: it registers itself for
/// hit-testing and paints its own share of the highlight.
///
/// It is transparent to layout: the child is drawn exactly where it would be
/// without it.
class SelectableRow extends StatefulWidget {
  const SelectableRow({super.key, required this.nodeId, required this.child});

  /// The node whose plain text the row shows.
  final String nodeId;

  /// The row.
  final Widget child;

  @override
  State<SelectableRow> createState() => _SelectableRowState();
}

class _SelectableRowState extends State<SelectableRow> {
  final _key = GlobalKey();
  SelectionLayerState? _scope;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final scope = SelectionScope.maybeOf(context);
    if (scope == _scope) return;
    _scope?.unregister(widget.nodeId, _key);
    _scope = scope;
    scope?.register(widget.nodeId, _key);
  }

  @override
  void dispose() {
    _scope?.unregister(widget.nodeId, _key);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final scope = _scope;
    if (scope == null) return widget.child;
    return AnimatedBuilder(
      animation: scope.selection,
      builder: (context, child) => CustomPaint(
        key: _key,
        painter: _HighlightPainter(
          context: context,
          selection: scope.selection,
          nodeId: widget.nodeId,
          order: scope.widget.order,
          text: scope.widget.store.plainText(widget.nodeId),
        ),
        child: child,
      ),
      child: widget.child,
    );
  }
}

/// Paints the selection's share of one row behind its text, from the boxes
/// the row's own paragraph reports.
class _HighlightPainter extends CustomPainter {
  _HighlightPainter({
    required this.context,
    required this.selection,
    required this.nodeId,
    required this.order,
    required this.text,
  });

  /// The row's build context, where the paragraph walk starts.
  final BuildContext context;

  final TranscriptSelection selection;
  final String nodeId;
  final List<String> order;
  final String text;

  @override
  void paint(Canvas canvas, Size size) {
    final span = selection.spanFor(nodeId, order, text);
    if (span == null) return;
    final (start, end) = span;
    final root = context.findRenderObject();
    if (root is! RenderBox) return;
    final origin = root.localToGlobal(Offset.zero);
    var offset = 0;
    final paragraphs = <RenderParagraph>[];
    void visit(RenderObject node) {
      if (node is RenderParagraph) paragraphs.add(node);
      node.visitChildren(visit);
    }

    visit(root);
    for (final paragraph in paragraphs) {
      final length = paragraph.text.toPlainText().length;
      final from = (start - offset).clamp(0, length);
      final to = (end - offset).clamp(0, length);
      if (to > from) {
        final boxes = paragraph.getBoxesForSelection(
          TextSelection(baseOffset: from, extentOffset: to),
        );
        final paint = Paint()
          ..color = Ember.resolve(
            MediaQuery.maybePlatformBrightnessOf(context) ?? Brightness.light,
          ).selected;
        for (final box in boxes) {
          final topLeft = paragraph.localToGlobal(box.toRect().topLeft);
          canvas.drawRect(
            Rect.fromLTWH(
              topLeft.dx - origin.dx,
              topLeft.dy - origin.dy,
              box.toRect().width,
              box.toRect().height,
            ),
            paint,
          );
        }
      }
      offset += length + 1;
    }
  }

  @override
  bool shouldRepaint(_HighlightPainter old) =>
      old.selection.base != selection.base ||
      old.selection.extent != selection.extent ||
      old.text != text;
}
