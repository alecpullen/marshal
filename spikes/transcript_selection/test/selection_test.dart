import 'dart:ui';
import 'package:flutter/gestures.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:transcript_selection/main.dart';

Future<void> setup(WidgetTester t) async {
  t.view.physicalSize = const Size(800, 600);
  t.view.devicePixelRatio = 1;
  addTearDown(t.view.reset);
}

Offset centerOfRow(WidgetTester t, String key) => t.getCenter(find.byKey(ValueKey(key)));

String _plain(SelectedContent? c) => c?.plainText ?? '<null>';

void main() {
  for (final approach in Approach.values) {
    group('$approach', () {
      testWidgets('virtualised: built window << total', (t) async {
        await setup(t);
        final key = GlobalKey<SpikeState>();
        await t.pumpWidget(MaterialApp(home: Spike(key: key, approach: approach)));
        print('[$approach] built ${key.currentState!.built.length} of ${approach == Approach.perTurn ? 500 : 5000} items');
        expect(key.currentState!.built.length < 100, isTrue);
      });

      testWidgets('drag across visible rows', (t) async {
        await setup(t);
        SelectedContent? sel;
        await t.pumpWidget(MaterialApp(home: Spike(approach: approach, onSelection: (c) => sel = c)));
        final g = await t.startGesture(const Offset(100, 10), kind: PointerDeviceKind.mouse);
        await t.pump();
        await g.moveTo(const Offset(300, 250));
        await t.pump();
        await g.up();
        await t.pump();
        final s = _plain(sel);
        print('[$approach] visible-drag selected ${s.length} chars, lines=${s.split('\n').length}\n--- head: ${s.substring(0, s.length.clamp(0, 120))}\n--- tail: ${s.substring((s.length - 80).clamp(0, s.length))}');
        expect(s.length > 50, isTrue);
      });

      testWidgets('selection survives appends', (t) async {
        await setup(t);
        SelectedContent? sel;
        final key = GlobalKey<SpikeState>();
        await t.pumpWidget(MaterialApp(home: Spike(key: key, approach: approach, onSelection: (c) => sel = c)));
        final g = await t.startGesture(const Offset(100, 10), kind: PointerDeviceKind.mouse);
        await t.pump();
        await g.moveTo(const Offset(300, 250));
        await t.pump();
        await g.up();
        await t.pump();
        final before = _plain(sel);
        for (var i = 0; i < 20; i++) {
          key.currentState!.append();
          await t.pump(const Duration(milliseconds: 250)); // 4/s
        }
        final after = _plain(sel);
        print('[$approach] append-survive: before=${before.length} after=${after.length} equal=${before == after}');
        expect(after, before);
      });

      testWidgets('selection with appends at end of list (tail anchored)', (t) async {
        await setup(t);
        SelectedContent? sel;
        final key = GlobalKey<SpikeState>();
        final sc = ScrollController();
        await t.pumpWidget(MaterialApp(home: Spike(key: key, approach: approach, controller: sc, onSelection: (c) => sel = c)));
        sc.jumpTo(sc.position.maxScrollExtent);
        await t.pump();
        await t.pump();
        final g = await t.startGesture(const Offset(100, 100), kind: PointerDeviceKind.mouse);
        await t.pump();
        await g.moveTo(const Offset(300, 400));
        await t.pump();
        await g.up();
        await t.pump();
        final before = _plain(sel);
        for (var i = 0; i < 20; i++) {
          key.currentState!.append();
          await t.pump(const Duration(milliseconds: 250));
        }
        final after = _plain(sel);
        print('[$approach] tail append: before=${before.length} after=${after.length} equal=${before == after}');
      });

      testWidgets('select start row, scroll start out of window, extend', (t) async {
        await setup(t);
        SelectedContent? sel;
        final sc = ScrollController();
        final key = GlobalKey<SpikeState>();
        await t.pumpWidget(MaterialApp(home: Spike(key: key, approach: approach, controller: sc, onSelection: (c) => sel = c)));
        final g = await t.startGesture(const Offset(100, 10), kind: PointerDeviceKind.mouse);
        await t.pump();
        await g.moveTo(const Offset(200, 100));
        await t.pump();
        final first = _plain(sel);
        // scroll far enough that the start leaves the cache extent, while mouse button held
        sc.jumpTo(3000);
        await t.pump();
        await t.pump();
        final stillBuilt0 = find.byKey(ValueKey(approach == Approach.perTurn ? 'turn0' : 'row0')).evaluate().isNotEmpty;
        await g.moveTo(const Offset(300, 300));
        await t.pump();
        await g.up();
        await t.pump();
        final s = _plain(sel);
        print('[$approach] scroll-out: start still built=$stillBuilt0; first=${first.length} chars; after=${s.length} chars; lines=${s.split("\n").length}; startsWithR0=${s.startsWith("R0")} head=${s.substring(0, s.length.clamp(0, 60))}');
      });

      testWidgets('autoscroll drag to far rows', (t) async {
        await setup(t);
        SelectedContent? sel;
        final sc = ScrollController();
        await t.pumpWidget(MaterialApp(home: Spike(approach: approach, controller: sc, onSelection: (c) => sel = c)));
        final g = await t.startGesture(const Offset(100, 20), kind: PointerDeviceKind.mouse);
        await t.pump();
        await g.moveTo(const Offset(300, 640));
        for (var i = 0; i < 300; i++) {
          await t.pump(const Duration(milliseconds: 16));
        }
        await g.up();
        await t.pump();
        final s = _plain(sel);
        print('[$approach] autoscroll: scrolled to ${sc.offset.toStringAsFixed(0)}; selected ${s.length} chars lines=${s.split("\n").length} startsWithR0=${s.startsWith("R0")}; head=${s.substring(0, s.length.clamp(0, 40))} tail=${s.substring((s.length - 40).clamp(0, s.length))}');
        // contiguity check: rows R0..Rk all present?
        final m = RegExp(r'R(\d+) w').allMatches(s).map((e) => int.parse(e.group(1)!)).toList();
        if (m.isNotEmpty) {
          final missing = <int>[];
          for (var i = m.first; i <= m.last; i++) { if (!m.contains(i)) missing.add(i); }
          print('[$approach] autoscroll rows ${m.first}..${m.last}, missing count=${missing.length}');
        }
      });


      testWidgets('release, scroll selection far away, copy', (t) async {
        await setup(t);
        String? clip;
        t.binding.defaultBinaryMessenger.setMockMethodCallHandler(SystemChannels.platform, (call) async {
          if (call.method == 'Clipboard.setData') clip = (call.arguments as Map)['text'] as String?;
          return null;
        });
        final sc = ScrollController();
        await t.pumpWidget(MaterialApp(home: Spike(approach: approach, controller: sc)));
        final g = await t.startGesture(const Offset(100, 10), kind: PointerDeviceKind.mouse);
        await t.pump();
        await g.moveTo(const Offset(300, 250));
        await t.pump();
        await g.up();
        await t.pump();
        sc.jumpTo(3000);
        await t.pump();
        await t.pump();
        final st = t.state<SelectableRegionState>(find.byType(SelectableRegion));
        st.copySelection(SelectionChangedCause.keyboard);
        await t.pump();
        final s = clip ?? '<none>';
        print('[$approach] scrolled-away copy: ${s.length} chars startsWith R0-ish=${s.contains("w0_1")} head=${s.substring(0, s.length.clamp(0, 50)).replaceAll("\n", r"\n")}');
        sc.jumpTo(0);
        await t.pump();
        await t.pump();
        clip = null;
        st.copySelection(SelectionChangedCause.keyboard);
        await t.pump();
        print('[$approach] scrolled-back copy: ${(clip ?? "<none>").length} chars');
        t.binding.defaultBinaryMessenger.setMockMethodCallHandler(SystemChannels.platform, null);
      });

      testWidgets('wheel-style incremental scroll while button held', (t) async {
        await setup(t);
        SelectedContent? sel;
        final sc = ScrollController();
        await t.pumpWidget(MaterialApp(home: Spike(approach: approach, controller: sc, onSelection: (c) => sel = c)));
        final g = await t.startGesture(const Offset(100, 10), kind: PointerDeviceKind.mouse);
        await t.pump();
        await g.moveTo(const Offset(200, 100));
        await t.pump();
        for (var i = 0; i < 40; i++) {
          sc.jumpTo(sc.offset + 120);
          await t.pump();
          await g.moveTo(Offset(200 + (i % 2), 300));
          await t.pump();
        }
        await g.up();
        await t.pump();
        final s = _plain(sel);
        final m = RegExp(r'R(\d+) w').allMatches(s).map((e) => int.parse(e.group(1)!)).toList();
        print('[$approach] wheel-scroll drag: offset=${sc.offset}; selected ${s.length} chars; rows ${m.isEmpty ? "none" : "${m.first}..${m.last}"} (${m.length}); containsStartRow=${s.contains("w0_1")}');
      });

      testWidgets('select all', (t) async {
        await setup(t);
        SelectedContent? sel;
        await t.pumpWidget(MaterialApp(home: Spike(approach: approach, onSelection: (c) => sel = c)));
        final st = t.state<SelectableRegionState>(find.byType(SelectableRegion));
        st.selectAll();
        await t.pump();
        final s = _plain(sel);
        final m = RegExp(r'R(\d+) w').allMatches(s).map((e) => int.parse(e.group(1)!)).toList();
        print('[$approach] select-all raw=${s.replaceAll("\n", r"\\n")}'); print('[$approach] select-all: ${s.length} chars, rows ${m.isEmpty ? "none" : "${m.first}..${m.last}"} of 5000 (count ${m.length})');
      });
    });
  }
}
