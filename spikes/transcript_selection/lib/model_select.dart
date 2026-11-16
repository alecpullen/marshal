import 'dart:async';
import 'package:flutter/gestures.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';

/// A (row, character offset) position in the data model, independent of which
/// rows are currently built.
class Pos implements Comparable<Pos> {
  const Pos(this.row, this.off);
  final int row, off;
  @override
  int compareTo(Pos o) => row != o.row ? row.compareTo(o.row) : off.compareTo(o.off);
}

/// Model-backed selection over a virtualised list: anchors live in the model,
/// rows paint their own highlight, copy is built from the model.
class ModelSelectList extends StatefulWidget {
  const ModelSelectList({super.key, required this.rows, this.controller});
  final List<String> rows; // mutable; the owner appends and rebuilds us
  final ScrollController? controller;
  @override
  State<ModelSelectList> createState() => ModelSelectListState();
}

class ModelSelectListState extends State<ModelSelectList> {
  late final ScrollController sc = widget.controller ?? ScrollController();
  final Map<int, GlobalKey> _keys = {};
  final _viewport = GlobalKey();
  Pos? anchor, focus;
  Timer? _auto;
  Offset? _last;

  GlobalKey _key(int i) => _keys.putIfAbsent(i, () => GlobalKey());

  (Pos, Pos)? get range {
    if (anchor == null || focus == null) return null;
    return anchor!.compareTo(focus!) <= 0 ? (anchor!, focus!) : (focus!, anchor!);
  }

  String get selectedText {
    final r = range;
    if (r == null) return '';
    final (a, b) = r;
    final out = <String>[];
    for (var i = a.row; i <= b.row; i++) {
      final s = widget.rows[i];
      final from = i == a.row ? a.off.clamp(0, s.length) : 0;
      final to = i == b.row ? b.off.clamp(0, s.length) : s.length;
      out.add(s.substring(from, to));
    }
    return out.join('\n');
  }

  void selectAll() => setState(() {
        anchor = const Pos(0, 0);
        focus = Pos(widget.rows.length - 1, widget.rows.last.length);
      });

  Future<void> copy() => Clipboard.setData(ClipboardData(text: selectedText));

  /// Hit-test a global point against the *built* rows; rows above/below the
  /// viewport clamp to the first/last built row.
  Pos? _posAt(Offset global) {
    Pos? best;
    double bestDist = double.infinity;
    for (final e in _keys.entries) {
      final ctx = e.value.currentContext;
      if (ctx == null) continue;
      final box = ctx.findRenderObject() as RenderBox?;
      if (box == null || !box.attached) continue;
      final top = box.localToGlobal(Offset.zero).dy;
      final bottom = top + box.size.height;
      final d = global.dy < top ? top - global.dy : (global.dy > bottom ? global.dy - bottom : 0.0);
      if (d < bestDist) {
        bestDist = d;
        final para = _paragraph(box);
        if (para == null) continue;
        final local = para.globalToLocal(global);
        final clamped = Offset(local.dx, local.dy.clamp(0.0, para.size.height - 0.01));
        best = Pos(e.key, para.getPositionForOffset(clamped).offset);
      }
    }
    return best;
  }

  RenderParagraph? _paragraph(RenderObject root) {
    RenderParagraph? found;
    void visit(RenderObject o) {
      if (found != null) return;
      if (o is RenderParagraph) {
        found = o;
        return;
      }
      o.visitChildren(visit);
    }
    visit(root);
    return found;
  }

  void _down(PointerDownEvent e) {
    if (e.kind != PointerDeviceKind.mouse || e.buttons != kPrimaryButton) return;
    final p = _posAt(e.position);
    setState(() {
      anchor = focus = p;
    });
    _last = e.position;
  }

  void _move(PointerMoveEvent e) {
    if (_last == null) return;
    _last = e.position;
    final p = _posAt(e.position);
    if (p != null && p.compareTo(focus ?? p) != 0) setState(() => focus = p);
    _auto ??= Timer.periodic(const Duration(milliseconds: 16), (_) => _tick());
  }

  void _tick() {
    final l = _last;
    final vb = _viewport.currentContext?.findRenderObject() as RenderBox?;
    if (l == null || vb == null) return;
    final top = vb.localToGlobal(Offset.zero).dy;
    final bottom = top + vb.size.height;
    var dy = 0.0;
    if (l.dy > bottom - 30) dy = (l.dy - (bottom - 30)).clamp(2.0, 40.0).toDouble();
    if (l.dy < top + 30) dy = -((top + 30) - l.dy).clamp(2.0, 40.0).toDouble();
    if (dy != 0) {
      sc.jumpTo((sc.offset + dy).clamp(0.0, sc.position.maxScrollExtent));
      WidgetsBinding.instance.addPostFrameCallback((_) {
        final p = _posAt(l);
        if (mounted && p != null) setState(() => focus = p);
      });
    }
  }

  void _up(PointerUpEvent e) {
    _last = null;
    _auto?.cancel();
    _auto = null;
  }

  @override
  void dispose() {
    _auto?.cancel();
    super.dispose();
  }

  TextSpan _span(int i) {
    final s = widget.rows[i];
    final r = range;
    if (r == null || i < r.$1.row || i > r.$2.row) return TextSpan(text: s);
    final from = i == r.$1.row ? r.$1.off.clamp(0, s.length) : 0;
    final to = i == r.$2.row ? r.$2.off.clamp(0, s.length) : s.length;
    const hl = TextStyle(backgroundColor: Color(0x663399FF));
    return TextSpan(children: [
      TextSpan(text: s.substring(0, from)),
      TextSpan(text: s.substring(from, to), style: hl),
      TextSpan(text: s.substring(to)),
    ]);
  }

  @override
  Widget build(BuildContext context) {
    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.keyC, meta: true): copy,
        const SingleActivator(LogicalKeyboardKey.keyC, control: true): copy,
        const SingleActivator(LogicalKeyboardKey.keyA, meta: true): selectAll,
        const SingleActivator(LogicalKeyboardKey.keyA, control: true): selectAll,
      },
      child: Focus(
        autofocus: true,
        child: Listener(
          onPointerDown: _down,
          onPointerMove: _move,
          onPointerUp: _up,
          child: ListView.builder(
            key: _viewport,
            controller: sc,
            itemCount: widget.rows.length,
            itemBuilder: (c, i) => Padding(
              padding: const EdgeInsets.symmetric(vertical: 4, horizontal: 12),
              child: Text.rich(_span(i), key: _key(i)),
            ),
          ),
        ),
      ),
    );
  }
}
