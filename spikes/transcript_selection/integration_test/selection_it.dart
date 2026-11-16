import 'dart:ui';
import 'package:flutter/gestures.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:transcript_selection/main.dart';
import 'package:transcript_selection/model_select.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  for (final approach in [Approach.listBuilderNewline, Approach.perTurn]) {
    testWidgets('SelectionArea $approach on real desktop', (t) async {
      SelectedContent? sel;
      final sc = ScrollController();
      final key = GlobalKey<SpikeState>();
      await t.pumpWidget(MaterialApp(home: Spike(key: key, approach: approach, controller: sc, appendEvery: const Duration(milliseconds: 250), onSelection: (c) => sel = c)));
      await t.pump(const Duration(milliseconds: 500));
      final g = await t.startGesture(const Offset(100, 100), kind: PointerDeviceKind.mouse);
      await t.pump();
      await g.moveTo(const Offset(300, 300));
      await t.pump();
      await g.up();
      for (var i = 0; i < 12; i++) { await t.pump(const Duration(milliseconds: 250)); }
      final plain = sel?.plainText ?? '';
      print('IT[$approach] visible drag with 4/s appends: ${plain.length} chars, ${plain.split("\n").length} lines, built=${key.currentState!.built.length}');
      // scroll-away jump, then rows lost?
      sc.jumpTo(3000);
      await t.pump(); await t.pump();
      final st = t.state<SelectableRegionState>(find.byType(SelectableRegion));
      st.selectAll();
      await t.pump();
      final all = sel?.plainText ?? '';
      final m = RegExp(r'R(\d+) w').allMatches(all).map((e) => int.parse(e.group(1)!)).toList();
      print('IT[$approach] select-all selected rows ${m.isEmpty ? "none" : "${m.first}..${m.last}"} (${m.length}) of ${key.currentState!.count}');
    });
  }

  testWidgets('model-backed on real desktop', (t) async {
    final rows = List.generate(5000, rowText);
    final key = GlobalKey<ModelSelectListState>();
    final sc = ScrollController();
    late StateSetter set;
    await t.pumpWidget(MaterialApp(home: Scaffold(body: StatefulBuilder(builder: (c, s) { set = s; return ModelSelectList(key: key, rows: rows, controller: sc); }))));
    await t.pump(const Duration(milliseconds: 300));
    final size = t.view.physicalSize / t.view.devicePixelRatio;
    final g = await t.startGesture(const Offset(100, 60), kind: PointerDeviceKind.mouse);
    await t.pump();
    await g.moveTo(Offset(300, size.height - 5));
    for (var i = 0; i < 100; i++) {
      rows.add(rowText(rows.length));
      set(() {});
      await t.pump(const Duration(milliseconds: 16));
    }
    await g.up();
    await t.pump();
    final st = key.currentState!;
    final text = st.selectedText;
    final m = RegExp(r'R(\d+) w').allMatches(text).map((e) => int.parse(e.group(1)!)).toList();
    print('IT[model] autoscroll+appends: offset=${sc.offset.toStringAsFixed(0)} rows ${m.first}..${m.last} (${m.length}) contiguous=${List.generate(m.length, (i) => m.first + i).toString() == m.toString()}');
    await st.copy();
    final clip = (await Clipboard.getData('text/plain'))?.text ?? '';
    print('IT[model] real clipboard length=${clip.length} equals selection=${clip == text}');
    expect(clip, text);
  });
}
