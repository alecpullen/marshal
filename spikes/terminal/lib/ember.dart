import 'package:flutter/painting.dart';
import 'package:xterm/xterm.dart';

/// Ember dark tokens (design-system.md section 2), copied here for the spike.
const emberBg = Color(0xFF151414);
const emberFg = Color(0xFFECE7E1);
const emberMuted = Color(0xFF9E9790);
const emberAccent = Color(0xFFF0865E);
const emberOk = Color(0xFF6FBF8E);
const emberWarn = Color(0xFFE2B04A);
const emberErr = Color(0xFFE0606E);

const emberTheme = TerminalTheme(
  cursor: emberAccent,
  selection: Color(0x2EF0865E),
  foreground: emberFg,
  background: emberBg,
  black: emberBg,
  red: emberErr,
  green: emberOk,
  yellow: emberWarn,
  blue: Color(0xFF7FA8D9),
  magenta: Color(0xFFC08FD0),
  cyan: Color(0xFF6FC0C8),
  white: emberFg,
  brightBlack: Color(0xFF6A645F),
  brightRed: emberErr,
  brightGreen: emberOk,
  brightYellow: emberWarn,
  brightBlue: Color(0xFF7FA8D9),
  brightMagenta: Color(0xFFC08FD0),
  brightCyan: Color(0xFF6FC0C8),
  brightWhite: Color(0xFFFFFFFF),
  searchHitBackground: emberWarn,
  searchHitBackgroundCurrent: emberAccent,
  searchHitForeground: emberBg,
);

const emberStyle = TerminalStyle(
  fontSize: 12,
  height: 1.45,
  fontFamily: 'Geist Mono',
  fontFamilyFallback: ['Menlo', 'monospace'],
);
