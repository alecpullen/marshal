/// The Ember tokens: every colour, text role, spacing, radius, size and
/// layout width the Studio's widgets may use.
///
/// This file is the code half of `docs/studio-flutter/design-system.md`
/// §2–§4, and the only file under `lib/src/ui/` that may contain a colour
/// value, construct a text style or name a font size, weight or family
/// (`test/design_rules_test.dart`). A value changes here and in that
/// document together, never in one alone (`test/contrast_test.dart` pins
/// them to each other).
library;

import 'package:flutter/widgets.dart';
import 'package:material_ui/material_ui.dart'
    show
        ColorScheme,
        Colors,
        NoSplash,
        TextSelectionThemeData,
        TextTheme,
        ThemeData;

/// One theme's tokens: the palette, the derived fills and the text roles.
///
/// There are exactly two: [Ember.dark] and [Ember.light]. A widget reads the
/// one in force with [Ember.of]; light and dark follow the system unless an
/// [EmberTheme] above it says otherwise.
@immutable
final class Ember {
  const Ember._({
    required this.brightness,
    required this.bg,
    required this.border,
    required this.fg,
    required this.muted,
    required this.faint,
    required this.accent,
    required this.onAccent,
    required this.ok,
    required this.warn,
    required this.err,
    required this.termBlue,
    required this.termMagenta,
    required this.termCyan,
  });

  static const dark = Ember._(
    brightness: Brightness.dark,
    bg: Color(0xFF151414),
    border: Color(0xFF2B2928),
    fg: Color(0xFFECE7E1),
    muted: Color(0xFF9E9790),
    faint: Color(0xFF6A645F),
    accent: Color(0xFFF0865E),
    onAccent: Color(0xFF1B0F0A),
    ok: Color(0xFF6FBF8E),
    warn: Color(0xFFE2B04A),
    err: Color(0xFFE0606E),
    termBlue: Color(0xFF7AA6DA),
    termMagenta: Color(0xFFC49ADB),
    termCyan: Color(0xFF6FBFC4),
  );

  static const light = Ember._(
    brightness: Brightness.light,
    bg: Color(0xFFFBF8F4),
    border: Color(0xFFE5DDD3),
    fg: Color(0xFF2A2420),
    muted: Color(0xFF75695F),
    faint: Color(0xFF8A7E73),
    accent: Color(0xFFBF4A22),
    onAccent: Color(0xFFFFFFFF),
    ok: Color(0xFF2C7A52),
    warn: Color(0xFF8F6300),
    err: Color(0xFFB8324A),
    termBlue: Color(0xFF2F6294),
    termMagenta: Color(0xFF7E4A9E),
    termCyan: Color(0xFF1F6F75),
  );

  /// The tokens for [brightness].
  static Ember resolve(Brightness brightness) =>
      brightness == Brightness.dark ? dark : light;

  /// The tokens in force at [context]: the nearest [EmberTheme]'s choice,
  /// or else the platform's brightness.
  static Ember of(BuildContext context) {
    final chosen = context
        .dependOnInheritedWidgetOfExactType<EmberTheme>()
        ?.brightness;
    return resolve(
      chosen ??
          MediaQuery.maybePlatformBrightnessOf(context) ??
          Brightness.light,
    );
  }

  final Brightness brightness;

  bool get isDark => brightness == Brightness.dark;

  // §2 — the palette.

  /// Every surface. There is one background.
  final Color bg;

  /// Every border and hairline.
  final Color border;

  /// Text; the running state; neutral data marks.
  final Color fg;

  /// Secondary text.
  final Color muted;

  /// Decoration only: chevrons, placeholders, pending marks. It meets 3:1,
  /// so it never carries information by itself.
  final Color faint;

  /// Primary action, selection, the current tab, the focus ring.
  final Color accent;

  /// Text and icons on [accent].
  final Color onAccent;

  /// Passed, ready, done.
  final Color ok;

  /// Needs you.
  final Color warn;

  /// Failed, blocked, destructive.
  final Color err;

  // §2 — terminal colours. Only the terminal widget reads these three
  // (`test/design_rules_test.dart`): the ANSI hues Ember has no token for.
  // The other ANSI colours reuse tokens: black is [faint], red [err],
  // green [ok], yellow [warn], white [fg]; bright variants reuse the same
  // eight.

  /// ANSI blue, in the terminal only.
  final Color termBlue;

  /// ANSI magenta, in the terminal only.
  final Color termMagenta;

  /// ANSI cyan, in the terminal only.
  final Color termCyan;

  // §2 — derived, defined here and nowhere else.

  /// The selected row.
  Color get selected => accent.withValues(alpha: isDark ? 0.18 : 0.11);

  /// A hovered row or button.
  Color get hover => fg.withValues(alpha: 0.05);

  /// Code and log blocks.
  Color get codeBg => fg.withValues(alpha: isDark ? 0.05 : 0.035);

  /// A removed diff line.
  Color get diffRemoved => err.withValues(alpha: 0.12);

  /// An added diff line.
  Color get diffAdded => ok.withValues(alpha: 0.12);

  /// The border of something that needs you.
  Color get attention => warn.withValues(alpha: 0.55);

  /// The border of something that failed.
  Color get failing => err.withValues(alpha: 0.55);

  /// What a sheet draws over everything behind it: black at 45% in dark,
  /// 30% in light.
  Color get scrim =>
      const Color(0xFF000000).withValues(alpha: isDark ? 0.45 : 0.30);

  /// No colour at all: an unselected tab's underline. Not a token of §2; it
  /// is the absence of one.
  Color get none => const Color(0x00000000);

  // §3 — the text roles, each in [fg]. Recolour with [TintText.tint].

  /// One headline number on a card. 22 / 600.
  TextStyle get display => _sans(22, FontWeight.w600);

  /// The title of a page. 20 / 600.
  TextStyle get heading => _sans(20, FontWeight.w600);

  /// A thread's title; the prompt in the new-work dialog. 15 / 600.
  TextStyle get title => _sans(15, FontWeight.w600);

  /// The title of a card or an ask. 14 / 600.
  TextStyle get strong => _sans(14, FontWeight.w600);

  /// Conversation prose. 14 / 400, line height 1.55.
  TextStyle get reading => _sans(14, FontWeight.w400, height: 1.55);

  /// The default. 13 / 400.
  TextStyle get body => _sans(13, FontWeight.w400);

  /// A row's name, an emphasised value. 13 / 600.
  TextStyle get bodyStrong => _sans(13, FontWeight.w600);

  /// Secondary lines. 12 / 400.
  TextStyle get small => _sans(12, FontWeight.w400);

  /// Chips, timestamps. 11 / 500.
  TextStyle get caption => _sans(11, FontWeight.w500);

  /// Section labels only: 11 / 600, tracking 0.6. The `Label` component
  /// uppercases its text; nothing else is uppercase.
  TextStyle get label => _sans(11, FontWeight.w600, letterSpacing: 0.6);

  /// Code, commands, paths, diffs. 12 / 400, line height 1.45.
  TextStyle get mono => _mono(12, height: 1.45);

  /// Counts, durations, short hashes. 11 / 400.
  TextStyle get monoSmall => _mono(11);

  /// Every role by name, for the gallery and the tests.
  Map<String, TextStyle> get roles => {
    'display': display,
    'heading': heading,
    'title': title,
    'strong': strong,
    'reading': reading,
    'body': body,
    'bodyStrong': bodyStrong,
    'small': small,
    'caption': caption,
    'label': label,
    'mono': mono,
    'monoSmall': monoSmall,
  };

  /// The interface family, bundled from `assets/fonts/` (pubspec.yaml).
  static const sans = 'Geist';

  /// Code, paths, identifiers, counts and durations.
  static const monoFamily = 'Geist Mono';

  /// The symbol fallback, bundled from `assets/fonts/` (pubspec.yaml):
  /// Noto Sans Symbols 2, under its OFL licence. Geist has no glyph for
  /// ⌘ ⌥ ⏎ ✓ ✗ ▸ ▾ ⋯ and the transcript needs them, so every role falls
  /// back to it rather than drawing tofu. It is never a role's first family.
  static const symbols = 'Noto Sans Symbols 2';

  TextStyle _sans(
    double size,
    FontWeight weight, {
    double? height,
    double letterSpacing = 0,
  }) => TextStyle(
    inherit: false,
    color: fg,
    fontFamily: sans,
    fontFamilyFallback: const [symbols],
    fontSize: size,
    fontWeight: weight,
    height: height,
    leadingDistribution: TextLeadingDistribution.even,
    letterSpacing: letterSpacing,
    textBaseline: TextBaseline.alphabetic,
    decoration: TextDecoration.none,
  );

  TextStyle _mono(double size, {double? height}) => TextStyle(
    inherit: false,
    color: fg,
    fontFamily: monoFamily,
    fontFamilyFallback: const [symbols],
    fontSize: size,
    fontWeight: FontWeight.w400,
    height: height,
    leadingDistribution: TextLeadingDistribution.even,
    letterSpacing: 0,
    textBaseline: TextBaseline.alphabetic,
    decoration: TextDecoration.none,
  );

  /// The Material theme the kit's few Material widgets (the text input)
  /// draw with, made from these tokens so they look like the rest.
  ThemeData get material => ThemeData(
    brightness: brightness,
    fontFamily: sans,
    scaffoldBackgroundColor: bg,
    canvasColor: bg,
    splashFactory: NoSplash.splashFactory,
    splashColor: Colors.transparent,
    highlightColor: Colors.transparent,
    hoverColor: hover,
    focusColor: Colors.transparent,
    colorScheme: ColorScheme(
      brightness: brightness,
      primary: accent,
      onPrimary: onAccent,
      secondary: accent,
      onSecondary: onAccent,
      error: err,
      onError: bg,
      surface: bg,
      onSurface: fg,
      outline: border,
      outlineVariant: border,
    ),
    textSelectionTheme: TextSelectionThemeData(
      cursorColor: accent,
      selectionColor: selected,
      selectionHandleColor: accent,
    ),
    textTheme: TextTheme(
      bodyLarge: body,
      bodyMedium: body,
      bodySmall: small,
      titleMedium: body,
      labelLarge: body,
    ),
  );
}

/// Recolours a text role. The role keeps its size, weight and family; only
/// the colour changes, and only to a token.
extension TintText on TextStyle {
  TextStyle tint(Color color) => copyWith(color: color);
}

/// Chooses light or dark for everything below it. With no [brightness] it
/// follows the system, which is the default (§2).
class EmberTheme extends InheritedWidget {
  const EmberTheme({super.key, this.brightness, required super.child});

  /// The user's choice, or null to follow the system.
  final Brightness? brightness;

  @override
  bool updateShouldNotify(EmberTheme old) => old.brightness != brightness;
}

/// §4 — the spacing scale. Every padding, gap and margin is one of these.
abstract final class Space {
  static const double s2 = 2;
  static const double s4 = 4;
  static const double s6 = 6;
  static const double s8 = 8;
  static const double s12 = 12;
  static const double s16 = 16;
  static const double s20 = 20;
  static const double s24 = 24;
  static const double s32 = 32;
}

/// §4 — the radii: 12 panel, 8 control, 2 bar, full for chips and dots.
abstract final class Radii {
  static const double panel = 12;
  static const double control = 8;
  static const double bar = 2;

  /// Fully round: a chip's ends, a dot.
  static const double full = 999;
}

/// §4 — sizes: the border, icons, controls and the touch target.
abstract final class Sizes {
  /// Every border and hairline.
  static const double border = 1;

  /// An icon inline with text.
  static const double iconInline = 11;

  /// An icon in a button or a row.
  static const double iconRow = 13;

  /// An icon in navigation.
  static const double iconNav = 15;

  /// A button and an icon button.
  static const double control = 28;

  /// A navigation row and a text input.
  static const double row = 30;

  /// The least a control measures on the narrow layout.
  static const double touch = 44;
}

/// §4 — the layout widths.
abstract final class Layout {
  static const double sidebar = 252;
  static const double inspector = 400;

  /// The reading column's maximum.
  static const double reading = 760;

  /// A page's maximum.
  static const double page = 1180;

  /// A page that is a single list.
  static const double pageList = 860;

  /// Page padding: 24 at the sides, 20 at the top.
  static const double pageSide = 24;
  static const double pageTop = 20;

  /// Below this the sidebar becomes a drawer.
  static const double narrow = 760;

  /// Below this the inspector becomes a sheet.
  static const double medium = 1080;
}
