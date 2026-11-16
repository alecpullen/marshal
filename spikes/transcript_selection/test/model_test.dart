import 'dart:ui';
import 'package:flutter/gestures.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:transcript_selection/main.dart';
import 'package:transcript_selection/model_select.dart';

void main() {
  testWidgets('model-backed selection: drag, autoscroll, appends, copy, select-all', (t) async {
    t.view.physicalSize = const Size(800, 600);
    t.view.devicePixelRatio = 1;
    addTearDown(t.view.reset);
    final rows = List.generate(5000, rowText);
    final key = GlobalKey<ModelSelectListState>();
    final sc = ScrollController();
    String? clip;
    t.binding.defaultBinaryMessenger.setMockMethodCallHandler(SystemChannels.platform, (c) async {
      if (c.method == 'Clipboard.setData') clip = (c.arguments as Map)['text'] as String?;
      return null;
    });
    late StateSetter set;
    await t.pumpWidget(MaterialApp(
        home: Scaffold(
            body: StatefulBuilder(builder: (c, s) {
      set = s;
      return ModelSelectList(key: key, rows: rows, controller: sc);
    }))));
    // drag from row 0 down past the viewport edge; autoscroll
    final g = await t.startGesture(const Offset(100, 10), kind: PointerDeviceKind.mouse);
    await t.pump();
    await g.moveTo(const Offset(300, 590));
    for (var i = 0; i < 200; i++) {
      await t.pump(const Duration(milliseconds: 16));
    }
    await g.up();
    await t.pump();
    final st = key.currentState!;
    final a = st.anchor!, f = st.focus!;
    print('[model] autoscroll: offset=${sc.offset.toStringAsFixed(0)} anchor=${a.row}:${a.off} focus=${f.row}:${f.off}');
    // scroll away far and append at 4/s while selection exists
    sc.jumpTo(100000);
    await t.pump();
    for (var i = 0; i < 20; i++) {
      rows.add(rowText(rows.length));
      set(() {});
      await t.pump(const Duration(milliseconds: 250));
    }
    // copy
    await t.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await t.sendKeyEvent(LogicalKeyboardKey.keyC);
    await t.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await t.pump();
    final s = clip ?? '<none>';
    final expectLines = f.row - a.row + 1;
    print('[model] copy after scroll-away + 20 appends: ${s.length} chars, lines=${s.split("\n").length} expected=$expectLines; starts="${s.substring(0, 20)}"');
    expect(s.split('\n').length, expectLines);
    final m = RegExp(r'R(\d+) w').allMatches(s).map((e) => int.parse(e.group(1)!)).toList();
    print('[model] rows in copy ${m.first}..${m.last}, contiguous=${List.generate(m.length, (i) => m.first + i).toString() == m.toString()}');
    // select all covers unbuilt rows
    await t.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await t.sendKeyEvent(LogicalKeyboardKey.keyA);
    await t.sendKeyEvent(LogicalKeyboardKey.keyC);
    await t.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await t.pump();
    print('[model] select-all copy: ${clip!.split("\n").length} lines of ${rows.length}');
    expect(clip!.split('\n').length, rows.length);
  });
}
