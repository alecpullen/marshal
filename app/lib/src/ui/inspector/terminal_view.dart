import 'package:flutter/widgets.dart';
import 'package:xterm/xterm.dart';

import '../tokens.dart';

/// The terminal a tab drives (f4-inspector.md decision 9): `xterm`'s buffer,
/// capped at the scrollback the finding measured (findings/terminal.md).
///
/// It is built here rather than in the tab so that this file is the only one
/// that imports `xterm`: a tab hands its keystrokes and its size to the object
/// it was given, and names no type from the package.
Terminal newTerminal() => Terminal(maxLines: 3000);

/// The terminal (f4-inspector.md decision 9): `xterm`, themed from the tokens.
///
/// It is the one file that imports `xterm`, and the one place the
/// terminal-only ANSI colours are used (design-system.md §2): blue, magenta
/// and cyan have no Ember hue, so `termBlue`, `termMagenta` and `termCyan`
/// exist for these eight and for nothing else. The bright variants are the
/// same eight, and the font is the `mono` role.
class TerminalPane extends StatelessWidget {
  const TerminalPane({
    super.key,
    required this.terminal,
    this.autofocus = true,
  });

  final Terminal terminal;
  final bool autofocus;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return TerminalView(
      terminal,
      theme: _theme(e),
      // The mono role, in the type xterm paints with: no size or family is
      // named here (§3).
      textStyle: TerminalStyle.fromTextStyle(e.mono),
      padding: const EdgeInsets.all(Space.s8),
      autofocus: autofocus,
    );
  }

  /// §2's ANSI table: black `faint`, red `err`, green `ok`, yellow `warn`,
  /// white `fg`, and the three terminal-only hues; the rest of the theme from
  /// the surface, the accent and the selection.
  static TerminalTheme _theme(Ember e) => TerminalTheme(
    cursor: e.accent,
    selection: e.selected,
    foreground: e.fg,
    background: e.bg,
    black: e.faint,
    red: e.err,
    green: e.ok,
    yellow: e.warn,
    blue: e.termBlue,
    magenta: e.termMagenta,
    cyan: e.termCyan,
    white: e.fg,
    brightBlack: e.faint,
    brightRed: e.err,
    brightGreen: e.ok,
    brightYellow: e.warn,
    brightBlue: e.termBlue,
    brightMagenta: e.termMagenta,
    brightCyan: e.termCyan,
    brightWhite: e.fg,
    searchHitBackground: e.selected,
    searchHitBackgroundCurrent: e.attention,
    searchHitForeground: e.onAccent,
  );
}
