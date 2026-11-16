import 'dart:math' as math;

import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/tokens.dart';

/// design-system.md §2, held in code: the palette is the one the document
/// states, and each pair meets its ratio in both themes. A token that drifts
/// from the document, or a new value that reads badly, fails here before it
/// reaches a screen.
void main() {
  const themes = {'dark': Ember.dark, 'light': Ember.light};

  group('the palette is the one in design-system.md §2', () {
    // Token | Dark | Light, copied from the document's table. Changing a
    // value means changing the document and this table together.
    const table = {
      'bg': (0xFF151414, 0xFFFBF8F4),
      'border': (0xFF2B2928, 0xFFE5DDD3),
      'fg': (0xFFECE7E1, 0xFF2A2420),
      'muted': (0xFF9E9790, 0xFF75695F),
      'faint': (0xFF6A645F, 0xFF8A7E73),
      'accent': (0xFFF0865E, 0xFFBF4A22),
      'onAccent': (0xFF1B0F0A, 0xFFFFFFFF),
      'ok': (0xFF6FBF8E, 0xFF2C7A52),
      'warn': (0xFFE2B04A, 0xFF8F6300),
      'err': (0xFFE0606E, 0xFFB8324A),
    };

    for (final MapEntry(key: name, value: (dark, light)) in table.entries) {
      test(name, () {
        expect(_palette(Ember.dark)[name]!.toARGB32(), dark, reason: 'dark');
        expect(_palette(Ember.light)[name]!.toARGB32(), light, reason: 'light');
      });
    }

    test('derived tokens are their base at the stated alpha', () {
      for (final e in themes.values) {
        final dark = e.isDark;
        _alpha(e.selected, e.accent, dark ? 0.18 : 0.11);
        _alpha(e.hover, e.fg, 0.05);
        _alpha(e.codeBg, e.fg, dark ? 0.05 : 0.035);
        _alpha(e.diffRemoved, e.err, 0.12);
        _alpha(e.diffAdded, e.ok, 0.12);
        _alpha(e.attention, e.warn, 0.55);
        _alpha(e.failing, e.err, 0.55);
      }
    });
  });

  group('contrast on bg meets §2 in both themes', () {
    for (final MapEntry(key: theme, value: e) in themes.entries) {
      // §2: these meet 4.5:1 on bg.
      for (final name in ['fg', 'muted', 'accent', 'ok', 'warn', 'err']) {
        test('$theme $name on bg is at least 4.5:1', () {
          final ratio = contrast(_palette(e)[name]!, e.bg);
          expect(
            ratio,
            greaterThanOrEqualTo(4.5),
            reason: '$name is ${ratio.toStringAsFixed(2)}:1 on bg',
          );
        });
      }
      // §2: faint meets 3:1 and so never carries information alone.
      test('$theme faint on bg is at least 3:1', () {
        final ratio = contrast(e.faint, e.bg);
        expect(
          ratio,
          greaterThanOrEqualTo(3.0),
          reason: 'faint is ${ratio.toStringAsFixed(2)}:1 on bg',
        );
      });
      // onAccent is "text and icons on accent": a primary button's label.
      test('$theme onAccent on accent is at least 4.5:1', () {
        final ratio = contrast(e.onAccent, e.accent);
        expect(
          ratio,
          greaterThanOrEqualTo(4.5),
          reason: 'onAccent is ${ratio.toStringAsFixed(2)}:1 on accent',
        );
      });
    }
  });

  test('the contrast formula is WCAG 2', () {
    // Black on white is the scale's top; a colour on itself its bottom.
    expect(
      contrast(const Color(0xFF000000), const Color(0xFFFFFFFF)),
      closeTo(21, 0.01),
    );
    expect(contrast(Ember.dark.bg, Ember.dark.bg), closeTo(1, 0.0001));
  });
}

Map<String, Color> _palette(Ember e) => {
  'bg': e.bg,
  'border': e.border,
  'fg': e.fg,
  'muted': e.muted,
  'faint': e.faint,
  'accent': e.accent,
  'onAccent': e.onAccent,
  'ok': e.ok,
  'warn': e.warn,
  'err': e.err,
};

void _alpha(Color derived, Color base, double alpha) {
  expect(derived.r, base.r);
  expect(derived.g, base.g);
  expect(derived.b, base.b);
  expect(derived.a, closeTo(alpha, 0.001));
}

/// The WCAG 2 contrast ratio of two opaque colours.
double contrast(Color a, Color b) {
  final la = _luminance(a);
  final lb = _luminance(b);
  return (math.max(la, lb) + 0.05) / (math.min(la, lb) + 0.05);
}

double _luminance(Color c) {
  double channel(double v) =>
      v <= 0.04045 ? v / 12.92 : math.pow((v + 0.055) / 1.055, 2.4).toDouble();
  return 0.2126 * channel(c.r) + 0.7152 * channel(c.g) + 0.0722 * channel(c.b);
}
