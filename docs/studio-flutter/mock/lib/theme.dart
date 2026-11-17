import 'package:material_ui/material_ui.dart';

/// The two visual directions the mock compares.
/// Design tokens. Every colour and radius in the mock comes from here, so
/// switching direction or brightness restyles the whole app at once.
class S {
  const S({
    required this.dark,
    required this.bg,
    required this.sidebar,
    required this.border,
    required this.fg,
    required this.muted,
    required this.faint,
    required this.accent,
    required this.onAccent,
    required this.primary,
    required this.onPrimary,
    required this.running,
    required this.ok,
    required this.warn,
    required this.err,
    required this.info,
    required this.violet,
  });

  final bool dark;
  final Color bg;
  final Color sidebar;
  final Color border;
  final Color fg;
  final Color muted;
  final Color faint;
  final Color accent;
  final Color onAccent;

  /// Primary buttons. Separate from [accent] so a palette can make them neutral.
  final Color primary;
  final Color onPrimary;

  /// The running state. Neutral in the new palettes; it pulses instead.
  final Color running;
  final Color ok;
  final Color warn;
  final Color err;
  final Color info;
  final Color violet;
  double get radius => 12;
  double get control => 8;

  /// Selection and hover fills. Chrome is grouped by borders, never by tint;
  /// these two are the only fills, and they mark state rather than structure.
  Color get selected => accent.withValues(alpha: dark ? 0.18 : 0.11);
  Color get hover => fg.withValues(alpha: 0.05);
  Color get codeBg => fg.withValues(alpha: dark ? 0.05 : 0.035);

  TextStyle text(double size, {Color? color, FontWeight weight = FontWeight.w400, double? height}) =>
      TextStyle(fontSize: size, color: color ?? fg, fontWeight: weight, height: height, letterSpacing: 0);

  TextStyle mono(double size, {Color? color, FontWeight weight = FontWeight.w400}) => TextStyle(
        fontSize: size,
        color: color ?? fg,
        fontWeight: weight,
        fontFamily: 'Menlo',
        fontFamilyFallback: const ['SF Mono', 'Consolas', 'monospace'],
        height: 1.45,
        letterSpacing: 0,
      );

  /// Ember: warm neutrals, coral as the one accent, three status colours.
  /// Running is neutral; it is shown by the pulse, not by a hue.
  static S resolve(bool dark) => dark
      ? const S(
          dark: true,
          bg: Color(0xFF151414),
          sidebar: Color(0xFF151414),
          border: Color(0xFF2B2928),
          fg: Color(0xFFECE7E1),
          muted: Color(0xFF9E9790),
          faint: Color(0xFF6A645F),
          accent: Color(0xFFF0865E),
          onAccent: Color(0xFF1B0F0A),
          primary: Color(0xFFF0865E),
          onPrimary: Color(0xFF1B0F0A),
          running: Color(0xFFECE7E1),
          ok: Color(0xFF6FBF8E),
          warn: Color(0xFFE2B04A),
          err: Color(0xFFE0606E),
          info: Color(0xFF9E9790),
          violet: Color(0xFF9E9790),
        )
      : const S(
          dark: false,
          bg: Color(0xFFFBF8F4),
          sidebar: Color(0xFFFBF8F4),
          border: Color(0xFFE5DDD3),
          fg: Color(0xFF2A2420),
          muted: Color(0xFF75695F),
          faint: Color(0xFF8A7E73),
          accent: Color(0xFFBF4A22),
          onAccent: Color(0xFFFFFFFF),
          primary: Color(0xFFBF4A22),
          onPrimary: Color(0xFFFFFFFF),
          running: Color(0xFF2A2420),
          ok: Color(0xFF2C7A52),
          warn: Color(0xFF8F6300),
          err: Color(0xFFB8324A),
          info: Color(0xFF75695F),
          violet: Color(0xFF75695F),
        );
}
