import 'dart:convert';

import 'package:material_ui/material_ui.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:terminal_spike/bridge_stream.dart';
import 'package:terminal_spike/ember.dart';
import 'package:terminal_spike/main.dart';
import 'package:xterm/xterm.dart';

String row(Terminal t, int y) => t.buffer.lines[y].getText().trimRight();
int cp(Terminal t, int y, int x) => t.buffer.lines[y].getCodePoint(x);

void main() {
  test('colours: SGR 16, 256 and truecolor are stored per cell', () {
    final t = Terminal();
    t.write('\x1b[31mR\x1b[0m\x1b[38;5;208mO\x1b[0m\x1b[38;2;240;134;94mT\x1b[0m\x1b[1mB');
    final l = t.buffer.lines[0];
    final c = CellData.empty();
    l.getCellData(0, c);
    expect(c.foreground & 0xff, 1); // palette index 1 (red)
    expect(c.foreground >> 25, CellColor.named >> 25);
    l.getCellData(1, c);
    expect(c.foreground & 0xff, 208);
    l.getCellData(2, c);
    expect(c.foreground & 0xffffff, 0xF0865E);
    l.getCellData(3, c);
    expect(c.flags & CellFlags.bold, isNot(0));
  });

  test('cursor movement: CUB/CUF, CUP, erase', () {
    final t = Terminal();
    t.write('abc\x1b[3D\x1b[1CX');
    expect(row(t, 0), 'aXc');
    t.write('\x1b[3;4HZ');
    expect(cp(t, 2, 3), 'Z'.codeUnitAt(0));
    t.write('\x1b[3;1H\x1b[2K');
    expect(row(t, 2), '');
  });

  test('wrap: a 120 char line wraps at 80 columns', () {
    final t = Terminal();
    t.write('0123456789' * 12);
    expect(t.buffer.lines[0].getText().length, 80);
    expect(t.buffer.lines[1].isWrapped, isTrue);
    expect(row(t, 1).length, 40);
  });

  test('scrollback: 5000 lines retained within maxLines, oldest evicted', () {
    final t = Terminal(maxLines: 3000);
    for (var i = 0; i < 5000; i++) {
      t.write('line $i\r\n');
    }
    expect(t.buffer.height, 3000);
    expect(row(t, 0), isNot('line 0'));
    expect(t.buffer.lines[t.buffer.height - 2].getText().trimRight(), 'line 4999');
  });

  test('alternate screen: enter keeps main buffer, leave restores it', () {
    final t = Terminal();
    t.write('main\r\n');
    t.write('\x1b[?1049h\x1b[2J\x1b[HALT\x1b[5;10Hx');
    expect(t.isUsingAltBuffer, isTrue);
    expect(row(t, 0), 'ALT');
    expect(cp(t, 4, 9), 'x'.codeUnitAt(0));
    t.write('\x1b[?1049l');
    expect(t.isUsingAltBuffer, isFalse);
    expect(row(t, 0), 'main');
  });

  test('bridge framing: base64 event decodes; split UTF-8 across chunks', () {
    final bytes = utf8.encode('é');
    final e1 = decodeTerminalEvent(jsonEncode({'data': base64Encode(bytes.sublist(0, 1))}))!;
    final e2 = decodeTerminalEvent(jsonEncode({'data': base64Encode(bytes.sublist(1))}))!;
    // Terminal.write takes a String: a naive per-chunk utf8.decode corrupts.
    final naive = utf8.decode(e1.bytes!, allowMalformed: true) + utf8.decode(e2.bytes!, allowMalformed: true);
    expect(naive, isNot('é'));
    // A streaming decoder handles it.
    final sink = StringBuffer();
    final conv = const Utf8Decoder().startChunkedConversion(StringConversionSink.fromStringSink(sink));
    conv.add(e1.bytes!);
    conv.add(e2.bytes!);
    conv.close();
    expect(sink.toString(), 'é');
    expect(decodeTerminalEvent('{"exit":2}')!.exit, 2);
    expect(inputBodies('a' * 40000).length, 3);
  });

  testWidgets('widget: renders, emits keystrokes, reports resize, themed', (tester) async {
    final t = Terminal(maxLines: 1000);
    final out = <String>[];
    final sizes = <String>[];
    t.onOutput = out.add;
    t.onResize = (w, h, pw, ph) => sizes.add('$w x $h');
    t.write(cannedStream());
    Future<void> pump(Size s) async {
      tester.view.physicalSize = s;
      tester.view.devicePixelRatio = 1;
      await tester.pumpWidget(MaterialApp(
        home: Scaffold(body: TerminalView(t, theme: emberTheme, textStyle: emberStyle, autofocus: true)),
      ));
      await tester.pump();
    }

    addTearDown(tester.view.reset);
    await pump(const Size(800, 400));
    final first = sizes.last;
    // Typed text.
    await tester.sendKeyEvent(LogicalKeyboardKey.keyA);
    await tester.sendKeyDownEvent(LogicalKeyboardKey.enter);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.enter);
    // Ctrl-C.
    await tester.sendKeyDownEvent(LogicalKeyboardKey.controlLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyC);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.controlLeft);
    // Arrow key and Escape (full-screen programs).
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowUp);
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    // ignore: avoid_print
    print('emitted: ${out.map((s) => s.codeUnits).toList()}');
    expect(out.join().contains('\x03'), isTrue, reason: 'ctrl-c');
    expect(out.join().contains('\r'), isTrue, reason: 'enter');
    expect(out.join().contains('\x1b[A') || out.join().contains('\x1bOA'), isTrue, reason: 'arrow up');
    // Resize.
    await pump(const Size(500, 300));
    expect(sizes.last, isNot(first));
    // ignore: avoid_print
    print('sizes: $sizes');
    // Theme: the view paints on the Ember background.
    final view = tester.widget<TerminalView>(find.byType(TerminalView));
    expect(view.theme.background, emberBg);
    expect(view.textStyle.fontFamily, 'Geist Mono');
  });

  testWidgets('app boots with canned stream', (tester) async {
    await tester.pumpWidget(const SpikeApp());
    expect(find.byType(TerminalView), findsOneWidget);
  });
}
