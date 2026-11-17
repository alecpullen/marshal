import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:terminal_spike/ember.dart';
import 'package:terminal_spike/main.dart';
import 'package:xterm/xterm.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('macOS: canned stream renders, keys and resize reported', (tester) async {
    await tester.pumpWidget(const SpikeApp());
    await tester.pumpAndSettle();
    expect(find.byType(TerminalView), findsOneWidget);
    final t = Terminal(maxLines: 1000);
    final out = <String>[];
    final sizes = <String>[];
    t.onOutput = out.add;
    t.onResize = (w, h, pw, ph) => sizes.add('$w x $h');
    t.write(cannedStream());
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(body: TerminalView(t, theme: emberTheme, textStyle: emberStyle, autofocus: true)),
    ));
    await tester.pumpAndSettle();
    expect(t.isUsingAltBuffer, isTrue);
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowUp);
    await tester.sendKeyDownEvent(LogicalKeyboardKey.controlLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyC);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.controlLeft);
    expect(out.join().contains('\x1b[A'), isTrue);
    expect(out.join().contains('\x03'), isTrue);
    expect(sizes, isNotEmpty);
    // Text input path (IME / typed characters).
    tester.testTextInput.enterText('hi');
    await tester.pump();
    // ignore: avoid_print
    print('macos emitted: ${out.map((s) => s.codeUnits).toList()} sizes: $sizes');
    expect(out.join().contains('hi'), isTrue);
  });
}
