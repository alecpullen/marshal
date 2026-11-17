import 'dart:convert';

import 'package:material_ui/material_ui.dart';
import 'package:xterm/xterm.dart';

import 'bridge_stream.dart';
import 'ember.dart';

void main() => runApp(const SpikeApp());

/// Canned stream: colours, cursor movement, wrap, scrollback, alt screen.
String cannedStream() {
  final b = StringBuffer();
  b.write('\x1b[31mred\x1b[0m \x1b[1;32mbold green\x1b[0m \x1b[38;5;208m256-orange\x1b[0m \x1b[38;2;240;134;94mtruecolor\x1b[0m\r\n');
  b.write('abc\x1b[3D\x1b[1CX\r\n');
  b.write('${'0123456789' * 12}\r\n');
  for (var i = 0; i < 2000; i++) {
    b.write('line $i\r\n');
  }
  b.write('\x1b[?1049h\x1b[2J\x1b[HALT SCREEN\x1b[5;10Hat 5,10');
  return b.toString();
}

class SpikeApp extends StatefulWidget {
  const SpikeApp({super.key});
  @override
  State<SpikeApp> createState() => _SpikeAppState();
}

class _SpikeAppState extends State<SpikeApp> {
  final terminal = Terminal(maxLines: 10000);
  final controller = TerminalController();

  @override
  void initState() {
    super.initState();
    terminal.onOutput = (s) => debugPrint('input: ${inputBodies(s)}');
    terminal.onResize = (w, h, pw, ph) => debugPrint('resize $w x $h');
    // Bridge chunks arrive as base64 JSON; feed as UTF-8 text.
    final ev = decodeTerminalEvent(jsonEncode({'data': base64Encode(utf8.encode(cannedStream()))}));
    terminal.write(utf8.decode(ev!.bytes!));
  }

  @override
  Widget build(BuildContext context) => MaterialApp(
        home: Scaffold(
          backgroundColor: emberBg,
          body: TerminalView(
            terminal,
            controller: controller,
            theme: emberTheme,
            textStyle: emberStyle,
            autofocus: true,
          ),
        ),
      );
}
